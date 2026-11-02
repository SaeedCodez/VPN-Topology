import * as net from 'net';
import * as tls from 'tls';
import { Bundle, CheckSpec } from '../contract';
import { Config } from '../config';
import { LatestStore } from '../engine/store';
import { toRemnawaveNode } from '../engine/remnawave';

/**
 * Collectors that run inside the hub in live mode. All of them are read-only:
 * GET requests, TCP connects and TLS handshakes. The Remnawave token is a
 * dedicated monitoring token and lives only in the hub's environment.
 */
export class LiveCollectors {
  private timers: NodeJS.Timeout[] = [];
  private lastRun = new Map<string, number>();
  private xuiCookies = new Map<string, string>();

  constructor(
    private cfg: Config,
    private store: LatestStore,
    private bundle: () => Bundle,
  ) {}

  start() {
    this.timers.push(setInterval(() => void this.remnawave(), 15_000));
    this.timers.push(setInterval(() => void this.xui(), 20_000));
    this.timers.push(setInterval(() => void this.probes(), 5_000));
    void this.remnawave();
    void this.xui();
    void this.probes();
  }

  stop() {
    for (const t of this.timers) clearInterval(t);
  }

  private async remnawave() {
    if (!this.cfg.rwApiUrl || !this.cfg.rwApiToken) return;
    const at = Date.now();
    const get = async (p: string) => {
      const r = await fetch(`${this.cfg.rwApiUrl}${p}`, { headers: { Authorization: `Bearer ${this.cfg.rwApiToken}` }, signal: AbortSignal.timeout(10_000) });
      if (!r.ok) throw new Error(`HTTP ${r.status} on ${p}`);
      return ((await r.json()) as any).response;
    };
    try {
      const nodes = await get('/api/nodes');
      let onlineNow: number | null = null;
      try {
        const stats = await get('/api/system/stats');
        onlineNow = stats?.onlineStats?.onlineNow ?? stats?.users?.onlineNow ?? null;
      } catch {
        /* optional */
      }
      this.store.remnawave = {
        at,
        ok: true,
        onlineNow,
        nodes: (nodes ?? []).map(toRemnawaveNode),
      };
    } catch (e: any) {
      this.store.remnawave = { at, ok: false, error: e?.name === 'TimeoutError' ? 'timeout after 10 s' : String(e?.message ?? e), nodes: this.store.remnawave?.nodes ?? [], onlineNow: null };
    }
  }

  private async xui() {
    for (const [name, x] of this.cfg.xui) {
      const at = Date.now();
      try {
        let cookie = this.xuiCookies.get(name);
        if (!cookie) {
          const r = await fetch(`${x.url}/login`, { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams({ username: x.user, password: x.pass }), signal: AbortSignal.timeout(10_000) });
          cookie = (r.headers.get('set-cookie') ?? '').split(';')[0];
          if (!r.ok || !cookie) throw new Error(`login failed (HTTP ${r.status})`);
          this.xuiCookies.set(name, cookie);
        }
        const r = await fetch(`${x.url}/panel/api/inbounds/list`, { headers: { Cookie: cookie }, signal: AbortSignal.timeout(10_000) });
        if (r.status === 401 || r.status === 404) this.xuiCookies.delete(name);
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        const body: any = await r.json();
        const inbounds = (body?.obj ?? []).map((i: any) => ({ remark: i.remark, tag: i.tag, enable: !!i.enable, up: Number(i.up ?? 0), down: Number(i.down ?? 0) }));
        this.store.xui.set(name, { at, ok: true, inbounds });
        for (const i of inbounds) {
          for (const id of [i.remark, i.tag]) {
            this.store.counter(`xui:${name}:${id}:up`, at, i.up);
            this.store.counter(`xui:${name}:${id}:down`, at, i.down);
          }
        }
      } catch (e: any) {
        this.store.xui.set(name, { at, ok: false, error: String(e?.message ?? e), inbounds: [] });
      }
    }
  }

  /** tcp-connect / http-probe / tls-expiry with from: hub, each on its own interval. */
  private async probes() {
    const now = Date.now();
    const checks: CheckSpec[] = [];
    const b = this.bundle();
    for (const n of b.nodes) checks.push(...(n.checks ?? []), ...(n.metrics ?? []));
    for (const l of b.links) checks.push(...(l.health ?? []));
    for (const r of b.routes) if (r.probe) checks.push(r.probe);
    for (const c of checks) {
      const hub = c.collector === 'tls-expiry' || (['tcp-connect', 'http-probe'].includes(c.collector) && (c.params.from ?? (c.collector === 'tcp-connect' ? 'probe' : 'hub')) === 'hub');
      if (!hub) continue;
      const interval = parseDur(c.params.interval, c.collector === 'tls-expiry' ? 3_600_000 : 30_000);
      const key = c.collector === 'tcp-connect' ? `tcp:${c.params.target}` : c.collector === 'http-probe' ? `http:${c.params.url}` : `tls:${c.params.host}|${c.params.connect ?? ''}:${c.params.port ?? 443}`;
      if (now - (this.lastRun.get(key) ?? 0) < interval) continue;
      this.lastRun.set(key, now);
      const p = c.collector === 'tcp-connect' ? tcpConnect(c.params.target, parseDur(c.params.timeout, 3000)) : c.collector === 'http-probe' ? httpGet(c.params.url, parseDur(c.params.max_latency, 10_000)) : tlsDays(c.params.host, c.params.connect, Number(c.params.port ?? 443));
      void p.then((r) => this.store.hubProbes.set(key, { ...r, at: Date.now() }));
    }
  }
}

function parseDur(v: any, def: number): number {
  const m = /^([0-9.]+)(ms|s|m|h|d)$/.exec(String(v ?? ''));
  if (!m) return def;
  return parseFloat(m[1]) * ({ ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 } as any)[m[2]];
}

function tcpConnect(target: string, timeout: number): Promise<{ ok: boolean; latencyMs: number; error?: string }> {
  const i = target.lastIndexOf(':');
  const host = target.slice(0, i);
  const port = Number(target.slice(i + 1));
  const t0 = performance.now();
  return new Promise((resolve) => {
    const s = net.connect({ host, port, timeout });
    const done = (ok: boolean, error?: string) => {
      s.destroy();
      resolve({ ok, latencyMs: performance.now() - t0, error });
    };
    s.once('connect', () => done(true));
    s.once('timeout', () => done(false, 'timeout'));
    s.once('error', (e: any) => done(false, e.code === 'ECONNREFUSED' ? 'refused' : e.code === 'ENOTFOUND' ? 'dns' : String(e.code ?? e.message)));
  });
}

async function httpGet(url: string, timeout: number): Promise<{ ok: boolean; status?: number; latencyMs: number; error?: string }> {
  const t0 = performance.now();
  try {
    const r = await fetch(url, { method: 'GET', redirect: 'manual', signal: AbortSignal.timeout(timeout) });
    await r.body?.cancel();
    return { ok: true, status: r.status, latencyMs: performance.now() - t0 };
  } catch (e: any) {
    return { ok: false, latencyMs: performance.now() - t0, error: e?.name === 'TimeoutError' ? `timeout after ${timeout / 1000} s` : String(e?.cause?.code ?? e?.message ?? e) };
  }
}

function tlsDays(host: string, connect: string | undefined, port: number): Promise<{ ok: boolean; latencyMs: number; certDays?: number; error?: string }> {
  const t0 = performance.now();
  return new Promise((resolve) => {
    const s = tls.connect({ host: connect || host, servername: host, port, rejectUnauthorized: false, timeout: 10_000 });
    const done = (r: { ok: boolean; certDays?: number; error?: string }) => {
      s.destroy();
      resolve({ ...r, latencyMs: performance.now() - t0 });
    };
    s.once('secureConnect', () => {
      const c = s.getPeerCertificate();
      if (!c?.valid_to) return done({ ok: false, error: 'no certificate' });
      done({ ok: true, certDays: (Date.parse(c.valid_to) - Date.now()) / 86_400_000 });
    });
    s.once('timeout', () => done({ ok: false, error: 'timeout' }));
    s.once('error', (e: any) => done({ ok: false, error: String(e.code ?? e.message) }));
  });
}
