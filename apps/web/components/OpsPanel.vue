<script setup lang="ts">
import type { OpsServerView, OpsSettingsView, OpsTaskView } from '#contract'

/**
 * Ops agent settings. The agent can act on servers, but only through a plan
 * the owner approves in Telegram, and only on servers whose access is on. The
 * SSH private key is write-only: the board shows the public half to install,
 * never the private key.
 */
const emit = defineEmits<{ close: [] }>()
const { ops } = useHub()

const view = ref<OpsSettingsView | null>(null)
const tasks = ref<OpsTaskView[]>([])
const err = ref<string | null>(null)
const saved = ref<string | null>(null)
const busy = ref(false)

const form = reactive({ enabled: false, actionModel: '', approverUserIds: '', hubIp: '' })
const grant = ref<{ id: string; grant: string; revoke: string } | null>(null)
const add = reactive({ id: '', host: '', user: 'vpnmap-op', nodeId: '' })
const testing = ref<Record<string, string>>({})
const openLog = ref<string | null>(null)

function fill(v: OpsSettingsView) {
  view.value = v
  form.enabled = v.enabled
  form.actionModel = v.actionModel
  form.approverUserIds = v.approverUserIds.join(', ')
  form.hubIp = v.hubIp
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

const saveSettings = () => save({ enabled: form.enabled, actionModel: form.actionModel.trim(), approverUserIds: form.approverUserIds, hubIp: form.hubIp.trim() })

function killSwitch() {
  form.enabled = false
  save({ enabled: false }, 'Ops off')
}

async function setAccess(s: OpsServerView, access: 'on' | 'off' | { until: number }) {
  await save({ servers: [{ id: s.id, access }] }, 'Access updated')
}

async function addServer() {
  if (!add.id || !add.host) return
  await save({ servers: [{ id: add.id, host: add.host, user: add.user, nodeId: add.nodeId || null, access: 'off' }] }, 'Server added')
  add.id = add.host = add.nodeId = ''
  add.user = 'vpnmap-op'
}

async function removeServer(s: OpsServerView) {
  if (!confirm(`Remove ${s.id}? Run the revoke command on the server too.`)) return
  await save({ servers: [{ id: s.id, remove: true }] }, 'Removed')
}

async function showGrant(s: OpsServerView) {
  grant.value = null
  try {
    const g = await ops.grant(s.id)
    grant.value = { id: s.id, ...g }
  } catch (e: any) {
    err.value = e.message
  }
}

async function testServer(s: OpsServerView) {
  testing.value = { ...testing.value, [s.id]: '…' }
  try {
    const r = await ops.testServer(s.id)
    testing.value = { ...testing.value, [s.id]: (r.ok ? '✓ ' : '✗ ') + r.detail }
    if (r.ok) fill(await ops.get())
  } catch (e: any) {
    testing.value = { ...testing.value, [s.id]: '✗ ' + e.message }
  }
}

function copy(text: string) {
  navigator.clipboard?.writeText(text)
}

const status = computed(() => {
  const v = view.value
  if (!v) return { tone: 'unknown', text: '' }
  if (!v.enabled) return { tone: 'unknown', text: 'Off' }
  if (!v.hasKey) return { tone: 'degraded', text: 'No SSH key' }
  if (!v.actionModel) return { tone: 'degraded', text: 'Needs an action model' }
  if (!v.approverUserIds.length) return { tone: 'degraded', text: 'No approver set' }
  const on = v.servers.filter((s) => s.access === 'on' || s.access === 'window').length
  return { tone: 'ok', text: `On · ${on}/${v.servers.length} server(s) reachable` }
})

const statusFa: Record<string, string> = { pending: 'در انتظار', approved: 'تأییدشده', running: 'در حال اجرا', done: 'انجام شد', failed: 'ناموفق', 'rolled-back': 'برگشت', cancelled: 'لغو', expired: 'منقضی' }
const tone = (s: string) => (s === 'done' ? 'ok' : s === 'running' || s === 'pending' || s === 'approved' ? 'unknown' : 'down')
</script>

<template>
  <div class="flex items-center gap-3 border-b border-line px-5 py-4">
    <div class="flex-1">
      <h2 class="text-[17px] font-semibold">Ops agent</h2>
      <div v-if="view" class="mt-1 flex items-center gap-1.5 text-[13px] text-fg-2">
        <span class="dot" :class="status.tone" style="width: 7px; height: 7px; box-shadow: none" />{{ status.text }}
      </div>
    </div>
    <button v-if="view?.enabled" class="btn-ghost text-down" title="Turn the ops agent off at once" @click="killSwitch">Kill switch</button>
    <button class="btn-ghost w-10 px-0" aria-label="Close" @click="emit('close')">✕</button>
  </div>

  <div class="scroll-thin flex-1 space-y-5 overflow-y-auto p-5">
    <p class="text-[14px] leading-6 text-fg-2">
      In Telegram, an approver can ask the agent to fix something. It investigates read-only, then sends a plan; nothing runs until you tap <b>Approve</b>. After each step the hub re-checks the map and rolls back automatically if anything gets worse.
    </p>
    <p v-if="err" class="rounded-ctl border border-down/40 bg-down/10 px-3 py-2 text-[14px] text-down">{{ err }}</p>

    <template v-if="view">
      <label class="flex items-center justify-between gap-3">
        <span class="text-[15px] font-medium">Act on servers</span>
        <input v-model="form.enabled" type="checkbox" class="h-5 w-5 accent-[rgb(var(--fg))]" />
      </label>

      <div>
        <label class="label" for="ops-model">Action model (OpenRouter)</label>
        <input id="ops-model" v-model="form.actionModel" class="input mt-1.5 font-mono text-[14px]" spellcheck="false" placeholder="anthropic/claude-sonnet-4.5" />
        <p class="mt-1.5 text-[13px] text-fg-3">Uses the same OpenRouter key as the assistant. Pick a strong model for changes to servers.</p>
      </div>

      <div>
        <label class="label" for="ops-approvers">Approver Telegram user ids</label>
        <input id="ops-approvers" v-model="form.approverUserIds" class="input mt-1.5 font-mono text-[14px]" inputmode="numeric" spellcheck="false" placeholder="123456789" />
        <p class="mt-1.5 text-[13px] text-fg-3">Only these people can ask the agent to act and tap Approve. Turn on Telegram 2-step for these accounts.</p>
      </div>

      <div>
        <label class="label" for="ops-ip">Hub outbound IP</label>
        <input id="ops-ip" v-model="form.hubIp" class="input mt-1.5 font-mono text-[14px]" spellcheck="false" placeholder="171.22.27.214" />
        <p class="mt-1.5 text-[13px] text-fg-3">The SSH key is restricted to this IP (<code>from="…"</code>).</p>
      </div>

      <div class="flex items-center gap-2">
        <button class="btn-primary" :disabled="busy" @click="saveSettings">{{ busy ? 'Saving…' : 'Save' }}</button>
        <button class="btn" :disabled="busy || !view.hasKey" title="Generate a new SSH key (every server must be re-granted)" @click="save({ rotateKey: true }, 'Key rotated')">Rotate key</button>
        <span v-if="saved" class="text-[13px] text-ok">{{ saved }}</span>
      </div>

      <!-- Public key -->
      <div v-if="view.publicKey">
        <label class="label">Hub SSH public key</label>
        <div class="mt-1.5 flex items-start gap-2">
          <code class="input flex-1 break-all py-2 font-mono text-[12px] leading-5" style="height: auto">{{ view.publicKey }}</code>
          <button class="btn" @click="copy(view.publicKey!)">Copy</button>
        </div>
      </div>

      <!-- Servers -->
      <div>
        <div class="label mb-2">Servers</div>
        <div v-for="s in view.servers" :key="s.id" class="card mb-2 p-3">
          <div class="flex items-center gap-2">
            <span class="dot" :class="s.access === 'on' || s.access === 'window' ? 'ok' : 'unknown'" style="width: 7px; height: 7px; box-shadow: none" />
            <span class="flex-1 font-medium">{{ s.id }} <span class="text-fg-3 font-normal">· {{ s.host }}</span></span>
            <select class="input h-8 w-auto px-2 text-[13px]" :value="s.access === 'window' ? 'window' : s.access" @change="(e: any) => setAccess(s, e.target.value === '24h' ? { until: Date.now() + 86400000 } : e.target.value === 'window' ? { until: Date.now() + 86400000 } : e.target.value)">
              <option value="on">on</option>
              <option value="off">off</option>
              <option value="24h">24h</option>
              <option v-if="s.access === 'window'" value="window">window ({{ fmtTime(s.accessUntil) }})</option>
            </select>
          </div>
          <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-fg-3">
            <span v-if="s.nodeId">map: {{ s.nodeId }}</span>
            <span v-if="s.hostKey">host key pinned</span>
            <span v-else class="text-warn">host key not pinned yet</span>
            <span v-if="s.lastOkAt">last ok {{ fmtTime(s.lastOkAt) }}</span>
          </div>
          <div class="mt-2 flex items-center gap-2">
            <button class="btn h-8 px-2.5 text-[13px]" @click="showGrant(s)">Grant / revoke cmd</button>
            <button class="btn h-8 px-2.5 text-[13px]" @click="testServer(s)">Test</button>
            <button class="btn-ghost h-8 px-2 text-[13px] text-down" @click="removeServer(s)">Remove</button>
            <span v-if="testing[s.id]" class="text-[12px]" :class="testing[s.id].startsWith('✓') ? 'text-ok' : 'text-fg-2'">{{ testing[s.id] }}</span>
          </div>
          <div v-if="grant?.id === s.id" class="mt-2 space-y-1.5">
            <div class="label">Run on the server to grant</div>
            <div class="flex items-start gap-2">
              <code class="input flex-1 break-all py-2 font-mono text-[12px] leading-5" style="height: auto">{{ grant.grant }}</code>
              <button class="btn" @click="copy(grant.grant)">Copy</button>
            </div>
            <div class="label">Revoke</div>
            <code class="input block break-all py-2 font-mono text-[12px] leading-5" style="height: auto">{{ grant.revoke }}</code>
          </div>
        </div>

        <!-- Add server -->
        <div class="card p-3">
          <div class="label mb-2">Add a server</div>
          <div class="grid grid-cols-2 gap-2">
            <input v-model="add.id" class="input text-[13px]" placeholder="id (e.g. relay-1)" spellcheck="false" />
            <input v-model="add.host" class="input text-[13px]" placeholder="host / IP" spellcheck="false" />
            <input v-model="add.user" class="input text-[13px]" placeholder="user" spellcheck="false" />
            <input v-model="add.nodeId" class="input text-[13px]" placeholder="map node id (optional)" spellcheck="false" />
          </div>
          <button class="btn mt-2 h-8 px-3 text-[13px]" :disabled="busy || !add.id || !add.host" @click="addServer">Add</button>
        </div>
      </div>

      <!-- Tasks -->
      <div v-if="tasks.length">
        <div class="label mb-2">Recent tasks</div>
        <div v-for="t in tasks" :key="t.id" class="card mb-2 p-3 text-[13.5px]">
          <div class="flex items-center gap-2">
            <span class="dot" :class="tone(t.status)" style="width: 7px; height: 7px; box-shadow: none" />
            <span class="flex-1 font-medium">{{ t.title }}</span>
            <span class="text-fg-3">{{ statusFa[t.status] ?? t.status }}</span>
          </div>
          <div class="mt-1 text-[12px] text-fg-3">{{ fmtTime(t.createdAt, true) }} · {{ t.steps.length }} step(s) · by {{ t.requestedBy }}</div>
          <div v-if="t.outcome" class="mt-1 text-fg-2">{{ t.outcome }}</div>
          <button v-if="t.log.length" class="btn-ghost mt-1 h-7 px-2 text-[12px]" @click="openLog = openLog === t.id ? null : t.id">{{ openLog === t.id ? 'Hide' : 'Show' }} steps</button>
          <div v-if="openLog === t.id" class="mt-1 space-y-1">
            <div v-for="l in t.log" :key="l.index" class="font-mono text-[11.5px] leading-5">
              <span :class="l.status === 'ok' ? 'text-ok' : 'text-down'">{{ l.status }}</span> {{ l.summary }}
            </div>
          </div>
        </div>
      </div>

      <p class="text-[13px] leading-5 text-fg-3">
        The agent can only run commands the classifier calls read on its own; anything that changes a server needs a plan you approve. Secret values from a server are never shown to the model or put in a reply. Rule 1 of this repo is relaxed for this agent: the map observes, and acts only through plans you approve.
      </p>
    </template>
  </div>
</template>
