import { Bundle, CheckSpec, TopoLink } from '../contract';
import { Push } from '../engine/protocol';
import { LatestStore } from '../engine/store';
import { httpId, logId, nftName, ssId } from '../engine/ids';
import { Effects, SCENARIOS, ScenarioDef } from './scenarios';

/**
 * Demo-mode simulator. It produces exactly what the real system would send:
 * agent pushes (host metrics, nft counters, wg dumps, ss counts, tcp results,
 * container states, local checks, Prometheus samples), probe results and
 * hub-collector results (Remnawave API, 3x-ui, HTTP/TLS probes). They go
 * through the same ingest path and resolvers as production data.
 *
 * Traffic follows an Iranian evening peak; the scale matches the Notion audit
 * (tens of online users, ~30-50 GB/day through Local-Node, ~300 connections).
 */

export interface ScheduledRun {
  scenario: ScenarioDef;
  start: number; // ms
}

// ---- deterministic noise
function hash(str: string, n: number): number {
  let h = 2166136261 ^ n;
  for (let i = 0; i < str.length; i++) {
    h ^= str.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  h ^= h >>> 13;
  h = Math.imul(h, 0x5bd1e995);
  h ^= h >>> 15;
  return (h >>> 0) / 4294967296;
}

/** Smooth noise in [-1, 1] with ~knotSec correlation. */
function noise(key: string, t: number, knotSec = 90): number {
  const x = t / 1000 / knotSec;
  const i = Math.floor(x);
  const f = x - i;
  const a = hash(key, i) * 2 - 1;
  const b = hash(key, i + 1) * 2 - 1;
  const s = f * f * (3 - 2 * f);
  return a + (b - a) * s;
}

const jitter = (key: string, t: number) => hash(key, Math.floor(t / 5000)) * 2 - 1;

/** Activity 0..1 by Tehran local hour: evening peak ~22:30, low ~05:00. */
export function activity(t: number): number {
  const h = (((t / 3_600_000 + 3.5) % 24) + 24) % 24;
  const d = (a: number, b: number) => {
    const x = Math.abs(a - b) % 24;
    return Math.min(x, 24 - x);
  };
  const v = 0.12 + 0.88 * Math.exp(-(d(h, 22.3) ** 2) / (2 * 4.2 ** 2)) + 0.25 * Math.exp(-(d(h, 13) ** 2) / (2 * 2.5 ** 2));
  return Math.min(1, v);
}

interface LinkWorld {
  up: number;
  down: number;
  conns: number;
}

export class Simulator {
  private runs: ScheduledRun[] = [];
  private counters = new Map<string, number>();
  private lastT: number | null = null;
  private seq = 0;
  private boot: Record<string, number> = {}; // node → boot time (s)
  private containers = new Map<string, { startedAt: number; restarts: number }>();
  private xrayStart: Record<string, number> = {};
  private emittedLogs = new Set<string>();
  private promAcc = new Map<string, number>();
  private promLast = 0;
  private hostOf = new Map<string, string>(); // non-agent node → agent node on the same server

  constructor(private b: Bundle, startT: number) {
    for (const n of b.nodes) if (n.agent) this.boot[n.id] = Math.floor(startT / 1000) - (12 + hash(n.id, 1) * 30) * 86400;
    this.xrayStart['Local-Node'] = startT - 5.2 * 86_400_000;
    this.xrayStart['Iran-Mullvad'] = startT - 11.4 * 86_400_000;
    for (const n of b.nodes) {
      if (n.agent) continue;
      const hosts = new Set<string>();
      for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) if (c.params.host) hosts.add(c.params.host);
      if (hosts.size === 1) this.hostOf.set(n.id, [...hosts][0]);
    }
  }

  setBundle(b: Bundle) {
    this.b = b;
  }

  schedule(id: string, start: number): ScheduledRun {
    const sc = SCENARIOS.find((s) => s.id === id);
    if (!sc) throw new Error(`unknown scenario ${id}`);
    const run = { scenario: sc, start };
    this.runs.push(run);
    return run;
  }

  active(t: number): { run: ScheduledRun; offset: number }[] {
    return this.runs
      .filter((r) => t >= r.start && t < r.start + r.scenario.durationSec * 1000)
      .map((run) => ({ run, offset: (t - run.start) / 1000 }));
  }

  scheduled(): ScheduledRun[] {
    return this.runs;
  }

  private effects(t: number): Effects {
    const e: Effects = { silent: [], bootAt: {}, containerRestart: [], logs: [], mainUsersFactor: 1 };
    for (const { run, offset } of this.active(t)) {
      const x = run.scenario.effects(offset);
      e.silent!.push(...(x.silent ?? []));
      for (const [n, at] of Object.entries(x.bootAt ?? {})) {
        if (offset >= at) e.bootAt![n] = Math.floor((run.start + at * 1000) / 1000);
      }
      for (const c of x.containerRestart ?? []) e.containerRestart!.push({ ...c, at: run.start + c.at * 1000 });
      for (const l of x.logs ?? []) e.logs!.push({ ...l, at: run.start + l.at * 1000 });
      e.xbridgerConnDown ||= x.xbridgerConnDown;
      e.rwApiHung ||= x.rwApiHung;
      e.franceDown ||= x.franceDown;
      e.gameTunnelDown ||= x.gameTunnelDown;
      if (x.mainUsersFactor !== undefined) e.mainUsersFactor = Math.min(e.mainUsersFactor!, x.mainUsersFactor);
    }
    // Scenario effects persist after the scenario ends (a reboot stays a reboot).
    for (const run of this.runs) {
      const end = run.start + run.scenario.durationSec * 1000;
      if (t < end) continue;
      const x = run.scenario.effects(run.scenario.durationSec - 1);
      for (const [n, at] of Object.entries(x.bootAt ?? {})) e.bootAt![n] = Math.max(e.bootAt![n] ?? 0, Math.floor((run.start + at * 1000) / 1000));
      for (const c of x.containerRestart ?? []) e.containerRestart!.push({ ...c, at: run.start + c.at * 1000 });
    }
    return e;
  }

  // ------------------------------------------------------------ world model

  private world(t: number, e: Effects) {
    const act = activity(t);
    const usersLocal = Math.max(0, Math.round((4 + 48 * act) * (1 + 0.08 * noise('ul', t)) * (e.mainUsersFactor ?? 1)));
    const usersMv = Math.max(0, Math.min(3, Math.round(2.2 * act + 0.6 * noise('um', t, 600))));
    const perUser = 380e3 * (1 + 0.3 * noise('pu', t, 45)) * (1 + 0.1 * jitter('pj', t));
    const clientDown = usersLocal * perUser;
    const clientUp = clientDown * (0.11 + 0.03 * noise('cu', t));
    const conn9000 = !e.xbridgerConnDown;
    const L: Record<string, LinkWorld> = {};
    const set = (id: string, up: number, down: number, conns = 0) => (L[id] = { up: Math.max(0, up), down: Math.max(0, down), conns: Math.max(0, Math.round(conns)) });
    const irShare = 0.18;
    const mainFactor = conn9000 ? 1 : irShare + 0.07; // only .ir works (+ retries)
    const franceOk = !e.franceDown;
    set('e1', clientUp * 1.03 * mainFactor, clientDown * 1.03 * mainFactor, usersLocal * (5.5 + noise('c1', t)));
    set('e2', L.e1.up, L.e1.down, L.e1.conns);
    const viaFrance = conn9000 && franceOk ? 1 - irShare : 0;
    set('e3', clientUp * viaFrance * 1.02, clientDown * viaFrance * 1.02, conn9000 ? usersLocal * (4.2 + noise('c3', t)) : 0);
    set('e4', clientUp * irShare, clientDown * irShare, 0);
    set('e5a', L.e3.up * 1.06, L.e3.down * 1.06 + (conn9000 ? 2e3 : 0), 0);
    set('e5b', L.e3.up * 1.04, L.e3.down * 1.04, 0);
    set('e6', L.e3.up + (franceOk ? 3e3 : 0), L.e3.down + (franceOk ? 4e3 : 0), 0);
    // Mullvad: DE + TR carry the squad; US is dead (08 #21).
    const mvDown = usersMv * 520e3 * (1 + 0.4 * noise('mv', t, 60)) * act;
    const h = (((t / 3_600_000 + 3.5) % 24) + 24) % 24;
    const gaming = !e.gameTunnelDown && (h >= 19.5 || h < 1) && noise('xbox', t, 1200) > -0.2;
    const gameDown = gaming ? 2.5e6 * (1 + 0.8 * noise('gd', t, 20)) + 4e5 : 600;
    const gameUp = gaming ? 3.2e5 * (1 + 0.6 * noise('gu', t, 20)) : 400;
    set('e8-de', mvDown * 0.08 * 0.6 + gameUp, mvDown * 0.6 + gameDown, 0);
    set('e8-us', 900, 0, 0);
    set('e8-tr', mvDown * 0.08 * 0.4, mvDown * 0.4, 0);
    set('e7', mvDown * 0.08, mvDown, usersMv * 7);
    set('wg-game', gameUp, gameDown, 0);
    set('mv-de-exit', L['e8-de'].up, L['e8-de'].down);
    set('mv-us-exit', 0, 0);
    set('mv-tr-exit', L['e8-tr'].up, L['e8-tr'].down);
    return { act, usersLocal, usersMv, links: L };
  }

  private add(key: string, bytesPerSec: number, dt: number) {
    this.counters.set(key, (this.counters.get(key) ?? hash(key, 0) * 5e9) + bytesPerSec * dt);
    return Math.floor(this.counters.get(key)!);
  }

  // ------------------------------------------------------------ one step

  /** Advance to time t and feed the store. Returns pushes (for events). */
  step(t: number, store: LatestStore, ingest: (p: Push, now: number) => void) {
    const dt = this.lastT === null ? 5 : Math.max(0.5, (t - this.lastT) / 1000);
    this.lastT = t;
    const e = this.effects(t);
    for (const [n, bt] of Object.entries(e.bootAt ?? {})) this.boot[n] = Math.max(this.boot[n] ?? 0, bt);
    for (const c of e.containerRestart ?? []) {
      const k = `${c.node}/${c.container}`;
      const cur = this.containers.get(k) ?? { startedAt: t - 7 * 86_400_000, restarts: 0 };
      if (cur.startedAt < c.at) this.containers.set(k, { startedAt: c.at, restarts: cur.restarts + 1 });
    }
    const w = this.world(t, e);
    const b = this.b;
    const ts = new Date(t).toISOString();
    const silent = new Set(e.silent ?? []);

    // Link rates in bytes/s for counters.
    const rateOf = (linkId: string, dir: 'up' | 'down') => (w.links[linkId]?.[dir] ?? 0) / 8;

    const agents = b.nodes.filter((n) => n.agent);
    const allChecks: { c: CheckSpec; link?: TopoLink }[] = [];
    for (const n of b.nodes) for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) allChecks.push({ c });
    for (const l of b.links) {
      for (const c of l.health ?? []) allChecks.push({ c, link: l });
      if (l.traffic) {
        allChecks.push({ c: l.traffic, link: l });
        if (l.traffic.fallback) allChecks.push({ c: l.traffic.fallback, link: l });
      }
      if (l.conns) allChecks.push({ c: l.conns, link: l });
    }
    for (const r of b.routes) {
      if (r.probe) allChecks.push({ c: r.probe });
      if (r.users) allChecks.push({ c: r.users });
    }

    const pushes = new Map<string, Push>();
    const mk = (node: string, kind: 'agent' | 'probe'): Push => {
      let p = pushes.get(node);
      if (!p) {
        p = {
          v: 1,
          agent: { node, kind, version: '0.1.0', hostname: node, config_sha: 'demo00000000', capabilities: {} },
          ts,
          seq: ++this.seq,
        };
        pushes.set(node, p);
      }
      return p;
    };

    // Host metrics.
    for (const n of agents) {
      const p = mk(n.id, 'agent');
      const base: Record<string, [number, number, number, number]> = {
        // cpu base, cpu peak add, mem %, disk %
        'relay-1': [3, 9, 22, 31],
        panel: [11, 16, 61, 48],
        'xbridger-node-3': [4, 11, 35, 28],
        'iran-mullvad': [2, 5, 30, 26],
        'france-exit': [5, 16, 52, 73],
        coolify: [13, 5, 58, 41],
      };
      const [cb, cp, mem, disk] = base[n.id] ?? [5, 5, 40, 40];
      let cpu = cb + cp * w.act + 3 * noise(`cpu:${n.id}`, t, 30) + 1.5 * jitter(`cj:${n.id}`, t);
      if (n.id === 'panel' && e.rwApiHung) cpu += 50;
      const bootT = this.boot[n.id];
      const up = t / 1000 - bootT;
      // NIC: everything touching the server.
      let nicBps = 30e3;
      for (const l of b.links) {
        const lw = w.links[l.id];
        if (!lw) continue;
        const on = (x: string) => x === n.id || this.hostOf.get(x) === n.id;
        if (on(l.from) || on(l.to)) nicBps += lw.up + lw.down;
      }
      const rx = this.add(`nic:${n.id}:rx`, (nicBps * 0.52) / 8, dt);
      const tx = this.add(`nic:${n.id}:tx`, (nicBps * 0.48) / 8, dt);
      p.host = {
        boot_time: bootT,
        uptime_s: Math.floor(up),
        cpu_pct: Math.max(0.5, Math.min(100, cpu)),
        num_cpu: n.id === 'coolify' ? 4 : n.id === 'france-exit' ? 1 : 2,
        load1: Math.max(0.01, (cpu / 100) * (n.id === 'coolify' ? 4 : 2) * (1 + 0.2 * noise(`l:${n.id}`, t))),
        load5: Math.max(0.01, (cpu / 100) * 1.8),
        load15: Math.max(0.01, (cpu / 100) * 1.7),
        mem_total_b: n.id === 'coolify' ? 8.3e9 : n.id === 'france-exit' ? 2.0e9 : 4.0e9,
        mem_used_b: 0,
        disks: [{ mount: '/', total_b: 35e9, used_b: 0, used_pct: disk + (n.id === 'france-exit' ? (t / 86_400_000) % 1 * 0.3 : 0) }],
        nics: [{ name: 'eth0', rx_b: rx, tx_b: tx, rx_p: Math.floor(rx / 900), tx_p: Math.floor(tx / 900) }],
      };
      p.host.mem_used_b = p.host.mem_total_b * (mem + 2 * noise(`m:${n.id}`, t, 600)) / 100;
      p.host.disks![0].used_b = (p.host.disks![0].total_b * p.host.disks![0].used_pct) / 100;
      p.agent.capabilities.listening = listening(n.id);
    }

    const probe = mk('probe', 'probe');
    const lan = (target: string) => /^(31\.56\.89\.|81\.12\.50\.23)/.test(target);

    for (const { c, link } of allChecks) {
      const host: string | undefined = c.params.host;
      switch (c.collector) {
        case 'systemd-unit': {
          const p = mk(host!, 'agent');
          (p.units ??= []).push({ name: c.params.unit, active: 'active', sub: 'running', n_restarts: 0 });
          (p.agent.capabilities.units ??= []).push(c.params.unit);
          break;
        }
        case 'docker-container': {
          const p = mk(host!, 'agent');
          const k = `${host}/${c.params.container}`;
          const st = this.containers.get(k) ?? { startedAt: t - (3 + hash(k, 2) * 9) * 86_400_000, restarts: 0 };
          if (!this.containers.has(k)) this.containers.set(k, st);
          const starting = t - st.startedAt < 30_000;
          const hasHealth = !['remnawave-nginx', 'remnanode', 'backend-app-1'].includes(c.params.container);
          (p.containers ??= []).push({
            name: c.params.container,
            state: 'running',
            health: hasHealth ? (starting ? 'starting' : 'healthy') : '',
            started_at: new Date(st.startedAt).toISOString(),
            restart_count: st.restarts,
          });
          (p.agent.capabilities.containers ??= []).push(c.params.container);
          break;
        }
        case 'nft-counter': {
          const p = mk(host!, 'agent');
          const lid = link?.id ?? '';
          for (const dir of ['up', 'down'] as const) {
            if (!c.params[dir]) continue;
            const name = nftName(c.key, dir);
            (p.nft ??= []).push({ name, bytes: this.add(`nft:${host}:${name}`, rateOf(lid, dir), dt), packets: 0 });
            (p.agent.capabilities.nft_counters ??= []).push(name);
          }
          break;
        }
        case 'ss-conns': {
          const p = mk(host!, 'agent');
          const ports: number[] = c.params.ports ?? [c.params.port];
          let total = link ? w.links[link.id]?.conns ?? 0 : 0;
          if (!link) {
            if (host === 'relay-1') total = w.links.e1.conns;
            else if (host === 'panel') total = w.links.e2.conns;
            else if (host === 'xbridger-node-3') total = w.links.e3.conns;
            else if (host === 'iran-mullvad') total = w.links.e7.conns;
          }
          ports.forEach((port, i) => {
            const share = ports.length === 1 ? 1 : [0.55, 0.05, 0.4][i] ?? 0.1;
            (p.ss ??= []).push({ id: ssId(c.key, port), port, side: c.params.side ?? 'local', established: Math.round(total * share) });
            (p.agent.capabilities.ss ??= []).push(ssId(c.key, port));
          });
          break;
        }
        case 'wg-transfer':
        case 'wg-handshake': {
          const p = mk(host!, 'agent');
          const iface = c.params.interface;
          const netns = c.params.netns;
          let wg = (p.wg ??= []).find((x) => x.iface === iface && (x.netns ?? '') === (netns ?? ''));
          if (!wg) {
            const lid = iface === 'wg-game' ? 'wg-game' : `e8-${iface.replace('wg-', '')}`;
            const dead = iface === 'wg-us' || (iface === 'wg-game' && e.gameTunnelDown);
            const sinceDead = iface === 'wg-us' ? 6.2 * 86400 : iface === 'wg-game' ? this.deadSince('wg-game', t, !!e.gameTunnelDown) : 0;
            const hsAge = dead ? sinceDead : 5 + ((t / 1000 + hash(iface, 3) * 120) % 115);
            const hostIsTo = link ? link.to === host : false;
            const upB = rateOf(lid, 'up');
            const downB = rateOf(lid, 'down');
            // tx from the host's point of view
            const txB = hostIsTo ? downB : upB;
            const rxB = hostIsTo ? upB : downB;
            wg = {
              netns,
              iface,
              listen_port: iface === 'wg-game' ? 51821 : undefined,
              peers: [
                {
                  key: hashKey(iface),
                  endpoint: iface === 'wg-game' ? 'home-isp:51821' : '',
                  last_handshake: Math.floor(t / 1000 - hsAge),
                  rx_b: this.add(`wg:${host}:${netns}/${iface}:rx`, dead ? 0 : rxB, dt),
                  tx_b: this.add(`wg:${host}:${netns}/${iface}:tx`, dead && iface === 'wg-game' ? 0 : txB + (dead ? 148 / 25 : 0), dt),
                },
              ],
            };
            p.wg!.push(wg);
            (p.agent.capabilities.wg ??= []).push(`${netns ? netns + '/' : ''}${iface}`);
          }
          break;
        }
        case 'tcp-connect': {
          const from = c.params.from ?? 'probe';
          const target: string = c.params.target;
          let ok = true;
          let error = '';
          if (target.startsWith('31.56.89.125:9000') && e.xbridgerConnDown) {
            ok = false;
            error = silent.has('xbridger-node-3') ? 'timeout' : 'refused';
          }
          if (target.startsWith('188.130.207.152') && e.franceDown) {
            ok = false;
            error = 'timeout';
          }
          const rtt = lan(target) && from !== 'probe' && from !== 'hub' ? 0.25 + 0.1 * jitter(`r:${target}`, t) : from === 'hub' ? 118 + 15 * noise(`rh:${target}`, t) : 14 + 6 * noise(`rp:${target}`, t);
          if (from === 'hub') {
            store.hubProbes.set(`tcp:${target}`, { ok, latencyMs: ok ? rtt : 0, error: ok ? undefined : error, at: t });
          } else if (!silent.has(from)) {
            const p = mk(from, from === 'probe' ? 'probe' : 'agent');
            (p.tcp ??= []).push({ target, ok, rtt_ms: ok ? rtt : 0, error: ok ? undefined : error, at: ts });
            (p.agent.capabilities.tcp ??= []).push(target);
          }
          break;
        }
        case 'http-probe': {
          const from = c.params.from ?? 'hub';
          const url: string = c.params.url;
          let ok = true;
          let status = 200;
          let latency = 40 + 25 * noise(`h:${url}`, t);
          let error: string | undefined;
          if (url.includes('wp.api.mohiraweb.ir')) {
            latency = 210 + 60 * noise('wp', t);
            if (e.rwApiHung) {
              ok = false;
              status = 0;
              latency = 10000;
              error = 'timeout after 10 s (CDN waiting for origin)';
            }
          } else if (url.includes('graphql')) {
            status = 400; // GET without a query
            latency = url.includes('node3') ? 95 + 30 * noise('n3', t) : 60 + 20 * noise('cen', t);
            if (url.includes('node3') && silent.has('xbridger-node-3')) {
              ok = false;
              status = 0;
              latency = 10000;
              error = 'timeout (Arvan 522)';
            }
          }
          if (from === 'hub') store.hubProbes.set(`http:${url}`, { ok, status, latencyMs: latency, error, at: t });
          else if (!silent.has(from)) {
            const p = mk(from, from === 'probe' ? 'probe' : 'agent');
            (p.http ??= []).push({ id: httpId(c.key), url, ok, status, latency_ms: latency, error, at: ts });
            (p.agent.capabilities.http ??= []).push(httpId(c.key));
          }
          break;
        }
        case 'tls-expiry': {
          const host2: string = c.params.host;
          const conn: string = c.params.connect ?? '';
          let days = 40 + hash(host2, 9) * 40 - (t / 86_400_000) % 1;
          if (conn === '81.12.50.23') days = host2.startsWith('pl.') ? -38 : -129; // 08 #2: origin certs expired
          store.hubProbes.set(`tls:${host2}|${conn}:${c.params.port ?? 443}`, { ok: true, latencyMs: 120, certDays: days, at: t });
          break;
        }
        case 'remnawave-node':
        case 'remnawave-online':
          break; // handled once below
        case 'xbridger-connection':
        case 'local-check': {
          const id = c.collector === 'xbridger-connection' ? c.params.check ?? 'xbridger-doctor' : c.params.check;
          if (silent.has(host!)) break;
          const p = mk(host!, 'agent');
          if ((p.checks ??= []).some((x) => x.id === id)) break;
          (p.agent.capabilities.checks ??= []).push(id);
          if (id === 'xbridger-doctor') {
            const down = e.xbridgerConnDown;
            p.checks.push({
              id,
              status: down ? 'down' : 'ok',
              detail: down ? 'DOWN: 1 of 1 connections not running' : 'HEALTHY: 1/1 connections running',
              data: {
                connections: [
                  down
                    ? { id: 'mt6snj5mlbnn', status: 'Error', desired: 'running', process: false, ports_open: false, reason: 'xray process not running after reboot (DB still said Connected)' }
                    : { id: 'mt6snj5mlbnn', status: 'Connected', desired: 'running', process: true, ports_open: true, latency: Math.round(175 + 40 * noise('xbl', t)) },
                ],
              },
              exit_code: down ? 2 : 0,
              duration_ms: 420,
              at: ts,
            });
          } else if (id === 'relay-forwards') {
            p.checks.push({ id, status: 'ok', value: 9, detail: '9 forwards, realm active (8 point to dead backends, 08 #4)', exit_code: 0, duration_ms: 35, at: ts });
          } else if (id === 'rw-api') {
            if (e.rwApiHung) {
              p.checks.push({ id, status: 'down', detail: 'no answer within 5 s', exit_code: 0, duration_ms: 5000, at: ts });
            } else {
              const ms = Math.round(18 + 8 * noise('api', t));
              p.checks.push({ id, status: 'ok', value: ms, detail: `HTTP 200 in ${ms} ms`, exit_code: 0, duration_ms: ms, at: ts });
            }
          } else if (id === 'remnawave-backup-age') {
            const age = ((t / 60000) % 30) + 0.6;
            p.checks.push({ id, status: age > 45 ? 'down' : age > 35 ? 'degraded' : 'ok', value: Math.round(age * 10) / 10, detail: `last upload ${Math.round(age)} min ago`, exit_code: 0, duration_ms: 12, at: ts });
          } else {
            p.checks.push({ id, status: 'ok', detail: 'ok', exit_code: 0, duration_ms: 20, at: ts });
          }
          break;
        }
        case 'log-match': {
          if (silent.has(host!)) break;
          const p = mk(host!, 'agent');
          const lines: string[] = [];
          for (const l of e.logs ?? []) {
            const k = `${l.node}:${l.at}:${l.line}`;
            if (l.node === host && c.key.includes(l.match) && l.at <= t && !this.emittedLogs.has(k)) {
              this.emittedLogs.add(k);
              lines.push(`${new Date(l.at).toISOString()} ${l.line}`);
            }
          }
          (p.logs ??= []).push({ id: logId(c.key), lines });
          (p.agent.capabilities.logs ??= []).push(logId(c.key));
          break;
        }
        case 'e2e-probe': {
          const route = c.params.route;
          let ok = true;
          let error = '';
          let lat = 0;
          const country = c.params.expect_country;
          if (route === 'main') {
            lat = 185 + 35 * noise('pm', t) + 15 * jitter('pmj', t);
            if (e.xbridgerConnDown) {
              ok = false;
              error = 'proxy_MAIN: dial tcp 31.56.89.125:9000: connection refused';
            } else if (e.franceDown) {
              ok = false;
              error = 'websocket: bad handshake (502 from Cloudflare)';
            }
          } else if (route === 'mullvad-us') {
            ok = false;
            error = 'socks out-mv-us: i/o timeout';
          } else lat = (route === 'mullvad-tr' ? 96 : 148) + 25 * noise(`p:${route}`, t);
          (probe.probes ??= []).push({ route, ok, latency_ms: ok ? lat : 0, exit_country: ok ? country : undefined, exit_ip: ok ? 'x.x.x.x' : undefined, error: ok ? undefined : error, at: ts });
          (probe.agent.capabilities.routes ??= []).push(route);
          break;
        }
      }
    }

    // Prometheus samples of the panel (xray-stats), updated every 30 s like the real panel.
    const promHosts = new Set(allChecks.filter((x) => x.c.collector === 'xray-stats').map((x) => x.c.params.host));
    for (const host of promHosts) {
      if (silent.has(host)) continue;
      const p = mk(host, 'agent');
      const uuid = (name: string) => `${hashKey(name)}-uuid`;
      for (const name of ['Local-Node', 'Iran-Mullvad']) {
        (p.prom ??= []).push({ target: 'remnawave', name: 'remnawave_node_basic_info', labels: { node_uuid: uuid(name), node_name: name }, value: 1 });
      }
      const outs: [string, string][] = [
        ['proxy_MAIN', 'e3'],
        ['DIRECT', 'e4'],
      ];
      const tick = Math.floor(t / 30000);
      for (const [tag, lid] of outs) {
        for (const what of ['upload', 'download'] as const) {
          const key = `prom:${tag}:${what}`;
          const rate = rateOf(lid, what === 'upload' ? 'up' : 'down');
          this.promAcc.set(key, (this.promAcc.get(key) ?? 3e10 * hash(key, 4)) + (e.rwApiHung ? 0 : rate * dt));
          if (tick !== this.promLast || !this.counters.has(key)) this.counters.set(key, this.promAcc.get(key)!);
          p.prom!.push({ target: 'remnawave', name: `remnawave_node_outbound_${what}_bytes`, labels: { node_uuid: uuid('Local-Node'), tag }, value: Math.floor(this.counters.get(key)!) });
        }
      }
      this.promLast = tick;
      (p.agent.capabilities.prom ??= []).push('remnawave');
    }

    // Hub-side: Remnawave API.
    if (e.rwApiHung) {
      store.remnawave = { at: t, ok: false, error: 'timeout after 10 s', nodes: store.remnawave?.nodes ?? [], onlineNow: null };
    } else {
      const nodes = [
        { name: 'Local-Node', users: w.usersLocal },
        { name: 'Iran-Mullvad', users: w.usersMv },
      ].map(({ name, users }) => ({
        uuid: `${hashKey(name)}-uuid`,
        name,
        isConnected: true,
        isNodeOnline: true,
        isXrayRunning: true,
        isDisabled: false,
        lastStatusMessage: null,
        xrayUptime: Math.floor((t - this.xrayStart[name]) / 1000),
        usersOnline: users,
      }));
      store.remnawave = { at: t, ok: true, nodes, onlineNow: w.usersLocal + w.usersMv + (w.act > 0.5 ? 1 : 0) };
    }

    // Hub-side: 3x-ui on France.
    if (e.franceDown) store.xui.set('france', { at: t, ok: false, error: 'connect: connection timed out', inbounds: [] });
    else {
      const up = this.add('xui:france:Cloudflare-WS:up', rateOf('e5b', 'up'), dt);
      const down = this.add('xui:france:Cloudflare-WS:down', rateOf('e5b', 'down'), dt);
      store.xui.set('france', { at: t, ok: true, inbounds: [{ remark: 'Cloudflare-WS', tag: 'inbound-443', enable: true, up, down }] });
      store.counter('xui:france:Cloudflare-WS:up', t, up);
      store.counter('xui:france:Cloudflare-WS:down', t, down);
    }

    for (const [node, p] of pushes) {
      if (silent.has(node)) continue;
      ingest(p, t);
    }
  }

  private deadSinceMap = new Map<string, number>();
  private deadSince(k: string, t: number, dead: boolean): number {
    if (!dead) {
      this.deadSinceMap.delete(k);
      return 0;
    }
    if (!this.deadSinceMap.has(k)) this.deadSinceMap.set(k, t - 110_000);
    return (t - this.deadSinceMap.get(k)!) / 1000;
  }
}

function hashKey(s: string): string {
  return Math.floor(hash(s, 7) * 0xffffffff).toString(16).padStart(8, '0');
}

/** Listening sockets per server (for drift): relay-1 still listens on its 8 dead forwards (08 #4). */
function listening(node: string): string[] {
  const m: Record<string, string[]> = {
    'relay-1': ['tcp/22', 'tcp/443', 'tcp/2053', 'tcp/2083', 'tcp/2087', 'tcp/2096', 'tcp/4443', 'tcp/8443', 'tcp/9443', 'tcp/10443'],
    panel: ['tcp/22', 'tcp/80', 'tcp/443', 'tcp/2053', 'tcp/2222'],
    'xbridger-node-3': ['tcp/22', 'tcp/80', 'tcp/443', 'tcp/2087', 'tcp/8443', 'tcp/9000'],
    'iran-mullvad': ['tcp/22', 'tcp/443', 'tcp/2053', 'tcp/2222', 'tcp/8443', 'udp/51821'],
    'france-exit': ['tcp/22', 'tcp/443', 'tcp/2096', 'tcp/57111'],
    coolify: ['tcp/22', 'tcp/80', 'tcp/443', 'tcp/8000'],
  };
  return m[node] ?? ['tcp/22'];
}
