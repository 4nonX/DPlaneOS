const KEY = 'dplaneos-theme'

export type Theme = 'dark' | 'light'

export function getTheme(): Theme {
  return (localStorage.getItem(KEY) as Theme) ?? 'dark'
}

export function setTheme(t: Theme) {
  localStorage.setItem(KEY, t)
  document.documentElement.setAttribute('data-theme', t)
}

export function toggleTheme() {
  setTheme(getTheme() === 'dark' ? 'light' : 'dark')
}

export function initTheme() {
  document.documentElement.setAttribute('data-theme', getTheme())
}
