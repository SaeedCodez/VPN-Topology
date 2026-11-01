<script setup lang="ts">
// "Probable root cause: X, N users affected" — the first thing to read in an outage.
const { snapshot, selected, panel } = useHub()
const causes = computed(() => snapshot.value?.overall.rootCauses ?? [])
const top = computed(() => causes.value[0])
const open = ref(false)
function focus(kind: string, id: string) {
  if (kind === 'route') return
  selected.value = { kind: kind as 'node' | 'link', id }
  panel.value = 'details'
}
</script>

<template>
  <div
    v-if="top"
    class="pointer-events-none absolute top-4 z-10 transition-[left,right] duration-200"
    :class="panel ? 'left-4 right-[472px]' : 'left-1/2 w-[min(760px,calc(100%-32px))] -translate-x-1/2'"
  >
    <div class="floating pointer-events-auto overflow-hidden" :class="top.impact === 'outage' ? 'border-down/40' : 'border-warn/40'">
      <button class="flex w-full items-center gap-3 px-4 py-3 text-left" @click="focus(top.kind, top.elementId)">
        <span class="dot" :class="top.impact === 'outage' ? 'down' : 'degraded'" />
        <span class="min-w-0 flex-1">
          <span class="flex items-center gap-2 text-[12px] font-medium uppercase tracking-[0.06em]" :class="top.impact === 'outage' ? 'text-down' : 'text-warn'">
            Probable root cause
            <span v-if="top.squads?.length" class="pill h-5 px-2 text-[11px] normal-case tracking-normal" :class="top.critical ? 'bg-down text-white' : 'bg-subtle text-fg-2'">{{ top.squads.join(' · ') }}</span>
          </span>
          <span class="block truncate text-[15px] font-semibold leading-6">
            {{ top.title }}<span class="font-normal text-fg-2"> — {{ top.summary }}</span>
          </span>
        </span>
        <span class="flex-none text-right">
          <span class="num block text-[18px] font-semibold leading-6">{{ top.usersSource === 'estimate' ? '≈' : '' }}{{ fmtNum(top.usersAffected) }}</span>
          <span class="block text-[12px] text-fg-3">{{ top.impact === 'outage' ? 'users affected' : 'users degraded' }} · since {{ fmtTime(top.since) }}</span>
        </span>
      </button>
      <div v-if="causes.length > 1" class="border-t border-line">
        <button class="flex h-9 w-full items-center px-4 text-[13px] text-fg-2 hover:bg-subtle" @click="open = !open">
          {{ open ? 'Hide' : 'Show' }} {{ causes.length - 1 }} other incident{{ causes.length > 2 ? 's' : '' }}
        </button>
        <div v-if="open" class="border-t border-line">
          <button v-for="c in causes.slice(1)" :key="c.id" class="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-subtle" @click="focus(c.kind, c.elementId)">
            <span class="dot" :class="c.impact === 'outage' ? 'down' : 'degraded'" style="box-shadow: none" />
            <span class="min-w-0 flex-1 truncate text-[14px]"><b class="font-medium">{{ c.title }}</b> <span class="text-fg-2">— {{ c.summary }}</span></span>
            <span class="num flex-none text-[13px] text-fg-2">{{ c.usersSource === 'estimate' ? '≈' : '' }}{{ fmtNum(c.usersAffected) }} users</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
