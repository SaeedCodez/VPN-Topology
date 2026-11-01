<script setup lang="ts">
import type { HubEvent } from '#contract'

/**
 * Bottom timeline: the last 24 h of events (state changes, incidents, alerts,
 * reboots, restarts, watchdog) and a slider that replays the whole board at
 * any past moment.
 */
const { events, at, travel, selected, panel, loadingPast } = useHub()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval>
onMounted(() => (timer = setInterval(() => (now.value = Date.now()), 5000)))
onBeforeUnmount(() => clearInterval(timer))

const span = 24 * 3_600_000
const start = computed(() => now.value - span)
const pct = (t: number) => Math.max(0, Math.min(100, ((t - start.value) / span) * 100))

const visible = computed(() => events.value.filter((e) => Date.parse(e.ts) >= start.value && e.entityKind !== 'check'))

const incidents = computed(() => {
  const open = new Map<string, HubEvent>()
  const latest = new Map<string, string>()
  const out: { id: string; from: number; to: number; sev: string; title: string; ev: HubEvent; lane: number }[] = []
  for (const e of [...visible.value].reverse()) {
    if (e.kind !== 'incident') continue
    const key = e.data?.rootCause
    if (e.data?.state === 'open') {
      open.set(key, e)
      latest.set(key, e.title)
    } else if (e.data?.state === 'update') latest.set(key, e.title.replace(/^Root cause updated: /, 'Incident: '))
    else if (e.data?.state === 'resolved' && open.has(key)) {
      const o = open.get(key)!
      out.push({ id: `${key}-${o.id}`, from: Date.parse(o.ts), to: Date.parse(e.ts), sev: o.severity, title: latest.get(key) ?? o.title, ev: o, lane: 0 })
      open.delete(key)
    }
  }
  for (const [key, o] of open) out.push({ id: `${key}-${o.id}`, from: Date.parse(o.ts), to: now.value, sev: o.severity, title: latest.get(key) ?? o.title, ev: o, lane: 0 })
  // Overlapping incidents get their own lane.
  out.sort((a, b) => a.from - b.from)
  const laneEnd: number[] = []
  for (const i of out) {
    let l = laneEnd.findIndex((end) => end < i.from - 60_000)
    if (l < 0) l = laneEnd.length
    laneEnd[l] = i.to
    i.lane = Math.min(l, 2)
  }
  return out
})
const lanes = computed(() => Math.max(1, ...incidents.value.map((i) => i.lane + 1)))

const marks = computed(() => visible.value.filter((e) => ['reboot', 'restart', 'watchdog', 'topology', 'demo'].includes(e.kind) || (e.kind === 'alert' && e.data?.state === 'firing')))

const hours = computed(() => {
  const out: { t: number; label: string }[] = []
  const h = 3_600_000
  for (let t = Math.ceil(start.value / h) * h; t <= now.value; t += 3 * h) out.push({ t, label: fmtTime(t).slice(0, 5) })
  return out
})

const slider = ref(span)
watch(at, (v) => (slider.value = v === null ? span : v - start.value), { immediate: true })
let debounce: ReturnType<typeof setTimeout>
function onSlide() {
  clearTimeout(debounce)
  const t = start.value + slider.value
  debounce = setTimeout(() => travel(slider.value >= span - 10_000 ? null : t), 120)
}
function jump(t: number, e?: HubEvent) {
  travel(Math.min(t + 30_000, Date.now()))
  if (e?.entityKind === 'node' || e?.entityKind === 'link') {
    selected.value = { kind: e.entityKind, id: e.entityId! }
    panel.value = 'details'
  }
}
function step(dir: number) {
  const cur = at.value ?? now.value
  travel(Math.max(start.value, Math.min(now.value, cur + dir * 5 * 60_000)))
}

const showList = ref(false)
const list = computed(() => visible.value.filter((e) => e.kind !== 'state' || e.entityKind !== 'check').slice(0, 120))
const color = (s: string) => (s === 'critical' ? 'bg-down' : s === 'warning' ? 'bg-warn' : s === 'ok' ? 'bg-ok' : 'bg-fg-3')
const kindIcon: Record<string, string> = { reboot: '↻', restart: '⟳', watchdog: 'W', topology: 'T', demo: '▶', alert: '!' }
</script>

<template>
  <section class="relative z-10 flex-none border-t border-line bg-surface" aria-label="Timeline">
    <div v-if="showList" class="scroll-thin absolute bottom-full left-0 right-0 max-h-[46vh] overflow-y-auto border-t border-line bg-surface shadow-float">
      <ul class="divide-y divide-line">
        <li v-for="e in list" :key="e.id">
          <button class="flex w-full items-center gap-3 px-5 py-2 text-left hover:bg-subtle" @click="jump(Date.parse(e.ts), e)">
            <span class="num w-[62px] flex-none font-mono text-[12px] text-fg-3">{{ fmtTime(e.ts) }}</span>
            <span class="h-2 w-2 flex-none rounded-full" :class="color(e.severity)" />
            <span class="w-[72px] flex-none text-[12px] uppercase tracking-[0.05em] text-fg-3">{{ e.kind }}</span>
            <span class="min-w-0 flex-1 truncate text-[13.5px]"><span class="font-medium">{{ e.title }}</span><span v-if="e.detail" class="text-fg-3"> — {{ e.detail }}</span></span>
          </button>
        </li>
      </ul>
    </div>

    <div class="flex items-center gap-3 px-4 pt-2.5">
      <button class="btn h-9 px-3" :aria-expanded="showList" @click="showList = !showList">Events <span class="num text-fg-3">{{ list.length }}</span></button>
      <div class="flex items-center gap-1">
        <button class="btn-ghost h-9 w-9 px-0" aria-label="Back 5 minutes" @click="step(-1)">‹</button>
        <button class="btn-ghost h-9 w-9 px-0" aria-label="Forward 5 minutes" :disabled="!at" @click="step(1)">›</button>
      </div>
      <div class="num font-mono text-[13px]" :class="at ? 'text-accent' : 'text-fg-2'">
        {{ at ? fmtTime(at, true) : 'Live' }}<span v-if="loadingPast" class="ml-2 text-fg-3">loading…</span>
      </div>
      <button v-if="at" class="btn-primary h-9 px-3" @click="travel(null)">Back to live</button>
      <div class="ml-auto hidden items-center gap-4 text-[12px] text-fg-3 lg:flex">
        <span class="flex items-center gap-1.5"><span class="h-2 w-5 rounded-full bg-down/70" /> outage</span>
        <span class="flex items-center gap-1.5"><span class="h-2 w-5 rounded-full bg-warn/70" /> degraded</span>
        <span>↻ reboot · ⟳ restart · W watchdog · ! alert</span>
      </div>
    </div>

    <div class="relative mx-4 mb-2 mt-1 h-[74px]">
      <!-- incident ranges (row 1) and point events (row 2) -->
      <div class="absolute inset-x-0 top-1 h-[40px]">
        <button
          v-for="i in incidents"
          :key="i.id"
          class="absolute min-w-[5px] rounded-[5px] opacity-80 hover:opacity-100"
          :class="i.sev === 'critical' ? 'bg-down/70' : 'bg-warn/70'"
          :style="{ left: `${pct(i.from)}%`, width: `${Math.max(0.3, pct(i.to) - pct(i.from))}%`, top: `${(i.lane * 17) / lanes}px`, height: `${17 / lanes - (lanes > 1 ? 1 : 0)}px` }"
          :title="`${fmtTime(i.from)}–${fmtTime(i.to)} · ${i.title}`"
          @click="jump(i.from + 60_000, i.ev)"
        />
        <button
          v-for="e in marks"
          :key="e.id"
          class="absolute top-[21px] grid h-[18px] w-[18px] -translate-x-1/2 place-items-center rounded-full border-2 border-surface text-[9px] font-bold text-white"
          :class="color(e.severity)"
          :style="{ left: `${pct(Date.parse(e.ts))}%` }"
          :title="`${fmtTime(e.ts)} · ${e.title}`"
          @click="jump(Date.parse(e.ts), e)"
        >{{ kindIcon[e.kind] ?? '•' }}</button>
      </div>
      <!-- slider -->
      <input
        v-model.number="slider"
        type="range"
        class="slider absolute inset-x-0 top-[40px]"
        :min="0"
        :max="span"
        :step="10_000"
        aria-label="Replay the board at a past moment"
        @input="onSlide"
      />
      <div class="pointer-events-none absolute inset-x-0 top-[62px] h-3">
        <span v-for="h in hours" :key="h.t" class="num absolute -translate-x-1/2 font-mono text-[10.5px] text-fg-3" :style="{ left: `${pct(h.t)}%` }">{{ h.label }}</span>
      </div>
    </div>
  </section>
</template>
