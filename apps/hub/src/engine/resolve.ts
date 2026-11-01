import { Bundle, CheckSpec, Health, TopoLink } from '../contract';
import { CheckObs, LinkObs, NodeObs, Observations, RouteObs, emptyNodeObs } from './observations';
import { LatestStore, promKey } from './store';
import { httpId, logId, nftName, PROM_REMNAWAVE, ssId } from './ids';

/**
 * Resolvers turn raw data in the LatestStore into check results, one function
 * per collector of the closed catalog. Unknown = no fresh data (grey).
 */

export interface ResolveCtx {
  b: Bundle;
  s: LatestStore;
  now: number;
  staleMs: number;
}

const dur = (v: any, def: number): number => {
  if (typeof v === 'number') return v * 1000;
  if (typeof v !== 'string' || !v) return def;
  const m = /^([0-9.]+)(ms|s|m|h|d)$/.exec(v.trim());
  if (!m) return def;
  const n = parseFloat(m[1]);
  return n * ({ ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 } as Record<string, number>)[m[2]];
};

const fmtMs = (ms: number) => (ms < 1 ? `${ms.toFixed(2)} ms` : ms < 10 ? `${ms.toFixed(1)} ms` : `${Math.round(ms)} ms`);

export function fmtAge(sec: number): string {
  if (sec < 90) return `${Math.round(sec)}s`;
  if (sec < 600) return Math.round(sec % 60) === 0 ? `${Math.round(sec / 60)}m` : `${Math.floor(sec / 60)}m${String(Math.round(sec % 60)).padStart(2, '0')}s`;
  if (sec < 5400) return `${Math.round(sec / 60)}m`;
  if (sec < 172800) {
    const m = Math.round(sec / 60);
    return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`;
  }
  return `${Math.round(sec / 86400)}d`;
}

function stale(ctx: ResolveCtx, at: number | null, intervalMs = 0): boolean {
  if (at === null) return true;
  return ctx.now - at > Math.max(ctx.staleMs, intervalMs * 3);
}

const none = (source: string, detail = 'no data'): CheckObs => ({ status: 'unknown', value: null, detail, observedAt: null, source });

function agentPush(ctx: ResolveCtx, node: string) {
  const a = ctx.s.agents.get(node);
  if (!a) return null;
  return a;
}

/** Status from an expect list like "2xx,401". */
function codeAccepted(code: number, expect: string): boolean {
  for (const part of String(expect || '2xx,3xx').split(',')) {
    const p = part.trim();
    if (/^\dxx$/.test(p) && Math.floor(code / 100) === Number(p[0])) return true;
    if (/^\d{3}$/.test(p) && code === Number(p)) return true;
  }
  return false;
}

type Resolver = (c: CheckSpec, ctx: ResolveCtx, owner: { linkId?: string }) => CheckObs;

const resolvers: Record<string, Resolver> = {
  'tcp-connect': (c, ctx) => {
    const from = c.params.from ?? 'probe';
    const target = c.params.target;
    const interval = dur(c.params.interval, 15_000);
    if (from === 'hub') {
      const r = ctx.s.hubResult(`tcp:${target}`);
      if (!r || stale(ctx, r.at, interval)) return none('hub');
      return r.ok
        ? { status: 'ok', value: r.latencyMs, detail: `${target} open · ${fmtMs(r.latencyMs)}`, observedAt: r.at, source: 'hub' }
        : { status: 'down', value: null, detail: `${target} ${r.error ?? 'failed'}`, observedAt: r.at, source: 'hub' };
    }
    const a = agentPush(ctx, from);
    const src = from === 'probe' ? 'probe' : `agent:${from}`;
    if (!a) return none(src, `${from} has not reported`);
    const r = a.push.tcp?.find((t) => t.target === target);
    if (!r) return none(src, `${from} does not check ${target} (agent config)`);
    const at = Date.parse(r.at) || a.lastSeen;
    if (stale(ctx, at, interval)) return none(src, `last result ${fmtAge((ctx.now - at) / 1000)} old`);
    return r.ok
      ? { status: 'ok', value: r.rtt_ms, detail: `${target} open · ${fmtMs(r.rtt_ms)}`, observedAt: at, source: src }
      : { status: 'down', value: null, detail: `${target} ${r.error || 'failed'} (from ${from})`, observedAt: at, source: src };
  },

  'http-probe': (c, ctx) => {
    const from = c.params.from ?? 'hub';
    const interval = dur(c.params.interval, 30_000);
    let ok: boolean, code: number | undefined, latency: number, err: string | undefined, at: number, src: string;
    if (from === 'hub') {
      const r = ctx.s.hubResult(`http:${c.params.url}`);
      src = 'hub';
      if (!r) return none(src);
      ({ ok, status: code, latencyMs: latency, error: err, at } = r);
    } else {
      src = from === 'probe' ? 'probe' : `agent:${from}`;
      const a = agentPush(ctx, from);
      if (!a) return none(src, `${from} has not reported`);
      const r = a.push.http?.find((h) => h.id === httpId(c.key));
      if (!r) return none(src, `${from} does not probe ${c.params.url} (agent config)`);
      ok = r.ok;
      code = r.status;
      latency = r.latency_ms;
      err = r.error;
      at = Date.parse(r.at) || a.lastSeen;
    }
    if (stale(ctx, at, interval)) return none(src, 'stale');
    const max = dur(c.params.max_latency, 10_000);
    const slow = c.params.slow ? dur(c.params.slow, 0) : 0;
    if (!ok && !code) return { status: 'down', value: null, detail: `${err ?? 'no answer'}`, observedAt: at, source: src };
    if (latency > max) return { status: 'down', value: latency, detail: `answered in ${(latency / 1000).toFixed(1)} s (limit ${max / 1000} s)`, observedAt: at, source: src };
    if (code && !codeAccepted(code, c.params.expect)) return { status: 'down', value: latency, detail: `HTTP ${code} (expected ${c.params.expect})`, observedAt: at, source: src };
    if (slow && latency > slow) return { status: 'degraded', value: latency, detail: `slow: ${fmtMs(latency)}`, observedAt: at, source: src };
    return { status: 'ok', value: latency, detail: `HTTP ${code ?? ''} · ${fmtMs(latency)}`.trim(), observedAt: at, source: src };
  },

  'tls-expiry': (c, ctx) => {
    const key = `tls:${c.params.host}|${c.params.connect ?? ''}:${c.params.port ?? 443}`;
    const r = ctx.s.hubResult(key);
    if (!r || stale(ctx, r.at, dur(c.params.interval, 3_600_000))) return none('hub');
    if (!r.ok || r.certDays === undefined) return { status: 'down', value: null, detail: `TLS handshake failed: ${r.error ?? 'error'}`, observedAt: r.at, source: 'hub' };
    const days = r.certDays;
    const warn = Number(c.params.warn_days ?? 14);
    const where = c.params.connect ? ` (origin ${c.params.connect})` : '';
    if (days < 0) return { status: 'down', value: days, detail: `certificate expired ${Math.round(-days)} days ago${where}`, observedAt: r.at, source: 'hub' };
    if (days < warn) return { status: 'degraded', value: days, detail: `certificate expires in ${Math.round(days)} days${where}`, observedAt: r.at, source: 'hub' };
    return { status: 'ok', value: days, detail: `certificate valid for ${Math.round(days)} days${where}`, observedAt: r.at, source: 'hub' };
  },

  'ss-conns': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    if (!a || stale(ctx, a.lastSeen)) return none(`agent:${host}`);
    const ports: number[] = c.params.ports ?? [c.params.port];
    let total = 0;
    let found = 0;
    for (const p of ports) {
      const r = a.push.ss?.find((s) => s.id === ssId(c.key, p));
      if (r) {
        total += r.established;
        found++;
      }
    }
    if (!found) return none(`agent:${host}`, 'not configured on the agent');
    return { status: 'ok', value: total, detail: `${total} established`, observedAt: a.lastSeen, source: `agent:${host}` };
  },

  'wg-handshake': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    if (!a || stale(ctx, a.lastSeen)) return none(`agent:${host}`);
    const w = a.push.wg?.find((x) => x.iface === c.params.interface && (x.netns ?? '') === (c.params.netns ?? ''));
    if (!w) return none(`agent:${host}`, `${c.params.netns ? c.params.netns + '/' : ''}${c.params.interface} not reported`);
    const last = Math.max(0, ...w.peers.map((p) => p.last_handshake));
    const tsSec = Date.parse(a.push.ts) / 1000 || a.lastSeen / 1000;
    if (!last) return { status: 'down', value: null, detail: 'no handshake yet', observedAt: a.lastSeen, source: `agent:${host}` };
    const age = Math.max(0, tsSec - last);
    const max = dur(c.params.max_age, 180_000) / 1000;
    return {
      status: age > max ? 'down' : 'ok',
      value: age,
      detail: `last handshake ${fmtAge(age)} ago${age > max ? ` (limit ${fmtAge(max)})` : ''}`,
      observedAt: a.lastSeen,
      source: `agent:${host}`,
    };
  },

  'remnawave-node': (c, ctx) => {
    const rw = ctx.s.remnawave;
    if (!rw || stale(ctx, rw.at, 30_000)) return none('hub', 'Remnawave API not polled yet');
    if (!rw.ok) return none('hub', `Remnawave API: ${rw.error ?? 'error'}`);
    const n = rw.nodes.find((x) => x.name === c.params.node);
    if (!n) return { status: 'down', value: null, detail: `node ${c.params.node} not found in Remnawave`, observedAt: rw.at, source: 'hub' };
    if (n.isDisabled) return { status: 'degraded', value: null, detail: 'node disabled in Remnawave', observedAt: rw.at, source: 'hub' };
    const ok = n.isConnected && n.isNodeOnline && n.isXrayRunning;
    const detail = ok
      ? `connected · Xray up ${fmtAge(n.xrayUptime)} · ${n.usersOnline} online`
      : `${!n.isConnected ? 'disconnected' : !n.isXrayRunning ? 'Xray not running' : 'offline'}${n.lastStatusMessage ? ': ' + n.lastStatusMessage : ''}`;
    return { status: ok ? 'ok' : 'down', value: n.xrayUptime, detail, observedAt: rw.at, source: 'hub' };
  },

  'remnawave-online': (c, ctx) => {
    const rw = ctx.s.remnawave;
    if (!rw || !rw.ok || stale(ctx, rw.at, 30_000)) return none('hub', rw?.error ? `Remnawave API: ${rw.error}` : 'no data');
    if (!c.params.node) {
      const v = rw.onlineNow ?? rw.nodes.reduce((s, n) => s + n.usersOnline, 0);
      return { status: 'ok', value: v, detail: `${v} online (all nodes)`, observedAt: rw.at, source: 'hub' };
    }
    const n = rw.nodes.find((x) => x.name === c.params.node);
    if (!n) return none('hub', `node ${c.params.node} not found`);
    return { status: 'ok', value: n.usersOnline, detail: `${n.usersOnline} online`, observedAt: rw.at, source: 'hub' };
  },

  'xbridger-connection': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    const src = `agent:${host}`;
    if (!a) return none(src, `${host} has not reported`);
    const ch = a.push.checks?.find((x) => x.id === (c.params.check ?? 'xbridger-doctor'));
    if (!ch) return none(src, `local check ${c.params.check ?? 'xbridger-doctor'} not installed`);
    const at = Date.parse(ch.at) || a.lastSeen;
    if (stale(ctx, at, 60_000)) return none(src, 'stale');
    const conns: any[] = ch.data?.connections ?? [];
    const conn = conns.find((x) => x.id === c.params.connection);
    if (!conn) return { status: 'down', value: null, detail: `connection ${c.params.connection} not found (doctor: ${ch.detail ?? ch.status})`, observedAt: at, source: src };
    const running = conn.status === 'Connected' && conn.process !== false && conn.ports_open !== false;
    const latency = typeof conn.latency === 'number' && conn.latency >= 0 ? conn.latency : null;
    return {
      status: running ? 'ok' : 'down',
      value: latency,
      detail: running
        ? `${c.params.connection} running · ports open${latency !== null ? ` · ${latency} ms` : ''}`
        : `${c.params.connection} ${conn.status ?? 'not running'}${conn.reason ? ': ' + conn.reason : ''}`,
      observedAt: at,
      source: src,
    };
  },

  '3xui-inbound': (c, ctx) => {
    const x = ctx.s.xui.get(c.params.panel);
    if (!x || stale(ctx, x.at, 30_000)) return none('hub');
    if (!x.ok) return { status: 'down', value: null, detail: `3x-ui API: ${x.error ?? 'error'}`, observedAt: x.at, source: 'hub' };
    const ib = x.inbounds.find((i) => i.remark === c.params.inbound || i.tag === c.params.inbound);
    if (!ib) return { status: 'down', value: null, detail: `inbound ${c.params.inbound} not found`, observedAt: x.at, source: 'hub' };
    return { status: ib.enable ? 'ok' : 'down', value: null, detail: ib.enable ? `inbound ${ib.remark} enabled` : `inbound ${ib.remark} disabled`, observedAt: x.at, source: 'hub' };
  },

  'systemd-unit': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    if (!a || stale(ctx, a.lastSeen)) return none(`agent:${host}`);
    const u = a.push.units?.find((x) => x.name === c.params.unit);
    if (!u) return none(`agent:${host}`, `${c.params.unit} not reported`);
    const st: Health = u.active === 'active' ? 'ok' : u.active === 'activating' || u.active === 'reloading' ? 'degraded' : 'down';
    return { status: st, value: null, detail: `${u.active} (${u.sub})`, observedAt: a.lastSeen, source: `agent:${host}` };
  },

  'docker-container': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    if (!a || stale(ctx, a.lastSeen, 15_000)) return none(`agent:${host}`);
    const ct = a.push.containers?.find((x) => x.name === c.params.container);
    if (!ct) return none(`agent:${host}`, `${c.params.container} not reported`);
    const up = ct.started_at ? (a.lastSeen - Date.parse(ct.started_at)) / 1000 : null;
    const upTxt = up !== null && up > 0 ? ` · up ${fmtAge(up)}` : '';
    if (ct.state !== 'running') return { status: 'down', value: ct.restart_count, detail: `${ct.state}`, observedAt: a.lastSeen, source: `agent:${host}` };
    if (ct.health === 'unhealthy')
      return { status: c.params.require_healthy === false ? 'degraded' : 'down', value: ct.restart_count, detail: `unhealthy${upTxt}`, observedAt: a.lastSeen, source: `agent:${host}` };
    if (ct.health === 'starting') return { status: 'degraded', value: ct.restart_count, detail: `starting${upTxt}`, observedAt: a.lastSeen, source: `agent:${host}` };
    return { status: 'ok', value: ct.restart_count, detail: `running${ct.health ? ' · ' + ct.health : ''}${upTxt}`, observedAt: a.lastSeen, source: `agent:${host}` };
  },

  'boot-time': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    if (!a || !a.bootTime) return none(`agent:${host}`);
    const up = ctx.now / 1000 - a.bootTime;
    const win = dur(c.params.window, 1_800_000) / 1000;
    if (up < win) return { status: 'degraded', value: up, detail: `rebooted ${fmtAge(up)} ago`, observedAt: a.lastSeen, source: `agent:${host}` };
    return { status: 'ok', value: up, detail: `up ${fmtAge(up)}`, observedAt: a.lastSeen, source: `agent:${host}` };
  },

  'log-match': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    const src = `agent:${host}`;
    const last = ctx.s.logMatchAt.get(c.key) ?? null;
    if (c.params.mode === 'freshness') {
      if (last === null) return none(src, 'no matching line seen since the hub started');
      const age = (ctx.now - last) / 1000;
      const max = dur(c.params.max_age, 2_100_000) / 1000;
      return { status: age > max ? 'down' : 'ok', value: age, detail: `last match ${fmtAge(age)} ago`, observedAt: last, source: src };
    }
    if (!a || stale(ctx, a.lastSeen)) return none(src);
    // event mode: degraded for 10 minutes after a matching line
    if (last !== null && ctx.now - last < 600_000) {
      return { status: 'degraded', value: (ctx.now - last) / 1000, detail: `${c.params.event ?? 'matched'} ${fmtAge((ctx.now - last) / 1000)} ago`, observedAt: last, source: src };
    }
    return { status: 'ok', value: null, detail: last ? `last ${c.params.event ?? 'match'} ${fmtAge((ctx.now - last) / 1000)} ago` : 'no matches', observedAt: a.lastSeen, source: src };
  },

  'e2e-probe': (c, ctx) => {
    const route = c.params.route;
    const p = ctx.s.probes.get(route);
    if (!p || stale(ctx, p.lastAt, dur(c.params.interval, 30_000))) return none('probe', 'probe has not reported this route');
    const need = Number(c.params.failures ?? 3);
    const r = p.last;
    const wrongCountry = r.ok && c.params.expect_country && r.exit_country && r.exit_country !== c.params.expect_country;
    if (wrongCountry) return { status: 'down', value: r.latency_ms, detail: `exit country ${r.exit_country}, expected ${c.params.expect_country}`, observedAt: p.lastAt, source: 'probe' };
    if (r.ok) return { status: 'ok', value: r.latency_ms, detail: `204 in ${fmtMs(r.latency_ms)} · exit ${r.exit_country ?? '?'}`, observedAt: p.lastAt, source: 'probe' };
    return {
      status: p.failures >= need ? 'down' : 'degraded',
      value: null,
      detail: `${r.error ?? 'failed'} (${p.failures} in a row)`,
      observedAt: p.lastAt,
      source: 'probe',
    };
  },

  'local-check': (c, ctx) => {
    const host = c.params.host;
    const a = agentPush(ctx, host);
    const src = `agent:${host}`;
    if (!a) return none(src, `${host} has not reported`);
    const ch = a.push.checks?.find((x) => x.id === c.params.check);
    if (!ch) return none(src, `local check ${c.params.check} is not installed on ${host}`);
    const at = Date.parse(ch.at) || a.lastSeen;
    if (stale(ctx, at, dur(c.params.interval, 30_000))) return none(src, 'stale');
    const st = (['ok', 'degraded', 'down'].includes(ch.status) ? ch.status : 'unknown') as Health;
    return { status: st, value: ch.value ?? null, detail: ch.detail ?? ch.status, observedAt: at, source: src };
  },

  // Traffic-only collectors still produce a check result (ok when data flows).
  'nft-counter': (c, ctx) => trafficObs(c, ctx),
  'wg-transfer': (c, ctx) => trafficObs(c, ctx),
  'xray-stats': (c, ctx) => trafficObs(c, ctx),
};

function trafficObs(c: CheckSpec, ctx: ResolveCtx): CheckObs {
  const t = traffic(c, ctx, undefined);
  if (!t) return none(c.params.host ? `agent:${c.params.host}` : 'hub');
  return { status: 'ok', value: (t.up ?? 0) + (t.down ?? 0), detail: 'counting', observedAt: ctx.now, source: c.params.host ? `agent:${c.params.host}` : 'hub' };
}

/** Throughput (bps) for a traffic spec, or null when there is no data. */
export function traffic(c: CheckSpec, ctx: ResolveCtx, link: TopoLink | undefined): { up: number | null; down: number | null } | null {
  const host = c.params.host;
  switch (c.collector) {
    case 'nft-counter': {
      const up = ctx.s.rate(`nft:${host}:${nftName(c.key, 'up')}`, ctx.now);
      const down = c.params.down ? ctx.s.rate(`nft:${host}:${nftName(c.key, 'down')}`, ctx.now) : null;
      if (up === null && down === null) return null;
      return { up, down };
    }
    case 'wg-transfer': {
      const id = `${c.params.netns ?? ''}/${c.params.interface}`;
      const rx = ctx.s.rate(`wg:${host}:${id}:rx`, ctx.now);
      const tx = ctx.s.rate(`wg:${host}:${id}:tx`, ctx.now);
      if (rx === null && tx === null) return null;
      let upIsTx = true;
      if (c.params.up) upIsTx = c.params.up === 'tx';
      else if (link) upIsTx = host === link.from;
      return upIsTx ? { up: tx, down: rx } : { up: rx, down: tx };
    }
    case 'xray-stats': {
      const a = ctx.s.agents.get(host);
      if (!a) return null;
      const info = a.push.prom?.find((p) => p.name === 'remnawave_node_basic_info' && p.labels?.node_name === c.params.node);
      const uuid = info?.labels?.node_uuid;
      if (!uuid) return null;
      const dir = c.params.outbound ? 'outbound' : 'inbound';
      const tag = c.params.outbound ?? c.params.inbound;
      // Match by label subset: the panel adds labels of its own (e.g. app="remnawave").
      const k = (what: string) => {
        const name = `remnawave_node_${dir}_${what}_bytes`;
        const s = a.push.prom?.find((p) => p.name === name && p.labels?.node_uuid === uuid && p.labels?.tag === tag);
        return `prom:${host}:${promKey(name, s?.labels ?? { node_uuid: uuid, tag })}`;
      };
      // The panel records node stats every 30 s: use a 90 s window.
      const up = ctx.s.rate(k('upload'), ctx.now, 90_000, 90_000);
      const down = ctx.s.rate(k('download'), ctx.now, 90_000, 90_000);
      if (up === null && down === null) return null;
      return { up, down };
    }
    case '3xui-inbound': {
      const up = ctx.s.rate(`xui:${c.params.panel}:${c.params.inbound}:up`, ctx.now, 60_000, 60_000);
      const down = ctx.s.rate(`xui:${c.params.panel}:${c.params.inbound}:down`, ctx.now, 60_000, 60_000);
      if (up === null && down === null) return null;
      return { up, down };
    }
  }
  return null;
}

export function resolveCheck(c: CheckSpec, ctx: ResolveCtx, owner: { linkId?: string } = {}): CheckObs {
  const r = resolvers[c.collector];
  if (!r) return none('hub', `collector ${c.collector} not implemented`);
  try {
    return r(c, ctx, owner);
  } catch (e: any) {
    return none('hub', `resolver error: ${e?.message ?? e}`);
  }
}

/** Resolve everything in the bundle into Observations for time `now`. */
export function observe(b: Bundle, s: LatestStore, now: number): Observations {
  const ctx: ResolveCtx = { b, s, now, staleMs: (b.map.settings.stale_after || 30) * 1000 };
  const obs: Observations = { at: now, checks: {}, nodes: {}, links: {}, routes: {}, metrics: {} };

  for (const n of b.nodes) {
    for (const c of n.checks ?? []) obs.checks[c.key] = resolveCheck(c, ctx);
    for (const m of n.metrics ?? []) {
      const r = resolveCheck(m, ctx);
      obs.checks[m.key] = r;
      if (['nft-counter', 'wg-transfer', 'xray-stats'].includes(m.collector)) {
        const t = traffic(m, ctx, undefined);
        obs.metrics[m.key] = t ? (t.up ?? 0) + (t.down ?? 0) : null;
      } else obs.metrics[m.key] = r.value;
    }
    const no = emptyNodeObs();
    const a = s.agents.get(n.id);
    if (n.agent && a) {
      const h = a.push.host;
      no.agentAge = Math.max(0, (now - a.lastSeen) / 1000);
      no.agentVersion = a.push.agent.version;
      no.configSha = a.push.agent.config_sha;
      no.bootTime = a.bootTime;
      if (h && !stale(ctx, a.lastSeen)) {
        no.cpu = h.cpu_pct;
        no.mem = h.mem_total_b ? (h.mem_used_b / h.mem_total_b) * 100 : null;
        no.disk = h.disks?.find((d) => d.mount === '/')?.used_pct ?? h.disks?.[0]?.used_pct ?? null;
        no.load = h.load1;
        no.uptime = a.bootTime ? now / 1000 - a.bootTime : h.uptime_s;
        const rx = s.rate(`nic:${n.id}:*:rx`, now);
        const tx = s.rate(`nic:${n.id}:*:tx`, now);
        no.net = rx === null && tx === null ? null : (rx ?? 0) + (tx ?? 0);
      }
    }
    obs.nodes[n.id] = no;
  }

  for (const l of b.links) {
    const lo: LinkObs = { up: null, down: null, conns: null, src: null };
    for (const c of l.health ?? []) obs.checks[c.key] = resolveCheck(c, ctx, { linkId: l.id });
    if (l.traffic) {
      let t = traffic(l.traffic, ctx, l);
      lo.src = t ? 'primary' : null;
      if (!t && l.traffic.fallback) {
        t = traffic(l.traffic.fallback, ctx, l);
        if (t) lo.src = 'fallback';
      }
      if (t) {
        lo.up = t.up;
        lo.down = t.down;
      }
      obs.checks[l.traffic.key] = t
        ? { status: 'ok', value: (t.up ?? 0) + (t.down ?? 0), detail: lo.src === 'fallback' ? 'from fallback source' : 'ok', observedAt: now, source: 'hub' }
        : none('hub', 'no traffic data');
    }
    if (l.conns) {
      const r = resolveCheck(l.conns, ctx);
      obs.checks[l.conns.key] = r;
      lo.conns = r.value;
    }
    obs.links[l.id] = lo;
  }

  for (const r of b.routes) {
    const ro: RouteObs = { users: null, usersSource: null, probeFailures: 0, latencyMs: null, exitCountry: null };
    if (r.users) {
      const u = resolveCheck(r.users, ctx);
      obs.checks[r.users.key] = u;
      if (u.value !== null) {
        ro.users = u.value;
        ro.usersSource = 'live';
      }
    }
    if (ro.users === null && r.users_estimate) {
      ro.users = r.users_estimate;
      ro.usersSource = 'estimate';
    }
    if (r.probe) {
      const p = resolveCheck(r.probe, ctx);
      obs.checks[r.probe.key] = p;
      const st = s.probes.get(r.id);
      ro.probeFailures = st?.failures ?? 0;
      ro.latencyMs = p.status === 'ok' ? p.value : null;
      ro.exitCountry = st?.last.exit_country ?? null;
    }
    obs.routes[r.id] = ro;
  }
  return obs;
}

/** Log-match: scan new lines for each log-match check; returns matches for events. */
export function scanLogs(b: Bundle, s: LatestStore, node: string, since: number): { check: CheckSpec; line: string; t: number }[] {
  const out: { check: CheckSpec; line: string; t: number }[] = [];
  const all: CheckSpec[] = [];
  for (const n of b.nodes) all.push(...(n.checks ?? []));
  for (const l of b.links) all.push(...(l.health ?? []));
  for (const c of all) {
    if (c.collector !== 'log-match' || c.params.host !== node) continue;
    let re: RegExp;
    try {
      re = new RegExp(String(c.params.pattern).replace(/^\(\?i\)/, ''), String(c.params.pattern).startsWith('(?i)') ? 'i' : '');
    } catch {
      continue;
    }
    for (const l of s.logLinesSince(node, logId(c.key), since)) {
      if (re.test(l.line)) {
        out.push({ check: c, line: l.line, t: l.t });
        s.logMatchAt.set(c.key, Math.max(s.logMatchAt.get(c.key) ?? 0, l.t));
      }
    }
  }
  return out;
}

export { PROM_REMNAWAVE };
