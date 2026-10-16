import type { View, TaskSummary } from './types'

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

/**
 * Strips any leading HTTP status code prefix from statusText so that rendering
 * status_code alongside status_text never duplicates the numeric code (e.g. "200 200 OK" -> "OK").
 */
export function formatStatusText(statusCode?: number, statusText?: string): string {
  if (!statusText) return ''
  const trimmed = statusText.trim()
  if (!trimmed) return ''
  if (statusCode != null) {
    const codeStr = String(statusCode)
    if (trimmed === codeStr) return ''
    if (trimmed.startsWith(`${codeStr} `)) {
      return trimmed.slice(codeStr.length).trimStart()
    }
  }
  return trimmed
}

/**
 * Formats a task identifier for display. If the task ID is a serialized JSON object
 * from Claude Code metadata.user_id, extracts the concise session/device identifier.
 */
export function formatTaskId(taskId: string): string {
  if (taskId.startsWith('{')) {
    try {
      const parsed = JSON.parse(taskId) as { session_id?: unknown; device_id?: unknown }
      if (typeof parsed?.session_id === 'string' && parsed.session_id.length > 0) {
        return `session-${parsed.session_id}`
      }
      if (typeof parsed?.device_id === 'string' && parsed.device_id.length > 0) {
        return `device-${parsed.device_id}`
      }
    } catch {
      // Fall through to raw taskId
    }
  }
  return taskId
}

export type TaskSortField =
  | 'task_id'
  | 'flow_count'
  | 'total_tokens_in'
  | 'total_tokens_out'
  | 'total_cost'
  | 'duration_ms'
  | 'last_seen'

export type SortDirection = 'asc' | 'desc'

export function sortTasks(tasks: readonly TaskSummary[], field: TaskSortField, direction: SortDirection): TaskSummary[] {
  const mult = direction === 'asc' ? 1 : -1
  return [...tasks].sort((a, b) => {
    let cmp = 0
    switch (field) {
      case 'task_id':
        cmp = formatTaskId(a.task_id).localeCompare(formatTaskId(b.task_id))
        break
      case 'flow_count':
        cmp = (a.flow_count ?? 0) - (b.flow_count ?? 0)
        break
      case 'total_tokens_in':
        cmp = (a.total_tokens_in ?? 0) - (b.total_tokens_in ?? 0)
        break
      case 'total_tokens_out':
        cmp = (a.total_tokens_out ?? 0) - (b.total_tokens_out ?? 0)
        break
      case 'total_cost':
        cmp = (a.total_cost ?? 0) - (b.total_cost ?? 0)
        break
      case 'duration_ms':
        cmp = (a.duration_ms ?? 0) - (b.duration_ms ?? 0)
        break
      case 'last_seen':
        cmp = Date.parse(a.last_seen) - Date.parse(b.last_seen)
        break
    }
    if (cmp !== 0) return cmp * mult
    return a.task_id.localeCompare(b.task_id)
  })
}

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
