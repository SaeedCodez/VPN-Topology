import { Bundle, Snapshot } from '../contract';
import { NotFound, path as pathView } from '../engine/views';

/**
 * Which part of the board a picture shows. Pure.
 *
 * Nothing set = the whole board. Otherwise the union of: a declared route,
 * a path (from → via… → to, the same path `vpnmap path` follows), explicit
 * nodes, and explicit links (with their two ends). With explicit nodes or
 * links, every link between two drawn nodes is drawn too, so "these three
 * servers" shows how they connect.
 */
export interface BoardSelection {
  route?: string;
  from?: string;
  to?: string;
  via?: string[];
  nodes?: string[];
  links?: string[];
}

export interface ResolvedSelection {
  all: boolean;
  nodes: Set<string>;
  links: Set<string>;
  /** Short English title for the image header ("Main route · France via Relay 1"). */
  title: string;
  /** The declared route when one was asked for (its users go in the header). */
  route?: string;
}

export const MAX_SELECTION_IDS = 60;

export function resolveSelection(b: Bundle, snap: Snapshot, sel: BoardSelection = {}): ResolvedSelection {
  const nodeIds = new Set(b.nodes.map((n) => n.id));
  const name = (id: string) => b.nodes.find((n) => n.id === id)?.name ?? id;
  const nodeId = (raw: string) => {
    const v = String(raw ?? '').trim();
    if (nodeIds.has(v)) return v;
    const byName = b.nodes.find((n) => n.name.toLowerCase() === v.toLowerCase() || n.id.toLowerCase() === v.toLowerCase());
    if (byName) return byName.id;
    throw new NotFound(`no node "${v}" (use list_entities for ids)`);
  };
  const linkId = (raw: string) => {
    const v = String(raw ?? '').trim();
    const l = b.links.find((x) => x.id === v || x.id.toLowerCase() === v.toLowerCase() || x.label?.toLowerCase() === v.toLowerCase());
    if (!l) throw new NotFound(`no link "${v}" (use list_entities for ids)`);
    return l;
  };

  const via = (sel.via ?? []).filter(Boolean);
  const listed = (sel.nodes ?? []).length + (sel.links ?? []).length + via.length;
  if (listed > MAX_SELECTION_IDS) throw new Error(`at most ${MAX_SELECTION_IDS} ids in one picture`);
  const empty = !sel.route && !sel.from && !sel.to && !via.length && !(sel.nodes ?? []).length && !(sel.links ?? []).length;
  if (empty) return { all: true, nodes: new Set(nodeIds), links: new Set(b.links.map((l) => l.id)), title: b.map.name || 'VPN Topology' };

  const nodes = new Set<string>();
  const links = new Set<string>();
  const titles: string[] = [];
  let routeId: string | undefined;

  if (sel.route) {
    const r = b.routes.find((x) => x.id === sel.route || x.id.toLowerCase() === String(sel.route).toLowerCase());
    if (!r) throw new NotFound(`no route "${sel.route}" (routes: ${b.routes.map((x) => x.id).join(', ')})`);
    r.nodes.forEach((n) => nodes.add(n));
    r.hops.forEach((h) => links.add(h));
    titles.push(r.title);
    routeId = r.id;
  }

  if (sel.from || sel.to || via.length) {
    if (!sel.from || !sel.to) throw new Error('a path needs both from and to');
    const stops = [nodeId(sel.from), ...via.map(nodeId), nodeId(sel.to)];
    for (let i = 0; i + 1 < stops.length; i++) {
      const p = pathView(b, snap, stops[i], stops[i + 1]);
      for (const h of p.hops) (h.kind === 'node' ? nodes : links).add(h.id);
    }
    titles.push(stops.map(name).join(' → '));
  }

  const explicit: string[] = [];
  for (const raw of sel.nodes ?? []) {
    const id = nodeId(raw);
    nodes.add(id);
    explicit.push(name(id));
  }
  for (const raw of sel.links ?? []) {
    const l = linkId(raw);
    links.add(l.id);
    nodes.add(l.from);
    nodes.add(l.to);
    explicit.push(l.label ?? l.id);
  }
  if (explicit.length) titles.push(explicit.length <= 4 ? explicit.join(', ') : `${explicit.length} items`);

  // Explicit nodes show how they connect; a route or path shows only its own hops
  // (users → France → internet should not grow the DIRECT link next to it).
  if ((sel.nodes ?? []).length || (sel.links ?? []).length) for (const l of b.links) if (nodes.has(l.from) && nodes.has(l.to)) links.add(l.id);
  return { all: false, nodes, links, title: titles.join(' + '), route: routeId };
}
