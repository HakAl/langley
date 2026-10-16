import { describe, it, expect } from 'vitest'
import type { TaskSummary } from './types'
import {
  TIME_RANGE_OPTIONS,
  formatTimeRangeLabel,
  timeRangeKey,
  formatStatusText,
  formatTaskId,
  sortTasks,
} from './utils'

describe('formatTimeRangeLabel', () => {
  it.each([
    [5 / 1440, '5 minutes'],
    [15 / 1440, '15 minutes'],
    [1 / 24, '1 hour'],
    [6 / 24, '6 hours'],
    [1, '24 hours'],
    [7, '7 days'],
    [30, '30 days'],
    [1.5, '36 hours'],
    [null, 'all time'],
  ])('formats %s days as %s', (days, label) => {
    expect(formatTimeRangeLabel(days)).toBe(label)
  })
})

describe('timeRangeKey', () => {
  it('round-trips every option', () => {
    for (const o of TIME_RANGE_OPTIONS) expect(timeRangeKey(o.days)).toBe(o.key)
  })

  it('tolerates float drift from recomputed fractions', () => {
    expect(timeRangeKey((5 * 60 * 1000) / 86_400_000)).toBe('5m')
    expect(timeRangeKey(0.25 + 1e-12)).toBe('6h')
  })

  it('falls back to all for unknown values', () => {
    expect(timeRangeKey(3)).toBe('all')
  })
})

describe('formatStatusText', () => {
  it('strips leading duplicate status code from status_text', () => {
    expect(formatStatusText(200, '200 OK')).toBe('OK')
    expect(formatStatusText(404, '404 Not Found')).toBe('Not Found')
    expect(formatStatusText(500, '500 Internal Server Error')).toBe('Internal Server Error')
  })

  it('preserves status_text that does not repeat the code', () => {
    expect(formatStatusText(200, 'OK')).toBe('OK')
    expect(formatStatusText(400, 'Bad Request')).toBe('Bad Request')
  })

  it('returns empty string when status_text is only the numeric code or empty', () => {
    expect(formatStatusText(200, '200')).toBe('')
    expect(formatStatusText(200, '')).toBe('')
    expect(formatStatusText(200, undefined)).toBe('')
  })
})

describe('formatTaskId', () => {
  it('extracts session_id from Claude Code JSON task_id', () => {
    const raw = '{"device_id":"dev-1","account_uuid":"acct-1","session_id":"sess-abc-123"}'
    expect(formatTaskId(raw)).toBe('session-sess-abc-123')
  })

  it('preserves plain task IDs', () => {
    expect(formatTaskId('task-abc123')).toBe('task-abc123')
    expect(formatTaskId('anthropic-1')).toBe('anthropic-1')
  })
})

describe('sortTasks', () => {
  const sampleTasks: TaskSummary[] = [
    {
      task_id: 'task-beta',
      flow_count: 10,
      total_tokens_in: 1000,
      total_tokens_out: 500,
      total_cost: 0.25,
      first_seen: '2026-10-16T10:00:00Z',
      last_seen: '2026-10-16T10:05:00Z',
      duration_ms: 300000,
    },
    {
      task_id: 'task-alpha',
      flow_count: 2,
      total_tokens_in: 5000,
      total_tokens_out: 100,
      total_cost: 0.05,
      first_seen: '2026-10-16T11:00:00Z',
      last_seen: '2026-10-16T11:01:00Z',
      duration_ms: 60000,
    },
    {
      task_id: 'task-gamma',
      flow_count: 25,
      total_tokens_in: 2000,
      total_tokens_out: 3000,
      total_cost: 1.5,
      first_seen: '2026-10-16T09:00:00Z',
      last_seen: '2026-10-16T09:30:00Z',
      duration_ms: 1800000,
    },
  ]

  it('sorts by total_cost desc and asc', () => {
    expect(sortTasks(sampleTasks, 'total_cost', 'desc').map(t => t.task_id)).toEqual([
      'task-gamma',
      'task-beta',
      'task-alpha',
    ])
    expect(sortTasks(sampleTasks, 'total_cost', 'asc').map(t => t.task_id)).toEqual([
      'task-alpha',
      'task-beta',
      'task-gamma',
    ])
  })

  it('sorts by flow_count, tokens, duration, task_id, and last_seen', () => {
    expect(sortTasks(sampleTasks, 'flow_count', 'desc').map(t => t.task_id)).toEqual([
      'task-gamma',
      'task-beta',
      'task-alpha',
    ])
    expect(sortTasks(sampleTasks, 'total_tokens_in', 'desc').map(t => t.task_id)).toEqual([
      'task-alpha',
      'task-gamma',
      'task-beta',
    ])
    expect(sortTasks(sampleTasks, 'total_tokens_out', 'desc').map(t => t.task_id)).toEqual([
      'task-gamma',
      'task-beta',
      'task-alpha',
    ])
    expect(sortTasks(sampleTasks, 'duration_ms', 'asc').map(t => t.task_id)).toEqual([
      'task-alpha',
      'task-beta',
      'task-gamma',
    ])
    expect(sortTasks(sampleTasks, 'task_id', 'asc').map(t => t.task_id)).toEqual([
      'task-alpha',
      'task-beta',
      'task-gamma',
    ])
    expect(sortTasks(sampleTasks, 'last_seen', 'desc').map(t => t.task_id)).toEqual([
      'task-alpha',
      'task-beta',
      'task-gamma',
    ])
  })
})
