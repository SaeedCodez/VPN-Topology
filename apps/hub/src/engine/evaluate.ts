import {
  ActiveAlert,
  Bundle,
  CheckSpec,
  CheckState,
  Health,
  Impact,
  LinkState,
  NodeState,
  Overall,
  RootCause,
  RouteState,
  RunbookCmd,
  Snapshot,
  TopoLink,
  TopoNode,
  TopoRoute,
} from '../contract';
import { CheckObs, Observations } from './observations';
import { fmtAge } from './resolve';

/**
 * Stage 2 of the pipeline: Observations + topology → Snapshot. Pure and
 * deterministic, so the live board, the CLI and time travel all agree.
 *
 * Root cause: every route is walked from the users toward the exit; the first
 * failing hop is the root and the hops after it are "affected". A failing link
 * whose destination node is itself failing (or whose agent went silent) points
 * at the node instead, because a dead server makes its incoming links fail.
 */

export interface EvalContext {
  mode: 'demo' | 'live';
  live: boolean;
  version: number;
  since: Map<string, number>; // element key → ms of last status change
  incidentSince: Map<string, number>;
  alerts: ActiveAlert[];
}

const RANK: Record<Health, number> = { ok: 0, unknown: 1, degraded: 2, down: 3 };

export function worst(a: Health, b: Health): Health {
  return RANK[a] >= RANK[b] ? a : b;
}

const iso = (ms: number) => new Date(ms).toISOString();

function checkState(spec: CheckSpec, o: CheckObs | undefined): CheckState {
  const obs = o ?? { status: 'unknown', value: null, detail: 'no data', observedAt: null, source: 'hub' };
  let effective: Health = obs.status;
  if (obs.status === 'down') effective = spec.on_fail === 'degraded' ? 'degraded' : 'down';
  return {
    key: spec.key,
    id: spec.id,
    title: spec.title || defaultTitle(spec),
    collector: spec.collector,
    status: obs.status,
    effective,
    value: obs.value,
    unit: unitOf(spec),
    detail: obs.detail,
    observedAt: obs.observedAt ? iso(obs.observedAt) : null,
    source: obs.source,
  };
}

export function defaultTitle(c: CheckSpec): string {
  const p = c.params;
  switch (c.collector) {
    case 'tcp-connect':
      return `TCP ${p.target} from ${p.from}`;
    case 'http-probe':
      return `HTTP ${p.url}`;
    case 'tls-expiry':
      return `TLS ${p.host}${p.connect ? ' @' + p.connect : ''}`;
    case 'systemd-unit':
      return `unit ${p.unit}`;
    case 'docker-container':
      return `container ${p.container}`;
    case 'wg-handshake':
      return `handshake ${p.netns ? p.netns + '/' : ''}${p.interface}`;
    case 'remnawave-node':
      return `Remnawave node ${p.node}`;
    case 'remnawave-online':
      return `online users${p.node ? ' on ' + p.node : ''}`;
    case 'xbridger-connection':
      return `XBridger connection ${p.connection}`;
    case '3xui-inbound':
      return `3x-ui inbound ${p.inbound}`;
    case 'boot-time':
      return 'boot time';
    case 'log-match':
      return p.event || `log ${p.path}`;
    case 'e2e-probe':
      return `end-to-end probe${p.expect_country ? ' (exit ' + p.expect_country + ')' : ''}`;
    case 'local-check':
      return `local check ${p.check}`;
    case 'ss-conns':
      return `connections :${p.port ?? (p.ports ?? []).join(',')}`;
    default:
      return c.collector;
  }
}

function unitOf(c: CheckSpec): string {
  if (c.unit) return c.unit;
  switch (c.collector) {
    case 'tcp-connect':
    case 'http-probe':
    case 'e2e-probe':
    case 'xbridger-connection':
      return 'ms';
    case 'tls-expiry':
      return 'days';
    case 'wg-handshake':
    case 'boot-time':
    case 'remnawave-node':
      return 's';
    case 'remnawave-online':
      return 'users';
    case 'ss-conns':
      return 'conns';
    case 'nft-counter':
    case 'wg-transfer':
    case 'xray-stats':
    case '3xui-inbound':
      return 'bps';
  }
  return '';
}

function combine(checks: CheckState[]): { status: Health; observed: boolean } {
  if (!checks.length) return { status: 'ok', observed: false };
  let st: Health = 'ok';
  let known = 0;
  for (const c of checks) {
    if (c.effective === 'unknown') continue;
    known++;
    st = worst(st, c.effective);
  }
  if (!known) return { status: 'unknown', observed: true };
  return { status: st, observed: true };
}

export interface Graph {
  nodes: Map<string, TopoNode>;
  links: Map<string, TopoLink>;
  out: Map<string, TopoLink[]>;
  in: Map<string, TopoLink[]>;
  routesOf: Map<string, string[]>; // element key → route ids
}

export function graphOf(b: Bundle): Graph {
  const g: Graph = { nodes: new Map(), links: new Map(), out: new Map(), in: new Map(), routesOf: new Map() };
  for (const n of b.nodes) {
    g.nodes.set(n.id, n);
    g.out.set(n.id, []);
    g.in.set(n.id, []);
  }
  for (const l of b.links) {
    g.links.set(l.id, l);
    g.out.get(l.from)?.push(l);
    g.in.get(l.to)?.push(l);
  }
  for (const r of b.routes) {
    for (const h of r.hops) push(g.routesOf, `link:${h}`, r.id);
    for (const n of r.nodes) push(g.routesOf, `node:${n}`, r.id);
  }
  return g;
}

function push<K, V>(m: Map<K, V[]>, k: K, v: V) {
  const a = m.get(k) ?? [];
  if (!a.includes(v)) a.push(v);
  m.set(k, a);
}

/** Elements of a route in walking order (skipping the first node, the users). */
function routeSequence(r: TopoRoute): { kind: 'node' | 'link'; id: string }[] {
  const seq: { kind: 'node' | 'link'; id: string }[] = [];
  r.hops.forEach((h, i) => {
    seq.push({ kind: 'link', id: h });
    seq.push({ kind: 'node', id: r.nodes[i + 1] });
  });
  return seq;
}

export function buildSnapshot(b: Bundle, obs: Observations, ctx: EvalContext): Snapshot {
  const g = graphOf(b);
  const now = obs.at;
  const sinceOf = (k: string) => iso(ctx.since.get(k) ?? now);
  const rebootWindow = (b.map.settings.reboot_window || 1800) * 1000;
  const staleSec = b.map.settings.stale_after || 30;

  // ---- nodes
  const nodes: Record<string, NodeState> = {};
  for (const n of b.nodes) {
    const checks = [...(n.checks ?? []), ...(n.metrics ?? []).filter((m) => !isTrafficOnly(m))].map((c) => checkState(c, obs.checks[c.key]));
    const no = obs.nodes[n.id];
    if (n.agent) {
      // Built-in heartbeat: a silent agent makes the node amber, not red, because
      // the server itself may be fine (the other checks tell).
      const age = no?.agentAge ?? null;
      const hb: CheckState = {
        key: `node:${n.id}:agent`,
        id: 'agent',
        title: 'agent heartbeat',
        collector: 'agent',
        status: age === null ? 'unknown' : age > staleSec ? 'down' : 'ok',
        effective: age === null ? 'unknown' : age > staleSec ? 'degraded' : 'ok',
        value: age,
        unit: 's',
        detail: age === null ? 'agent never reported' : age > staleSec ? `no push for ${fmtAge(age)}` : `last push ${Math.round(age)}s ago${no?.agentVersion ? ' · v' + no.agentVersion : ''}`,
        observedAt: age === null ? null : iso(now - age * 1000),
        source: `agent:${n.id}`,
      };
      checks.unshift(hb);
    }
    const { status, observed } = combine(checks);
    const metrics: NodeState['metrics'] = {};
    if (n.agent && no) {
      metrics.cpu = { value: no.cpu, unit: '%', label: 'CPU' };
      metrics.mem = { value: no.mem, unit: '%', label: 'Memory' };
      metrics.disk = { value: no.disk, unit: '%', label: 'Disk' };
      metrics.load = { value: no.load, unit: '', label: 'Load' };
      metrics.uptime = { value: no.uptime, unit: 's', label: 'Uptime' };
      metrics.net = { value: no.net, unit: 'bps', label: 'Network' };
    }
    for (const m of n.metrics ?? []) metrics[m.id] = { value: obs.metrics[m.key] ?? null, unit: unitOf(m), label: m.label || m.id };
    for (const c of n.checks ?? []) {
      if (!metrics[c.id]) metrics[c.id] = { value: obs.checks[c.key]?.value ?? null, unit: unitOf(c), label: c.label || c.id };
    }
    const bootTime = no?.bootTime ?? null;
    nodes[n.id] = {
      id: n.id,
      status,
      observed,
      impact: null,
      checks,
      metrics,
      bootTime: bootTime ? iso(bootTime * 1000) : null,
      uptimeSec: no?.uptime ?? null,
      rebootedRecently: bootTime !== null && now - bootTime * 1000 < rebootWindow,
      agent: n.agent ? { lastSeen: no?.agentAge != null ? iso(now - no.agentAge * 1000) : null, version: no?.agentVersion, configSha: no?.configSha, ageSec: no?.agentAge ?? null } : null,
      since: sinceOf(`node:${n.id}`),
    };
  }

  // ---- links
  const links: Record<string, LinkState> = {};
  for (const l of b.links) {
    const checks = (l.health ?? []).map((c) => checkState(c, obs.checks[c.key]));
    const { status, observed } = combine(checks);
    const lo = obs.links[l.id];
    links[l.id] = {
      id: l.id,
      status,
      observed,
      impact: null,
      upBps: lo?.up ?? null,
      downBps: lo?.down ?? null,
      conns: lo?.conns ?? null,
      trafficSource: lo?.src ?? null,
      checks,
      since: sinceOf(`link:${l.id}`),
    };
  }

  const st = (e: { kind: 'node' | 'link'; id: string }) => (e.kind === 'node' ? nodes[e.id]?.status : links[e.id]?.status) ?? 'unknown';
  const agentSilent = (id: string) => {
    const hb = nodes[id]?.checks.find((c) => c.collector === 'agent');
    return hb?.status === 'down';
  };

  // ---- routes
  const routes: Record<string, RouteState> = {};
  for (const r of b.routes) {
    const ro = obs.routes[r.id];
    const seq = routeSequence(r);
    const probeCheck = r.probe ? checkState(r.probe, obs.checks[r.probe.key]) : null;
    let root: RouteState['root'] = null;
    for (let i = 0; i < seq.length; i++) {
      const e = seq[i];
      if (st(e) !== 'down') continue;
      const next = seq[i + 1];
      if (e.kind === 'link' && next && next.kind === 'node' && (st(next) === 'down' || agentSilent(next.id))) {
        root = { kind: 'node', id: next.id };
      } else root = { kind: e.kind, id: e.id };
      break;
    }
    const probeDown = probeCheck?.status === 'down';
    if (!root && probeDown) root = { kind: 'route', id: r.id };
    const broken = root !== null;
    let status: Health = broken ? 'down' : 'ok';
    if (!broken) {
      for (const e of seq) {
        const s = st(e);
        if (s === 'degraded') status = worst(status, 'degraded');
      }
      if (probeCheck && probeCheck.status === 'degraded') status = worst(status, 'degraded');
      if (probeCheck && probeCheck.status === 'unknown' && seq.every((e) => st(e) === 'unknown')) status = 'unknown';
    }
    routes[r.id] = {
      id: r.id,
      status,
      broken,
      probe: probeCheck
        ? { status: probeCheck.status, latencyMs: ro?.latencyMs ?? null, exitCountry: ro?.exitCountry ?? null, failures: ro?.probeFailures ?? 0, detail: probeCheck.detail }
        : null,
      users: ro?.users ?? null,
      usersSource: ro?.usersSource ?? null,
      root,
      hops: [
        { kind: 'node' as 'node' | 'link', id: r.nodes[0], status: nodes[r.nodes[0]]?.status ?? ('unknown' as Health), impact: null as Impact },
        ...seq.map((e) => ({ kind: e.kind, id: e.id, status: st(e), impact: null as Impact })),
      ],
      since: sinceOf(`route:${r.id}`),
    };
  }

  // ---- impact marking (root / affected)
  const brokenRoutes = b.routes.filter((r) => routes[r.id].broken);
  const healthyRoutes = new Set(b.routes.filter((r) => !routes[r.id].broken).map((r) => r.id));
  const setImpact = (kind: 'node' | 'link', id: string, imp: Impact) => {
    const s = kind === 'node' ? nodes[id] : links[id];
    if (!s) return;
    if (s.impact === 'root') return;
    s.impact = imp;
  };
  const affectedBy = new Map<string, { nodes: Set<string>; links: Set<string> }>();
  for (const r of brokenRoutes) {
    const rs = routes[r.id];
    const root = rs.root!;
    const rootKey = `${root.kind}:${root.id}`;
    const aff = affectedBy.get(rootKey) ?? { nodes: new Set(), links: new Set() };
    affectedBy.set(rootKey, aff);
    if (root.kind !== 'route') setImpact(root.kind, root.id, 'root');
    const seq = routeSequence(r);
    let after = root.kind === 'route';
    for (const e of seq) {
      if (!after) {
        if (e.kind === root.kind && e.id === root.id) after = true;
        continue;
      }
      // Shared elements stay unmarked while a healthy route still uses them.
      const usedHealthy = (g.routesOf.get(`${e.kind}:${e.id}`) ?? []).some((x) => healthyRoutes.has(x));
      if (usedHealthy) continue;
      if (e.kind === 'node' && g.nodes.get(e.id)?.kind === 'internet') continue;
      setImpact(e.kind, e.id, 'affected');
      (e.kind === 'node' ? aff.nodes : aff.links).add(e.id);
    }
    // A link failing because its target node is the root is affected too.
    if (root.kind === 'node') {
      const idx = seq.findIndex((e) => e.kind === 'node' && e.id === root.id);
      const prev = seq[idx - 1];
      if (prev && prev.kind === 'link' && links[prev.id].status === 'down') {
        setImpact('link', prev.id, 'affected');
        aff.links.add(prev.id);
      }
    }
    for (const h of rs.hops) h.impact = (h.kind === 'node' ? nodes[h.id]?.impact : links[h.id]?.impact) ?? null;
  }

  // A failing node also takes down the control links it serves (panel → nodes).
  for (const [k, aff] of affectedBy) {
    if (!k.startsWith('node:')) continue;
    const id = k.slice(5);
    for (const l of g.out.get(id) ?? []) {
      if (l.plane === 'control' && links[l.id].status !== 'ok') {
        setImpact('link', l.id, 'affected');
        aff.links.add(l.id);
      }
    }
  }

  // Failures outside every broken route (control plane, standalone checks).
  const covered = new Set<string>();
  for (const [k, aff] of affectedBy) {
    covered.add(k);
    aff.nodes.forEach((n) => covered.add(`node:${n}`));
    aff.links.forEach((l) => covered.add(`link:${l}`));
  }
  const extraRoots: { kind: 'node' | 'link'; id: string }[] = [];
  for (const n of b.nodes) if (nodes[n.id].status === 'down' && !covered.has(`node:${n.id}`)) extraRoots.push({ kind: 'node', id: n.id });
  for (const l of b.links) {
    if (links[l.id].status !== 'down' || covered.has(`link:${l.id}`)) continue;
    // A control link out of a failing node is a consequence of that node.
    if (nodes[l.from]?.status === 'down') continue;
    if (nodes[l.to]?.status === 'down') continue;
    extraRoots.push({ kind: 'link', id: l.id });
  }
  for (const r of extraRoots) {
    setImpact(r.kind, r.id, 'root');
    const aff = { nodes: new Set<string>(), links: new Set<string>() };
    if (r.kind === 'node') {
      for (const l of [...(g.out.get(r.id) ?? []), ...(g.in.get(r.id) ?? [])]) {
        if (l.plane === 'control' && links[l.id].status !== 'ok') {
          setImpact('link', l.id, 'affected');
          aff.links.add(l.id);
        }
      }
    }
    affectedBy.set(`${r.kind}:${r.id}`, aff);
  }

  // ---- root causes
  const rootCauses: RootCause[] = [];
  for (const [key, aff] of affectedBy) {
    const [kind, id] = key.split(/:(.*)/s) as ['node' | 'link' | 'route', string];
    const rts = brokenRoutes.filter((r) => {
      const rr = routes[r.id].root!;
      return `${rr.kind}:${rr.id}` === key;
    });
    const outage = rts.filter((r) => r.impact === 'outage');
    const usersOf = (list: TopoRoute[]) => {
      const seen = new Set<string>();
      let total = 0;
      let live = 0;
      let est = 0;
      for (const r of list) {
        const k = r.users ? JSON.stringify([r.users.collector, r.users.params]) : `est:${r.id}:${(r.squads ?? []).join(',')}`;
        if (seen.has(k)) continue;
        seen.add(k);
        const ro = routes[r.id];
        total += ro.users ?? 0;
        if (ro.usersSource === 'live') live++;
        else est++;
      }
      return { total, source: (live && est ? 'mixed' : live ? 'live' : 'estimate') as RootCause['usersSource'] };
    };
    const u = usersOf(outage.length ? outage : rts);
    const el = kind === 'node' ? g.nodes.get(id) : kind === 'link' ? g.links.get(id) : undefined;
    const route = kind === 'route' ? b.routes.find((r) => r.id === id) : undefined;
    const failing = failingChecks(kind, id, nodes, links, routes);
    const title =
      kind === 'node' ? (el as TopoNode).name : kind === 'link' ? linkName(el as TopoLink, g) : `route ${route?.title ?? id} (no single failing hop)`;
    const summary = failing.length ? failing.map((c) => c.detail).slice(0, 2).join('; ') : kind === 'route' ? routes[id].probe?.detail ?? 'probe failing' : 'failing';
    const evidence: string[] = [];
    for (const c of failing) evidence.push(`${kind} ${id} · ${c.title}: ${c.detail}`);
    for (const r of rts) if (routes[r.id].probe && routes[r.id].probe!.status !== 'ok') evidence.push(`route ${r.id} probe: ${routes[r.id].probe!.detail}`);
    for (const n of aff.nodes) for (const c of nodes[n].checks.filter((c) => c.effective === 'down' || c.effective === 'degraded')) evidence.push(`node ${n} · ${c.title}: ${c.detail}`);
    for (const l of aff.links) {
      for (const c of links[l].checks.filter((c) => c.effective === 'down')) evidence.push(`link ${l} · ${c.title}: ${c.detail}`);
    }
    if (kind === 'node' && nodes[id].rebootedRecently) evidence.push(`node ${id} rebooted at ${nodes[id].bootTime}`);
    const runbook: RootCause['runbook'] = [];
    const addRb = (from: string, list?: RunbookCmd[]) => list?.forEach((x) => runbook.push({ ...x, from }));
    if (kind === 'node') addRb(id, g.nodes.get(id)?.runbook);
    if (kind === 'link') {
      addRb(id, g.links.get(id)?.runbook);
      addRb((el as TopoLink).to, g.nodes.get((el as TopoLink).to)?.runbook);
    }
    for (const l of aff.links) addRb(l, g.links.get(l)?.runbook);
    for (const n of aff.nodes) addRb(n, g.nodes.get(n)?.runbook);
    const docs = new Set<string>();
    if (el && (el as any).docs) docs.add((el as any).docs);
    for (const r of rts) if (r.docs) docs.add(r.docs);
    // One incident per set of broken routes, so a root that is refined while
    // data arrives (e.g. E3 → XBridger node 3) stays the same incident.
    const incidentId = rts.length ? `routes:${rts.map((r) => r.id).sort().join('+')}` : key;
    const since = ctx.incidentSince.get(incidentId) ?? now;
    rootCauses.push({
      id: incidentId,
      kind,
      elementId: id,
      title,
      summary,
      impact: outage.length || (!rts.length && kind !== 'route') ? 'outage' : 'degraded',
      usersAffected: rts.length ? u.total : 0,
      usersSource: u.source,
      routes: rts.map((r) => r.id),
      affected: { nodes: [...aff.nodes], links: [...aff.links] },
      since: iso(since),
      evidence: dedupe(evidence).slice(0, 12),
      runbook: dedupeRb(runbook).slice(0, 8),
      docs: [...docs],
      definedIn: el ? (el as any).defined_in : route?.defined_in ?? null,
    });
    if (!rts.length) rootCauses[rootCauses.length - 1].impact = kind === 'node' && g.nodes.get(id)?.kind === 'panel' ? 'degraded' : 'degraded';
  }
  // Rank: users who lost connectivity count fully, degraded service (e.g.
  // subscriptions cannot update) counts 5 %.
  const score = (r: RootCause) => r.usersAffected * (r.impact === 'outage' ? 1 : 0.05) + (r.kind === 'node' ? 0.5 : 0);
  rootCauses.sort((a, b2) => score(b2) - score(a));

  // ---- overall
  const counts: Record<Health, number> = { ok: 0, degraded: 0, down: 0, unknown: 0 };
  for (const n of Object.values(nodes)) if (n.observed) counts[n.status]++;
  for (const l of Object.values(links)) if (l.observed) counts[l.status]++;
  const usersNode = b.nodes.find((n) => n.kind === 'users');
  let usersOnline: number | null = null;
  if (usersNode) {
    const m = (usersNode.metrics ?? []).find((x) => x.collector === 'remnawave-online');
    if (m) usersOnline = obs.metrics[m.key] ?? null;
  }
  let up = 0;
  let down = 0;
  for (const l of b.links) {
    const from = g.nodes.get(l.from);
    if (l.plane === 'data' && from && (from.kind === 'users' || from.kind === 'router')) {
      up += links[l.id].upBps ?? 0;
      down += links[l.id].downBps ?? 0;
    }
  }
  const totalUsers = Math.max(1, usersOnline ?? b.routes.reduce((s, r) => s + (routes[r.id].users ?? 0), 0));
  let status: Health = 'ok';
  if (counts.degraded || counts.down || brokenRoutes.length) status = 'degraded';
  for (const rc of rootCauses) if (rc.impact === 'outage' && rc.usersAffected >= Math.max(5, totalUsers * 0.1)) status = 'down';
  if (!counts.ok && !counts.degraded && !counts.down) status = 'unknown';
  const overall: Overall = {
    status,
    usersOnline,
    throughput: { upBps: up, downBps: down },
    activeAlerts: ctx.alerts.filter((a) => !a.silencedUntil).length,
    rootCauses,
    counts,
  };

  return {
    at: iso(now),
    live: ctx.live,
    mode: ctx.mode,
    topology: { version: ctx.version, hash: b.hash, gitCommit: b.source?.git_commit },
    overall,
    nodes,
    links,
    routes,
    alerts: ctx.alerts,
    drift: [],
  };
}

function isTrafficOnly(c: CheckSpec) {
  return ['nft-counter', 'wg-transfer', 'xray-stats', 'ss-conns', 'remnawave-online'].includes(c.collector);
}

export function linkName(l: TopoLink, g?: Graph): string {
  if (l.title) return `${l.label ? l.label + ' · ' : ''}${l.title}`;
  const f = g?.nodes.get(l.from)?.name ?? l.from;
  const t = g?.nodes.get(l.to)?.name ?? l.to;
  return `${l.label ? l.label + ' · ' : ''}${f} → ${t}`;
}

function failingChecks(kind: string, id: string, nodes: Record<string, NodeState>, links: Record<string, LinkState>, _routes: Record<string, RouteState>): CheckState[] {
  const list = kind === 'node' ? nodes[id]?.checks : kind === 'link' ? links[id]?.checks : [];
  return (list ?? []).filter((c) => c.effective === 'down').concat((list ?? []).filter((c) => c.effective === 'degraded'));
}

function dedupe(a: string[]) {
  return [...new Set(a)];
}

function dedupeRb<T extends { cmd: string }>(a: T[]) {
  const seen = new Set<string>();
  return a.filter((x) => (seen.has(x.cmd) ? false : (seen.add(x.cmd), true)));
}
