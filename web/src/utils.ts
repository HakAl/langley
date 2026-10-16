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

export interface TimeRangeOption {
  key: string
  label: string
  days: number | null
}

const MINUTES_PER_DAY = 24 * 60

export const TIME_RANGE_OPTIONS: TimeRangeOption[] = [
  { key: '5m', label: 'Last 5 minutes', days: 5 / MINUTES_PER_DAY },
  { key: '15m', label: 'Last 15 minutes', days: 15 / MINUTES_PER_DAY },
  { key: '1h', label: 'Last 1 hour', days: 1 / 24 },
  { key: '6h', label: 'Last 6 hours', days: 6 / 24 },
  { key: '24h', label: 'Last 24 hours', days: 1 },
  { key: '7d', label: 'Last 7 days', days: 7 },
  { key: '30d', label: 'Last 30 days', days: 30 },
  { key: '90d', label: 'Last 90 days', days: 90 },
  { key: 'all', label: 'All time', days: null },
]

/** Maps a day count to its option key; matches within one second to absorb float error. */
export function timeRangeKey(days: number | null): string {
  if (days == null) return 'all'
  const opt = TIME_RANGE_OPTIONS.find(o => o.days != null && Math.abs(o.days - days) * 86400 < 1)
  return opt ? opt.key : 'all'
}

/** Human label for a time range, e.g. "5 minutes", "1 hour", "24 hours", "7 days", "all time". */
export function formatTimeRangeLabel(days: number | null): string {
  if (days == null) return 'all time'
  const minutes = Math.round(days * MINUTES_PER_DAY)
  const plural = (n: number, unit: string) => `${n} ${unit}${n === 1 ? '' : 's'}`
  if (minutes < 60) return plural(Math.max(minutes, 1), 'minute')
  if (minutes <= MINUTES_PER_DAY && minutes % 60 === 0) return plural(minutes / 60, 'hour')
  if (minutes < MINUTES_PER_DAY) return plural(minutes, 'minute')
  const wholeDays = Math.round(days)
  return Math.abs(days - wholeDays) * MINUTES_PER_DAY < 1 ? plural(wholeDays, 'day') : plural(Math.round(days * 24), 'hour')
}

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
