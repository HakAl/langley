import type { View } from './types'

export const VALID_VIEWS: View[] = ['flows', 'analytics', 'tasks', 'tools', 'anomalies', 'settings']
export const DEFAULT_VIEW: View = 'flows'

export function parseHash(hash: string): View {
  const raw = hash.replace(/^#\/?/, '')
  return VALID_VIEWS.includes(raw as View) ? (raw as View) : DEFAULT_VIEW
}

export const formatTime = (timestamp: string) => new Date(timestamp).toLocaleTimeString()
export const formatDate = (timestamp: string) => new Date(timestamp).toLocaleDateString()
export const formatCost = (cost?: number) => cost != null ? `$${cost.toFixed(4)}` : null
export const formatDuration = (ms?: number) => ms != null ? `${(ms / 1000).toFixed(2)}s` : null

export function getStatusClass(code?: number): string {
  if (!code) return ''
  if (code < 300) return 'success'
  if (code < 400) return 'redirect'
  return 'error'
}

export function getSeverityClass(severity: string): string {
  switch (severity) {
    case 'critical': return 'error'
    case 'warning': return 'warning'
    default: return 'info'
  }
}

export function getInitialTheme(): 'dark' | 'light' {
  const stored = localStorage.getItem('langley_theme')
  if (stored === 'dark' || stored === 'light') return stored
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}
