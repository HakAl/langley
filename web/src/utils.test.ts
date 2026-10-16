import { describe, it, expect } from 'vitest'
import { TIME_RANGE_OPTIONS, formatTimeRangeLabel, timeRangeKey } from './utils'

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
