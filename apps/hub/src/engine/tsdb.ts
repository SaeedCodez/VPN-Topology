import { Bundle } from '../contract';
import { CODE_STATUS, Observations, STATUS_CODE, emptyNodeObs } from './observations';

/**
 * Time-series storage for observations. Every 10 s the hub writes one value
 * per series; any past board can be rebuilt from these series.
 *
 * Series keys:
 *   c:<check key>:s   check status code (0 ok, 1 degraded, 2 down, -1 unknown)
 *   c:<check key>:v   check value
 *   l:<link>:up|down|conns|src
 *   n:<node>:cpu|mem|disk|load|uptime|net|boot|age
 *   m:<metric key>    node metric value
 *   r:<route>:users|usrc|fail|lat
 */
export interface TSDB {
  readonly name: string;
  write(at: number, values: Map<string, number | null>): Promise<void>;
  /** Latest value of every series at or before `at` (within lookback). */
  instant(at: number, lookbackMs?: number): Promise<Map<string, number>>;
  range(keys: string[], from: number, to: number, stepMs: number): Promise<Map<string, [number, number | null][]>>;
}

export const STEP_MS = 10_000;

/** In-memory ring buffer TSDB used in demo mode (and as a cache). */
export class MemoryTSDB implements TSDB {
  readonly name = 'memory';
  private series = new Map<string, Float64Array>();
  private readonly slots: number;
  private latestSlot = -Infinity;

  constructor(retentionMs = 27 * 3_600_000) {
    this.slots = Math.ceil(retentionMs / STEP_MS);
  }

  private slot(at: number) {
    return Math.floor(at / STEP_MS);
  }

  async write(at: number, values: Map<string, number | null>) {
    const s = this.slot(at);
    // Clear slots skipped since the last write so stale values never leak.
    if (s > this.latestSlot + 1 && this.latestSlot > -Infinity) {
      for (let x = this.latestSlot + 1; x < s && x < this.latestSlot + this.slots; x++) {
        for (const arr of this.series.values()) arr[x % this.slots] = NaN;
      }
    }
    this.latestSlot = Math.max(this.latestSlot, s);
    for (const [k, v] of values) {
      let arr = this.series.get(k);
      if (!arr) {
        arr = new Float64Array(this.slots).fill(NaN);
        this.series.set(k, arr);
      }
      arr[s % this.slots] = v === null || v === undefined || Number.isNaN(v) ? NaN : v;
    }
  }

  private valid(s: number) {
    return s <= this.latestSlot && s > this.latestSlot - this.slots;
  }

  async instant(at: number, lookbackMs = 30_000) {
    const out = new Map<string, number>();
    const s = this.slot(at);
    const back = Math.ceil(lookbackMs / STEP_MS);
    for (const [k, arr] of this.series) {
      for (let x = s; x >= s - back; x--) {
        if (!this.valid(x)) continue;
        const v = arr[x % this.slots];
        if (!Number.isNaN(v)) {
          out.set(k, v);
          break;
        }
      }
    }
    return out;
  }

  async range(keys: string[], from: number, to: number, stepMs: number) {
    const out = new Map<string, [number, number | null][]>();
    const step = Math.max(STEP_MS, Math.round(stepMs / STEP_MS) * STEP_MS);
    for (const k of keys) {
      const arr = this.series.get(k);
      const pts: [number, number | null][] = [];
      for (let t = Math.floor(from / step) * step; t <= to; t += step) {
        let sum = 0;
        let n = 0;
        for (let x = this.slot(t); x < this.slot(t + step); x++) {
          if (!arr || !this.valid(x)) continue;
          const v = arr[x % this.slots];
          if (!Number.isNaN(v)) {
            sum += v;
            n++;
          }
        }
        pts.push([t, n ? sum / n : null]);
      }
      out.set(k, pts);
    }
    return out;
  }
}

/**
 * VictoriaMetrics: one metric `vpnmap` with label k=<series key>, written with
 * the Prometheus import API and read with last_over_time / avg_over_time.
 */
export class VictoriaMetricsTSDB implements TSDB {
  readonly name = 'victoriametrics';
  constructor(private readonly url: string) {}

  async write(at: number, values: Map<string, number | null>) {
    const lines: string[] = [];
    for (const [k, v] of values) {
      if (v === null || v === undefined || Number.isNaN(v)) continue;
      lines.push(`vpnmap{k="${k.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"} ${v} ${at}`);
    }
    if (!lines.length) return;
    const res = await fetch(`${this.url}/api/v1/import/prometheus`, { method: 'POST', body: lines.join('\n') + '\n' });
    if (!res.ok) throw new Error(`VictoriaMetrics write: HTTP ${res.status}`);
  }

  async instant(at: number, lookbackMs = 30_000) {
    const q = `last_over_time(vpnmap[${Math.round(lookbackMs / 1000)}s])`;
    const res = await fetch(`${this.url}/api/v1/query?query=${encodeURIComponent(q)}&time=${at / 1000}`);
    if (!res.ok) throw new Error(`VictoriaMetrics query: HTTP ${res.status}`);
    const body: any = await res.json();
    const out = new Map<string, number>();
    for (const r of body?.data?.result ?? []) out.set(r.metric.k, Number(r.value[1]));
    return out;
  }

  async range(keys: string[], from: number, to: number, stepMs: number) {
    const out = new Map<string, [number, number | null][]>();
    const step = Math.max(10, Math.round(stepMs / 1000));
    for (const k of keys) {
      const q = `avg_over_time(vpnmap{k="${k.replace(/"/g, '\\"')}"}[${step}s])`;
      const res = await fetch(`${this.url}/api/v1/query_range?query=${encodeURIComponent(q)}&start=${from / 1000}&end=${to / 1000}&step=${step}`);
      const body: any = res.ok ? await res.json() : null;
      const vals: [number, string][] = body?.data?.result?.[0]?.values ?? [];
      const byT = new Map(vals.map(([t, v]) => [Math.round(t * 1000), Number(v)]));
      const pts: [number, number | null][] = [];
      for (let t = Math.floor(from / (step * 1000)) * step * 1000; t <= to; t += step * 1000) pts.push([t, byT.get(t) ?? null]);
      out.set(k, pts);
    }
    return out;
  }
}

/** Flatten observations into series values. */
export function toSeries(obs: Observations): Map<string, number | null> {
  const m = new Map<string, number | null>();
  for (const [k, c] of Object.entries(obs.checks)) {
    m.set(`c:${k}:s`, STATUS_CODE[c.status]);
    m.set(`c:${k}:v`, c.value);
  }
  for (const [id, n] of Object.entries(obs.nodes)) {
    m.set(`n:${id}:cpu`, n.cpu);
    m.set(`n:${id}:mem`, n.mem);
    m.set(`n:${id}:disk`, n.disk);
    m.set(`n:${id}:load`, n.load);
    m.set(`n:${id}:uptime`, n.uptime);
    m.set(`n:${id}:net`, n.net);
    m.set(`n:${id}:boot`, n.bootTime);
    m.set(`n:${id}:age`, n.agentAge);
  }
  for (const [id, l] of Object.entries(obs.links)) {
    m.set(`l:${id}:up`, l.up);
    m.set(`l:${id}:down`, l.down);
    m.set(`l:${id}:conns`, l.conns);
    m.set(`l:${id}:src`, l.src === 'primary' ? 1 : l.src === 'fallback' ? 2 : null);
  }
  for (const [id, r] of Object.entries(obs.routes)) {
    m.set(`r:${id}:users`, r.users);
    m.set(`r:${id}:usrc`, r.usersSource === 'live' ? 1 : r.usersSource === 'estimate' ? 2 : null);
    m.set(`r:${id}:fail`, r.probeFailures);
    m.set(`r:${id}:lat`, r.latencyMs);
  }
  for (const [k, v] of Object.entries(obs.metrics)) m.set(`m:${k}`, v);
  return m;
}

/** Rebuild observations at `at` from stored series. Details come from events. */
export function fromSeries(b: Bundle, at: number, s: Map<string, number>, details: Map<string, string>): Observations {
  const obs: Observations = { at, checks: {}, nodes: {}, links: {}, routes: {}, metrics: {} };
  const val = (k: string) => (s.has(k) ? s.get(k)! : null);
  const keys: string[] = [];
  for (const n of b.nodes) {
    for (const c of [...(n.checks ?? []), ...(n.metrics ?? [])]) keys.push(c.key);
    for (const m of n.metrics ?? []) obs.metrics[m.key] = val(`m:${m.key}`);
    const no = emptyNodeObs();
    no.cpu = val(`n:${n.id}:cpu`);
    no.mem = val(`n:${n.id}:mem`);
    no.disk = val(`n:${n.id}:disk`);
    no.load = val(`n:${n.id}:load`);
    no.uptime = val(`n:${n.id}:uptime`);
    no.net = val(`n:${n.id}:net`);
    no.bootTime = val(`n:${n.id}:boot`);
    no.agentAge = val(`n:${n.id}:age`);
    obs.nodes[n.id] = no;
  }
  for (const l of b.links) {
    for (const c of l.health ?? []) keys.push(c.key);
    if (l.traffic) keys.push(l.traffic.key);
    if (l.conns) keys.push(l.conns.key);
    const src = val(`l:${l.id}:src`);
    obs.links[l.id] = { up: val(`l:${l.id}:up`), down: val(`l:${l.id}:down`), conns: val(`l:${l.id}:conns`), src: src === 1 ? 'primary' : src === 2 ? 'fallback' : null };
  }
  for (const r of b.routes) {
    if (r.probe) keys.push(r.probe.key);
    if (r.users) keys.push(r.users.key);
    const us = val(`r:${r.id}:usrc`);
    obs.routes[r.id] = {
      users: val(`r:${r.id}:users`),
      usersSource: us === 1 ? 'live' : us === 2 ? 'estimate' : null,
      probeFailures: val(`r:${r.id}:fail`) ?? 0,
      latencyMs: val(`r:${r.id}:lat`),
      exitCountry: null,
    };
  }
  for (const k of keys) {
    const code = val(`c:${k}:s`);
    const status = code === null ? 'unknown' : CODE_STATUS[String(code)] ?? 'unknown';
    obs.checks[k] = { status, value: val(`c:${k}:v`), detail: details.get(k) ?? (code === null ? 'no data' : status), observedAt: code === null ? null : at, source: 'history' };
  }
  return obs;
}
