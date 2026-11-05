import { BadRequestException, NotFoundException } from '@nestjs/common';
import { Diagnosis, HubEvent, Series } from './contract';
import { HubService } from './hub.service';
import { describe, diagnose, NotFound, path as pathView, why } from './engine/views';

/**
 * The read-only questions the board, the CLI (through the API) and the
 * Telegram assistant all ask. One implementation so the three always show the
 * same facts. Nothing here writes anything.
 */

/** Parse `at`: RFC 3339 ("2026-10-22T02:03Z"), relative ("-2h", "90m"), or unix ms. */
export function parseAt(v: string | undefined | null): number | null {
  if (!v) return null;
  const rel = /^-?(\d+(?:\.\d+)?)(s|m|h|d)$/.exec(v.trim());
  if (rel) return Date.now() - parseFloat(rel[1]) * ({ s: 1e3, m: 6e4, h: 3.6e6, d: 8.64e7 } as any)[rel[2]];
  if (/^\d{12,}$/.test(v)) return Number(v);
  const t = Date.parse(v);
  if (Number.isNaN(t)) throw new BadRequestException(`invalid time "${v}" (use RFC 3339 like 2026-10-22T02:03Z, or -2h)`);
  if (t > Date.now() + 60_000) throw new BadRequestException('time is in the future');
  return t;
}

export function parseDuration(v: string | undefined | null, def: number): number {
  if (!v) return def;
  const m = /^(\d+(?:\.\d+)?)(s|m|h|d)$/.exec(v.trim().replace(/^-/, ''));
  if (!m) throw new BadRequestException(`invalid duration "${v}" (use e.g. 30m, 2h, 1d)`);
  return parseFloat(m[1]) * ({ s: 1e3, m: 6e4, h: 3.6e6, d: 8.64e7 } as any)[m[2]];
}

function wrap<T>(fn: () => T): T {
  try {
    return fn();
  } catch (e) {
    if (e instanceof NotFound) throw new NotFoundException(e.message);
    throw e;
  }
}

export class Queries {
  constructor(private readonly hub: HubService) {}

  async status(at: number | null) {
    const { snap, bundle } = await this.hub.snapshotAt(at);
    return {
      at: snap.at,
      live: snap.live,
      mode: snap.mode,
      topology: snap.topology,
      overall: snap.overall,
      routes: bundle.routes.map((r) => ({ ...snap.routes[r.id], title: r.title, impact: r.impact, squads: r.squads ?? [] })),
      alerts: snap.alerts,
      drift: snap.drift,
    };
  }

  async describe(id: string, at: number | null) {
    const { snap, bundle } = await this.hub.snapshotAt(at);
    const ent = wrap(() => describe(bundle, snap, id, []));
    const evs = await this.hub.db.events({ entity: { kind: ent.kind, id: ent.id }, until: at ? new Date(at) : undefined, limit: 20 });
    ent.events = evs.filter((e) => e.kind !== 'state' || e.entityKind !== 'check');
    return ent;
  }

  async why(id: string, at: number | null) {
    const { snap, bundle } = await this.hub.snapshotAt(at);
    return { id, at: snap.at, reasons: wrap(() => why(bundle, snap, id)) };
  }

  async path(from: string, to: string, route: string | undefined, at: number | null) {
    if (!from || !to) throw new BadRequestException('from and to are required');
    const { snap, bundle } = await this.hub.snapshotAt(at);
    return wrap(() => pathView(bundle, snap, from, to, route));
  }

  async diagnose(at: number | null): Promise<Diagnosis> {
    const { snap, bundle } = await this.hub.snapshotAt(at);
    const end = at ?? Date.now();
    const recent = (await this.hub.db.events({ since: new Date(end - 2 * 3_600_000), until: new Date(end), limit: 200 })).filter((e) => !(e.kind === 'state' && e.entityKind === 'check')).slice(0, 25);
    return diagnose(bundle, snap, recent);
  }

  async events(q: { sinceMs: number; until: number | null; entity?: string; kinds?: string[]; limit?: number; checks?: boolean }): Promise<HubEvent[]> {
    const end = q.until ?? Date.now();
    let ent: { kind: string; id: string } | undefined;
    if (q.entity) {
      const b = this.hub.bundle;
      const id = q.entity;
      ent = b.nodes.some((x) => x.id === id) ? { kind: 'node', id } : b.links.some((x) => x.id === id) ? { kind: 'link', id } : b.routes.some((x) => x.id === id) ? { kind: 'route', id } : undefined;
      if (!ent) throw new NotFoundException(`no node, link or route "${id}"`);
    }
    const list = await this.hub.db.events({ since: new Date(end - q.sinceMs), until: new Date(end), entity: ent, kinds: q.kinds, limit: q.limit ?? 500 });
    return q.checks ? list : list.filter((e) => !(e.kind === 'state' && e.entityKind === 'check'));
  }

  /** Series for one entity (default keys) or explicit keys. */
  async metrics(q: { entity?: string; keys?: string[]; spanMs: number; until: number | null; stepMs?: number }): Promise<Series[]> {
    const end = q.until ?? Date.now();
    const stepMs = q.stepMs ?? Math.max(10_000, Math.round(q.spanMs / 240 / 10_000) * 10_000);
    const defs = q.keys?.length ? q.keys.map((k) => ({ key: k, label: k, unit: '' })) : q.entity ? this.defaultSeries(q.entity) : [];
    if (!defs.length) throw new BadRequestException('give entity=<id> or keys=<series keys>');
    const m = await this.hub.tsdb.range(defs.map((d) => d.key), end - q.spanMs, end, stepMs);
    return defs.map((d) => ({ ...d, points: m.get(d.key) ?? [] }));
  }

  private defaultSeries(id: string): { key: string; label: string; unit: string }[] {
    const b = this.hub.bundle;
    const n = b.nodes.find((x) => x.id === id);
    if (n) {
      const out: { key: string; label: string; unit: string }[] = [];
      if (n.agent) out.push({ key: `n:${id}:cpu`, label: 'CPU', unit: '%' }, { key: `n:${id}:mem`, label: 'Memory', unit: '%' }, { key: `n:${id}:net`, label: 'Network', unit: 'bps' });
      for (const m of n.metrics ?? []) out.push({ key: `m:${m.key}`, label: m.label ?? m.id, unit: m.collector === 'remnawave-online' ? 'users' : m.collector === 'ss-conns' ? 'conns' : '' });
      for (const c of n.checks ?? []) if (['http-probe', 'tcp-connect', 'xbridger-connection'].includes(c.collector)) out.push({ key: `c:${c.key}:v`, label: c.title ?? c.id, unit: 'ms' });
      return out;
    }
    const l = b.links.find((x) => x.id === id || x.label?.toLowerCase() === id.toLowerCase());
    if (l) {
      const out = [
        { key: `l:${l.id}:up`, label: 'Up', unit: 'bps' },
        { key: `l:${l.id}:down`, label: 'Down', unit: 'bps' },
      ];
      if (l.conns) out.push({ key: `l:${l.id}:conns`, label: 'Connections', unit: 'conns' });
      for (const c of l.health ?? []) if (['tcp-connect', 'http-probe', 'xbridger-connection'].includes(c.collector)) out.push({ key: `c:${c.key}:v`, label: c.title ?? c.id, unit: 'ms' });
      for (const c of l.health ?? []) if (c.collector === 'wg-handshake') out.push({ key: `c:${c.key}:v`, label: 'Handshake age', unit: 's' });
      return out;
    }
    const r = b.routes.find((x) => x.id === id);
    if (r)
      return [
        { key: `r:${id}:users`, label: 'Users', unit: 'users' },
        { key: `r:${id}:lat`, label: 'Probe latency', unit: 'ms' },
        { key: `r:${id}:fail`, label: 'Probe failures', unit: '' },
      ];
    throw new NotFoundException(`no node, link or route "${id}"`);
  }
}
