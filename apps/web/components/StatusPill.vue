<script setup lang="ts">
import type { Health } from '#contract'
const props = defineProps<{ status: Health | 'affected' | 'root'; label?: string; size?: 'sm' | 'md' }>()
const cls = computed(
  () =>
    ({
      ok: 'bg-ok/10 text-ok',
      degraded: 'bg-warn/10 text-warn',
      down: 'bg-down/10 text-down',
      unknown: 'bg-unknown/15 text-fg-2',
      affected: 'bg-affected/15 text-affected',
      root: 'bg-down text-white',
    })[props.status],
)
const text = computed(() => props.label ?? ({ ok: 'Healthy', degraded: 'Degraded', down: 'Down', unknown: 'No data', affected: 'Affected', root: 'Root cause' } as const)[props.status])
</script>

<template>
  <span class="pill" :class="[cls, size === 'md' ? 'h-7 px-3 text-[13px]' : '']">
    <span v-if="status !== 'root'" class="dot" :class="status === 'affected' ? 'affected' : status" style="width: 7px; height: 7px; box-shadow: none" />
    {{ text }}
  </span>
</template>
