import { Push, RemnawaveNode, HubProbeResult, XuiInbound } from './protocol';

/**
 * LatestStore keeps the most recent raw observations: the last push of every
 * agent, short counter histories (to compute rates), the results of hub-side
 * collectors and per-route probe streaks. It is fed the same way in live and
 * demo mode (the simulator pushes fake payloads through ingest()), so the
 * resolvers run identical code in both modes.
 */

export interface AgentRecord {
  node: string;
  push: Push;
  lastSeen: number; // ms
  bootTime: number | null; // unix seconds
}

export interface IngestEffects {
  reboots: { node: string; bootTime: number; previous: number }[];
  restarts: { node: string; container: string; count: number; previous: number }[];
  firstSeen: string[];
}

interface LogLine {
  t: number;
  line: string;
}

const COUNTER_KEEP_MS = 5 * 60_000;

export class LatestStore {
  readonly agents = new Map<string, AgentRecord>();
  readonly counters = new Map<string, [number, number][]>();
  readonly logs = new Map<string, LogLine[]>(); // "node/logid"
  readonly probes = new Map<string, { failures: number; last: NonNullable<Push['probes']>[number]; lastAt: number }>();
  readonly logMatchAt = new Map<string, number>(); // check key → last match (ms)

  // Hub-side collector results.
  remnawave: { at: number; ok: boolean; error?: string; nodes: RemnawaveNode[]; onlineNow: number | null } | null = null;
  readonly hubProbes = new Map<string, HubProbeResult>(); // "tcp:<target>", "http:<url>", "tls:<host>|<connect>:<port>"
  readonly xui = new Map<string, { at: number; ok: boolean; error?: string; inbounds: XuiInbound[] }>();

  /** Store a push. `now` is the receive time (ms). */
  ingest(push: Push, now: number): IngestEffects {
    const fx: IngestEffects = { reboots: [], restarts: [], firstSeen: [] };
    const node = push.agent.node;
    const prev = this.agents.get(node);
    if (!prev) fx.firstSeen.push(node);

    const bootTime = push.host?.boot_time ?? prev?.bootTime ?? null;
    if (prev?.bootTime && bootTime && bootTime - prev.bootTime > 30) {
      fx.reboots.push({ node, bootTime, previous: prev.bootTime });
    }
    if (prev?.push.containers && push.containers) {
      for (const c of push.containers) {
        const old = prev.push.containers.find((x) => x.name === c.name);
        if (old && c.restart_count > old.restart_count) {
          fx.restarts.push({ node, container: c.name, count: c.restart_count, previous: old.restart_count });
        }
      }
    }
    this.agents.set(node, { node, push, lastSeen: now, bootTime });

    for (const c of push.nft ?? []) this.counter(`nft:${node}:${c.name}`, now, c.bytes);
    for (const w of push.wg ?? []) {
      const id = `${w.netns ?? ''}/${w.iface}`;
      let rx = 0;
      let tx = 0;
      for (const p of w.peers) {
        rx += p.rx_b;
        tx += p.tx_b;
      }
      this.counter(`wg:${node}:${id}:rx`, now, rx);
      this.counter(`wg:${node}:${id}:tx`, now, tx);
    }
    let nicRx = 0;
    let nicTx = 0;
    for (const n of push.host?.nics ?? []) {
      nicRx += n.rx_b;
      nicTx += n.tx_b;
      this.counter(`nic:${node}:${n.name}:rx`, now, n.rx_b);
      this.counter(`nic:${node}:${n.name}:tx`, now, n.tx_b);
    }
    if (push.host?.nics?.length) {
      this.counter(`nic:${node}:*:rx`, now, nicRx);
      this.counter(`nic:${node}:*:tx`, now, nicTx);
    }
    for (const s of push.prom ?? []) this.counter(`prom:${node}:${promKey(s.name, s.labels)}`, now, s.value);
    for (const l of push.logs ?? []) {
      const k = `${node}/${l.id}`;
      const arr = this.logs.get(k) ?? [];
      for (const line of l.lines) arr.push({ t: now, line });
      if (arr.length > 300) arr.splice(0, arr.length - 300);
      this.logs.set(k, arr);
    }
    for (const p of push.probes ?? []) {
      const at = Date.parse(p.at) || now;
      const cur = this.probes.get(p.route);
      if (cur && at <= cur.lastAt) continue;
      // A failure of the probe's own client (sing-box missing, outbound file
      // missing) says nothing about the route: keep the streak unchanged.
      const failures = p.ok ? 0 : probeSideError(p.error) ? cur?.failures ?? 0 : (cur?.failures ?? 0) + 1;
      this.probes.set(p.route, { failures, last: p, lastAt: at });
    }
    return fx;
  }

  counter(key: string, t: number, v: number) {
    const arr = this.counters.get(key) ?? [];
    if (arr.length && arr[arr.length - 1][0] >= t) return;
    arr.push([t, v]);
    while (arr.length > 2 && arr[0][0] < t - COUNTER_KEEP_MS) arr.shift();
    this.counters.set(key, arr);
  }

  /**
   * Rate in bits per second over the last `windowMs`, robust to counter resets
   * (only positive deltas are summed). Null when there is not enough data or
   * the newest sample is older than staleMs.
   */
  rate(key: string, now: number, windowMs = 30_000, staleMs = 30_000): number | null {
    const arr = this.counters.get(key);
    if (!arr || arr.length < 2) return null;
    const last = arr[arr.length - 1];
    if (now - last[0] > staleMs) return null;
    let i = arr.length - 1;
    while (i > 0 && last[0] - arr[i - 1][0] <= windowMs) i--;
    if (i === arr.length - 1) i = arr.length - 2;
    let bytes = 0;
    for (let j = i + 1; j < arr.length; j++) {
      const d = arr[j][1] - arr[j - 1][1];
      bytes += d >= 0 ? d : arr[j][1]; // reset: count from zero
    }
    const dt = (last[0] - arr[i][0]) / 1000;
    if (dt <= 0) return null;
    return (bytes * 8) / dt;
  }

  /** New log lines of node/logId since `since` (ms). */
  logLinesSince(node: string, id: string, since: number): LogLine[] {
    return (this.logs.get(`${node}/${id}`) ?? []).filter((l) => l.t > since);
  }

  hubResult(key: string): HubProbeResult | undefined {
    return this.hubProbes.get(key);
  }
}

/** Errors reported by the probe about itself rather than about the route. */
export function probeSideError(err: string | undefined): boolean {
  return !!err && err.startsWith('sing-box:');
}

export function promKey(name: string, labels?: Record<string, string>): string {
  if (!labels) return name;
  const parts = Object.keys(labels)
    .sort()
    .map((k) => `${k}=${labels[k]}`);
  return `${name}{${parts.join(',')}}`;
}
