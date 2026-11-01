import { ActiveAlert, AlertExpr, Bundle, Health, Snapshot, TopoAlert } from '../contract';
import { linkName, graphOf } from './evaluate';

/**
 * Alert rules from the component files, evaluated on every snapshot. A rule
 * with a wildcard (node.*, link.*) or a collector reference fires once per
 * matching entity. `for 2m` / `for 3 samples` hold conditions are honoured.
 * Telegram delivery (Persian messages, dedupe, recovery, maintenance mute) is
 * done by the notifier on top of the fired/resolved transitions.
 */

interface Instance {
  key: string; // "<kind>:<id>" or check key, "" for global
  name: string;
}

interface HoldState {
  trueSince: number | null;
  samples: number;
  firing: boolean;
  since: number;
}

export interface AlertTransition {
  alert: TopoAlert;
  instance: Instance;
  fired: boolean; // true = fired, false = resolved
  at: number;
}

export type BaselineFn = (linkId: string, at: number) => number | null;

export class AlertEngine {
  private hold = new Map<string, HoldState>();
  silences = new Map<string, number>(); // "alertId" or "alertId|instance" → until ms

  evaluate(b: Bundle, snap: Snapshot, at: number, baseline: BaselineFn): { active: ActiveAlert[]; transitions: AlertTransition[] } {
    const active: ActiveAlert[] = [];
    const transitions: AlertTransition[] = [];
    const g = graphOf(b);
    const seen = new Set<string>();
    for (const a of b.alerts) {
      if (!a.expr) continue;
      for (const inst of instances(a.expr, b, g)) {
        const key = `${a.id}|${inst.key}`;
        seen.add(key);
        const truth = evalExpr(a.expr, snap, b, inst, at, baseline);
        const h = this.hold.get(key) ?? { trueSince: null, samples: 0, firing: false, since: at };
        if (truth) {
          h.trueSince ??= at;
          h.samples++;
        } else {
          h.trueSince = null;
          h.samples = 0;
        }
        const need = a.expr.for;
        const held = truth && (!need || (need.samples ? h.samples >= need.samples : at - (h.trueSince ?? at) >= (need.seconds ?? 0) * 1000));
        if (held && !h.firing) {
          h.firing = true;
          h.since = h.trueSince ?? at;
          transitions.push({ alert: a, instance: inst, fired: true, at });
        } else if (!truth && h.firing) {
          h.firing = false;
          transitions.push({ alert: a, instance: inst, fired: false, at });
        }
        this.hold.set(key, h);
        if (h.firing) {
          const until = Math.max(this.silences.get(a.id) ?? 0, this.silences.get(key) ?? 0);
          active.push({
            id: a.id,
            instance: inst.key,
            severity: a.severity,
            title: a.title || a.id,
            message: fill(a.message_en || a.title || a.id, inst),
            messageFa: fill(a.message_fa, inst),
            since: new Date(h.since).toISOString(),
            silencedUntil: until > at ? new Date(until).toISOString() : null,
            definedIn: a.defined_in,
          });
        }
      }
    }
    for (const k of [...this.hold.keys()]) if (!seen.has(k)) this.hold.delete(k);
    active.sort((x, y) => (x.severity === y.severity ? x.since.localeCompare(y.since) : x.severity === 'critical' ? -1 : 1));
    return { active, transitions };
  }
}

function fill(msg: string, inst: Instance): string {
  return msg.replace(/\{name\}/g, inst.name).replace(/\{id\}/g, inst.key);
}

function refsOf(e: AlertExpr): NonNullable<AlertExpr['ref']>[] {
  if (e.ref) return [e.ref];
  return (e.args ?? []).flatMap(refsOf);
}

/** One instance per entity matched by a wildcard or collector reference. */
function instances(e: AlertExpr, b: Bundle, g: ReturnType<typeof graphOf>): Instance[] {
  const dyn = refsOf(e).find((r) => r.id === '*' || r.kind === 'collector');
  if (!dyn) return [{ key: '', name: '' }];
  if (dyn.kind === 'node') return b.nodes.filter((n) => n.agent || dyn.field === 'status' || dyn.field === 'health').map((n) => ({ key: `node:${n.id}`, name: n.name }));
  if (dyn.kind === 'link') return b.links.map((l) => ({ key: `link:${l.id}`, name: linkName(l, g) }));
  if (dyn.kind === 'route') return b.routes.map((r) => ({ key: `route:${r.id}`, name: r.title }));
  const out: Instance[] = [];
  const add = (owner: string, c: { key: string; collector: string; title?: string; params: any }) => {
    if (c.collector === dyn.id) out.push({ key: c.key, name: `${owner}${c.title ? ' · ' + c.title : ''}${c.params.host && c.collector === 'tls-expiry' ? '' : ''}` });
  };
  for (const n of b.nodes) for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) add(n.name, c);
  for (const l of b.links) for (const c of l.health ?? []) add(linkName(l, g), c);
  return out.map((i) => {
    const c = findCheck(b, i.key);
    if (c && c.collector === 'tls-expiry') i.name = `${c.params.host}${c.params.connect ? ' (origin)' : ''}`;
    if (c && c.collector === 'remnawave-node') i.name = c.params.node;
    return i;
  });
}

function findCheck(b: Bundle, key: string) {
  for (const n of b.nodes) for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) if (c.key === key) return c;
  for (const l of b.links) for (const c of l.health ?? []) if (c.key === key) return c;
  return undefined;
}

type Val = { t: 'status'; v: Health } | { t: 'bool'; v: boolean } | { t: 'number'; v: number } | null;

function resolveRef(r: NonNullable<AlertExpr['ref']>, snap: Snapshot, b: Bundle, inst: Instance, at: number, baseline: BaselineFn): Val {
  let kind = r.kind;
  let id = r.id;
  if (r.id === '*' && inst.key) id = inst.key.split(':')[1];
  if (kind === 'collector') {
    const st = checkByKey(snap, inst.key);
    if (!st) return null;
    if (r.field === 'status') return { t: 'status', v: st.status };
    return st.value === null ? null : { t: 'number', v: st.value };
  }
  if (r.check) {
    const owner = kind === 'node' ? snap.nodes[id] : kind === 'link' ? snap.links[id] : null;
    const key = `${kind}:${id}:${r.check}`;
    const st = owner?.checks.find((c) => c.key === key) ?? checkByKey(snap, key);
    if (!st) return null;
    if (r.field === 'status') return { t: 'status', v: st.status };
    const v = st.value;
    if (v === null) return null;
    return { t: 'number', v: r.field === 'latency' ? v / 1000 : v };
  }
  if (kind === 'node') {
    const n = snap.nodes[id];
    if (!n) return null;
    switch (r.field) {
      case 'status':
      case 'health':
        return { t: 'status', v: n.status };
      case 'rebooted':
        return { t: 'bool', v: n.rebootedRecently };
      case 'uptime':
        return n.uptimeSec === null ? null : { t: 'number', v: n.uptimeSec };
      case 'agent_age':
        return n.agent?.ageSec == null ? null : { t: 'number', v: n.agent.ageSec };
      default: {
        const m = n.metrics[r.field];
        return m?.value == null ? null : { t: 'number', v: m.value };
      }
    }
  }
  if (kind === 'link') {
    const l = snap.links[id];
    if (!l) return null;
    switch (r.field) {
      case 'status':
      case 'health':
        return { t: 'status', v: l.status };
      case 'up_bps':
        return l.upBps === null ? null : { t: 'number', v: l.upBps };
      case 'down_bps':
        return l.downBps === null ? null : { t: 'number', v: l.downBps };
      case 'total_bps':
        return l.upBps === null && l.downBps === null ? null : { t: 'number', v: (l.upBps ?? 0) + (l.downBps ?? 0) };
      case 'conns':
        return l.conns === null ? null : { t: 'number', v: l.conns };
      case 'traffic_drop': {
        const cur = (l.upBps ?? 0) + (l.downBps ?? 0);
        const base = baseline(id, at);
        // Ignore links that are quiet anyway (< 200 kbps yesterday).
        if (base === null || base < 200e3 || l.upBps === null) return { t: 'number', v: 0 };
        return { t: 'number', v: Math.max(0, (1 - cur / base) * 100) };
      }
    }
    return null;
  }
  if (kind === 'route') {
    const rt = snap.routes[id];
    if (!rt) return null;
    switch (r.field) {
      case 'status':
        return { t: 'status', v: rt.status };
      case 'probe':
        return rt.probe ? { t: 'status', v: rt.probe.status } : null;
      case 'probe_failures':
        return { t: 'number', v: rt.probe?.failures ?? 0 };
      case 'users':
        return rt.users === null ? null : { t: 'number', v: rt.users };
      case 'latency':
        return rt.probe?.latencyMs == null ? null : { t: 'number', v: rt.probe.latencyMs / 1000 };
    }
  }
  return null;
}

function checkByKey(snap: Snapshot, key: string) {
  for (const n of Object.values(snap.nodes)) for (const c of n.checks) if (c.key === key) return c;
  for (const l of Object.values(snap.links)) for (const c of l.checks) if (c.key === key) return c;
  return undefined;
}

export function evalExpr(e: AlertExpr, snap: Snapshot, b: Bundle, inst: Instance, at: number, baseline: BaselineFn): boolean {
  switch (e.op) {
    case 'and':
      return (e.args ?? []).every((a) => evalExpr(a, snap, b, inst, at, baseline));
    case 'or':
      return (e.args ?? []).some((a) => evalExpr(a, snap, b, inst, at, baseline));
    case 'not':
      return !evalExpr(e.args![0], snap, b, inst, at, baseline);
  }
  const v = resolveRef(e.ref!, snap, b, inst, at, baseline);
  if (v === null) return false; // no data never fires an alert by itself
  const want = e.value!;
  if (v.t === 'status' && v.v === 'unknown' && want.status !== 'unknown') return false;
  if (v.t === 'status' || v.t === 'bool') {
    const eq = v.t === 'status' ? v.v === want.status : v.v === want.bool;
    return e.cmp === '==' ? eq : e.cmp === '!=' ? !eq : false;
  }
  const x = v.v;
  const y = want.number ?? 0;
  switch (e.cmp) {
    case '==':
      return x === y;
    case '!=':
      return x !== y;
    case '>':
      return x > y;
    case '>=':
      return x >= y;
    case '<':
      return x < y;
    case '<=':
      return x <= y;
  }
  return false;
}
