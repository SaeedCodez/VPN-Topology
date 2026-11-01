<script setup lang="ts">
import { Handle, Position } from '@vue-flow/core'
import type { NodeState, TopoNode } from '#contract'

const props = defineProps<{
  data: { spec: TopoNode; state?: NodeState; sparks: Record<string, (number | null)[]>; drift: number; ghost?: boolean }
}>()

const st = computed(() => props.data.state)
const spec = computed(() => props.data.spec)
const status = computed(() => (st.value?.impact === 'affected' ? 'affected' : !st.value ? 'unknown' : !st.value.observed ? 'hollow' : st.value.status))
const address = computed(() => spec.value.address?.[0] ?? '')
const cardMetrics = computed(() =>
  (spec.value.card ?? []).slice(0, 3).map((id) => {
    const m = st.value?.metrics[id]
    return { id, label: m?.label ?? id, value: fmtMetric(m?.value ?? null, m?.unit ?? ''), spark: props.data.sparks[id] }
  }),
)
const failing = computed(() => st.value?.checks.find((c) => c.effective === 'down') ?? st.value?.checks.find((c) => c.effective === 'degraded'))
const uptime = computed(() => (st.value?.uptimeSec != null ? fmtAge(st.value.uptimeSec) : null))
const statusText = computed(() => {
  const s = st.value
  if (!s) return 'No data'
  if (s.impact === 'affected') return 'Affected'
  if (!s.observed) return 'Not observed'
  return statusLabel[s.status]
})
</script>

<template>
  <div
    class="node-card relative w-[252px] rounded-card border bg-surface text-left transition-[border-color,opacity] duration-200"
    :class="[
      st?.impact === 'root' ? 'border-down shadow-[0_0_0_3px_rgb(var(--down)/0.15)]' : st?.impact === 'affected' ? 'border-affected/60' : 'border-line',
      data.ghost ? 'border-dashed opacity-70' : '',
    ]"
    :aria-label="`${spec.name}: ${statusText}`"
  >
    <div v-if="st?.impact === 'affected'" class="pointer-events-none absolute inset-0 rounded-card" style="background: repeating-linear-gradient(135deg, rgb(var(--affected) / 0.07) 0 6px, transparent 6px 12px)" />
    <Handle type="target" :position="Position.Left" />
    <Handle type="source" :position="Position.Right" />

    <div class="flex items-center gap-2.5 px-4 pt-3.5">
      <span class="dot" :class="[status, st?.status === 'down' && st?.impact === 'root' ? 'pulse-ring' : '']" />
      <span class="grid h-7 w-7 flex-none place-items-center rounded-lg bg-subtle text-fg-2"><RoleIcon :kind="spec.kind" :size="16" /></span>
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <span class="truncate text-[15px] font-semibold leading-5 tracking-[-0.01em]">{{ spec.name }}</span>
          <span v-if="st?.rebootedRecently" class="pill h-5 flex-none bg-warn/12 px-1.5 text-[11px] text-warn" :title="`rebooted ${uptime} ago`">↻ {{ uptime }}</span>
        </div>
        <div class="truncate font-mono text-[11.5px] leading-4 text-fg-3">{{ address || spec.kind }}</div>
      </div>
    </div>

    <div v-if="cardMetrics.length" class="mt-3 grid gap-px px-4" :style="{ gridTemplateColumns: `repeat(${cardMetrics.length}, minmax(0, 1fr))` }">
      <div v-for="m in cardMetrics" :key="m.id" class="min-w-0">
        <div class="truncate text-[11px] font-medium uppercase tracking-[0.05em] text-fg-3">{{ m.label }}</div>
        <div class="num text-[15px] font-semibold leading-5">{{ m.value }}</div>
        <Sparkline :values="m.spark" :width="62" :height="16" class="text-fg-3" />
      </div>
    </div>
    <div v-else-if="spec.role" class="mt-2.5 line-clamp-2 px-4 text-[12.5px] leading-[18px] text-fg-2">{{ spec.role }}</div>

    <div class="mt-2.5 flex min-h-[34px] items-center gap-1.5 border-t border-line px-4 py-2 text-[12px]">
      <template v-if="st?.impact === 'root'">
        <span class="pill h-5 flex-none whitespace-nowrap bg-down px-2 text-[11px] text-white">root cause</span>
      </template>
      <template v-else-if="st?.impact === 'affected'">
        <span class="pill h-5 flex-none whitespace-nowrap bg-affected/15 px-2 text-[11px] text-affected">affected</span>
      </template>
      <span v-if="data.drift" class="pill h-5 border border-dashed border-line-strong px-2 text-[11px] text-fg-2" :title="`${data.drift} undeclared item(s)`">drift {{ data.drift }}</span>
      <span v-if="failing && st?.impact !== 'root' && st?.impact !== 'affected'" class="min-w-0 truncate" :class="failing.effective === 'down' ? 'text-down' : 'text-warn'">{{ failing.detail }}</span>
      <span v-else-if="failing && st?.impact === 'root'" class="min-w-0 truncate text-down">{{ failing.detail }}</span>
      <span class="ml-auto flex-none text-fg-3">
        <template v-if="uptime && !st?.rebootedRecently && !failing">up {{ uptime }}</template>
        <template v-else-if="!spec.agent && !st?.checks.length">{{ spec.kind }}</template>
      </span>
    </div>
  </div>
</template>
