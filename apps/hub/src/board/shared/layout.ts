import type { Bundle } from '../../contract'

export const NODE_W = 252
export const NODE_H = 150
const GAP_X = 96
const GAP_Y = 44

/**
 * Left-to-right layered layout made for this kind of map: users on the left,
 * the internet on the right, every data hop one column further, exits pulled
 * next to the internet, and control-only nodes (panel, central, storage)
 * placed beside what they manage. Rows keep group members together so
 * datacenter frames stay compact. Saved positions (dragging) always win.
 */
export function autoLayout(b: Bundle): Record<string, { x: number; y: number }> {
  const ids = b.nodes.map((n) => n.id)
  const data = b.links.filter((l) => l.plane === 'data')
  const hasData = new Set(data.flatMap((l) => [l.from, l.to]))
  const edges: { from: string; to: string; w: number }[] = data.map((l) => ({ from: l.from, to: l.to, w: 3 }))
  for (const l of b.links.filter((x) => x.plane === 'control')) {
    if (!hasData.has(l.from) && hasData.has(l.to)) {
      // A manager (panel, central) sits in the same column as what it manages.
      for (const p of data.filter((d) => d.to === l.to)) edges.push({ from: p.from, to: l.from, w: 0.5 })
    } else edges.push({ from: l.from, to: l.to, w: 1 })
  }
  const succ = new Map<string, string[]>(ids.map((i) => [i, []]))
  const pred = new Map<string, string[]>(ids.map((i) => [i, []]))
  for (const e of edges) {
    if (e.from === e.to || !succ.has(e.from) || !succ.has(e.to)) continue
    succ.get(e.from)!.push(e.to)
    pred.get(e.to)!.push(e.from)
  }

  // Topological order (back edges of cycles are ignored).
  const order: string[] = []
  const state = new Map<string, number>()
  const back = new Set<string>()
  const visit = (n: string) => {
    state.set(n, 1)
    for (const s of succ.get(n)!) {
      if (state.get(s) === 1) back.add(`${n}>${s}`)
      else if (!state.get(s)) visit(s)
    }
    state.set(n, 2)
    order.unshift(n)
  }
  for (const n of ids) if (!pred.get(n)!.length && !state.get(n)) visit(n)
  for (const n of ids) if (!state.get(n)) visit(n)
  const fwd = (a: string, s: string) => !back.has(`${a}>${s}`)

  // Columns: longest path from the sources...
  const rank = new Map<string, number>(ids.map((i) => [i, 0]))
  for (const n of order) for (const s of succ.get(n)!) if (fwd(n, s)) rank.set(s, Math.max(rank.get(s)!, rank.get(n)! + 1))
  // ...then pull every non-source node right, next to its nearest successor.
  for (const n of [...order].reverse()) {
    const ss = succ.get(n)!.filter((s) => fwd(n, s))
    if (!ss.length || !pred.get(n)!.length) continue
    rank.set(n, Math.max(rank.get(n)!, Math.min(...ss.map((s) => rank.get(s)!)) - 1))
  }

  const maxRank = Math.max(...rank.values())
  const cols: string[][] = Array.from({ length: maxRank + 1 }, () => [])
  const groupIdx = new Map(b.map.groups.map((g, i) => [g.id, i]))
  const byId = new Map(b.nodes.map((n) => [n.id, n]))
  const groupKey = (id: string) => {
    const n = byId.get(id)!
    const g = b.map.groups.find((x) => x.id === n.group)
    return g && g.kind !== 'external' ? n.group : `solo:${id}`
  }
  for (const n of ids) cols[rank.get(n)!].push(n)
  for (const c of cols) c.sort((a, z) => (groupIdx.get(byId.get(a)!.group) ?? 0) - (groupIdx.get(byId.get(z)!.group) ?? 0))

  // Row order: barycenter sweeps, keeping group members contiguous.
  const idx = new Map<string, number>()
  const reindex = () => cols.forEach((c) => c.forEach((n, i) => idx.set(n, i - c.length / 2)))
  reindex()
  const neighbours = (n: string, dir: 'in' | 'out' | 'both') => {
    const out: { id: string; w: number }[] = []
    for (const e of edges) {
      if ((dir !== 'in' && e.from === n) || (dir === 'both' && e.to === n)) out.push({ id: e.from === n ? e.to : e.from, w: e.w })
      else if (dir === 'in' && e.to === n) out.push({ id: e.from, w: e.w })
    }
    return out
  }
  for (let it = 0; it < 8; it++) {
    const seq = it % 2 === 0 ? cols.keys() : [...cols.keys()].reverse().values()
    for (const ci of seq) {
      const c = cols[ci]
      const bary = new Map<string, number>()
      for (const n of c) {
        const nb = neighbours(n, it % 2 === 0 ? 'in' : 'out')
        const all = nb.length ? nb : neighbours(n, 'both')
        const tw = all.reduce((s, x) => s + x.w, 0)
        bary.set(n, tw ? all.reduce((s, x) => s + idx.get(x.id)! * x.w, 0) / tw : idx.get(n)!)
      }
      const gb = new Map<string, number[]>()
      for (const n of c) gb.set(groupKey(n), [...(gb.get(groupKey(n)) ?? []), bary.get(n)!])
      const gmean = (k: string) => gb.get(k)!.reduce((s, x) => s + x, 0) / gb.get(k)!.length
      c.sort((a, z) => gmean(groupKey(a)) - gmean(groupKey(z)) || bary.get(a)! - bary.get(z)!)
      reindex()
    }
  }

  // Vertical positions: pull towards neighbours, then remove overlaps in order.
  const y = new Map<string, number>()
  const step = NODE_H + GAP_Y
  cols.forEach((c) => c.forEach((n, i) => y.set(n, (i - (c.length - 1) / 2) * step)))
  for (let it = 0; it < 30; it++) {
    for (const c of cols) {
      for (const n of c) {
        const nb = neighbours(n, 'both')
        const tw = nb.reduce((s, x) => s + x.w, 0)
        if (tw) y.set(n, 0.5 * y.get(n)! + 0.5 * (nb.reduce((s, x) => s + y.get(x.id)! * x.w, 0) / tw))
      }
      // Groups need a little extra air between them (frame titles).
      for (let i = 1; i < c.length; i++) {
        const gap = groupKey(c[i]) === groupKey(c[i - 1]) ? step : step + 34
        if (y.get(c[i])! < y.get(c[i - 1])! + gap) y.set(c[i], y.get(c[i - 1])! + gap)
      }
      for (let i = c.length - 2; i >= 0; i--) {
        const gap = groupKey(c[i]) === groupKey(c[i + 1]) ? step : step + 34
        if (y.get(c[i])! > y.get(c[i + 1])! - gap) y.set(c[i], (y.get(c[i])! + y.get(c[i + 1])! - gap) / 2)
      }
      for (let i = 1; i < c.length; i++) {
        const gap = groupKey(c[i]) === groupKey(c[i - 1]) ? step : step + 34
        if (y.get(c[i])! < y.get(c[i - 1])! + gap) y.set(c[i], y.get(c[i - 1])! + gap)
      }
    }
  }
  const out: Record<string, { x: number; y: number }> = {}
  for (const n of ids) out[n] = { x: rank.get(n)! * (NODE_W + GAP_X), y: y.get(n)! }
  separateFrames(b, out, groupKey)
  const minY = Math.min(...Object.values(out).map((p) => p.y))
  for (const n of ids) out[n] = { x: out[n].x, y: Math.round(out[n].y - minY) }
  return out
}

/**
 * Frames must not cover nodes of other groups. Each framed group moves as one
 * block and each ungrouped node on its own; whichever block sits lower is
 * pushed down until nothing overlaps.
 */
function separateFrames(b: Bundle, pos: Record<string, { x: number; y: number }>, groupKey: (id: string) => string) {
  const blocks = new Map<string, string[]>()
  for (const n of b.nodes) blocks.set(groupKey(n.id), [...(blocks.get(groupKey(n.id)) ?? []), n.id])
  const rect = (members: string[], framed: boolean) => {
    const xs = members.map((m) => pos[m].x)
    const ys = members.map((m) => pos[m].y)
    const pad = framed ? 18 : 6
    return { x0: Math.min(...xs) - pad, x1: Math.max(...xs) + NODE_W + pad, y0: Math.min(...ys) - pad - (framed ? 30 : 0), y1: Math.max(...ys) + NODE_H + pad }
  }
  const keys = [...blocks.keys()]
  for (let it = 0; it < 60; it++) {
    let moved = false
    const r = new Map(keys.map((k) => [k, rect(blocks.get(k)!, !k.startsWith('solo:'))]))
    for (let i = 0; i < keys.length; i++) {
      for (let j = i + 1; j < keys.length; j++) {
        const a = r.get(keys[i])!
        const c = r.get(keys[j])!
        if (a.x0 >= c.x1 || c.x0 >= a.x1 || a.y0 >= c.y1 || c.y0 >= a.y1) continue
        // Push the lower block down below the upper one.
        const [upper, lower] = a.y0 + a.y1 <= c.y0 + c.y1 ? [a, keys[j]] : [c, keys[i]]
        const lr = r.get(lower)!
        const dy = upper.y1 - lr.y0 + 16
        for (const m of blocks.get(lower)!) pos[m].y += dy
        r.set(lower, { ...lr, y0: lr.y0 + dy, y1: lr.y1 + dy })
        moved = true
      }
    }
    if (!moved) break
  }
}

export interface Frame {
  id: string
  title: string
  kind: string
  x: number
  y: number
  w: number
  h: number
}

/** Group frames are recomputed from member positions, so they follow drags. */
export function frames(b: Bundle, pos: Record<string, { x: number; y: number }>): Frame[] {
  const out: Frame[] = []
  for (const gr of b.map.groups) {
    if (!(gr.frame ?? gr.kind !== 'external')) continue
    const members = b.nodes.filter((n) => n.group === gr.id && pos[n.id])
    if (!members.length) continue
    const xs = members.map((n) => pos[n.id].x)
    const ys = members.map((n) => pos[n.id].y)
    const pad = 18
    const x = Math.min(...xs) - pad
    const y = Math.min(...ys) - pad - 30
    out.push({ id: gr.id, title: gr.title, kind: gr.kind, x, y, w: Math.max(...xs) + NODE_W + pad - x, h: Math.max(...ys) + NODE_H + pad - y })
  }
  return out
}
