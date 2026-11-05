/**
 * Number formatting shared by the board (re-exported from utils/format.ts) and
 * the hub's board image renderer, so both print the same values.
 */

export function fmtBps(v: number | null | undefined, short = false): string {
  if (v === null || v === undefined) return '–'
  const u = short ? ['', 'K', 'M', 'G'] : [' bps', ' Kbps', ' Mbps', ' Gbps']
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}${u[3]}`
  if (v >= 1e6) return `${(v / 1e6).toFixed(v >= 1e8 ? 0 : 1)}${u[2]}`
  if (v >= 1e3) return `${Math.round(v / 1e3)}${u[1]}`
  return `${Math.round(v)}${u[0]}`
}

export function fmtMbps(v: number | null | undefined): string {
  if (v === null || v === undefined) return '–'
  const m = v / 1e6
  return m >= 100 ? m.toFixed(0) : m >= 10 ? m.toFixed(1) : m.toFixed(2)
}

export function fmtAge(sec: number | null | undefined): string {
  if (sec === null || sec === undefined) return '–'
  if (sec < 90) return `${Math.round(sec)}s`
  if (sec < 5400) return `${Math.round(sec / 60)}m`
  if (sec < 172800) {
    const m = Math.round(sec / 60)
    return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
  }
  return `${Math.round(sec / 86400)}d`
}

export function fmtNum(v: number | null | undefined): string {
  if (v === null || v === undefined) return '–'
  return Math.abs(v) >= 1000 ? Math.round(v).toLocaleString('en-US') : Number.isInteger(v) ? String(v) : v.toFixed(1)
}

export function fmtMetric(v: number | null | undefined, unit: string): string {
  if (v === null || v === undefined) return '–'
  switch (unit) {
    case '%':
      return `${Math.round(v)}%`
    case 'bps':
      return fmtBps(v, true)
    case 'ms':
      return v < 10 ? `${v.toFixed(1)}ms` : `${Math.round(v)}ms`
    case 's':
      return fmtAge(v)
    case 'days':
      return `${Math.round(v)}d`
    default:
      return fmtNum(v)
  }
}
