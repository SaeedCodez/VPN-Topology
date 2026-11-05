<script setup lang="ts">
// Small line icons per node kind (shared with the hub's image renderer).
import { roleIcon } from '#board/icons'
const props = defineProps<{ kind: string; size?: number }>()
const d = computed(() => roleIcon(props.kind))
</script>

<template>
  <svg :width="size ?? 18" :height="size ?? 18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path v-for="(p, i) in d" :key="i" :d="p" />
  </svg>
</template>
