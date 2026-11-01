// Every colour is a design token defined once per theme in assets/css/main.css.
const token = (name: string) => `rgb(var(--${name}) / <alpha-value>)`

export default {
  content: ['./components/**/*.vue', './pages/**/*.vue', './app.vue', './composables/**/*.ts', './utils/**/*.ts'],
  darkMode: ['selector', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        bg: token('bg'),
        surface: token('surface'),
        subtle: token('subtle'),
        line: token('line'),
        'line-strong': token('line-strong'),
        fg: token('fg'),
        'fg-2': token('fg-2'),
        'fg-3': token('fg-3'),
        accent: token('accent'),
        ok: token('ok'),
        warn: token('warn'),
        down: token('down'),
        unknown: token('unknown'),
        affected: token('affected'),
      },
      fontFamily: {
        sans: ['Geist', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['"Geist Mono"', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      fontSize: { base: ['15px', '22px'] },
      borderRadius: { card: '16px', ctl: '11px' },
      boxShadow: { float: 'var(--shadow-float)' },
    },
  },
}
