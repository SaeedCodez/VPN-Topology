import type { Health } from '#contract'

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
  if (sec < 172800) return `${Math.floor(sec / 3600)}h ${String(Math.round((sec % 3600) / 60)).padStart(2, '0')}m`
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

const tz = 'Asia/Tehran'

export function fmtTime(iso: string | number | null | undefined, withDate = false): string {
  if (iso === null || iso === undefined) return '–'
  const d = new Date(iso)
  return d.toLocaleString('en-GB', {
    timeZone: tz,
    hour: '2-digit',
    minute: '2-digit',
    second: withDate ? undefined : '2-digit',
    day: withDate ? '2-digit' : undefined,
    month: withDate ? 'short' : undefined,
    hour12: false,
  })
}

export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '–'
  return `${fmtAge((now - Date.parse(iso)) / 1000)} ago`
}

export const statusLabel: Record<Health, string> = { ok: 'Healthy', degraded: 'Degraded', down: 'Down', unknown: 'No data' }

export function rgbVar(name: string, alpha = 1): string {
  if (typeof window === 'undefined') return '#888'
  const v = getComputedStyle(document.documentElement).getPropertyValue(`--${name}`).trim()
  return alpha === 1 ? `rgb(${v})` : `rgb(${v} / ${alpha})`
}
