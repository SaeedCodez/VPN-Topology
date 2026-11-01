export type ThemePref = 'system' | 'light' | 'dark'

const pref = ref<ThemePref>('system')
const effective = ref<'light' | 'dark'>('light')
let mq: MediaQueryList | null = null

function apply() {
  const dark = pref.value === 'dark' || (pref.value === 'system' && !!mq?.matches)
  effective.value = dark ? 'dark' : 'light'
  document.documentElement.dataset.theme = effective.value
}

/** Theme follows the system by default; the top-bar toggle remembers a choice. */
export function useTheme() {
  if (import.meta.client && !mq) {
    mq = matchMedia('(prefers-color-scheme: dark)')
    try {
      pref.value = (localStorage.getItem('vpnmap-theme') as ThemePref) || 'system'
    } catch {
      pref.value = 'system'
    }
    mq.addEventListener('change', apply)
    apply()
  }
  function set(p: ThemePref) {
    pref.value = p
    try {
      localStorage.setItem('vpnmap-theme', p)
    } catch {
      /* private mode */
    }
    apply()
  }
  function cycle() {
    set(pref.value === 'system' ? (effective.value === 'dark' ? 'light' : 'dark') : pref.value === 'light' ? 'dark' : 'system')
  }
  return { pref, effective, set, cycle }
}
