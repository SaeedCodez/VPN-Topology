<script setup lang="ts">
// A read-only diagnostic command: copy button only, never executed.
const props = defineProps<{ title: string; cmd: string; from?: string; notes?: string }>()
const copied = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(props.cmd)
  } catch {
    const t = document.createElement('textarea')
    t.value = props.cmd
    document.body.appendChild(t)
    t.select()
    document.execCommand('copy')
    t.remove()
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1400)
}
</script>

<template>
  <div class="rounded-xl border border-line bg-subtle/60">
    <div class="flex items-center gap-2 px-3 pt-2">
      <span class="min-w-0 flex-1 truncate text-[13px] font-medium">{{ title }}</span>
      <span v-if="from" class="font-mono text-[11px] text-fg-3">{{ from }}</span>
      <button class="btn h-8 rounded-lg px-2.5 text-[12.5px]" :aria-label="`Copy: ${title}`" @click="copy">{{ copied ? 'Copied' : 'Copy' }}</button>
    </div>
    <pre class="scroll-thin overflow-x-auto px-3 pb-2.5 pt-1.5 font-mono text-[12px] leading-[18px] text-fg-2">{{ cmd }}</pre>
  </div>
</template>
