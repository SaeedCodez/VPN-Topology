<script setup lang="ts">
import type { DemoScenario } from '#contract'
const emit = defineEmits<{ close: [] }>()
const { scenarios, play, travel } = useHub()
const list = ref<(DemoScenario & { running: boolean; lastStart?: string })[]>([])
const busy = ref<string | null>(null)
onMounted(async () => (list.value = await scenarios()))
async function run(id: string) {
  busy.value = id
  try {
    await play(id)
    await travel(null)
    list.value = await scenarios()
    emit('close')
  } finally {
    busy.value = null
  }
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="floating absolute right-0 top-12 z-40 w-[420px] p-2" role="menu">
    <div class="px-3 pb-2 pt-1.5 text-[13px] text-fg-2">Replay a real incident on the demo hub. It plays live, compressed to minutes; the board, CLI and alerts react as they would in production.</div>
    <button
      v-for="s in list"
      :key="s.id"
      class="flex w-full items-start gap-3 rounded-xl px-3 py-2.5 text-left hover:bg-subtle disabled:opacity-60"
      :disabled="busy !== null || s.running"
      role="menuitem"
      @click="run(s.id)"
    >
      <span class="mt-1 font-mono text-[11.5px] text-fg-3">{{ s.date }}</span>
      <span class="min-w-0 flex-1">
        <span class="block text-[14px] font-medium leading-5">{{ s.title }}</span>
        <span class="mt-0.5 block text-[12.5px] leading-[18px] text-fg-2">{{ s.description }}</span>
      </span>
      <span class="mt-0.5 flex-none text-[12px] text-fg-3">{{ s.running ? 'running' : `${Math.round(s.durationSec / 60)} min` }}</span>
    </button>
  </div>
</template>
