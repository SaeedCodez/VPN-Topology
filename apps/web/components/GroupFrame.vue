<script setup lang="ts">
const props = defineProps<{ data: { title: string; kind: string; w: number; h: number } }>()
</script>

<template>
  <div
    class="rounded-[22px] border border-line"
    :class="props.data.kind === 'personal' ? 'border-dashed' : ''"
    :style="{ width: `${data.w}px`, height: `${data.h}px`, background: 'rgb(var(--subtle) / 0.55)' }"
  >
    <div class="flex h-9 items-center gap-2 px-4 text-[12px] font-medium uppercase tracking-[0.06em] text-fg-3">
      {{ data.title }}
    </div>
  </div>
</template>
