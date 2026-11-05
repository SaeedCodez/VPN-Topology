<script setup lang="ts">
import type { Describe, LinkState, NodeState, Series, TopoLink, TopoNode } from '#contract'

const { selected, panel, describe, metrics, snapshot, at, bundle } = useHub()
const d = ref<Describe | null>(null)
const charts = ref<{ unit: string; title: string; series: Series[] }[]>([])
const err = ref<string | null>(null)
const loading = ref(false)

async function load() {
  if (panel.value !== 'details' || !selected.value) return
  const id = selected.value.id
  loading.value = true
  err.value = null
  try {
    const [desc, ser] = await Promise.all([describe(id), metrics(id, '24h')])
    if (selected.value?.id !== id) return
    d.value = desc
    const groups = new Map<string, Series[]>()
    for (const s of ser.filter((x) => x.points.some((p) => p[1] !== null))) {
      const k = s.unit || s.label
      groups.set(k, [...(groups.get(k) ?? []), s])
    }
    charts.value = [...groups.entries()].map(([unit, series]) => ({
      unit,
      title: series.map((s) => s.label).join(' · '),
      series,
    }))
  } catch (e: any) {
    err.value = e.message
  } finally {
    loading.value = false
  }
}
watch([selected, panel, at], load, { immediate: true })
// Refresh the live state of the open element every snapshot (cheap: from the snapshot).
const liveState = computed<NodeState | LinkState | null>(() => {
  if (!selected.value || !snapshot.value) return null
  return selected.value.kind === 'node' ? snapshot.value.nodes[selected.value.id] : snapshot.value.links[selected.value.id]
})
const spec = computed(() => d.value?.spec as (TopoNode & TopoLink) | undefined)
const state = computed(() => liveState.value ?? (d.value?.state as any))
const name = computed(() => {
  if (!spec.value) return selected.value?.id
  if (d.value?.kind === 'link') return `${spec.value.label ? spec.value.label + ' · ' : ''}${spec.value.title ?? `${spec.value.from} → ${spec.value.to}`}`
  return spec.value.name
})
const nodeName = (id: string) => bundle.value?.nodes.find((n) => n.id === id)?.name ?? id
const close = () => {
  panel.value = null
  selected.value = null
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && panel.value) close()
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

const alerts = computed(() => snapshot.value?.alerts ?? [])
const drift = computed(() => snapshot.value?.drift ?? [])
</script>

<template>
  <aside v-if="panel" class="floating absolute bottom-4 right-4 top-4 z-20 flex w-[440px] max-w-[calc(100%-32px)] flex-col overflow-hidden" :aria-label="panel === 'details' ? `Details of ${name}` : panel">
    <!-- Alerts -->
    <template v-if="panel === 'alerts'">
      <div class="flex items-center gap-3 border-b border-line px-5 py-4">
        <h2 class="flex-1 text-[17px] font-semibold">Active alerts</h2>
        <button class="btn-ghost w-10 px-0" aria-label="Close" @click="close">✕</button>
      </div>
      <div class="scroll-thin flex-1 space-y-3 overflow-y-auto p-5">
        <p v-if="!alerts.length" class="text-fg-2">No active alerts.</p>
        <div v-for="a in alerts" :key="a.id + a.instance" class="card p-4">
          <div class="flex items-center gap-2">
            <StatusPill :status="a.severity === 'critical' ? 'down' : 'degraded'" :label="a.severity" />
            <span class="text-[13px] text-fg-3">since {{ fmtTime(a.since) }}</span>
            <span v-if="a.silencedUntil" class="pill bg-subtle text-fg-2">silenced</span>
          </div>
          <div class="mt-2 text-[15px] font-medium leading-6">{{ a.message }}</div>
          <div class="mt-1 text-[14px] leading-6 text-fg-2" dir="rtl" lang="fa">{{ a.messageFa }}</div>
          <div class="mt-2 font-mono text-[11.5px] text-fg-3">{{ a.id }}<template v-if="a.instance"> · {{ a.instance }}</template> · {{ a.definedIn.file }}:{{ a.definedIn.line }}</div>
        </div>
      </div>
    </template>

    <!-- Telegram assistant settings -->
    <AssistantPanel v-else-if="panel === 'assistant'" @close="close" />

    <!-- Drift -->
    <template v-else-if="panel === 'drift'">
      <div class="flex items-center gap-3 border-b border-line px-5 py-4">
        <h2 class="flex-1 text-[17px] font-semibold">Drift</h2>
        <button class="btn-ghost w-10 px-0" aria-label="Close" @click="close">✕</button>
      </div>
      <div class="scroll-thin flex-1 space-y-3 overflow-y-auto p-5">
        <p class="text-[14px] text-fg-2">Things the servers report that the topology files do not declare. The hub never adds them by itself: run <code class="font-mono text-[13px]">vpnmap discover</code> to draft YAML, then review and apply.</p>
        <p v-if="!drift.length" class="text-fg-2">Nothing undeclared.</p>
        <div v-for="x in drift" :key="x.id" class="card border-dashed p-4">
          <div class="text-[14px] font-medium">{{ x.title }}</div>
          <div class="mt-1 font-mono text-[12.5px] text-fg-2">{{ x.detail }}</div>
        </div>
      </div>
    </template>

    <!-- Details of a node or link -->
    <template v-else>
      <div class="border-b border-line px-5 py-4">
        <div class="flex items-start gap-3">
          <span v-if="d?.kind === 'node'" class="grid h-9 w-9 flex-none place-items-center rounded-xl bg-subtle text-fg-2"><RoleIcon :kind="spec?.kind ?? 'server'" /></span>
          <div class="min-w-0 flex-1">
            <h2 class="text-[17px] font-semibold leading-6 tracking-[-0.01em]">{{ name }}</h2>
            <div class="mt-1.5 flex flex-wrap items-center gap-1.5">
              <StatusPill v-if="state" :status="state.observed === false ? 'unknown' : state.status" :label="state.observed === false ? 'Not observed' : undefined" />
              <StatusPill v-if="state?.impact" :status="state.impact" />
              <span v-if="at" class="pill bg-accent/10 text-accent">at {{ fmtTime(at, true) }}</span>
            </div>
          </div>
          <button class="btn-ghost w-10 flex-none px-0" aria-label="Close details" @click="close">✕</button>
        </div>
        <div v-if="d" class="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px]">
          <span class="font-mono text-fg-3">defined in {{ d.definedIn.file }}:{{ d.definedIn.line }}</span>
          <a v-for="(u, i) in d.docs" :key="u" :href="u" target="_blank" rel="noopener noreferrer" class="font-medium text-accent hover:underline">{{ i === 0 ? 'Notion page' : 'Component docs' }} ↗</a>
        </div>
      </div>

      <div class="scroll-thin flex-1 space-y-6 overflow-y-auto px-5 py-5">
        <p v-if="err" class="text-down">{{ err }}</p>
        <p v-else-if="!d && loading" class="text-fg-3">Loading…</p>

        <template v-if="d && spec">
          <section v-if="d.why.length && state?.status !== 'ok'" aria-label="Why">
            <div class="label mb-2">Why</div>
            <ul class="space-y-1.5 text-[14px] leading-[21px]">
              <li v-for="(w, i) in d.why" :key="i" class="flex gap-2"><span class="mt-[7px] h-1.5 w-1.5 flex-none rounded-full bg-fg-3" />{{ w }}</li>
            </ul>
          </section>

          <section>
            <div class="label mb-2">Details</div>
            <dl class="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-[13.5px]">
              <template v-if="d.kind === 'node'">
                <dt class="text-fg-3">Address</dt><dd class="font-mono text-[12.5px]">{{ spec.address?.join(', ') || '–' }}</dd>
                <dt class="text-fg-3">Group</dt><dd>{{ bundle?.map.groups.find((g) => g.id === spec?.group)?.title ?? spec?.group }}</dd>
                <dt class="text-fg-3">Role</dt><dd>{{ spec.role || '–' }}</dd>
                <dt class="text-fg-3">Agent</dt><dd>{{ spec.agent ? (state as NodeState)?.agent?.lastSeen ? `reporting · last push ${ago((state as NodeState).agent!.lastSeen, at ?? Date.now())}` : 'not reporting' : 'none' }}</dd>
                <template v-if="(state as NodeState)?.bootTime">
                  <dt class="text-fg-3">Booted</dt><dd>{{ fmtTime((state as NodeState).bootTime, true) }} · up {{ fmtAge((state as NodeState).uptimeSec) }}</dd>
                </template>
                <template v-for="f in spec.facts ?? []" :key="f.label"><dt class="text-fg-3">{{ f.label }}</dt><dd>{{ f.value }}</dd></template>
              </template>
              <template v-else>
                <dt class="text-fg-3">Path</dt><dd>{{ nodeName(spec.from) }} → {{ nodeName(spec.to) }}</dd>
                <dt class="text-fg-3">Plane</dt><dd>{{ spec.plane === 'data' ? 'Data (user traffic)' : 'Control' }}</dd>
                <dt class="text-fg-3">Protocol</dt><dd>{{ spec.protocol || '–' }}</dd>
                <dt class="text-fg-3">Throughput</dt>
                <dd class="num font-mono text-[12.5px]">↑ {{ fmtBps((state as LinkState)?.upBps) }} · ↓ {{ fmtBps((state as LinkState)?.downBps) }}<span v-if="(state as LinkState)?.trafficSource === 'fallback'" class="text-fg-3"> (fallback source)</span></dd>
                <template v-if="(state as LinkState)?.conns != null"><dt class="text-fg-3">Connections</dt><dd class="num">{{ fmtNum((state as LinkState).conns) }}</dd></template>
                <template v-if="spec.notes"><dt class="text-fg-3">Notes</dt><dd>{{ spec.notes }}</dd></template>
              </template>
              <template v-if="d.routes.length"><dt class="text-fg-3">Routes</dt><dd>{{ d.routes.join(', ') }}</dd></template>
            </dl>
          </section>

          <section v-if="state?.checks?.length">
            <div class="label mb-2">Checks</div>
            <ul class="divide-y divide-line rounded-xl border border-line">
              <li v-for="c in state.checks" :key="c.key" class="flex gap-3 px-3 py-2.5">
                <span class="dot mt-1.5" :class="c.effective" style="box-shadow: none" />
                <div class="min-w-0 flex-1">
                  <div class="flex items-baseline gap-2">
                    <span class="truncate text-[13.5px] font-medium">{{ c.title }}</span>
                    <span class="ml-auto flex-none font-mono text-[11px] text-fg-3">{{ c.collector }}</span>
                  </div>
                  <div class="text-[13px] leading-[19px]" :class="c.effective === 'down' ? 'text-down' : c.effective === 'degraded' ? 'text-warn' : 'text-fg-2'">{{ c.detail }}</div>
                  <div class="text-[11.5px] text-fg-3">{{ c.source }}<template v-if="c.observedAt"> · {{ ago(c.observedAt, at ?? Date.now()) }}</template></div>
                </div>
              </li>
            </ul>
          </section>

          <section v-if="charts.length">
            <div class="label mb-1">Last 24 hours</div>
            <div v-for="c in charts" :key="c.unit" class="mt-3">
              <div class="mb-1 flex items-center gap-2 text-[12.5px] text-fg-2">
                <span class="truncate">{{ c.title }}</span><span class="ml-auto text-fg-3">{{ c.unit }}</span>
              </div>
              <LineChart :series="c.series" :unit="c.unit" :height="110" :marker="at" />
            </div>
          </section>

          <section v-if="d.runbook.length">
            <div class="label mb-1">Read-only diagnostics</div>
            <p class="mb-2.5 text-[12.5px] text-fg-3">From the runbook. Copy and run them yourself; the board never executes anything.</p>
            <div class="space-y-2">
              <CopyCmd v-for="r in d.runbook" :key="r.cmd" :title="r.title" :cmd="r.cmd" :from="r.from" />
            </div>
          </section>

          <section v-if="d.events.length">
            <div class="label mb-2">Recent events</div>
            <ul class="space-y-2">
              <li v-for="e in d.events" :key="e.id" class="flex gap-2.5 text-[13px] leading-[19px]">
                <span class="num w-14 flex-none font-mono text-[11.5px] text-fg-3">{{ fmtTime(e.ts).slice(0, 5) }}</span>
                <span class="dot mt-1.5" :class="e.severity === 'critical' ? 'down' : e.severity === 'warning' ? 'degraded' : e.severity === 'ok' ? 'ok' : 'unknown'" style="width: 7px; height: 7px; box-shadow: none" />
                <span class="min-w-0"><span class="font-medium">{{ e.title }}</span><span v-if="e.detail" class="block truncate text-fg-3">{{ e.detail }}</span></span>
              </li>
            </ul>
          </section>
        </template>
      </div>
    </template>
  </aside>
</template>
