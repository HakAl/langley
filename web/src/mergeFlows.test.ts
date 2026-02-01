import { describe, it, expect } from 'vitest'
import type { Flow } from './types'
import { mergeFlows } from './mergeFlows'

function flow(overrides: Partial<Flow> & { id: string }): Flow {
  return {
    host: 'api.example.com',
    method: 'POST',
    path: '/v1/chat',
    is_sse: false,
    timestamp: '2025-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('mergeFlows', () => {
  it('returns incoming when current is empty', () => {
    const incoming = [flow({ id: '1' }), flow({ id: '2' })]
    expect(mergeFlows([], incoming)).toHaveLength(2)
  })

  it('returns current when incoming is empty (WS-only preserved)', () => {
    const current = [flow({ id: '1' })]
    expect(mergeFlows(current, [])).toEqual(current)
  })

  it('returns empty when both are empty', () => {
    expect(mergeFlows([], [])).toEqual([])
  })

  it('keeps current flow when it has status_code but incoming does not', () => {
    const current = [flow({ id: '1', status_code: 200, duration_ms: 150 })]
    const incoming = [flow({ id: '1' })] // no status_code — WS raced ahead
    const result = mergeFlows(current, incoming)
    expect(result).toHaveLength(1)
    expect(result[0].status_code).toBe(200)
    expect(result[0].duration_ms).toBe(150)
  })

  it('uses incoming when both have status_code (DB is canonical)', () => {
    const current = [flow({ id: '1', status_code: 200, duration_ms: 100 })]
    const incoming = [flow({ id: '1', status_code: 200, duration_ms: 105 })]
    const result = mergeFlows(current, incoming)
    expect(result[0].duration_ms).toBe(105)
  })

  it('uses incoming when neither has status_code', () => {
    const current = [flow({ id: '1', path: '/old' })]
    const incoming = [flow({ id: '1', path: '/new' })]
    const result = mergeFlows(current, incoming)
    expect(result[0].path).toBe('/new')
  })

  it('uses incoming when only incoming has status_code', () => {
    const current = [flow({ id: '1' })]
    const incoming = [flow({ id: '1', status_code: 500 })]
    const result = mergeFlows(current, incoming)
    expect(result[0].status_code).toBe(500)
  })

  it('preserves WS-only flows not in REST response', () => {
    const current = [
      flow({ id: 'ws-only', timestamp: '2025-01-01T00:00:01Z' }),
      flow({ id: 'both', timestamp: '2025-01-01T00:00:00Z' }),
    ]
    const incoming = [flow({ id: 'both', timestamp: '2025-01-01T00:00:00Z' })]
    const result = mergeFlows(current, incoming)
    expect(result).toHaveLength(2)
    expect(result.map(f => f.id)).toContain('ws-only')
  })

  it('sorts by timestamp descending', () => {
    const current: Flow[] = []
    const incoming = [
      flow({ id: '1', timestamp: '2025-01-01T00:00:00Z' }),
      flow({ id: '2', timestamp: '2025-01-01T00:00:02Z' }),
      flow({ id: '3', timestamp: '2025-01-01T00:00:01Z' }),
    ]
    const result = mergeFlows(current, incoming)
    expect(result.map(f => f.id)).toEqual(['2', '3', '1'])
  })

  it('caps at 100 flows', () => {
    const incoming = Array.from({ length: 120 }, (_, i) =>
      flow({ id: `f${i}`, timestamp: `2025-01-01T00:${String(i).padStart(2, '0')}:00Z` })
    )
    const result = mergeFlows([], incoming)
    expect(result).toHaveLength(100)
  })
})
