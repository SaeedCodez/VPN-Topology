<script setup lang="ts">
import type { OpsServerView, OpsSettingsView, OpsTaskView } from '#contract'
import type { OpsGrant } from '~/composables/useHub'

/**
 * Ops actions. The assistant can read and change servers only through the
 * closed catalog (docs/ops-actions.md): reads run at once, changes only after
 * an approver taps Approve in Telegram. Servers are the topology nodes with
 * `ops: {runner: true}`, so a new node appears here by itself. The SSH
 * private key never leaves the hub; the panel shows the commands you run on
 * each server yourself.
 */
const emit = defineEmits<{ close: [] }>()
const { ops } = useHub()

const view = ref<OpsSettingsView | null>(null)
const tasks = ref<OpsTaskView[]>([])
const err = ref<string | null>(null)
const saved = ref<string | null>(null)
const busy = ref(false)
const form = reactive({ enabled: false, actionModel: '', approverUserIds: '' })
const grant = ref<{ node: string; g: OpsGrant } | null>(null)
const testing = ref<Record<string, string>>({})
const fp = ref<Record<string, string>>({})
const hostEdit = ref<Record<string, string>>({})
const openLog = ref<string | null>(null)

function fill(v: OpsSettingsView) {
  view.value = v
  form.enabled = v.enabled
  form.actionModel = v.actionModel
  form.approverUserIds = v.approverUserIds.join(', ')
}

async function load() {
  err.value = null
  try {
    fill(await ops.get())
    tasks.value = await ops.tasks()
  } catch (e: any) {
    err.value = e.message
  }
}
onMounted(load)

async function save(patch: Record<string, unknown>, note = 'Saved') {
  busy.value = true
  err.value = null
  saved.value = null
  try {
    fill(await ops.save(patch))
    saved.value = note
    setTimeout(() => (saved.value = null), 2000)
  } catch (e: any) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

const saveSettings = () => save({ enabled: form.enabled, actionModel: form.actionModel.trim(), approverUserIds: form.approverUserIds })
const killSwitch = () => save({ enabled: false }, 'Ops off')
const setAccess = (s: OpsServerView, v: string) => save({ servers: [{ node: s.node, access: v === '1h' ? { minutes: 60 } : v === '24h' ? { minutes: 1440 } : v }] }, 'Access updated')
const saveFp = (s: OpsServerView) => save({ servers: [{ node: s.node, hostKey: (fp.value[s.node] ?? '').trim() || null }] }, 'Host key saved')
const saveHost = (s: OpsServerView) => save({ servers: [{ node: s.node, host: (hostEdit.value[s.node] ?? '').trim() || null }] }, 'Host saved')

async function showGrant(s: OpsServerView) {
  grant.value = null
  try {
    grant.value = { node: s.node, g: await ops.grant(s.node) }
  } catch (e: any) {
    err.value = e.message
  }
}

async function testServer(s: OpsServerView) {
  testing.value = { ...testing.value, [s.node]: '…' }
  try {
    const r = await ops.testServer(s.node)
    testing.value = { ...testing.value, [s.node]: (r.ok ? '✓ ' : '✗ ') + r.detail }
    fill(await ops.get())
  } catch (e: any) {
    testing.value = { ...testing.value, [s.node]: '✗ ' + e.message }
  }
}

const accessValue = (s: OpsServerView) => (typeof s.access === 'object' ? 'window' : s.access)

const status = computed(() => {
  const v = view.value
  if (!v) return { tone: 'unknown', text: '' }
  if (v.demo) return { tone: 'unknown', text: 'Disabled in demo mode' }
  if (!v.enabled) return { tone: 'unknown', text: 'Off' }
  if (!v.actionModel) return { tone: 'degraded', text: 'Needs an action model' }
  if (!v.approverUserIds.length) return { tone: 'degraded', text: 'No approver set' }
  const on = v.servers.filter((s) => s.reachable).length
  return { tone: 'ok', text: `On · ${on}/${v.servers.length} server(s) with access${v.running ? ' · a plan is running' : ''}` }
})

const statusFa: Record<string, string> = { pending: 'در انتظار', confirming: 'تأیید دوم', approved: 'تأییدشده', running: 'در حال اجرا', done: 'انجام شد', failed: 'ناموفق', 'rolled-back': 'برگشت', cancelled: 'لغو', expired: 'منقضی' }
const tone = (s: string) => (s === 'done' ? 'ok' : ['running', 'pending', 'approved', 'confirming'].includes(s) ? 'unknown' : 'down')
</script>

<template>
  <div class="flex items-center gap-3 border-b border-line px-5 py-4">
    <div class="flex-1">
      <h2 class="text-[17px] font-semibold">Ops actions</h2>
      <div v-if="view" class="mt-1 flex items-center gap-1.5 text-[13px] text-fg-2">
        <span class="dot" :class="status.tone" style="width: 7px; height: 7px; box-shadow: none" />{{ status.text }}
      </div>
    </div>
    <button v-if="view?.enabled" class="btn-ghost text-down" title="Turn ops off at once; a running plan stops before its next step" @click="killSwitch">Kill switch</button>
    <button class="btn-ghost w-10 px-0" aria-label="Close" @click="emit('close')">✕</button>
  </div>

  <div class="scroll-thin flex-1 space-y-5 overflow-y-auto p-5">
    <p class="text-[14px] leading-6 text-fg-2">
      In Telegram an approver can ask the assistant to fix something. It can only use the actions each node's <code>ops:</code> block allows: reads run at once, changes are sent as a plan and run only after you tap <b>Approve</b>. The runner on each server checks every request again and refuses anything else.
    </p>
    <p v-if="err" class="rounded-ctl border border-down/40 bg-down/10 px-3 py-2 text-[14px] text-down">{{ err }}</p>

    <template v-if="view">
      <p v-if="view.demo" class="rounded-ctl border border-line bg-subtle px-3 py-2 text-[14px] text-fg-2">This hub runs in demo mode without a login, so every ops action is refused.</p>

      <label class="flex items-center justify-between gap-3">
        <span class="text-[15px] font-medium">Allow ops actions</span>
        <input v-model="form.enabled" type="checkbox" class="h-5 w-5 accent-[rgb(var(--fg))]" :disabled="view.demo" />
      </label>

      <div>
        <label class="label" for="ops-model">Model for ops (OpenRouter)</label>
        <input id="ops-model" v-model="form.actionModel" class="input mt-1.5 font-mono text-[14px]" spellcheck="false" placeholder="anthropic/claude-sonnet-4.5" :disabled="view.demo" />
        <p class="mt-1.5 text-[13px] text-fg-3">Uses the assistant's OpenRouter key.</p>
      </div>

      <div>
        <label class="label" for="ops-approvers">Approver Telegram user ids</label>
        <input id="ops-approvers" v-model="form.approverUserIds" class="input mt-1.5 font-mono text-[14px]" inputmode="numeric" spellcheck="false" placeholder="123456789" :disabled="view.demo" />
        <p class="mt-1.5 text-[13px] text-fg-3">Only they can use ops in the alert chat and tap Approve. Turn on Telegram 2-step verification for these accounts.</p>
      </div>

      <div class="flex items-center gap-2">
        <button class="btn-primary" :disabled="busy || view.demo" @click="saveSettings">{{ busy ? 'Saving…' : 'Save' }}</button>
        <button class="btn" :disabled="busy || view.demo || !view.sshPublicKey" title="New SSH key: grant every server again" @click="save({ rotateKey: true }, 'Key rotated')">Rotate key</button>
        <span v-if="saved" class="text-[13px] text-ok">{{ saved }}</span>
      </div>

      <div v-if="view.sshPublicKey">
        <label class="label">Hub SSH public key</label>
        <code class="input mt-1.5 block break-all py-2 font-mono text-[12px] leading-5" style="height: auto">{{ view.sshPublicKey }}</code>
        <p class="mt-1.5 text-[13px] text-fg-3">No <code>from=</code> address: the hub reaches servers through changing VPNs. A copied key still reaches only each server's own catalog.</p>
      </div>

      <!-- Servers: every node with ops.runner -->
      <div>
        <div class="label mb-2">Servers (nodes with <code>ops: {runner: true}</code>)</div>
        <p v-if="!view.servers.length" class="text-[13px] text-fg-3">No node has a runner yet. Add an <code>ops:</code> block to a node and apply the topology.</p>
        <div v-for="s in view.servers" :key="s.node" class="card mb-2 p-3">
          <div class="flex items-center gap-2">
            <span class="dot" :class="s.reachable ? (s.configCurrent ? 'ok' : 'degraded') : 'unknown'" style="width: 7px; height: 7px; box-shadow: none" />
            <span class="flex-1 font-medium">{{ s.name }} <span class="font-normal text-fg-3">· {{ s.node }} · {{ s.host || 'no host' }}:{{ s.port }}</span></span>
            <select class="input h-8 w-auto px-2 text-[13px]" :value="accessValue(s)" :disabled="view.demo" @change="(e: any) => setAccess(s, e.target.value)">
              <option value="off">off</option>
              <option value="on">on</option>
              <option value="1h">1 h</option>
              <option value="24h">24 h</option>
              <option v-if="typeof s.access === 'object'" value="window">until {{ fmtTime(s.access.until) }}</option>
            </select>
          </div>
          <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-fg-3">
            <span v-if="s.cutsUsers" class="text-down">changes here cut paying users</span>
            <span v-if="s.changes.length">changes: {{ s.changes.join(', ') }}</span>
            <span v-else>read only</span>
            <span v-if="s.configCurrent">runner config {{ s.expectedConfig }} ✓</span>
            <span v-else class="text-warn">runner config {{ s.reportedConfig ?? 'unknown' }} · map expects {{ s.expectedConfig }}</span>
            <span v-if="s.lastOkAt">last answer {{ fmtTime(s.lastOkAt) }}</span>
            <span v-if="s.lastError" class="text-down">{{ s.lastError }}</span>
          </div>
          <div class="mt-2 flex items-center gap-2">
            <input v-model="fp[s.node]" class="input h-8 flex-1 font-mono text-[12px]" :placeholder="s.hostKey ?? 'SHA256:… (host key fingerprint, required)'" spellcheck="false" :disabled="view.demo" />
            <button class="btn h-8 px-2.5 text-[13px]" :disabled="view.demo" @click="saveFp(s)">Set key</button>
          </div>
          <div class="mt-2 flex items-center gap-2">
            <input v-model="hostEdit[s.node]" class="input h-8 flex-1 font-mono text-[12px]" :placeholder="`SSH host override (default ${s.host || 'none'})`" spellcheck="false" :disabled="view.demo" />
            <button class="btn h-8 px-2.5 text-[13px]" :disabled="view.demo" @click="saveHost(s)">Set host</button>
          </div>
          <div class="mt-2 flex items-center gap-2">
            <button class="btn h-8 px-2.5 text-[13px]" :disabled="view.demo || !view.sshPublicKey" @click="showGrant(s)">Install commands</button>
            <button class="btn h-8 px-2.5 text-[13px]" :disabled="view.demo || !s.hostKey" @click="testServer(s)">Test</button>
            <span v-if="testing[s.node]" class="text-[12px]" :class="testing[s.node].startsWith('✓') ? 'text-ok' : 'text-fg-2'">{{ testing[s.node] }}</span>
          </div>
          <div v-if="grant?.node === s.node" class="mt-3 space-y-2">
            <p class="text-[12.5px] text-fg-2">Run these yourself on {{ s.name }}, in order. The hub never installs anything.</p>
            <p v-for="m in grant.g.missing" :key="m" class="text-[12.5px] text-warn">{{ m }}</p>
            <CopyCmd title="1. Save the runner config (it holds no secret)" :cmd="grant.g.config" />
            <CopyCmd title="2. Install vpnmap-ops (sha256-checked)" :cmd="grant.g.install" />
            <CopyCmd title="3. Host key fingerprint: paste it above" :cmd="grant.g.fingerprint" />
            <CopyCmd title="Audit log" :cmd="grant.g.check" />
            <CopyCmd title="Remove (revoke)" :cmd="grant.g.uninstall" />
          </div>
        </div>
      </div>

      <div v-if="view.remnawave.length">
        <div class="label mb-2">Remnawave</div>
        <div v-for="r in view.remnawave" :key="r.node" class="text-[13px] text-fg-2">{{ r.name }} ({{ r.node }}): {{ r.actions.join(', ') }}</div>
      </div>

      <div v-if="tasks.length">
        <div class="label mb-2">Recent plans</div>
        <div v-for="t in tasks" :key="t.id" class="card mb-2 p-3 text-[13.5px]">
          <div class="flex items-center gap-2">
            <span class="dot" :class="tone(t.status)" style="width: 7px; height: 7px; box-shadow: none" />
            <span class="flex-1 font-medium" dir="auto">{{ t.title }}</span>
            <span class="text-fg-3">{{ statusFa[t.status] ?? t.status }}</span>
          </div>
          <div class="mt-1 text-[12px] text-fg-3">{{ fmtTime(t.createdAt, true) }} · {{ t.steps.length }} step(s) · by {{ t.requestedBy }}<span v-if="t.approvedBy"> · approved by {{ t.approvedBy }}</span></div>
          <div v-for="(st, i) in t.steps" :key="i" class="mt-1 font-mono text-[11.5px] leading-5 text-fg-2">{{ st.node }} {{ st.line }}</div>
          <div v-if="t.outcome" class="mt-1 text-fg-2" dir="auto">{{ t.outcome }}</div>
          <button v-if="t.log.length" class="btn-ghost mt-1 h-7 px-2 text-[12px]" @click="openLog = openLog === t.id ? null : t.id">{{ openLog === t.id ? 'Hide' : 'Show' }} log</button>
          <div v-if="openLog === t.id" class="mt-1 space-y-1">
            <div v-for="(l, i) in t.log" :key="i" class="font-mono text-[11.5px] leading-5">
              <span :class="l.status === 'ok' || l.status === 'rolled-back' ? 'text-ok' : 'text-down'">{{ l.status }}</span> {{ l.node }} {{ l.line }}
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
