// VPN Topology board: a static SPA. In production the web container (nginx)
// serves it and proxies /api, /ws and /agent to the hub on the same origin.
import { fileURLToPath } from 'node:url'

const hub = process.env.HUB_URL ?? 'http://localhost:4000'

export default defineNuxtConfig({
  ssr: false,
  compatibilityDate: '2025-07-15',
  devtools: { enabled: false },
  modules: ['@nuxtjs/tailwindcss'],
  css: ['~/assets/css/main.css'],
  tailwindcss: { cssPath: false, viewer: false },
  app: {
    head: {
      title: 'VPN Topology',
      htmlAttrs: { lang: 'en' },
      meta: [
        { name: 'robots', content: 'noindex, nofollow, noarchive' },
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'color-scheme', content: 'light dark' },
      ],
      link: [
        { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' },
        { rel: 'preload', href: '/fonts/Geist-Variable.woff2', as: 'font', type: 'font/woff2', crossorigin: '' },
      ],
      // Apply the saved theme before the first paint (no flash).
      script: [
        {
          innerHTML:
            "(function(){try{var t=localStorage.getItem('vpnmap-theme')||'system';var d=t==='dark'||(t==='system'&&matchMedia('(prefers-color-scheme: dark)').matches);document.documentElement.dataset.theme=d?'dark':'light';}catch(e){document.documentElement.dataset.theme='light'}})()",
        },
      ],
    },
  },
  // The board uses the hub's API types directly: one contract, no copies.
  alias: { '#contract': fileURLToPath(new URL('../hub/src/contract', import.meta.url)) },
  nitro: {
    devProxy: {
      '/api': { target: `${hub}/api`, changeOrigin: true },
      '/agent': { target: `${hub}/agent`, changeOrigin: true },
      '/ws': { target: `${hub}/ws`, ws: true, changeOrigin: true },
    },
  },
  vite: {
    server: { hmr: { protocol: 'ws' } },
  },
  typescript: { strict: true },
})
