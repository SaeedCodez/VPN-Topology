<script setup lang="ts">
const { auth, start, bundle, snapshot, error } = useHub()
useTheme()
onMounted(start)
</script>

<template>
  <div class="flex h-full flex-col">
    <LoginView v-if="auth.checked && !auth.authenticated" />
    <template v-else>
      <TopBar />
      <main class="relative min-h-0 flex-1">
        <div v-if="!bundle || !snapshot" class="grid h-full place-items-center text-fg-3">
          <span v-if="error" class="text-down">{{ error }}</span>
          <span v-else>Connecting to the hub…</span>
        </div>
        <template v-else>
          <Board />
          <RootCauseBanner />
          <SidePanel />
        </template>
      </main>
      <Timeline v-if="bundle && snapshot" />
    </template>
  </div>
</template>
