import { Bundle, Describe, Diagnosis, Health, HubEvent, PathView, RootCause, RunbookCmd, Snapshot, TopoLink, TopoNode, TopoRoute } from '../contract';
import { graphOf, linkName } from './evaluate';
import { fmtAge } from './resolve';

/**
 * Read views shared by the board and the CLI: describe, why, path, diagnose.
 * They only rearrange facts already in the snapshot.
 */

export class NotFound extends Error {}

function fmtBps(v: number | null): string {
  if (v === null) return '–';
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)} Gbps`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)} Mbps`;
  if (v >= 1e3) return `${Math.round(v / 1e3)} Kbps`;
  return `${Math.round(v)} bps`;
}

export function findEntity(b: Bundle, id: string): { kind: 'node' | 'link' | 'route'; spec: TopoNode | TopoLink | TopoRoute } {
  const n = b.nodes.find((x) => x.id === id);
  if (n) return { kind: 'node', spec: n };
  const l = b.links.find((x) => x.id === id || x.label?.toLowerCase() === id.toLowerCase());
  if (l) return { kind: 'link', spec: l };
  const r = b.routes.find((x) => x.id === id);
  if (r) return { kind: 'route', spec: r };
  const all = [...b.nodes.map((x) => x.id), ...b.links.map((x) => x.id), ...b.routes.map((x) => x.id)];
  const close = all.filter((x) => x.includes(id) || id.includes(x)).slice(0, 5);
  throw new NotFound(`no node, link or route "${id}"${close.length ? ` (did you mean ${close.join(', ')}?)` : ''}`);
}

export function why(b: Bundle, snap: Snapshot, id: string): string[] {
  const { kind, spec } = findEntity(b, id);
  const out: string[] = [];
  if (kind === 'route') {
    const r = snap.routes[spec.id];
    if (!r.broken && r.status === 'ok') return [`route ${spec.id} is healthy${r.probe ? `: probe ${r.probe.detail}` : ''}`];
    if (r.root) out.push(r.root.kind === 'route' ? 'the probe fails but no single hop reports a failure' : `first failing hop: ${r.root.kind} ${r.root.id}`);
    if (r.probe && r.probe.status !== 'ok') out.push(`probe: ${r.probe.detail}`);
    for (const h of r.hops) if (h.status !== 'ok') out.push(`${h.kind} ${h.id} is ${h.status}${h.impact ? ` (${h.impact})` : ''}`);
    return out;
  }
  const st = kind === 'node' ? snap.nodes[spec.id] : snap.links[spec.id];
  if (st.impact === 'affected') {
    const rc = snap.overall.rootCauses.find((x) => x.affected.nodes.includes(spec.id) || x.affected.links.includes(spec.id));
    if (rc) out.push(`affected by the root cause ${rc.kind} ${rc.elementId} (${rc.title}): ${rc.summary}`);
  }
  if (st.impact === 'root') out.push(`this is the probable root cause`);
  if (!st.observed) out.push('no checks are defined here; its colour comes only from up/downstream impact');
  for (const c of st.checks) {
    if (c.effective === 'ok') continue;
    out.push(`${c.title}: ${c.status}${c.effective !== c.status ? ` (counts as ${c.effective})` : ''} — ${c.detail}${c.observedAt ? ` [${c.source}]` : ''}`);
  }
  if (kind === 'node') {
    const n = snap.nodes[spec.id];
    if (n.rebootedRecently && n.uptimeSec !== null) out.push(`rebooted ${fmtAge(n.uptimeSec)} ago`);
  }
  if (!out.length) out.push(`${kind} ${spec.id} is ${st.status}: all checks pass`);
  return out;
}

export function describe(b: Bundle, snap: Snapshot, id: string, events: HubEvent[]): Describe {
  const { kind, spec } = findEntity(b, id);
  const g = graphOf(b);
  const state = kind === 'node' ? snap.nodes[spec.id] : kind === 'link' ? snap.links[spec.id] : snap.routes[spec.id];
  const upstream: Describe['upstream'] = [];
  const downstream: Describe['downstream'] = [];
  if (kind === 'node') {
    for (const l of g.in.get(spec.id) ?? []) upstream.push({ kind: 'link', id: l.id, status: snap.links[l.id].status });
    for (const l of g.out.get(spec.id) ?? []) downstream.push({ kind: 'link', id: l.id, status: snap.links[l.id].status });
  } else if (kind === 'link') {
    const l = spec as TopoLink;
    upstream.push({ kind: 'node', id: l.from, status: snap.nodes[l.from].status });
    downstream.push({ kind: 'node', id: l.to, status: snap.nodes[l.to].status });
  }
  const routes = kind === 'route' ? [spec.id] : g.routesOf.get(`${kind}:${spec.id}`) ?? [];
  const runbook: (RunbookCmd & { from: string })[] = [];
  for (const r of (spec as any).runbook ?? []) runbook.push({ ...r, from: spec.id });
  if (kind === 'link') for (const r of g.nodes.get((spec as TopoLink).to)?.runbook ?? []) runbook.push({ ...r, from: (spec as TopoLink).to });
  const comp = b.components.find((c) => c.name === spec.defined_in.component);
  for (const r of comp?.runbook ?? []) runbook.push({ ...r, from: comp!.name });
  const docs = [...new Set([(spec as any).docs, comp?.docs].filter(Boolean))] as string[];
  return {
    kind,
    id: spec.id,
    at: snap.at,
    spec,
    state,
    definedIn: spec.defined_in,
    upstream,
    downstream,
    routes,
    events,
    runbook,
    docs,
    why: why(b, snap, spec.id),
  };
}

export function path(b: Bundle, snap: Snapshot, from: string, to: string, routeId?: string): PathView {
  const g = graphOf(b);
  let route: TopoRoute | undefined;
  if (routeId) {
    route = b.routes.find((r) => r.id === routeId);
    if (!route) throw new NotFound(`no route "${routeId}"`);
  } else {
    const cands = b.routes.filter((r) => r.nodes.includes(from) && r.nodes.includes(to) && r.nodes.indexOf(from) < r.nodes.indexOf(to));
    cands.sort((a, c) => (snap.routes[c.id].users ?? 0) - (snap.routes[a.id].users ?? 0));
    route = cands[0];
  }
  let seq: { kind: 'node' | 'link'; id: string }[] = [];
  if (route) {
    const all: { kind: 'node' | 'link'; id: string }[] = [{ kind: 'node', id: route.nodes[0] }];
    route.hops.forEach((h, i) => all.push({ kind: 'link', id: h }, { kind: 'node', id: route!.nodes[i + 1] }));
    const a = all.findIndex((e) => e.kind === 'node' && e.id === from);
    const z = all.findIndex((e, i) => i >= a && e.kind === 'node' && e.id === to);
    if (a < 0 || z < 0) throw new NotFound(`route ${route.id} does not go from ${from} to ${to}`);
    seq = all.slice(a, z + 1);
  } else {
    // No declared route: shortest path over data links (BFS).
    if (!g.nodes.has(from)) throw new NotFound(`no node "${from}"`);
    if (!g.nodes.has(to)) throw new NotFound(`no node "${to}"`);
    const prev = new Map<string, TopoLink>();
    const q = [from];
    const seen = new Set([from]);
    while (q.length) {
      const cur = q.shift()!;
      if (cur === to) break;
      for (const l of g.out.get(cur) ?? []) {
        if (seen.has(l.to)) continue;
        seen.add(l.to);
        prev.set(l.to, l);
        q.push(l.to);
      }
    }
    if (!seen.has(to)) throw new NotFound(`no path from ${from} to ${to}`);
    const rev: { kind: 'node' | 'link'; id: string }[] = [{ kind: 'node', id: to }];
    let cur = to;
    while (cur !== from) {
      const l = prev.get(cur)!;
      rev.push({ kind: 'link', id: l.id }, { kind: 'node', id: l.from });
      cur = l.from;
    }
    seq = rev.reverse();
  }
  let status: Health = 'ok';
  const hops: PathView['hops'] = seq.map((e) => {
    if (e.kind === 'node') {
      const n = snap.nodes[e.id];
      const spec = g.nodes.get(e.id)!;
      const bad = n.checks.filter((c) => c.effective !== 'ok' && c.effective !== 'unknown');
      const card = (spec.card ?? []).map((k) => n.metrics[k]).filter(Boolean);
      if (n.observed) status = worstH(status, n.status);
      return {
        kind: 'node',
        id: e.id,
        name: spec.name,
        status: n.observed ? n.status : 'ok',
        impact: n.impact,
        detail: bad.length ? bad.map((c) => c.detail).join('; ') : n.observed ? 'all checks pass' : 'not observed directly',
        metrics: card.map((m) => `${m!.label} ${fmtMetric(m!.value, m!.unit)}`).join(' · '),
      };
    }
    const l = snap.links[e.id];
    const spec = g.links.get(e.id)!;
    const bad = l.checks.filter((c) => c.effective !== 'ok');
    if (l.observed) status = worstH(status, l.status);
    return {
      kind: 'link',
      id: e.id,
      name: linkName(spec, g),
      status: l.observed ? l.status : 'ok',
      impact: l.impact,
      detail: bad.length ? bad.map((c) => c.detail).join('; ') : l.checks.length ? l.checks.map((c) => c.detail).join('; ') : spec.protocol ?? '',
      metrics: `↑ ${fmtBps(l.upBps)} ↓ ${fmtBps(l.downBps)}${l.conns !== null ? ` · ${l.conns} conns` : ''}`,
    };
  });
  const rs = route ? snap.routes[route.id] : null;
  if (rs?.broken) status = 'down';
  const firstBad = hops.find((h) => h.status === 'down') ?? hops.find((h) => h.status === 'degraded');
  const verdict = rs?.broken
    ? `broken at ${rs.root?.kind} ${rs.root?.id}${rs.probe ? ` · probe: ${rs.probe.detail}` : ''}`
    : firstBad
      ? `degraded at ${firstBad.kind} ${firstBad.id}: ${firstBad.detail}`
      : `healthy${rs?.probe ? ` · probe: ${rs.probe.detail}` : ''}`;
  return { at: snap.at, from, to, route: route?.id ?? null, hops, verdict, status };
}

function worstH(a: Health, b: Health): Health {
  const r: Record<Health, number> = { ok: 0, unknown: 1, degraded: 2, down: 3 };
  return r[a] >= r[b] ? a : b;
}

export function fmtMetric(v: number | null, unit: string): string {
  if (v === null || v === undefined) return '–';
  switch (unit) {
    case '%':
      return `${Math.round(v)}%`;
    case 'bps':
      return fmtBps(v);
    case 'ms':
      return v < 10 ? `${v.toFixed(1)} ms` : `${Math.round(v)} ms`;
    case 's':
      return fmtAge(v);
    case 'days':
      return `${Math.round(v)} d`;
    default:
      return Number.isInteger(v) ? String(v) : v.toFixed(1);
  }
}

export function diagnose(b: Bundle, snap: Snapshot, recent: HubEvent[]): Diagnosis {
  const g = graphOf(b);
  const degraded: Diagnosis['degraded'] = [];
  const inIncident = new Set<string>();
  for (const rc of snap.overall.rootCauses) {
    inIncident.add(`${rc.kind}:${rc.elementId}`);
    rc.affected.nodes.forEach((n) => inIncident.add(`node:${n}`));
    rc.affected.links.forEach((l) => inIncident.add(`link:${l}`));
  }
  for (const n of Object.values(snap.nodes)) {
    if (n.status !== 'ok' && n.observed && !inIncident.has(`node:${n.id}`))
      degraded.push({ kind: 'node', id: n.id, status: n.status, detail: n.checks.filter((c) => c.effective !== 'ok').map((c) => `${c.title}: ${c.detail}`).join('; ') });
  }
  for (const l of Object.values(snap.links)) {
    if (l.status !== 'ok' && l.observed && !inIncident.has(`link:${l.id}`))
      degraded.push({ kind: 'link', id: l.id, status: l.status, detail: l.checks.filter((c) => c.effective !== 'ok').map((c) => `${c.title}: ${c.detail}`).join('; ') });
  }
  const top: RootCause | undefined = snap.overall.rootCauses[0];
  const headline = top
    ? `probable root cause: ${top.title} (${top.kind} ${top.elementId}) — ${top.summary}; ${top.usersAffected.toLocaleString('en-US')} users affected${top.impact === 'degraded' ? ' (degraded service)' : ''}`
    : snap.overall.status === 'ok'
      ? 'all routes healthy'
      : `no outage; ${degraded.length} element(s) degraded`;
  void g;
  return { at: snap.at, status: snap.overall.status, headline, rootCauses: snap.overall.rootCauses, degraded, alerts: snap.alerts, recentEvents: recent };
}
