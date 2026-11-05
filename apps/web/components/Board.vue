<script setup lang="ts">
import { VueFlow, Panel, useVueFlow, type Node, type Edge, type NodeDragEvent } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls, ControlButton } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import { autoLayout, frames, NODE_W } from '#board/layout'

const { bundle, snapshot, layout, sparks, selected, panel, saveLayout, resetLayout, board } = useHub()
const { fitView, onPaneReady } = useVueFlow()
const { effective: theme } = useTheme()

// Picture mode: pick nodes (click) or a route, then download the picture or
// send it to the Telegram chat. The hub draws it, as for the assistant.
const picking = ref(false)
const picked = ref<string[]>([])
const pickRoute = ref('')
const pickContext = ref(false)
const pickBusy = ref(false)
const pickNote = ref<{ tone: 'ok' | 'down'; text: string } | null>(null)
const pickedIds = computed(() => new Set(pickRoute.value ? bundle.value?.routes.find((r) => r.id === pickRoute.value)?.nodes ?? [] : picked.value))
const pictureQuery = computed(() => ({
  route: pickRoute.value || undefined,
  nodes: pickRoute.value ? [] : picked.value,
  context: pickContext.value,
  theme: theme.value,
}))
const pickScope = computed(() => (pickRoute.value ? bundle.value?.routes.find((r) => r.id === pickRoute.value)?.title ?? pickRoute.value : picked.value.length ? `${picked.value.length} node${picked.value.length === 1 ? '' : 's'} picked` : 'Whole board'))
function togglePicking() {
  picking.value = !picking.value
  picked.value = []
  pickRoute.value = ''
  pickNote.value = null
}
function downloadPicture() {
  const a = document.createElement('a')
  a.href = board.pictureUrl(pictureQuery.value, true)
  a.download = 'vpn-board.png'
  a.click()
}
async function sendPicture() {
  pickBusy.value = true
  pickNote.value = null
  try {
    const r = await board.sendPicture(pictureQuery.value)
    pickNote.value = { tone: 'ok', text: `Sent to Telegram${r.asFile ? ' as a file' : ''}` }
  } catch (e: any) {
    pickNote.value = { tone: 'down', text: e?.message ?? String(e) }
  } finally {
    pickBusy.value = false
  }
}
const onEsc = (e: KeyboardEvent) => e.key === 'Escape' && picking.value && togglePicking()
onMounted(() => window.addEventListener('keydown', onEsc))
onBeforeUnmount(() => window.removeEventListener('keydown', onEsc))

const reducedMotion = ref(false)
const showLabels = ref(true)
onMounted(() => {
  const mq = matchMedia('(prefers-reduced-motion: reduce)')
  reducedMotion.value = mq.matches
  mq.addEventListener('change', (e) => (reducedMotion.value = e.matches))
})

// Positions: saved layout wins, auto-layout fills the rest.
const positions = ref<Record<string, { x: number; y: number }>>({})
watch(
  [bundle, layout],
  () => {
    if (!bundle.value) return
    const auto = autoLayout(bundle.value)
    const out: Record<string, { x: number; y: number }> = {}
    for (const n of bundle.value.nodes) out[n.id] = layout.value[n.id] ?? auto[n.id]
    positions.value = out
  },
  { immediate: true },
)

const sparkFor = (nodeId: string) => {
  const b = bundle.value!
  const n = b.nodes.find((x) => x.id === nodeId)!
  const out: Record<string, (number | null)[]> = {}
  for (const id of n.card ?? []) {
    if (['cpu', 'mem', 'disk', 'load', 'uptime', 'net'].includes(id)) out[id] = sparks.value[`n:${nodeId}:${id}`]
    else {
      const m = n.metrics?.find((x) => x.id === id)
      if (m) out[id] = sparks.value[`m:${m.key}`]
      const c = n.checks?.find((x) => x.id === id)
      if (c) out[id] = sparks.value[`c:${c.key}:v`]
    }
  }
  return out
}

const driftByHost = computed(() => {
  const m: Record<string, number> = {}
  for (const d of snapshot.value?.drift ?? []) if (d.host) m[d.host] = (m[d.host] ?? 0) + (d.kind === 'port' ? d.detail.split(',').length : 1)
  return m
})

const nodes = computed<Node[]>(() => {
  const b = bundle.value
  if (!b) return []
  const fr = frames(b, positions.value).map<Node>((f) => ({
    id: `frame:${f.id}`,
    type: 'frame',
    position: { x: f.x, y: f.y },
    data: { title: f.title, kind: f.kind, w: f.w, h: f.h },
    draggable: false,
    selectable: false,
    focusable: false,
    zIndex: -10,
    class: 'st-frame',
  }))
  const real = b.nodes.map<Node>((n) => ({
    id: n.id,
    type: 'vpn',
    position: positions.value[n.id] ?? { x: 0, y: 0 },
    data: { spec: n, state: snapshot.value?.nodes[n.id], sparks: sparkFor(n.id), drift: driftByHost.value[n.id] ?? 0, picked: picking.value && pickedIds.value.has(n.id) },
    selected: selected.value?.kind === 'node' && selected.value.id === n.id,
    class: `st-${snapshot.value?.nodes[n.id]?.status ?? 'unknown'}`,
  }))
  // Ghost nodes: agents reporting from servers the topology does not declare.
  const ghosts = (snapshot.value?.drift ?? [])
    .filter((d) => d.kind === 'agent')
    .map<Node>((d, i) => ({
      id: `ghost:${d.id}`,
      type: 'vpn',
      position: { x: -NODE_W - 120, y: 40 + i * 190 },
      data: { spec: { id: d.id, name: d.title, kind: 'server', group: 'external', agent: true, role: d.detail, defined_in: { component: '', file: '', line: 0 } }, sparks: {}, drift: 1, ghost: true },
      draggable: false,
    }))
  return [...fr, ...real, ...ghosts]
})

const edges = computed<Edge[]>(() => {
  const b = bundle.value
  if (!b) return []
  return b.links.map<Edge>((l) => ({
    id: l.id,
    source: l.from,
    target: l.to,
    type: 'traffic',
    data: { spec: l, state: snapshot.value?.links[l.id], reducedMotion: reducedMotion.value, showLabel: showLabels.value },
    selected: selected.value?.kind === 'link' && selected.value.id === l.id,
    zIndex: l.plane === 'control' ? 0 : 1,
  }))
})

function onDrag(e: NodeDragEvent) {
  for (const n of e.nodes) if (!n.id.startsWith('frame:') && !n.id.startsWith('ghost:')) positions.value[n.id] = { x: n.position.x, y: n.position.y }
}
function onDragStop(e: NodeDragEvent) {
  onDrag(e)
  const pos: Record<string, { x: number; y: number }> = {}
  for (const n of e.nodes) if (positions.value[n.id]) pos[n.id] = { x: Math.round(n.position.x), y: Math.round(n.position.y) }
  saveLayout(pos)
}

function onNodeClick({ node }: { node: Node }) {
  if (node.id.startsWith('frame:')) return
  if (picking.value) {
    if (node.id.startsWith('ghost:')) return
    if (pickRoute.value) picked.value = [...pickedIds.value]
    pickRoute.value = ''
    picked.value = picked.value.includes(node.id) ? picked.value.filter((x) => x !== node.id) : [...picked.value, node.id]
    pickNote.value = null
    return
  }
  if (node.id.startsWith('ghost:')) {
    panel.value = 'drift'
    return
  }
  selected.value = { kind: 'node', id: node.id }
  panel.value = 'details'
}
function onEdgeClick({ edge }: { edge: Edge }) {
  if (picking.value) return
  selected.value = { kind: 'link', id: edge.id }
  panel.value = 'details'
}
function onPaneClick() {
  if (picking.value) return
  selected.value = null
  if (panel.value === 'details') panel.value = null
}

// Keyboard: Tab moves between nodes (Vue Flow makes them focusable), Enter opens details.
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Enter') return
  const el = (e.target as HTMLElement).closest('.vue-flow__node, .vue-flow__edge') as HTMLElement | null
  const id = el?.dataset.id
  if (!id || id.startsWith('frame:')) return
  selected.value = { kind: el!.classList.contains('vue-flow__edge') ? 'link' : 'node', id }
  panel.value = 'details'
}

async function relayout() {
  await resetLayout()
  await nextTick()
  fitView({ padding: 0.12, duration: 400 })
}

function minimapClass(n: Node) {
  return n.class as string
}

onPaneReady(() => setTimeout(() => fitView({ padding: 0.1 }), 50))

// Focus a node from elsewhere (root-cause banner, timeline).
const { setCenter, getNode } = useVueFlow()
watch(selected, (s) => {
  if (!s || s.kind !== 'node') return
  const n = getNode.value(s.id)
  if (n && n.dimensions?.width) {
    const v = document.querySelector('.vue-flow')?.getBoundingClientRect()
    if (v) setCenter(n.position.x + n.dimensions.width / 2 + 180, n.position.y + n.dimensions.height / 2, { zoom: 1, duration: 450 })
  }
})
</script>

<template>
  <VueFlow
    :nodes="nodes"
    :edges="edges"
    :min-zoom="0.2"
    :max-zoom="2"
    :default-edge-options="{ type: 'traffic' }"
    :nodes-connectable="false"
    :edges-updatable="false"
    :elevate-edges-on-select="true"
    :select-nodes-on-drag="false"
    :zoom-on-double-click="false"
    class="h-full w-full"
    aria-label="VPN topology map: Tab to move between servers, Enter for details"
    @keydown="onKeydown"
    @node-drag="onDrag"
    @node-drag-stop="onDragStop"
    @node-click="onNodeClick"
    @edge-click="onEdgeClick"
    @pane-click="onPaneClick"
  >
    <template #node-vpn="p"><VpnNode :data="p.data" /></template>
    <template #node-frame="p"><GroupFrame :data="p.data" /></template>
    <template #edge-traffic="p"><TrafficEdge v-bind="p" /></template>
    <Panel v-if="picking" position="bottom-center">
      <div class="floating flex flex-wrap items-center gap-2 px-3 py-2 text-[13px]" role="toolbar" aria-label="Picture of the board">
        <span class="font-medium">{{ pickScope }}</span>
        <span class="text-fg-3">· click nodes to pick, or</span>
        <select v-model="pickRoute" class="input h-8 w-auto px-2 text-[13px]" aria-label="Pick a route" @change="picked = []">
          <option value="">route…</option>
          <option v-for="r in bundle?.routes ?? []" :key="r.id" :value="r.id">{{ r.title }}</option>
        </select>
        <label class="flex items-center gap-1.5 text-fg-2"><input v-model="pickContext" type="checkbox" /> surroundings</label>
        <button v-if="picked.length || pickRoute" class="btn-ghost h-8 px-2 text-[13px]" @click="(picked = []), (pickRoute = '')">Clear</button>
        <button class="btn-ghost h-8 px-2.5 text-[13px]" @click="downloadPicture">Download PNG</button>
        <button class="btn-primary h-8 px-3 text-[13px]" :disabled="pickBusy" @click="sendPicture">{{ pickBusy ? 'Sending…' : 'Send to Telegram' }}</button>
        <span v-if="pickNote" class="max-w-[320px] truncate" :class="pickNote.tone === 'ok' ? 'text-ok' : 'text-down'" :title="pickNote.text">{{ pickNote.text }}</span>
        <button class="btn-ghost h-8 w-8 px-0" aria-label="Close picture mode" @click="togglePicking">✕</button>
      </div>
    </Panel>
    <Background :gap="22" :size="1.2" />
    <MiniMap position="bottom-right" :node-class-name="minimapClass" pannable zoomable :width="200" :height="130" />
    <Controls position="bottom-left" :show-interactive="false">
      <ControlButton title="Auto-layout (forget saved positions)" @click="relayout">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="4" width="6" height="6" rx="1.5" /><rect x="15" y="4" width="6" height="6" rx="1.5" /><rect x="9" y="14" width="6" height="6" rx="1.5" /><path d="M9 7h6M6 10v2a2 2 0 0 0 2 2h1M18 10v2a2 2 0 0 1-2 2h-1" /></svg>
      </ControlButton>
      <ControlButton :title="picking ? 'Close picture mode' : 'Picture of the board (download or send to Telegram)'" :class="picking ? 'text-accent' : ''" @click="togglePicking">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 8h3l2-3h6l2 3h3v11H4z" /><circle cx="12" cy="13" r="3.5" /></svg>
      </ControlButton>
      <ControlButton :title="showLabels ? 'Hide edge labels' : 'Show edge labels'" @click="showLabels = !showLabels">
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 7h16M4 12h10M4 17h7" /></svg>
      </ControlButton>
    </Controls>
  </VueFlow>
</template>
