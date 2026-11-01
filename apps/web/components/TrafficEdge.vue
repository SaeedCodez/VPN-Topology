<script setup lang="ts">
import { EdgeLabelRenderer, getBezierPath, type EdgeProps } from '@vue-flow/core'
import type { LinkState, TopoLink } from '#contract'

/**
 * A link: thickness and dot speed scale with throughput (log scale), colour
 * is health, dashed for the control plane, red edges pulse. With
 * prefers-reduced-motion the dots are not drawn (colours stay).
 */
const props = defineProps<EdgeProps<{ spec: TopoLink; state?: LinkState; reducedMotion: boolean; showLabel: boolean }>>()

const geom = computed(() => {
  const [path, labelX, labelY] = getBezierPath({
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    targetX: props.targetX,
    targetY: props.targetY,
    sourcePosition: props.sourcePosition,
    targetPosition: props.targetPosition,
    curvature: 0.35,
  })
  return { path, labelX, labelY }
})

const st = computed(() => props.data?.state)
const spec = computed(() => props.data!.spec)
const total = computed(() => (st.value?.upBps ?? 0) + (st.value?.downBps ?? 0))
const mbps = computed(() => total.value / 1e6)
const hasTraffic = computed(() => st.value?.upBps != null || st.value?.downBps != null)
const width = computed(() => (spec.value.plane === 'control' ? 1.5 : Math.min(7.5, 1.6 + 2.1 * Math.log10(1 + mbps.value * 2))))
const status = computed(() => (st.value?.impact === 'affected' ? 'affected' : !st.value?.observed ? (hasTraffic.value && total.value > 0 ? 'ok' : 'idle') : st.value.status))
const color = computed(
  () =>
    ({
      ok: 'rgb(var(--ok))',
      degraded: 'rgb(var(--warn))',
      down: 'rgb(var(--down))',
      unknown: 'rgb(var(--unknown))',
      affected: 'rgb(var(--affected))',
      idle: 'rgb(var(--edge-idle))',
    })[status.value],
)
const opacity = computed(() => (status.value === 'ok' ? 0.75 : status.value === 'idle' ? 0.9 : 1))
// Dots: more traffic = faster and more of them; none when idle or dead.
const dots = computed(() => {
  if (props.data?.reducedMotion || spec.value.plane === 'control' || total.value < 2000 || status.value === 'down') return []
  const dur = Math.max(1.4, 6.2 - 1.25 * Math.log10(1 + mbps.value * 4))
  const n = mbps.value > 5 ? 3 : mbps.value > 0.5 ? 2 : 1
  return Array.from({ length: n }, (_, i) => ({ dur: `${dur.toFixed(2)}s`, begin: `${((i * dur) / n).toFixed(2)}s` }))
})
const label = computed(() => {
  const s = st.value
  const parts: string[] = []
  if (hasTraffic.value) parts.push(`↑ ${fmtMbps(s?.upBps)}  ↓ ${fmtMbps(s?.downBps)} Mbps`)
  if (s?.conns != null) parts.push(`${fmtNum(s.conns)} conn`)
  return parts.join(' · ')
})
const problem = computed(() => st.value?.checks.find((c) => c.effective === 'down') ?? st.value?.checks.find((c) => c.effective === 'degraded'))
</script>

<template>
  <g :class="[`edge-${status}`, selected ? 'edge-selected' : '']">
    <path :d="geom.path" fill="none" stroke="transparent" :stroke-width="Math.max(18, width + 12)" class="cursor-pointer" />
    <path
      v-if="selected"
      :d="geom.path"
      fill="none"
      stroke="rgb(var(--accent))"
      :stroke-width="width + 6"
      stroke-opacity="0.18"
      stroke-linecap="round"
    />
    <path
      class="edge-main"
      :d="geom.path"
      fill="none"
      :stroke="color"
      :stroke-opacity="opacity"
      :stroke-width="width"
      :stroke-dasharray="spec.plane === 'control' ? '6 6' : undefined"
      stroke-linecap="round"
    />
    <!-- Hidden until its motion starts, otherwise it would sit at the canvas origin. -->
    <circle v-for="(d, i) in dots" :key="`${i}-${d.dur}`" r="2.6" :fill="color" fill-opacity="0.95" opacity="0">
      <set attributeName="opacity" to="1" :begin="d.begin" />
      <animateMotion :dur="d.dur" :begin="d.begin" repeatCount="indefinite" :path="geom.path" rotate="auto" />
    </circle>
  </g>
  <EdgeLabelRenderer v-if="data?.showLabel && (hasTraffic || st?.observed || selected)">
    <div
      class="nodrag nopan pointer-events-auto absolute flex cursor-pointer items-center gap-1.5 whitespace-nowrap rounded-full border bg-surface px-2.5 py-[3px] text-[11.5px] leading-4 shadow-sm"
      :class="[
        status === 'down' ? 'border-down/50 text-down' : status === 'degraded' ? 'border-warn/50 text-warn' : status === 'affected' ? 'border-affected/40 text-affected' : 'border-line text-fg-2',
        selected ? 'ring-2 ring-accent/40' : '',
      ]"
      :style="{ transform: `translate(-50%, -50%) translate(${geom.labelX}px, ${geom.labelY}px)` }"
      :title="problem ? `${problem.title}: ${problem.detail}` : spec.protocol"
    >
      <span class="font-semibold text-fg">{{ spec.label ?? spec.id }}</span>
      <span v-if="label" class="num font-mono text-[11px]">{{ label }}</span>
      <span v-else-if="problem" class="max-w-[180px] truncate">{{ problem.detail }}</span>
      <span v-else class="max-w-[160px] truncate text-fg-3">{{ spec.protocol }}</span>
    </div>
  </EdgeLabelRenderer>
</template>
