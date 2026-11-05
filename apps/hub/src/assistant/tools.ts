import { Bundle, HubEvent, Series } from '../contract';
import { ToolDef } from './openrouter';

/**
 * The assistant's tools. Every one is a read of the same facts the board and
 * `vpnmap` show (board_image sends a picture of them to the chat); there is
 * no tool that changes anything, so the model cannot
 * touch the map or a server whatever it is told. Outputs are compacted and
 * capped so one call cannot flood the context.
 */

export const MAX_TOOL_OUTPUT = 8000;

/** What the tools need from the hub (the Queries class, or a fake in tests). */
export interface ReadApi {
  bundle(): Bundle;
  status(at: number | null): Promise<any>;
  diagnose(at: number | null): Promise<any>;
  describe(id: string, at: number | null): Promise<any>;
  why(id: string, at: number | null): Promise<any>;
  path(from: string, to: string, route: string | undefined, at: number | null): Promise<any>;
  events(q: { sinceMs: number; until: number | null; entity?: string; limit?: number }): Promise<HubEvent[]>;
  metrics(q: { entity: string; spanMs: number; until: number | null }): Promise<Series[]>;
}

const at = { type: 'string', description: 'Optional moment to look at, RFC 3339 (2026-10-22T02:03Z) or relative (-2h). Omit for now, or for the time of the alert the user replied to.' };
const id = { type: 'string', description: 'Node, link or route id (see list_entities), e.g. relay-1, e3, main.' };
const span = { type: 'string', description: 'How far back, e.g. 30m, 2h, 1d (max 7d).' };

export const TOOLS: ToolDef[] = [
  fn('list_entities', 'All nodes, links and routes with id, name/title, kind and group. Call this first if you do not know an id.', {}),
  fn('status', 'Overall state: status, users online, throughput, root causes, every route (status, users, probe) and active alerts.', { at }),
  fn('diagnose', 'What is wrong and why: headline, root causes with affected users, degraded elements, alerts, recent events (2 h).', { at }),
  fn('describe', 'Everything about one node, link or route: its spec (address, role, facts, runbook), live state and checks, neighbours, routes, recent events.', { id, at }, ['id']),
  fn('why', 'Short reasons why one element has its current status.', { id, at }, ['id']),
  fn('path', 'Hop-by-hop path between two nodes (default users → an exit) with each hop status and metrics.', { from: { type: 'string' }, to: { type: 'string' }, route: { type: 'string', description: 'optional route id' }, at }, ['from', 'to']),
  fn('events', 'Event log (state changes, incidents, alerts, topology changes), newest first.', { since: span, entity: { type: 'string', description: 'optional node/link/route id' }, at }),
  fn('metrics', 'Time series for one node/link/route (cpu, mem, traffic, latency, users...) summarised as min/avg/max/last plus a coarse series.', { id, since: span, at }, ['id']),
];

/**
 * Offered only when the hub can send to Telegram. Sending a picture of the
 * board to the same chat is still a read: it shows what the board shows.
 */
export const IMAGE_TOOL: ToolDef = fn(
  'board_image',
  'Send a picture of the board to this chat (it is sent right away). No selection = the whole board. A part: route (a route id), or from + to with optional via (node ids in order, e.g. from "users", via ["france-exit"], to "internet"), and/or nodes / links (ids). Use list_entities for ids. After it, reply with one short line; do not describe the picture again. At most 3 pictures per question.',
  {
    route: { type: 'string', description: 'Route id, e.g. main.' },
    from: { type: 'string', description: 'Start node id of a path.' },
    to: { type: 'string', description: 'End node id of a path.' },
    via: { type: 'array', items: { type: 'string' }, description: 'Node ids the path must pass, in order.' },
    nodes: { type: 'array', items: { type: 'string' }, description: 'Node ids to show (links between them are drawn).' },
    links: { type: 'array', items: { type: 'string' }, description: 'Link ids or labels (E3) to show, with their ends.' },
    context: { type: 'boolean', description: 'Keep the rest of the board around the part visible, dimmed.' },
    theme: { type: 'string', enum: ['light', 'dark'], description: 'Default light.' },
    file: { type: 'boolean', description: 'Send as a full-resolution file instead of a photo (big boards are sent as a file anyway).' },
    caption: { type: 'string', description: 'Optional short caption in Persian.' },
    at,
  },
);

function fn(name: string, description: string, properties: Record<string, unknown>, required: string[] = []): ToolDef {
  return { type: 'function', function: { name, description, parameters: { type: 'object', properties, required, additionalProperties: false } } };
}

export interface ToolContext {
  api: ReadApi;
  /** Default moment (the alert the user replied to), ms; null = now. */
  defaultAt: number | null;
  parseAt: (v: string | undefined | null) => number | null;
  parseDuration: (v: string | undefined | null, def: number) => number;
  /** Sends a picture of the board to the chat; set only when Telegram is on. */
  image?: (args: Record<string, unknown>, at: number | null) => Promise<unknown>;
}

/** Run one tool call; errors come back as text for the model, never thrown. */
export async function runTool(name: string, rawArgs: string, ctx: ToolContext): Promise<string> {
  let args: any = {};
  try {
    args = rawArgs ? JSON.parse(rawArgs) : {};
  } catch {
    return 'error: arguments are not valid JSON';
  }
  try {
    const t = args.at ? ctx.parseAt(String(args.at)) : ctx.defaultAt;
    const out = await dispatch(name, args, t, ctx);
    return cap(JSON.stringify(out));
  } catch (e: any) {
    return `error: ${e?.message ?? String(e)}`;
  }
}

async function dispatch(name: string, a: any, t: number | null, ctx: ToolContext): Promise<unknown> {
  const api = ctx.api;
  switch (name) {
    case 'list_entities': {
      const b = api.bundle();
      return {
        nodes: b.nodes.map((n) => ({ id: n.id, name: n.name, kind: n.kind, group: n.group, role: n.role })),
        links: b.links.map((l) => ({ id: l.id, label: l.label, title: l.title, from: l.from, to: l.to, plane: l.plane, protocol: l.protocol })),
        routes: b.routes.map((r) => ({ id: r.id, title: r.title, impact: r.impact, squads: r.squads })),
      };
    }
    case 'status': {
      const s = await api.status(t);
      return {
        at: s.at,
        overall: s.overall,
        routes: s.routes.map((r: any) => ({ id: r.id, title: r.title, status: r.status, users: r.users, usersSource: r.usersSource, root: r.root, probe: r.probe, impact: r.impact, squads: r.squads })),
        alerts: s.alerts.map((x: any) => ({ id: x.id, instance: x.instance, severity: x.severity, message: x.messageFa || x.message, since: x.since, silencedUntil: x.silencedUntil })),
        driftItems: s.drift?.length ?? 0,
      };
    }
    case 'diagnose': {
      const d = await api.diagnose(t);
      return { ...d, recentEvents: (d.recentEvents ?? []).map(ev) };
    }
    case 'describe': {
      const d = await api.describe(String(a.id ?? ''), t);
      return { ...d, events: (d.events ?? []).slice(0, 15).map(ev) };
    }
    case 'why':
      return api.why(String(a.id ?? ''), t);
    case 'path':
      return api.path(String(a.from ?? ''), String(a.to ?? ''), a.route ? String(a.route) : undefined, t);
    case 'events': {
      const sinceMs = Math.min(ctx.parseDuration(a.since, 7_200_000), 7 * 86_400_000);
      const list = await api.events({ sinceMs, until: t, entity: a.entity ? String(a.entity) : undefined, limit: 60 });
      return list.map(ev);
    }
    case 'metrics': {
      const spanMs = Math.min(ctx.parseDuration(a.since, 3_600_000), 7 * 86_400_000);
      const series = await api.metrics({ entity: String(a.id ?? ''), spanMs, until: t });
      return series.map(summarise);
    }
    case 'board_image':
      if (!ctx.image) throw new Error('pictures cannot be sent here');
      return ctx.image(a, t);
    default:
      throw new Error(`unknown tool "${name}"`);
  }
}

function ev(e: HubEvent) {
  return { ts: e.ts, kind: e.kind, severity: e.severity, entity: e.entityId ? `${e.entityKind}:${e.entityId}` : undefined, title: e.title, detail: e.detail || undefined };
}

export function summarise(s: Series) {
  const vals = s.points.map((p) => p[1]).filter((v): v is number => v !== null && Number.isFinite(v));
  const r = (v: number) => Math.round(v * 100) / 100;
  const coarse: [string, number | null][] = [];
  const stride = Math.max(1, Math.ceil(s.points.length / 24));
  for (let i = 0; i < s.points.length; i += stride) {
    const p = s.points[i];
    coarse.push([new Date(p[0]).toISOString().slice(11, 16), p[1] === null ? null : r(p[1])]);
  }
  return {
    label: s.label,
    unit: s.unit,
    samples: vals.length,
    min: vals.length ? r(Math.min(...vals)) : null,
    avg: vals.length ? r(vals.reduce((x, y) => x + y, 0) / vals.length) : null,
    max: vals.length ? r(Math.max(...vals)) : null,
    last: vals.length ? r(vals[vals.length - 1]) : null,
    series: coarse,
  };
}

function cap(s: string): string {
  return s.length > MAX_TOOL_OUTPUT ? s.slice(0, MAX_TOOL_OUTPUT) + '…(truncated)' : s;
}
