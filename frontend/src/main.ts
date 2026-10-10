import { mount } from 'svelte'
import App from './App.svelte'
import './app.css'
import { applyCustomTheme } from './lib/customPalette'
import { loadPreferences } from './lib/storage'

try {
  const appearance = loadPreferences(localStorage)
  document.documentElement.dataset.theme = appearance.themeName
  document.documentElement.dataset.terminalColors = appearance.terminalColors
  document.documentElement.dataset.uiSize = appearance.uiSize
  applyCustomTheme(document.documentElement, appearance.customPalette, appearance.themeName === "custom", appearance.terminalColors === "neutral")
} catch {
  // The default palette remains usable when WebView storage is unavailable.
}

mount(App, { target: document.getElementById('app')! })
