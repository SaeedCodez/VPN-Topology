import type { Bundle, Describe, HubEvent, Series, Snapshot, DemoScenario } from '#contract'

/**
 * Client state of the board. Everything comes from the hub API; the browser
 * only renders it (the CLI shows the same facts).
 */

const bundle = shallowRef<Bundle | null>(null)
const liveSnapshot = shallowRef<Snapshot | null>(null)
const pastSnapshot = shallowRef<Snapshot | null>(null)
const at = ref<number | null>(null) // null = live
const events = shallowRef<HubEvent[]>([])
const layout = ref<Record<string, { x: number; y: number }>>({})
const conn = ref<'connecting' | 'live' | 'offline'>('connecting')
const auth = ref<{ checked: boolean; authenticated: boolean; mode?: string }>({ checked: false, authenticated: false })
const sparks = shallowRef<Record<string, (number | null)[]>>({})
const selected = ref<{ kind: 'node' | 'link'; id: string } | null>(null)
const panel = ref<'details' | 'alerts' | 'drift' | null>(null)
const loadingPast = ref(false)
const error = ref<string | null>(null)
let ws: WebSocket | null = null
let started = false
let sparkTimer: ReturnType<typeof setInterval> | null = null

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } })
  if (res.status === 401) {
    auth.value = { ...auth.value, authenticated: false }
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      msg = (await res.json()).message ?? msg
    } catch {
      /* not json */
    }
    throw new Error(msg)
  }
  return res.json() as Promise<T>
}

function connectWs() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/ws`)
  conn.value = 'connecting'
  ws.onopen = () => (conn.value = 'live')
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data)
    if (msg.type === 'snapshot') liveSnapshot.value = msg.data
    else if (msg.type === 'events') events.value = [...msg.data.slice().reverse(), ...events.value].slice(0, 3000)
    else if (msg.type === 'topology') void loadTopology()
  }
  ws.onclose = () => {
    conn.value = 'offline'
    setTimeout(() => auth.value.authenticated && connectWs(), 3000)
  }
}

async function loadTopology() {
  const t = await api<{ version: number; bundle: Bundle }>('/api/topology')
  bundle.value = t.bundle
  layout.value = await api('/api/layout')
}

async function loadEvents() {
  events.value = await api<HubEvent[]>('/api/events?since=25h&limit=3000')
}

/** Series keys shown as sparklines on node cards. */
function cardKeys(b: Bundle): string[] {
  const keys: string[] = []
  for (const n of b.nodes) {
    for (const id of (n.card ?? []).slice(0, 3)) {
      if (['cpu', 'mem', 'disk', 'load', 'uptime', 'net'].includes(id)) keys.push(`n:${n.id}:${id}`)
      else {
        const m = n.metrics?.find((x) => x.id === id)
        if (m) keys.push(`m:${m.key}`)
        const c = n.checks?.find((x) => x.id === id)
        if (c) keys.push(`c:${c.key}:v`)
      }
    }
  }
  return keys
}

async function loadSparks() {
  if (!bundle.value) return
  const keys = cardKeys(bundle.value)
  if (!keys.length) return
  const until = at.value ? `&until=${new Date(at.value).toISOString()}` : ''
  const series = await api<Series[]>(`/api/metrics?keys=${encodeURIComponent(keys.join(','))}&since=1h&step=1m${until}`)
  const out: Record<string, (number | null)[]> = {}
  for (const s of series) out[s.key] = s.points.map((p) => p[1])
  sparks.value = out
}

async function start() {
  if (started) return
  started = true
  try {
    const me = await api<{ authenticated: boolean; mode: string }>('/api/auth/me')
    auth.value = { checked: true, authenticated: me.authenticated, mode: me.mode }
    if (!me.authenticated) {
      started = false
      return
    }
    await loadTopology()
    liveSnapshot.value = await api('/api/snapshot')
    await loadEvents()
    connectWs()
    void loadSparks()
    sparkTimer = setInterval(() => void loadSparks().catch(() => {}), 30_000)
  } catch (e: any) {
    started = false
    if (e.message !== 'unauthorized') error.value = e.message
    auth.value = { ...auth.value, checked: true }
  }
}

async function login(password: string, totp: string) {
  await api('/api/auth/login', { method: 'POST', body: JSON.stringify({ password, totp }) })
  auth.value = { checked: true, authenticated: true }
  await start()
}

async function logout() {
  await api('/api/auth/logout', { method: 'POST' })
  ws?.close()
  if (sparkTimer) clearInterval(sparkTimer)
  started = false
  auth.value = { checked: true, authenticated: false }
}

let travelSeq = 0
/** Replay the board at a past moment (null = back to live). */
async function travel(t: number | null) {
  if (t !== null && t > Date.now() - 8000) t = null
  at.value = t
  if (t === null) {
    pastSnapshot.value = null
    void loadSparks()
    return
  }
  const seq = ++travelSeq
  loadingPast.value = true
  try {
    const s = await api<Snapshot>(`/api/snapshot?at=${new Date(t).toISOString()}`)
    if (seq === travelSeq) pastSnapshot.value = s
    void loadSparks()
  } finally {
    if (seq === travelSeq) loadingPast.value = false
  }
}

let layoutTimer: ReturnType<typeof setTimeout> | null = null
function saveLayout(pos: Record<string, { x: number; y: number }>) {
  layout.value = { ...layout.value, ...pos }
  if (layoutTimer) clearTimeout(layoutTimer)
  layoutTimer = setTimeout(() => void api('/api/layout', { method: 'PUT', body: JSON.stringify(layout.value) }).catch(() => {}), 600)
}

async function resetLayout() {
  layout.value = {}
  await api('/api/layout', { method: 'PUT', body: JSON.stringify({}) }).catch(() => {})
}

function describe(id: string): Promise<Describe> {
  const q = at.value ? `?at=${new Date(at.value).toISOString()}` : ''
  return api<Describe>(`/api/describe/${encodeURIComponent(id)}${q}`)
}

function metrics(id: string, since = '24h'): Promise<Series[]> {
  const q = at.value ? `&until=${new Date(at.value).toISOString()}` : ''
  return api<Series[]>(`/api/metrics?entity=${encodeURIComponent(id)}&since=${since}${q}`)
}

function scenarios(): Promise<(DemoScenario & { running: boolean; lastStart?: string })[]> {
  return api('/api/demo/scenarios')
}

function play(id: string) {
  return api('/api/demo/play', { method: 'POST', body: JSON.stringify({ scenario: id }) })
}

export function useHub() {
  const snapshot = computed(() => (at.value !== null ? pastSnapshot.value ?? liveSnapshot.value : liveSnapshot.value))
  return {
    bundle,
    snapshot,
    liveSnapshot,
    at,
    events,
    layout,
    conn,
    auth,
    sparks,
    selected,
    panel,
    loadingPast,
    error,
    start,
    login,
    logout,
    travel,
    saveLayout,
    resetLayout,
    describe,
    metrics,
    scenarios,
    play,
  }
}
