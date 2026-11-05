<script setup lang="ts">
import type { AssistantModel, AssistantSettingsView, AssistantTestResult } from '#contract'

/**
 * Settings of the Telegram assistant. The OpenRouter key is write-only: it is
 * sent to the hub, stored encrypted, and never shown again (only a hint).
 * Changes apply at once; no redeploy.
 */
const emit = defineEmits<{ close: [] }>()
const { assistant } = useHub()

const view = ref<AssistantSettingsView | null>(null)
const models = ref<AssistantModel[]>([])
const err = ref<string | null>(null)
const saved = ref<string | null>(null)
const busy = ref(false)
const testing = ref(false)
const test = ref<AssistantTestResult | null>(null)

const form = reactive({ enabled: false, model: '', allowedUserIds: '', dailyLimit: 50 })
const newKey = ref('')
const editingKey = ref(false)

function fill(v: AssistantSettingsView) {
  view.value = v
  form.enabled = v.enabled
  form.model = v.model
  form.allowedUserIds = v.allowedUserIds.join(', ')
  form.dailyLimit = v.dailyLimit
  editingKey.value = !v.hasKey || v.keyUnreadable
}

async function load() {
  err.value = null
  try {
    fill(await assistant.get())
  } catch (e: any) {
    err.value = e.message
  }
  try {
    models.value = await assistant.models()
  } catch {
    /* the picker is a convenience; typing a model id still works */
  }
}
onMounted(load)

const model = computed(() => models.value.find((m) => m.id === form.model.trim()) ?? null)
const toolModels = computed(() => models.value.filter((m) => m.tools))
const dirty = computed(() => {
  const v = view.value
  if (!v) return false
  return form.enabled !== v.enabled || form.model.trim() !== v.model || norm(form.allowedUserIds) !== v.allowedUserIds.join(',') || Number(form.dailyLimit) !== v.dailyLimit || (editingKey.value && newKey.value.trim() !== '')
})

function norm(s: string) {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean)
    .join(',')
}

async function save() {
  busy.value = true
  err.value = null
  saved.value = null
  try {
    const patch: Record<string, unknown> = { enabled: form.enabled, model: form.model.trim(), allowedUserIds: form.allowedUserIds, dailyLimit: Number(form.dailyLimit) }
    if (editingKey.value && newKey.value.trim()) patch.key = newKey.value.trim()
    fill(await assistant.save(patch))
    newKey.value = ''
    saved.value = 'Saved'
    setTimeout(() => (saved.value = null), 2500)
  } catch (e: any) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

async function clearKey() {
  if (!confirm('Remove the stored OpenRouter key? The assistant stops answering until a new key is saved.')) return
  busy.value = true
  try {
    fill(await assistant.save({ key: '' }))
  } catch (e: any) {
    err.value = e.message
  } finally {
    busy.value = false
  }
}

async function runTest() {
  testing.value = true
  test.value = null
  try {
    test.value = await assistant.test()
  } catch (e: any) {
    test.value = { ok: false, model: form.model, ms: 0, error: e.message }
  } finally {
    testing.value = false
  }
}

const status = computed(() => {
  const v = view.value
  if (!v) return { tone: 'unknown', text: '' }
  if (!v.telegram.enabled) return { tone: 'unknown', text: `Telegram is off (${v.telegram.reason})` }
  if (!v.enabled) return { tone: 'unknown', text: 'Off' }
  if (!v.hasKey || v.keyUnreadable || !v.model) return { tone: 'degraded', text: 'Needs a key and a model' }
  if (!v.allowedUserIds.length) return { tone: 'degraded', text: 'Listening, but no user is allowed yet' }
  if (v.polling.error) return { tone: 'down', text: 'Cannot read Telegram' }
  if (v.polling.running) return { tone: 'ok', text: 'Listening in the group' }
  return { tone: 'degraded', text: 'Starting…' }
})

const price = (m: AssistantModel) => (m.promptPrice == null ? '' : `$${m.promptPrice}/$${m.completionPrice} per 1M tokens`)
</script>

<template>
  <div class="flex items-center gap-3 border-b border-line px-5 py-4">
    <div class="flex-1">
      <h2 class="text-[17px] font-semibold">Telegram assistant</h2>
      <div v-if="view" class="mt-1 flex items-center gap-1.5 text-[13px] text-fg-2">
        <span class="dot" :class="status.tone" style="width: 7px; height: 7px; box-shadow: none" />{{ status.text }}
      </div>
    </div>
    <button class="btn-ghost w-10 px-0" aria-label="Close" @click="emit('close')">✕</button>
  </div>

  <div class="scroll-thin flex-1 space-y-5 overflow-y-auto p-5">
    <p class="text-[14px] leading-6 text-fg-2">
      Reply to an alert in the Telegram group, or write <code class="font-mono text-[13px]">/ask …</code>, and an OpenRouter model answers from the same facts as this board. It can only read: it has no tool that changes anything.
    </p>

    <p v-if="err" class="rounded-ctl border border-down/40 bg-down/10 px-3 py-2 text-[14px] text-down">{{ err }}</p>

    <template v-if="view">
      <label class="flex items-center justify-between gap-3">
        <span class="text-[15px] font-medium">Answer questions</span>
        <input v-model="form.enabled" type="checkbox" class="h-5 w-5 accent-[rgb(var(--fg))]" />
      </label>

      <!-- Key -->
      <div>
        <label class="label" for="or-key">OpenRouter API key</label>
        <div v-if="!editingKey" class="mt-1.5 flex items-center gap-2">
          <span class="input flex flex-1 items-center font-mono text-[14px] text-fg-2">{{ view.keyHint }}</span>
          <button class="btn" @click="editingKey = true">Replace</button>
          <button class="btn-ghost" @click="clearKey">Remove</button>
        </div>
        <div v-else class="mt-1.5 flex items-center gap-2">
          <input id="or-key" v-model="newKey" class="input font-mono text-[14px]" type="password" autocomplete="off" spellcheck="false" placeholder="sk-or-v1-…" />
          <button v-if="view.hasKey && !view.keyUnreadable" class="btn-ghost" @click="(editingKey = false), (newKey = '')">Cancel</button>
        </div>
        <p v-if="view.keyUnreadable" class="mt-1.5 text-[13px] text-warn">The stored key can no longer be read (SESSION_SECRET changed). Enter it again.</p>
        <p v-else class="mt-1.5 text-[13px] text-fg-3">Stored encrypted in the hub database. It is never shown again.</p>
      </div>

      <!-- Model -->
      <div>
        <label class="label" for="or-model">Model</label>
        <input id="or-model" v-model="form.model" class="input mt-1.5 font-mono text-[14px]" list="or-models" spellcheck="false" placeholder="google/gemini-2.5-flash" />
        <datalist id="or-models">
          <option v-for="m in toolModels" :key="m.id" :value="m.id">{{ m.name }}</option>
        </datalist>
        <p class="mt-1.5 text-[13px] text-fg-3">
          <template v-if="model">{{ model.name }}<template v-if="price(model)"> · {{ price(model) }}</template><span v-if="!model.tools" class="text-warn"> · this model does not support tool calling; pick another</span></template>
          <template v-else-if="models.length">Any OpenRouter model id with tool calling ({{ toolModels.length }} available).</template>
          <template v-else>An OpenRouter model id with tool calling.</template>
        </p>
      </div>

      <!-- Users -->
      <div>
        <label class="label" for="tg-users">Allowed Telegram user ids</label>
        <input id="tg-users" v-model="form.allowedUserIds" class="input mt-1.5 font-mono text-[14px]" inputmode="numeric" spellcheck="false" placeholder="123456789, 987654321" />
        <p class="mt-1.5 text-[13px] text-fg-3">Only these people get answers; everyone else in the group is ignored.</p>
        <p v-if="view.lastIgnored" class="mt-1.5 text-[13px] text-fg-2">
          Last ignored question: {{ view.lastIgnored.fromName || 'someone' }} · id <code class="font-mono">{{ view.lastIgnored.fromId }}</code> · {{ fmtTime(view.lastIgnored.at) }}
        </p>
      </div>

      <div>
        <label class="label" for="limit">Questions per day</label>
        <input id="limit" v-model.number="form.dailyLimit" class="input mt-1.5 w-32" type="number" min="1" max="10000" />
      </div>

      <div class="flex items-center gap-2">
        <button class="btn-primary" :disabled="busy || !dirty" @click="save">{{ busy ? 'Saving…' : 'Save' }}</button>
        <button class="btn" :disabled="testing || dirty || !view.hasKey || !view.model" :title="dirty ? 'Save first' : 'One short request to the model'" @click="runTest">{{ testing ? 'Testing…' : 'Test model' }}</button>
        <span v-if="saved" class="text-[13px] text-ok">{{ saved }}</span>
      </div>
      <p v-if="test" class="rounded-ctl border px-3 py-2 text-[14px]" :class="test.ok ? 'border-ok/30 bg-ok/8 text-ok' : 'border-down/40 bg-down/10 text-down'">
        <template v-if="test.ok">{{ test.model }} answered in {{ (test.ms / 1000).toFixed(1) }} s<template v-if="test.costUsd != null"> · ${{ test.costUsd.toFixed(6) }}</template></template>
        <template v-else>{{ test.error }}</template>
      </p>

      <!-- Status -->
      <div class="card space-y-1.5 p-4 text-[13.5px]">
        <div class="label mb-1">Today ({{ view.usage.day }})</div>
        <div class="flex justify-between"><span class="text-fg-2">Questions</span><span class="num">{{ view.usage.questions }} / {{ view.dailyLimit }}</span></div>
        <div class="flex justify-between"><span class="text-fg-2">Tokens</span><span class="num">{{ fmtNum(view.usage.promptTokens) }} in · {{ fmtNum(view.usage.completionTokens) }} out</span></div>
        <div class="flex justify-between"><span class="text-fg-2">Cost</span><span class="num">${{ view.usage.costUsd.toFixed(4) }}</span></div>
        <div v-if="view.polling.lastPollAt" class="flex justify-between"><span class="text-fg-2">Last Telegram poll</span><span>{{ fmtTime(view.polling.lastPollAt) }}</span></div>
        <div v-if="view.polling.error" class="pt-1 text-down">Telegram: {{ view.polling.error }}</div>
        <div v-if="view.lastError" class="pt-1 text-down">Last error {{ fmtTime(view.lastError.at) }}: {{ view.lastError.error }}</div>
      </div>

      <p class="text-[13px] leading-5 text-fg-3">
        Privacy: each question and the board facts the model looks up (names, IPs, users, events) are sent to OpenRouter and the model's provider. Keys, tokens and passwords are not part of the board data.
      </p>
    </template>
  </div>
</template>
