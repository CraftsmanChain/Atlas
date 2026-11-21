import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import './i18n'
import { ThemeProvider } from 'next-themes'

// A tab opened before a deploy may request lazy chunks/CSS of the previous build.
// Reload once to pick up the new index; the guard prevents a reload loop if the asset is truly missing.
window.addEventListener('vite:preloadError', event => {
  const key = 'atlas-preload-reload-at'
  const last = Number(sessionStorage.getItem(key) || 0)
  if (Date.now() - last < 60_000) return
  event.preventDefault()
  sessionStorage.setItem(key, String(Date.now()))
  window.location.reload()
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider attribute="data-theme" defaultTheme="system" enableSystem themes={['light', 'dark', 'color']}>
      <App />
    </ThemeProvider>
  </StrictMode>,
)
