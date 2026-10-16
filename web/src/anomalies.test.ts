import { describe, it, expect } from 'vitest'
import type { Anomaly } from './types'
import {
  DISMISSED_ANOMALIES_STORAGE_KEY,
  getAnomalyKey,
  loadDismissedAnomalyKeys,
  saveDismissedAnomalyKeys,
  filterAnomalies,
} from './anomalies'

function anomaly(overrides: Partial<Anomaly> = {}): Anomaly {
  return {
    type: 'high_latency',
    flow_id: 'flow-1',
    timestamp: '2025-01-01T00:00:00Z',
    severity: 'warning',
    description: 'slow',
    value: 10,
    threshold: 5,
    ...overrides,
  }
}

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial))
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => { data.set(k, v) },
  }
}

describe('getAnomalyKey', () => {
  it('combines type, flow_id and timestamp', () => {
    expect(getAnomalyKey(anomaly())).toBe('high_latency:flow-1:2025-01-01T00:00:00Z')
  })

  it('is stable for equal anomalies regardless of mutable fields', () => {
    expect(getAnomalyKey(anomaly({ value: 1, description: 'a' }))).toBe(getAnomalyKey(anomaly({ value: 99, description: 'b' })))
  })

  it('distinguishes anomalies differing by type, flow or timestamp', () => {
    const keys = new Set([
      getAnomalyKey(anomaly()),
      getAnomalyKey(anomaly({ type: 'large_context' })),
      getAnomalyKey(anomaly({ flow_id: 'flow-2' })),
      getAnomalyKey(anomaly({ timestamp: '2025-01-02T00:00:00Z' })),
    ])
    expect(keys.size).toBe(4)
  })

  it('treats a missing flow_id as empty', () => {
    const a = anomaly()
    ;(a as { flow_id?: string }).flow_id = undefined
    expect(getAnomalyKey(a)).toBe('high_latency::2025-01-01T00:00:00Z')
  })
})

describe('loadDismissedAnomalyKeys', () => {
  it('returns an empty set when nothing is stored', () => {
    expect(loadDismissedAnomalyKeys(memoryStorage()).size).toBe(0)
  })

  it('returns an empty set when storage is unavailable', () => {
    expect(loadDismissedAnomalyKeys(undefined).size).toBe(0)
  })

  it('parses a stored string array', () => {
    const s = memoryStorage({ [DISMISSED_ANOMALIES_STORAGE_KEY]: JSON.stringify(['a', 'b']) })
    expect([...loadDismissedAnomalyKeys(s)].sort()).toEqual(['a', 'b'])
  })

  it('falls back to an empty set on corrupt JSON', () => {
    const s = memoryStorage({ [DISMISSED_ANOMALIES_STORAGE_KEY]: '{not json' })
    expect(loadDismissedAnomalyKeys(s).size).toBe(0)
  })

  it('falls back to an empty set when the value is not an array', () => {
    const s = memoryStorage({ [DISMISSED_ANOMALIES_STORAGE_KEY]: JSON.stringify({ a: 1 }) })
    expect(loadDismissedAnomalyKeys(s).size).toBe(0)
  })

  it('ignores non-string entries', () => {
    const s = memoryStorage({ [DISMISSED_ANOMALIES_STORAGE_KEY]: JSON.stringify(['a', 1, null, 'b']) })
    expect([...loadDismissedAnomalyKeys(s)].sort()).toEqual(['a', 'b'])
  })

  it('falls back to an empty set when getItem throws', () => {
    const s = { getItem: () => { throw new Error('denied') } }
    expect(loadDismissedAnomalyKeys(s).size).toBe(0)
  })
})

describe('saveDismissedAnomalyKeys', () => {
  it('writes keys as a JSON array that round-trips through load', () => {
    const s = memoryStorage()
    saveDismissedAnomalyKeys(new Set(['x', 'y']), s)
    expect(JSON.parse(s.data.get(DISMISSED_ANOMALIES_STORAGE_KEY)!)).toEqual(['x', 'y'])
    expect([...loadDismissedAnomalyKeys(s)].sort()).toEqual(['x', 'y'])
  })

  it('writes an empty array for no keys', () => {
    const s = memoryStorage({ [DISMISSED_ANOMALIES_STORAGE_KEY]: '["old"]' })
    saveDismissedAnomalyKeys([], s)
    expect(s.data.get(DISMISSED_ANOMALIES_STORAGE_KEY)).toBe('[]')
  })

  it('does not throw when setItem throws', () => {
    const s = { setItem: () => { throw new Error('quota') } }
    expect(() => saveDismissedAnomalyKeys(['a'], s)).not.toThrow()
  })
})

describe('filterAnomalies', () => {
  const a1 = anomaly({ flow_id: 'f1' })
  const a2 = anomaly({ flow_id: 'f2' })
  const a3 = anomaly({ flow_id: 'f3' })
  const all = [a1, a2, a3]

  it('returns everything with zero dismissed when no keys', () => {
    expect(filterAnomalies(all, new Set())).toEqual({ visible: all, activeCount: 3, dismissedCount: 0 })
  })

  it('hides dismissed anomalies by default', () => {
    const r = filterAnomalies(all, new Set([getAnomalyKey(a2)]))
    expect(r.visible).toEqual([a1, a3])
    expect(r.activeCount).toBe(2)
    expect(r.dismissedCount).toBe(1)
  })

  it('shows all anomalies in original order when showDismissed is true', () => {
    const r = filterAnomalies(all, new Set([getAnomalyKey(a2)]), true)
    expect(r.visible).toEqual(all)
    expect(r.activeCount).toBe(2)
    expect(r.dismissedCount).toBe(1)
  })

  it('ignores dismissed keys that do not match any current anomaly', () => {
    const r = filterAnomalies(all, new Set(['stale:key:2020', getAnomalyKey(a1)]))
    expect(r.dismissedCount).toBe(1)
    expect(r.activeCount).toBe(2)
  })

  it('handles all anomalies dismissed', () => {
    const r = filterAnomalies(all, new Set(all.map(getAnomalyKey)))
    expect(r).toEqual({ visible: [], activeCount: 0, dismissedCount: 3 })
  })

  it('handles an empty anomaly list', () => {
    expect(filterAnomalies([], new Set(['a']), true)).toEqual({ visible: [], activeCount: 0, dismissedCount: 0 })
  })
})
