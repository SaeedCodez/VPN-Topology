<script setup lang="ts">
// Tiny inline sparkline (SVG), coloured with the current text colour.
const props = withDefaults(defineProps<{ values?: (number | null)[]; width?: number; height?: number; status?: string }>(), { width: 64, height: 18 })
const path = computed(() => {
  const v = (props.values ?? []).map((x) => (x === null || Number.isNaN(x) ? null : x))
  const nums = v.filter((x): x is number => x !== null)
  if (nums.length < 2) return ''
  const min = Math.min(...nums)
  const max = Math.max(...nums)
  const span = max - min || Math.max(1, Math.abs(max) * 0.1)
  const w = props.width
  const h = props.height - 2
  let d = ''
  let pen = false
  v.forEach((x, i) => {
    if (x === null) {
      pen = false
      return
    }
    const px = (i / (v.length - 1)) * w
    const py = 1 + h - ((x - min) / span) * h
    d += `${pen ? 'L' : 'M'}${px.toFixed(1)},${py.toFixed(1)}`
    pen = true
  })
  return d
})
</script>

<template>
  <svg :width="width" :height="height" :viewBox="`0 0 ${width} ${height}`" class="overflow-visible" aria-hidden="true">
    <path v-if="path" :d="path" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
    <line v-else :x1="0" :x2="width" :y1="height / 2" :y2="height / 2" stroke="currentColor" stroke-opacity="0.25" stroke-dasharray="2 3" />
  </svg>
</template>
