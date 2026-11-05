<script setup lang="ts">
const { snapshot, conn, at, travel, panel, auth, logout } = useHub()
const { pref, cycle } = useTheme()

const o = computed(() => snapshot.value?.overall)
const status = computed(() => o.value?.status ?? 'unknown')
const statusText = computed(() => ({ ok: 'All systems normal', degraded: 'Degraded', down: 'Outage', unknown: 'No data' })[status.value])
const crit = computed(() => snapshot.value?.alerts.filter((a) => a.severity === 'critical' && !a.silencedUntil).length ?? 0)
const showDemo = ref(false)
</script>

<template>
  <header class="relative z-20 flex h-16 flex-none items-center gap-3 border-b border-line bg-surface px-4">
    <div class="flex items-center gap-2.5 pr-2">
      <svg width="26" height="26" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="8" fill="rgb(var(--fg))" /><circle cx="9" cy="16" r="3" fill="rgb(var(--bg))" /><circle cx="23" cy="10" r="3" fill="rgb(var(--bg))" /><circle cx="23" cy="22" r="3" fill="rgb(var(--ok))" /><path d="M12 15l8-4M12 17l8 4" stroke="rgb(var(--bg))" stroke-width="1.6" stroke-linecap="round" /></svg>
      <div class="leading-tight">
        <div class="text-[15px] font-semibold tracking-[-0.01em]">VPN Topology</div>
        <div class="text-[11.5px] text-fg-3">
          v{{ snapshot?.topology.version ?? '–' }}<span v-if="snapshot?.topology.gitCommit"> · {{ snapshot.topology.gitCommit.slice(0, 7) }}</span>
        </div>
      </div>
    </div>

    <div class="h-8 w-px bg-line" />

    <div
      class="flex h-10 items-center gap-2 rounded-full border px-3.5 text-[14px] font-medium"
      :class="{ 'border-ok/30 bg-ok/8 text-ok': status === 'ok', 'border-warn/30 bg-warn/8 text-warn': status === 'degraded', 'border-down/40 bg-down/10 text-down': status === 'down', 'border-line text-fg-2': status === 'unknown' }"
      role="status"
    >
      <span class="dot" :class="status" />
      {{ statusText }}
    </div>

    <dl class="hidden items-center gap-6 pl-3 md:flex">
      <div>
        <dt class="label">Users online</dt>
        <dd class="num text-[16px] font-semibold leading-5">{{ o?.usersOnline != null ? fmtNum(o.usersOnline) : '–' }}</dd>
      </div>
      <div>
        <dt class="label">Throughput</dt>
        <dd class="num font-mono text-[14px] font-medium leading-5">
          <span class="text-fg-3">↑</span>{{ fmtBps(o?.throughput.upBps ?? null) }} <span class="text-fg-3">↓</span>{{ fmtBps(o?.throughput.downBps ?? null) }}
        </dd>
      </div>
      <div>
        <dt class="label">Alerts</dt>
        <dd>
          <button class="num flex items-center gap-1.5 rounded-md text-[16px] font-semibold leading-5 hover:text-fg-2" :aria-label="`${o?.activeAlerts ?? 0} active alerts`" @click="panel = panel === 'alerts' ? null : 'alerts'">
            {{ o?.activeAlerts ?? 0 }}
            <span v-if="crit" class="pill h-5 bg-down/10 px-2 text-[11px] text-down">{{ crit }} critical</span>
          </button>
        </dd>
      </div>
    </dl>

    <div class="ml-auto flex items-center gap-2">
      <span v-if="at" class="pill h-8 bg-accent/10 px-3 text-[13px] text-accent">
        Replaying {{ fmtTime(at, true) }}
        <button class="ml-1 font-semibold underline-offset-2 hover:underline" @click="travel(null)">Back to live</button>
      </span>
      <span v-else class="flex items-center gap-1.5 text-[13px] text-fg-3" :title="`WebSocket: ${conn}`">
        <span class="dot" :class="conn === 'live' ? 'ok' : conn === 'connecting' ? 'degraded' : 'down'" style="width: 7px; height: 7px; box-shadow: none" />
        {{ conn === 'live' ? 'Live' : conn === 'connecting' ? 'Connecting' : 'Offline' }}
      </span>
      <span v-if="snapshot?.mode === 'demo'" class="pill h-7 border border-line px-2.5 text-fg-2">Demo data</span>
      <div v-if="snapshot?.mode === 'demo'" class="relative">
        <button class="btn" :aria-expanded="showDemo" @click="showDemo = !showDemo">Replay incident</button>
        <DemoMenu v-if="showDemo" @close="showDemo = false" />
      </div>
      <button class="btn-ghost" title="Telegram assistant settings" :aria-pressed="panel === 'assistant'" @click="panel = panel === 'assistant' ? null : 'assistant'">Assistant</button>
      <button class="btn-ghost" :title="snapshot?.drift.length ? `${snapshot.drift.length} drift item(s)` : 'No drift'" @click="panel = panel === 'drift' ? null : 'drift'">
        Drift <span class="num rounded-full bg-subtle px-2 text-[12px]">{{ snapshot?.drift.length ?? 0 }}</span>
      </button>
      <button class="btn-ghost w-10 px-0" :title="`Theme: ${pref} (click to change)`" :aria-label="`Theme: ${pref}`" @click="cycle">
        <svg v-if="pref === 'system'" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /></svg>
        <svg v-else-if="pref === 'dark'" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" /></svg>
        <svg v-else width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></svg>
      </button>
      <button v-if="!auth.mode || auth.mode === 'live'" class="btn-ghost" @click="logout">Sign out</button>
    </div>
  </header>
</template>
