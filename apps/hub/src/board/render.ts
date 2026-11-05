import { Bundle, Health, LinkState, NodeState, Snapshot, TopoLink, TopoNode } from '../contract';
import { estimate, Measure } from './fonts';
import { fmtAge, fmtMbps, fmtMetric, fmtNum } from './shared/format';
import { roleIcon } from './shared/icons';
import { frames, NODE_W } from './shared/layout';
import { ResolvedSelection } from './select';

/**
 * Draws the board as an SVG: the same positions (saved layout, else the
 * board's auto-layout), cards, links, group frames, labels and colours as the
 * web board, at one moment. Pure (no I/O): the hub rasterises it to PNG for
 * Telegram and the API. A selection crops to its nodes; with `context` the
 * rest of the board inside the crop stays visible, dimmed.
 *
 * The geometry and colours mirror VpnNode.vue, TrafficEdge.vue,
 * GroupFrame.vue and assets/css/main.css; change them together.
 */

export type Theme = 'light' | 'dark';

export interface RenderInput {
  bundle: Bundle;
  snap: Snapshot;
  positions: Record<string, { x: number; y: number }>;
  selection: ResolvedSelection;
  theme?: Theme;
  /** Series for the card sparklines, keyed like the board (n:<node>:cpu, m:<key>, c:<key>:v). */
  sparks?: Record<string, (number | null)[]>;
  /** Show the rest of the board inside the crop, dimmed. */
  context?: boolean;
  /** Link labels (traffic, connections). Default true. */
  labels?: boolean;
  measure?: Measure;
}

export interface RenderedSvg {
  svg: string;
  width: number;
  height: number;
  nodes: string[];
  links: string[];
  status: Health;
}

type Rgb = [number, number, number];
const TOKENS: Record<Theme, Record<string, Rgb>> = {
  light: {
    bg: [250, 250, 250], surface: [255, 255, 255], subtle: [244, 244, 245], line: [233, 233, 235], 'line-strong': [212, 212, 216],
    fg: [10, 10, 10], 'fg-2': [82, 82, 91], 'fg-3': [140, 140, 150], accent: [10, 114, 239], ok: [22, 163, 74], warn: [217, 119, 6],
    down: [220, 38, 38], unknown: [161, 161, 170], affected: [176, 138, 74], grid: [214, 214, 219], 'edge-idle': [190, 190, 198],
  },
  dark: {
    bg: [8, 8, 8], surface: [17, 17, 17], subtle: [26, 26, 28], line: [38, 38, 40], 'line-strong': [58, 58, 62],
    fg: [237, 237, 237], 'fg-2': [161, 161, 170], 'fg-3': [105, 105, 112], accent: [59, 142, 234], ok: [34, 197, 94], warn: [245, 158, 11],
    down: [239, 68, 68], unknown: [113, 113, 122], affected: [184, 151, 95], grid: [40, 40, 44], 'edge-idle': [70, 70, 76],
  },
};

const HEADER_H = 76;
const PAD = 44;
const SANS = "Geist, 'Geist Mono', sans-serif";
const MONO = "'Geist Mono', Geist, monospace";
const STATUS_LABEL: Record<Health, string> = { ok: 'Healthy', degraded: 'Degraded', down: 'Down', unknown: 'No data' };
const BUILTIN = ['cpu', 'mem', 'disk', 'load', 'uptime', 'net'];

/** Series key of a card metric, as the board's sparklines use it. */
export function cardSeriesKey(n: TopoNode, cardId: string): string | null {
  if (BUILTIN.includes(cardId)) return `n:${n.id}:${cardId}`;
  const m = n.metrics?.find((x) => x.id === cardId);
  if (m) return `m:${m.key}`;
  const c = n.checks?.find((x) => x.id === cardId);
  if (c) return `c:${c.key}:v`;
  return null;
}

export function cardSeriesKeys(b: Bundle, nodeIds?: Set<string>): string[] {
  const out: string[] = [];
  for (const n of b.nodes) {
    if (nodeIds && !nodeIds.has(n.id)) continue;
    for (const id of (n.card ?? []).slice(0, 3)) {
      const k = cardSeriesKey(n, id);
      if (k) out.push(k);
    }
  }
  return out;
}

/** Card height as the board lays it out (header, metrics or role, footer). */
export function cardHeight(n: TopoNode, measure: Measure = estimate): number {
  const header = 50;
  const footer = 10 + 38;
  if ((n.card ?? []).length) return header + 72 + footer;
  if (n.role) return header + 10 + 18 * Math.min(2, wrap(n.role, 220, 12.5, measure).length) + footer;
  return header + footer;
}

export function renderBoardSvg(inp: RenderInput): RenderedSvg {
  const { bundle: b, snap, selection: sel } = inp;
  const measure = inp.measure ?? estimate;
  const theme = inp.theme ?? 'light';
  const T = TOKENS[theme];
  const c = (name: string, a = 1) => (a >= 1 ? `rgb(${T[name].join(',')})` : `rgba(${T[name].join(',')},${a})`);
  const labels = inp.labels ?? true;
  const sparks = inp.sparks ?? {};
  const byId = new Map(b.nodes.map((n) => [n.id, n]));
  const pos = (id: string) => inp.positions[id] ?? { x: 0, y: 0 };
  const heights = new Map(b.nodes.map((n) => [n.id, cardHeight(n, measure)]));

  // ---- what is drawn, and the crop
  const focus = b.nodes.filter((n) => sel.nodes.has(n.id));
  if (!focus.length) throw new Error('nothing to draw: the selection has no nodes');
  const box = { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
  const grow = (x0: number, y0: number, x1: number, y1: number) => {
    box.x0 = Math.min(box.x0, x0);
    box.y0 = Math.min(box.y0, y0);
    box.x1 = Math.max(box.x1, x1);
    box.y1 = Math.max(box.y1, y1);
  };
  for (const n of focus) grow(pos(n.id).x, pos(n.id).y, pos(n.id).x + NODE_W, pos(n.id).y + heights.get(n.id)!);
  const focusPos = Object.fromEntries(focus.map((n) => [n.id, pos(n.id)]));
  const frameList = frames(b, inp.context || sel.all ? inp.positions : focusPos).map((f) => ({ ...f, h: frameHeight(b, f.id, inp.context || sel.all ? null : sel.nodes, inp.positions, heights, f) }));
  if (!inp.context) for (const f of frameList) grow(f.x, f.y, f.x + f.w, f.y + f.h);
  else for (const f of frameList) if (b.nodes.some((n) => n.group === f.id && sel.nodes.has(n.id))) grow(f.x, f.y, f.x + f.w, f.y + f.h);

  const inCrop = (n: TopoNode) => {
    const p = pos(n.id);
    return p.x < box.x1 && p.x + NODE_W > box.x0 && p.y < box.y1 && p.y + heights.get(n.id)! > box.y0;
  };
  const drawn = b.nodes.filter((n) => sel.nodes.has(n.id) || (inp.context && inCrop(n)));
  const drawnIds = new Set(drawn.map((n) => n.id));
  const edges = b.links.filter((l) => drawnIds.has(l.from) && drawnIds.has(l.to) && (sel.links.has(l.id) || inp.context));

  // Edge geometry first: labels can stick out of the node box.
  const geo = new Map(edges.map((l) => [l.id, bezier(l, pos, heights)]));
  const edgeLabels = labels
    ? edges.filter((l) => sel.links.has(l.id)).map((l) => ({ l, ...edgeLabel(l, snap.links[l.id], geo.get(l.id)!, measure) })).filter((x) => x.show)
    : [];
  for (const e of edgeLabels) grow(e.x - e.w / 2, e.y - 12, e.x + e.w / 2, e.y + 12);

  const ox = box.x0 - PAD;
  const oy = box.y0 - PAD;
  const worldW = box.x1 - box.x0 + 2 * PAD;
  const worldH = box.y1 - box.y0 + 2 * PAD;
  const width = Math.max(560, Math.ceil(worldW));
  const height = Math.ceil(worldH) + HEADER_H;
  const dx = (width - worldW) / 2; // centre a narrow selection under the header

  // ---- status of what is shown
  const status: Health = sel.all ? snap.overall.status : worstOf([...focus.map((n) => snap.nodes[n.id]), ...b.links.filter((l) => sel.links.has(l.id)).map((l) => snap.links[l.id])]);

  const out: string[] = [];
  out.push(`<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">`);
  out.push(
    `<defs><pattern id="grid" width="22" height="22" patternUnits="userSpaceOnUse" x="${(-ox + dx) % 22}" y="${(-oy + HEADER_H) % 22}"><circle cx="11" cy="11" r="1.2" fill="${c('grid')}"/></pattern>`,
    `<pattern id="stripes" width="12" height="12" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="6" height="12" fill="${c('affected', 0.07)}"/></pattern></defs>`,
  );
  out.push(`<rect width="${width}" height="${height}" fill="${c('bg')}"/>`);
  out.push(`<rect y="${HEADER_H}" width="${width}" height="${height - HEADER_H}" fill="url(#grid)"/>`);

  // ---- header: what, when, how it is
  out.push(...header(inp, sel, status, width, c, measure));

  out.push(`<g transform="translate(${r(dx - ox)},${r(HEADER_H - oy)})">`);
  for (const f of frameList) {
    const dim = inp.context && !b.nodes.some((n) => n.group === f.id && sel.nodes.has(n.id));
    out.push(
      `<g${dim ? ' opacity="0.45"' : ''}><rect x="${r(f.x)}" y="${r(f.y)}" width="${r(f.w)}" height="${r(f.h)}" rx="22" fill="${c('subtle', 0.55)}" stroke="${c('line')}"${f.kind === 'personal' ? ' stroke-dasharray="5 4"' : ''}/>`,
      `<text x="${r(f.x + 16)}" y="${r(f.y + 22)}" font-family="${SANS}" font-size="12" font-weight="500" letter-spacing="0.72" fill="${c('fg-3')}">${esc(f.title.toUpperCase())}</text></g>`,
    );
  }
  for (const l of [...edges].sort((a, z) => (a.plane === 'control' ? 0 : 1) - (z.plane === 'control' ? 0 : 1))) {
    out.push(edgeSvg(l, snap.links[l.id], geo.get(l.id)!, c, !sel.links.has(l.id)));
  }
  for (const n of drawn) out.push(nodeSvg(n, snap.nodes[n.id], pos(n.id), heights.get(n.id)!, c, measure, sparks, !sel.nodes.has(n.id), snap.drift));
  for (const e of edgeLabels) out.push(e.svg(c));
  out.push('</g></svg>');
  return { svg: out.join(''), width, height, nodes: focus.map((n) => n.id), links: [...sel.links], status };
}

// ------------------------------------------------------------------ pieces

type Color = (name: string, a?: number) => string;

function header(inp: RenderInput, sel: ResolvedSelection, status: Health, width: number, c: Color, measure: Measure): string[] {
  const { bundle: b, snap } = inp;
  const out: string[] = [];
  const route = sel.route ? b.routes.find((x) => x.id === sel.route) : undefined;
  const users = sel.all ? snap.overall.usersOnline : route ? snap.routes[route.id]?.users ?? null : null;
  const when = tehran(snap.at);
  const sub = `${sel.all ? '' : `${b.map.name || 'VPN Topology'} · `}${when} Tehran${snap.live ? '' : ' · history'}${snap.mode === 'demo' ? ' · demo' : ''}`;
  // Right side: status pill, then users and throughput.
  const right: string[] = [];
  if (users !== null) right.push(`${fmtNum(users)} users`);
  if (sel.all) right.push(`↑ ${fmtMbps(snap.overall.throughput.upBps)}  ↓ ${fmtMbps(snap.overall.throughput.downBps)} Mbps`);
  const rightText = right.join('  ·  ');
  const rightW = measure(rightText, 13, 'sans', 500);
  const pillText = STATUS_LABEL[status];
  const pillW = 30 + measure(pillText, 12.5, 'sans', 600);
  const pillX = width - 24 - pillW;
  const titleMax = pillX - 24 - (rightText ? rightW + 16 : 0) - 24;
  out.push(`<rect width="${width}" height="${HEADER_H}" fill="${c('surface')}"/><rect y="${HEADER_H - 1}" width="${width}" height="1" fill="${c('line')}"/>`);
  out.push(`<text x="24" y="34" font-family="${SANS}" font-size="17" font-weight="600" letter-spacing="-0.2" fill="${c('fg')}">${esc(fit(sel.title, Math.max(120, titleMax), 17, measure, 'sans', 600))}</text>`);
  out.push(`<text x="24" y="56" font-family="${SANS}" font-size="12.5" fill="${c('fg-3')}">${esc(fit(sub, Math.max(120, titleMax), 12.5, measure))}</text>`);
  const col = status === 'ok' ? 'ok' : status === 'degraded' ? 'warn' : status === 'down' ? 'down' : 'unknown';
  out.push(
    `<rect x="${r(pillX)}" y="24" width="${r(pillW)}" height="28" rx="14" fill="${c(col, 0.12)}"/>`,
    `<circle cx="${r(pillX + 15)}" cy="38" r="4.5" fill="${c(col)}"/>`,
    `<text x="${r(pillX + 25)}" y="42.5" font-family="${SANS}" font-size="12.5" font-weight="600" fill="${c(col)}">${pillText}</text>`,
  );
  if (rightText) out.push(`<text x="${r(pillX - 16)}" y="42.5" text-anchor="end" font-family="${SANS}" font-size="13" font-weight="500" fill="${c('fg-2')}">${esc(rightText)}</text>`);
  return out;
}

function nodeSvg(
  n: TopoNode,
  st: NodeState | undefined,
  p: { x: number; y: number },
  h: number,
  c: Color,
  measure: Measure,
  sparks: Record<string, (number | null)[]>,
  dim: boolean,
  drift: Snapshot['drift'],
): string {
  const W = NODE_W;
  const o: string[] = [`<g transform="translate(${r(p.x)},${r(p.y)})"${dim ? ' opacity="0.28"' : ''}>`];
  const root = st?.impact === 'root';
  const affected = st?.impact === 'affected';
  if (root) o.push(`<rect x="-3" y="-3" width="${W + 6}" height="${h + 6}" rx="19" fill="none" stroke="${c('down', 0.15)}" stroke-width="6"/>`);
  o.push(`<rect x="0.5" y="0.5" width="${W - 1}" height="${h - 1}" rx="16" fill="${c('surface')}" stroke="${root ? c('down') : affected ? c('affected', 0.6) : c('line')}"/>`);
  if (affected) o.push(`<rect x="0.5" y="0.5" width="${W - 1}" height="${h - 1}" rx="16" fill="url(#stripes)"/>`);

  // Header: status dot, icon, name, address.
  const status = affected ? 'affected' : !st ? 'unknown' : !st.observed ? 'hollow' : st.status;
  const dotCol = { ok: 'ok', degraded: 'warn', down: 'down', unknown: 'unknown', affected: 'affected', hollow: 'fg-3' }[status]!;
  if (status === 'down') o.push(`<circle cx="20.5" cy="32" r="8.5" fill="${c('down', 0.18)}"/>`);
  o.push(status === 'hollow' ? `<circle cx="20.5" cy="32" r="3.75" fill="none" stroke="${c('fg-3')}" stroke-width="1.5"/>` : `<circle cx="20.5" cy="32" r="4.5" fill="${c(dotCol)}"/>`);
  o.push(`<rect x="35" y="18" width="28" height="28" rx="8" fill="${c('subtle')}"/>`);
  o.push(`<g transform="translate(41,24) scale(${16 / 24})" fill="none" stroke="${c('fg-2')}" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">${roleIcon(n.kind).map((d) => `<path d="${d}"/>`).join('')}</g>`);
  const textX = 73;
  let nameMax = W - 16 - textX;
  if (st?.rebootedRecently) {
    const up = `↻ ${fmtAge(st.uptimeSec)}`;
    const pw = measure(up, 11, 'sans', 500) + 12;
    const nw = Math.min(measure(n.name, 15, 'sans', 600), nameMax - pw - 6);
    o.push(`<rect x="${r(textX + nw + 6)}" y="19" width="${r(pw)}" height="20" rx="10" fill="${c('warn', 0.12)}"/>`);
    o.push(`<text x="${r(textX + nw + 12)}" y="33" font-family="${SANS}" font-size="11" font-weight="500" fill="${c('warn')}">${esc(up)}</text>`);
    nameMax = nw;
  }
  o.push(`<text x="${textX}" y="29" font-family="${SANS}" font-size="15" font-weight="600" letter-spacing="-0.15" fill="${c('fg')}">${esc(fit(n.name, nameMax, 15, measure, 'sans', 600))}</text>`);
  o.push(`<text x="${textX}" y="45" font-family="${MONO}" font-size="11.5" fill="${c('fg-3')}">${esc(fit(n.address?.[0] ?? n.kind, W - 16 - textX, 11.5, measure, 'mono'))}</text>`);

  // Body: card metrics with sparklines, or the role.
  let y = 50;
  const card = (n.card ?? []).slice(0, 3);
  if (card.length) {
    const colW = (W - 32) / card.length;
    card.forEach((id, i) => {
      const m = st?.metrics[id];
      const x = 16 + i * colW;
      o.push(`<text x="${r(x)}" y="76" font-family="${SANS}" font-size="11" font-weight="500" letter-spacing="0.55" fill="${c('fg-3')}">${esc(fit((m?.label ?? id).toUpperCase(), colW - 4, 11, measure, 'sans', 500))}</text>`);
      o.push(`<text x="${r(x)}" y="98" font-family="${SANS}" font-size="15" font-weight="600" fill="${c('fg')}">${esc(fmtMetric(m?.value ?? null, m?.unit ?? ''))}</text>`);
      const key = cardSeriesKey(n, id);
      o.push(spark(key ? sparks[key] : undefined, x, 104, 62, 16, c('fg-3')));
    });
    y = 122;
  } else if (n.role) {
    const lines = wrap(n.role, W - 32, 12.5, measure).slice(0, 2);
    const all = wrap(n.role, W - 32, 12.5, measure);
    if (all.length > 2) lines[1] = fit(`${lines[1]} ${all.slice(2).join(' ')}`, W - 32, 12.5, measure);
    lines.forEach((t, i) => o.push(`<text x="16" y="${73 + i * 18}" font-family="${SANS}" font-size="12.5" fill="${c('fg-2')}">${esc(t)}</text>`));
    y = 60 + 18 * lines.length;
  }

  // Footer: root cause / affected, drift, the failing check, uptime.
  const fy = y + 10;
  o.push(`<rect x="0.5" y="${fy}" width="${W - 1}" height="1" fill="${c('line')}"/>`);
  const mid = fy + 19;
  let fx = 16;
  const pill = (text: string, bg: string, fg: string, dashed = false) => {
    const w = measure(text, 11, 'sans', 500) + 16;
    o.push(dashed ? `<rect x="${r(fx)}" y="${mid - 10}" width="${r(w)}" height="20" rx="10" fill="none" stroke="${bg}" stroke-dasharray="3 2"/>` : `<rect x="${r(fx)}" y="${mid - 10}" width="${r(w)}" height="20" rx="10" fill="${bg}"/>`);
    o.push(`<text x="${r(fx + 8)}" y="${mid + 4}" font-family="${SANS}" font-size="11" font-weight="500" fill="${fg}">${esc(text)}</text>`);
    fx += w + 6;
  };
  if (root) pill('root cause', c('down'), '#ffffff');
  else if (affected) pill('affected', c('affected', 0.15), c('affected'));
  const driftN = drift.filter((d) => d.host === n.id).reduce((s, d) => s + (d.kind === 'port' ? d.detail.split(',').length : 1), 0);
  if (driftN) pill(`drift ${driftN}`, c('line-strong'), c('fg-2'), true);
  const failing = st?.checks.find((x) => x.effective === 'down') ?? st?.checks.find((x) => x.effective === 'degraded');
  const uptime = st?.uptimeSec != null ? fmtAge(st.uptimeSec) : null;
  const rightText = uptime && !st?.rebootedRecently && !failing ? `up ${uptime}` : !n.agent && !st?.checks.length ? n.kind : '';
  const rw = rightText ? measure(rightText, 12, 'sans') : 0;
  if (rightText) o.push(`<text x="${W - 16}" y="${mid + 4}" text-anchor="end" font-family="${SANS}" font-size="12" fill="${c('fg-3')}">${esc(rightText)}</text>`);
  if (failing && (!affected || root)) {
    const col = failing.effective === 'down' ? c('down') : c('warn');
    o.push(`<text x="${r(fx)}" y="${mid + 4}" font-family="${SANS}" font-size="12" fill="${col}">${esc(fit(failing.detail, W - 16 - fx - (rw ? rw + 8 : 0), 12, measure))}</text>`);
  }
  o.push('</g>');
  return o.join('');
}

interface Geo {
  path: string;
  lx: number;
  ly: number;
}

/** Vue Flow's bezier edge (getBezierPath, curvature 0.35) from the right handle to the left handle. */
function bezier(l: TopoLink, pos: (id: string) => { x: number; y: number }, heights: Map<string, number>): Geo {
  const s = pos(l.from);
  const t = pos(l.to);
  const sx = s.x + NODE_W;
  const sy = s.y + (heights.get(l.from) ?? 150) / 2;
  const tx = t.x;
  const ty = t.y + (heights.get(l.to) ?? 150) / 2;
  const off = (d: number) => (d >= 0 ? 0.5 * d : 0.35 * 25 * Math.sqrt(-d));
  const scx = sx + off(tx - sx);
  const tcx = tx - off(tx - sx);
  return {
    path: `M${r(sx)},${r(sy)} C${r(scx)},${r(sy)} ${r(tcx)},${r(ty)} ${r(tx)},${r(ty)}`,
    lx: sx * 0.125 + scx * 0.375 + tcx * 0.375 + tx * 0.125,
    ly: sy * 0.5 + ty * 0.5,
  };
}

function linkLook(l: TopoLink, st: LinkState | undefined) {
  const total = (st?.upBps ?? 0) + (st?.downBps ?? 0);
  const hasTraffic = st?.upBps != null || st?.downBps != null;
  const status = st?.impact === 'affected' ? 'affected' : !st?.observed ? (hasTraffic && total > 0 ? 'ok' : 'idle') : st.status;
  const width = l.plane === 'control' ? 1.5 : Math.min(7.5, 1.6 + 2.1 * Math.log10(1 + (total / 1e6) * 2));
  const color = { ok: 'ok', degraded: 'warn', down: 'down', unknown: 'unknown', affected: 'affected', idle: 'edge-idle' }[status]!;
  const opacity = status === 'ok' ? 0.75 : status === 'idle' ? 0.9 : 1;
  return { status, width, color, opacity, hasTraffic };
}

function edgeSvg(l: TopoLink, st: LinkState | undefined, g: Geo, c: Color, dim: boolean): string {
  const k = linkLook(l, st);
  return `<path d="${g.path}" fill="none" stroke="${c(k.color)}" stroke-opacity="${dim ? 0.25 : k.opacity}" stroke-width="${r(k.width)}" stroke-linecap="round"${l.plane === 'control' ? ' stroke-dasharray="6 6"' : ''}/>`;
}

function edgeLabel(l: TopoLink, st: LinkState | undefined, g: Geo, measure: Measure) {
  const k = linkLook(l, st);
  const show = k.hasTraffic || !!st?.observed;
  const name = l.label ?? l.id;
  const parts: string[] = [];
  if (k.hasTraffic) parts.push(`↑ ${fmtMbps(st?.upBps)}  ↓ ${fmtMbps(st?.downBps)} Mbps`);
  if (st?.conns != null) parts.push(`${fmtNum(st.conns)} conn`);
  const problem = st?.checks.find((x) => x.effective === 'down') ?? st?.checks.find((x) => x.effective === 'degraded');
  const detail = parts.length ? parts.join(' · ') : problem ? fit(problem.detail, 180, 11.5, measure) : fit(l.protocol ?? '', 160, 11.5, measure);
  const detailMono = parts.length > 0;
  const nameW = measure(name, 11.5, 'sans', 600);
  const detailW = detail ? (detailMono ? measure(detail, 11, 'mono') : measure(detail, 11.5, 'sans')) : 0;
  const w = 20 + nameW + (detail ? 6 + detailW : 0) + 2;
  const svg = (c: Color) => {
    const border = k.status === 'down' ? c('down', 0.5) : k.status === 'degraded' ? c('warn', 0.5) : k.status === 'affected' ? c('affected', 0.4) : c('line');
    const fg = k.status === 'down' ? c('down') : k.status === 'degraded' ? c('warn') : k.status === 'affected' ? c('affected') : parts.length || problem ? c('fg-2') : c('fg-3');
    const x0 = g.lx - w / 2;
    return [
      `<rect x="${r(x0)}" y="${r(g.ly - 11.5)}" width="${r(w)}" height="23" rx="11.5" fill="${c('surface')}" stroke="${border}"/>`,
      `<text x="${r(x0 + 11)}" y="${r(g.ly + 4)}" font-family="${SANS}" font-size="11.5" font-weight="600" fill="${c('fg')}">${esc(name)}</text>`,
      detail ? `<text x="${r(x0 + 11 + nameW + 6)}" y="${r(g.ly + 4)}" font-family="${detailMono ? MONO : SANS}" font-size="${detailMono ? 11 : 11.5}" fill="${fg}">${esc(detail)}</text>` : '',
    ].join('');
  };
  return { show, x: g.lx, y: g.ly, w, svg };
}

function spark(values: (number | null)[] | undefined, x: number, y: number, w: number, h: number, color: string): string {
  const v = (values ?? []).map((n) => (n === null || Number.isNaN(n) ? null : n));
  const nums = v.filter((n): n is number => n !== null);
  if (nums.length < 2) return `<line x1="${r(x)}" x2="${r(x + w)}" y1="${r(y + h / 2)}" y2="${r(y + h / 2)}" stroke="${color}" stroke-opacity="0.25" stroke-dasharray="2 3"/>`;
  const min = Math.min(...nums);
  const max = Math.max(...nums);
  const span = max - min || Math.max(1, Math.abs(max) * 0.1);
  let d = '';
  let pen = false;
  v.forEach((n, i) => {
    if (n === null) {
      pen = false;
      return;
    }
    d += `${pen ? 'L' : 'M'}${r(x + (i / (v.length - 1)) * w)},${r(y + 1 + (h - 2) - ((n - min) / span) * (h - 2))}`;
    pen = true;
  });
  return `<path d="${d}" fill="none" stroke="${color}" stroke-width="1.4" stroke-linejoin="round" stroke-linecap="round"/>`;
}

/** Frames come from layout.frames() (fixed NODE_H); stretch them to the real card heights. */
function frameHeight(b: Bundle, groupId: string, only: Set<string> | null, positions: Record<string, { x: number; y: number }>, heights: Map<string, number>, f: { y: number; h: number }): number {
  const members = b.nodes.filter((n) => n.group === groupId && positions[n.id] && (!only || only.has(n.id)));
  const bottom = Math.max(...members.map((n) => positions[n.id].y + heights.get(n.id)!)) + 18;
  return Math.max(f.h, bottom - f.y);
}

// ------------------------------------------------------------------ helpers

const RANK: Record<Health, number> = { ok: 0, unknown: 1, degraded: 2, down: 3 };

function worstOf(states: ({ status: Health; observed: boolean } | undefined)[]): Health {
  let w: Health = 'ok';
  let seen = false;
  for (const s of states) {
    if (!s || !s.observed) continue;
    seen = true;
    if (RANK[s.status] > RANK[w]) w = s.status;
  }
  return seen ? w : 'unknown';
}

function tehran(iso: string): string {
  const d = new Date(iso);
  const parts = new Intl.DateTimeFormat('en-GB', { timeZone: 'Asia/Tehran', day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).formatToParts(d);
  const p = (t: string) => parts.find((x) => x.type === t)?.value ?? '';
  return `${p('day')} ${p('month')} ${p('year')} ${p('hour')}:${p('minute')}`;
}

/** Truncate with an ellipsis to fit maxW. */
export function fit(text: string, maxW: number, size: number, measure: Measure, face: 'sans' | 'mono' = 'sans', weight = 400): string {
  if (measure(text, size, face, weight) <= maxW) return text;
  const chars = [...text];
  let lo = 0;
  let hi = chars.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (measure(chars.slice(0, mid).join('').trimEnd() + '…', size, face, weight) <= maxW) lo = mid;
    else hi = mid - 1;
  }
  return chars.slice(0, lo).join('').trimEnd() + '…';
}

function wrap(text: string, maxW: number, size: number, measure: Measure): string[] {
  const lines: string[] = [];
  let cur = '';
  for (const word of text.split(/\s+/).filter(Boolean)) {
    const next = cur ? `${cur} ${word}` : word;
    if (cur && measure(next, size) > maxW) {
      lines.push(cur);
      cur = word;
    } else cur = next;
  }
  if (cur) lines.push(cur);
  return lines;
}

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function r(v: number): string {
  return String(Math.round(v * 10) / 10);
}
