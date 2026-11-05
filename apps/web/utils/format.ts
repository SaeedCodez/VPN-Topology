import type { Health } from '#contract'

// Shared with the hub's image renderer (one formatting for both).
export { fmtBps, fmtMbps, fmtAge, fmtNum, fmtMetric } from '#board/format'
import { fmtAge } from '#board/format'

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
