<script setup lang="ts">
// Single admin: password + TOTP code. HTTPS only in production.
const { login } = useHub()
const password = ref('')
const totp = ref('')
const err = ref<string | null>(null)
const busy = ref(false)
async function submit() {
  busy.value = true
  err.value = null
  try {
    await login(password.value, totp.value)
  } catch (e: any) {
    err.value = e.message === 'unauthorized' ? 'Wrong password or code.' : e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="grid h-full place-items-center bg-bg px-4">
    <form class="card w-full max-w-[380px] p-7" @submit.prevent="submit">
      <div class="mb-6 flex items-center gap-2.5">
        <svg width="28" height="28" viewBox="0 0 32 32" aria-hidden="true"><rect width="32" height="32" rx="8" fill="rgb(var(--fg))" /><circle cx="9" cy="16" r="3" fill="rgb(var(--bg))" /><circle cx="23" cy="10" r="3" fill="rgb(var(--bg))" /><circle cx="23" cy="22" r="3" fill="rgb(var(--ok))" /><path d="M12 15l8-4M12 17l8 4" stroke="rgb(var(--bg))" stroke-width="1.6" stroke-linecap="round" /></svg>
        <h1 class="text-[18px] font-semibold tracking-[-0.01em]">VPN Topology</h1>
      </div>
      <label class="mb-1.5 block text-[13px] font-medium text-fg-2" for="pw">Password</label>
      <input id="pw" v-model="password" class="input mb-4" type="password" autocomplete="current-password" required />
      <label class="mb-1.5 block text-[13px] font-medium text-fg-2" for="otp">Authenticator code</label>
      <input id="otp" v-model="totp" class="input mb-5 font-mono tracking-[0.3em]" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required />
      <p v-if="err" class="mb-4 text-[14px] text-down" role="alert">{{ err }}</p>
      <button class="btn-primary w-full" :disabled="busy">{{ busy ? 'Signing in…' : 'Sign in' }}</button>
    </form>
  </div>
</template>
