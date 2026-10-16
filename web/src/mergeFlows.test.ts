import { describe, it, expect } from 'vitest'
import type { Flow } from './types'
import {
  mergeFlows,
  matchesFlowFilter,
  applyFlowUpdate,
  applyLiveDeltaToTotal,
  computeQuietRefreshDelay,
  reconcileCountRefresh,
  reconcileFlowPage,
  runCountRefreshAttempts,
} from './mergeFlows'

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

  it('preserves all incoming flows without arbitrary client truncation', () => {
    const incoming = Array.from({ length: 120 }, (_, i) =>
      flow({ id: `f${i}`, timestamp: `2025-01-01T00:${String(i).padStart(2, '0')}:00Z` })
    )
    const result = mergeFlows([], incoming)
    expect(result).toHaveLength(120)
  })
})

describe('matchesFlowFilter', () => {
  it('matches all when no filter criteria are set', () => {
    expect(matchesFlowFilter(flow({ id: '1' }))).toBe(true)
    expect(matchesFlowFilter(flow({ id: '1' }), { status: 'all' })).toBe(true)
  })

  it('filters by host with exact equality, matching the server', () => {
    const f = flow({ id: '1', host: 'api.example.test' })
    expect(matchesFlowFilter(f, { host: 'api.example.test' })).toBe(true)
    expect(matchesFlowFilter(f, { host: 'example' })).toBe(false)
    expect(matchesFlowFilter(f, { host: 'API.EXAMPLE.TEST' })).toBe(false)
  })

  it('filters by taskId with exact equality, matching the server', () => {
    const f = flow({ id: '1', task_id: 'task-123' })
    expect(matchesFlowFilter(f, { taskId: 'task-123' })).toBe(true)
    expect(matchesFlowFilter(f, { taskId: 'task-1' })).toBe(false)
    expect(matchesFlowFilter(f, { taskId: 'TASK-123' })).toBe(false)
    expect(matchesFlowFilter(flow({ id: '2' }), { taskId: 'task-123' })).toBe(false)
  })

  it('filters by status success and error', () => {
    const pending = flow({ id: '1' })
    const ok = flow({ id: '2', status_code: 200 })
    const err = flow({ id: '3', status_code: 500 })

    expect(matchesFlowFilter(pending, { status: 'success' })).toBe(false)
    expect(matchesFlowFilter(ok, { status: 'success' })).toBe(true)
    expect(matchesFlowFilter(err, { status: 'success' })).toBe(false)

    expect(matchesFlowFilter(pending, { status: 'error' })).toBe(false)
    expect(matchesFlowFilter(ok, { status: 'error' })).toBe(false)
    expect(matchesFlowFilter(err, { status: 'error' })).toBe(true)
  })
})

describe('applyFlowUpdate', () => {
  it('does not double-count totalFlows when flow_start is followed by flow_complete on page 0', () => {
    const known = new Set<string>()
    const started = flow({ id: 'f1' })
    const step1 = applyFlowUpdate([], started, 0, undefined, known)
    expect(step1.flows).toEqual([started])
    expect(step1.totalDelta).toBe(1)

    const completed = flow({ id: 'f1', status_code: 200 })
    const step2 = applyFlowUpdate(step1.flows, completed, 0, undefined, known)
    expect(step2.flows).toEqual([completed])
    expect(step2.totalDelta).toBe(0)
  })

  it('increments totalDelta once for new flows on page > 0 without prepending to page list', () => {
    const known = new Set<string>(['page1-flow'])
    const page1Flows = [flow({ id: 'page1-flow' })]

    const started = flow({ id: 'new-flow' })
    const step1 = applyFlowUpdate(page1Flows, started, 1, undefined, known)
    expect(step1.flows).toEqual(page1Flows)
    expect(step1.totalDelta).toBe(1)

    const completed = flow({ id: 'new-flow', status_code: 200 })
    const step2 = applyFlowUpdate(step1.flows, completed, 1, undefined, known)
    expect(step2.flows).toEqual(page1Flows)
    expect(step2.totalDelta).toBe(0)
  })

  it('updates an existing flow in place on page > 0 without changing totalDelta', () => {
    const known = new Set<string>(['page1-flow'])
    const page1Flows = [flow({ id: 'page1-flow' })]
    const updated = flow({ id: 'page1-flow', status_code: 200 })

    const res = applyFlowUpdate(page1Flows, updated, 1, undefined, known)
    expect(res.flows).toEqual([updated])
    expect(res.totalDelta).toBe(0)
  })

  it('ignores incoming flows that do not match active filters and removes existing flows that stop matching', () => {
    const known = new Set<string>()
    const nonMatching = flow({ id: 'f1', host: 'api.openai.com' })
    const res1 = applyFlowUpdate([], nonMatching, 0, { host: 'api.anthropic.com' }, known)
    expect(res1.flows).toEqual([])
    expect(res1.totalDelta).toBe(0)

    const matching = flow({ id: 'f2', host: 'api.anthropic.com', status_code: 200 })
    const res2 = applyFlowUpdate([], matching, 0, { host: 'api.anthropic.com', status: 'success' }, known)
    expect(res2.flows).toEqual([matching])
    expect(res2.totalDelta).toBe(1)

    const turnedError = flow({ id: 'f2', host: 'api.anthropic.com', status_code: 500 })
    const res3 = applyFlowUpdate(res2.flows, turnedError, 0, { host: 'api.anthropic.com', status: 'success' }, known)
    expect(res3.flows).toEqual([])
    expect(res3.totalDelta).toBe(-1)
  })

  it('does not double-count when a page refetch lands between flow_start and flow_complete on page > 0', () => {
    const page = [flow({ id: 'old-page-row' })]
    const known = new Set(page.map(f => f.id))

    const started = applyFlowUpdate(page, flow({ id: 'new-flow' }), 1, undefined, known)
    expect(started.totalDelta).toBe(1)

    // fetchFlowPage unions the refetched page into the known set; the server's
    // refreshed total already includes new-flow, which lives on another page.
    for (const f of page) known.add(f.id)

    const completed = applyFlowUpdate(page, flow({ id: 'new-flow', status_code: 200 }), 1, undefined, known)
    expect(completed.flows).toEqual(page)
    expect(completed.totalDelta).toBe(0)
  })

  it('does not count an unseen completion on page > 0 even if the known set was reset', () => {
    const page = [flow({ id: 'old-page-row' })]
    const known = new Set(page.map(f => f.id))
    const res = applyFlowUpdate(page, flow({ id: 'new-flow', status_code: 200 }), 1, undefined, known)
    expect(res.totalDelta).toBe(0)
  })

  it('counts a completion on page > 0 under a status filter, since flow_start never matched', () => {
    const page = [flow({ id: 'old-page-row', status_code: 200 })]
    const known = new Set(page.map(f => f.id))
    const filter = { status: 'success' as const }

    const started = applyFlowUpdate(page, flow({ id: 'new-flow' }), 1, filter, known)
    expect(started.totalDelta).toBe(0)

    const completed = applyFlowUpdate(page, flow({ id: 'new-flow', status_code: 200 }), 1, filter, known)
    expect(completed.totalDelta).toBe(1)
  })

  it('filters out WebSocket updates older than the active time window and prunes expired rows', () => {
    const nowMs = Date.parse('2026-10-16T12:00:00Z')
    const fiveMinDays = 5 / 1440
    const recent = flow({ id: 'recent', timestamp: new Date(nowMs - 270_000).toISOString(), status_code: 200 })
    const oldCompleting = flow({ id: 'old', timestamp: new Date(nowMs - 600_000).toISOString(), status_code: 200 })
    const known = new Set([recent.id])

    // Old completion outside the 5m window is ignored
    const step1 = applyFlowUpdate([recent], oldCompleting, 0, { days: fiveMinDays, nowMs }, known)
    expect(step1.flows).toEqual([recent])
    expect(step1.totalDelta).toBe(0)

    // Advance 60s so `recent` (now 5.5m old) expires out of the 5m window
    const step2 = applyFlowUpdate(step1.flows, oldCompleting, 0, { days: fiveMinDays, nowMs: nowMs + 60_000 }, known)
    expect(step2.flows).toEqual([])
    expect(step2.totalDelta).toBe(-1)
    expect(known.has(recent.id)).toBe(false)
  })
})

describe('reconcileFlowPage', () => {
  it('preserves count across start -> fresh REST snapshot -> completion (no double-counting)', () => {
    const pageRows = [flow({ id: 'old-page-row' })]
    const known = new Set(pageRows.map(f => f.id))
    let total = 100

    // 1. Live flow_start arrives before the REST request starts
    const started = applyFlowUpdate(pageRows, flow({ id: 'new-flow' }), 1, undefined, known)
    total += started.totalDelta
    expect(total).toBe(101)

    // 2. Fresh REST request starts (preFetchTotal = 101) and finishes after flow_start (server total already 101, no in-flight deltas)
    const preFetchTotal = total
    const tracker = new Map<string, number>()
    const reconciled = reconcileFlowPage({
      currentFlows: started.flows,
      incomingFlows: pageRows,
      incomingTotal: 101,
      preFetchTotal,
      knownIds: known,
      inFlightDeltas: tracker,
    })
    total = reconciled.total
    expect(total).toBe(101)

    // 3. Live flow_complete arrives afterward
    const completed = applyFlowUpdate(reconciled.flows, flow({ id: 'new-flow', status_code: 200 }), 1, undefined, known)
    total += completed.totalDelta
    expect(total).toBe(101)
  })

  it('preserves count when a delayed REST snapshot (started before flow_start) resolves before flow_complete', () => {
    const pageRows = [flow({ id: 'old-page-row' })]
    const known = new Set(pageRows.map(f => f.id))
    let total = 100

    // 1. REST request for page 2 starts while client and server total are 100
    const preFetchTotal = total
    const tracker = new Map<string, number>()

    // 2. While REST request is in flight, an off-page flow_start arrives over WebSocket
    const started = applyFlowUpdate(pageRows, flow({ id: 'new-flow' }), 1, undefined, known)
    if (started.totalDelta !== 0) {
      tracker.set('new-flow', (tracker.get('new-flow') ?? 0) + started.totalDelta)
      total += started.totalDelta
    }
    expect(total).toBe(101)

    // 3. Held REST response (total: 100) resolves; in-flight delta against preFetchTotal preserves 101
    const reconciled = reconcileFlowPage({
      currentFlows: started.flows,
      incomingFlows: pageRows,
      incomingTotal: 100,
      preFetchTotal,
      knownIds: known,
      inFlightDeltas: tracker,
    })
    total = reconciled.total
    expect(total).toBe(101)

    // 4. Live flow_complete for new-flow arrives; total stays 101
    const completed = applyFlowUpdate(reconciled.flows, flow({ id: 'new-flow', status_code: 200 }), 1, undefined, known)
    total += completed.totalDelta
    expect(total).toBe(101)
  })

  it('does not double-count an off-page flow already included in incomingTotal when flow_start arrives in flight', () => {
    const pageRows = [flow({ id: 'old-page-row' })]
    const known = new Set(pageRows.map(f => f.id))
    let total = 100

    // 1. REST request for page 2 starts when client total is 100
    const preFetchTotal = total
    const tracker = new Map<string, number>()

    // 2. Server saves new-flow (on page 1) and computes total: 101; WS flow_start reaches browser before REST response
    const started = applyFlowUpdate(pageRows, flow({ id: 'new-flow' }), 1, undefined, known)
    if (started.totalDelta !== 0) {
      tracker.set('new-flow', (tracker.get('new-flow') ?? 0) + started.totalDelta)
      total += started.totalDelta
    }
    expect(total).toBe(101)

    // 3. Held REST response (total: 101, already including new-flow) resolves; total stays 101 (not 102)
    const reconciled = reconcileFlowPage({
      currentFlows: started.flows,
      incomingFlows: pageRows,
      incomingTotal: 101,
      preFetchTotal,
      knownIds: known,
      inFlightDeltas: tracker,
    })
    total = reconciled.total
    expect(total).toBe(101)

    // 4. Live flow_complete for new-flow arrives; total stays 101
    const completed = applyFlowUpdate(reconciled.flows, flow({ id: 'new-flow', status_code: 200 }), 1, undefined, known)
    total += completed.totalDelta
    expect(total).toBe(101)
  })

  it('does not double-apply in-flight delta if the returned REST page already includes the flow', () => {
    const known = new Set<string>(['one'])
    const tracker = new Map<string, number>([['new-flow', 1]])

    const reconciled = reconcileFlowPage({
      currentFlows: [flow({ id: 'one' }), flow({ id: 'new-flow' })],
      incomingFlows: [flow({ id: 'new-flow' }), flow({ id: 'one' })],
      incomingTotal: 2,
      preFetchTotal: 1,
      merge: true,
      knownIds: known,
      inFlightDeltas: tracker,
    })
    expect(reconciled.total).toBe(2)
    expect(reconciled.countInvalidated).toBe(false)
  })

  it('retains live increment when no prior baseline exists (initial load or filter change)', () => {
    const pageRows = [flow({ id: 'old-page-row' })]
    const known = new Set(pageRows.map(f => f.id))
    const tracker = new Map<string, number>([['new-flow', 1]])

    const reconciled = reconcileFlowPage({
      currentFlows: pageRows,
      incomingFlows: pageRows,
      incomingTotal: 100,
      preFetchTotal: null,
      knownIds: known,
      inFlightDeltas: tracker,
    })
    expect(reconciled.total).toBe(101)
    expect(reconciled.countInvalidated).toBe(true)
  })
})

describe('reconcileCountRefresh', () => {
  it('accepts incomingCount as authoritative when no overlap occurred during count refresh', () => {
    const refreshed = reconcileCountRefresh({
      incomingCount: 102,
      preRefreshTotal: 101,
      inFlightDeltas: new Map(),
    })
    expect(refreshed.total).toBe(102)
    expect(refreshed.countInvalidated).toBe(false)
  })

  it('flags countInvalidated and reconciles provisionally on continued overlap during count refresh', () => {
    const staleRefresh = reconcileCountRefresh({
      incomingCount: 101,
      preRefreshTotal: 101,
      inFlightDeltas: new Map([['second-flow', 1]]),
    })
    expect(staleRefresh.total).toBe(102)
    expect(staleRefresh.countInvalidated).toBe(true)

    const includedRefresh = reconcileCountRefresh({
      incomingCount: 102,
      preRefreshTotal: 101,
      inFlightDeltas: new Map([['second-flow', 1]]),
    })
    expect(includedRefresh.total).toBe(102)
    expect(includedRefresh.countInvalidated).toBe(true)
  })
})

describe('applyLiveDeltaToTotal & runCountRefreshAttempts (exhaustion and quiet recovery)', () => {
  it('does not mark an invalidated count refresh authoritative and recovers via quiet retry after budget exhaustion', async () => {
    const pageRows = [flow({ id: 'old-page-row' })]
    const known = new Set(pageRows.map(f => f.id))
    const activeTrackers = new Set<Map<string, number>>()

    // Baseline 100 from initial non-overlapping page load
    let totalFlows = 100
    let authoritativeDerivedTotal: number | null = 100
    let hasAuthoritativeTotal = true
    const scheduledQuietRetries: number[] = []

    const emitLive = (incoming: Flow) => {
      const res = applyFlowUpdate(pageRows, incoming, 1, undefined, known)
      if (res.totalDelta !== 0) {
        for (const tracker of activeTrackers) {
          tracker.set(incoming.id, (tracker.get(incoming.id) ?? 0) + res.totalDelta)
        }
        const updated = applyLiveDeltaToTotal({
          currentTotal: totalFlows,
          authoritativeDerivedTotal,
          hasAuthoritativeTotal,
          totalDelta: res.totalDelta,
        })
        authoritativeDerivedTotal = updated.authoritativeDerivedTotal
        totalFlows = updated.total
      }
    }

    // 1. Page 2 request overlaps with flow A ('new-flow')
    const pageTracker = new Map<string, number>()
    activeTrackers.add(pageTracker)
    const preFetchTotal = authoritativeDerivedTotal
    emitLive(flow({ id: 'new-flow', status_code: undefined }))
    expect(totalFlows).toBe(101)

    const pageReconciled = reconcileFlowPage({
      currentFlows: pageRows,
      incomingFlows: pageRows,
      incomingTotal: 101,
      preFetchTotal,
      knownIds: known,
      inFlightDeltas: pageTracker,
    })
    activeTrackers.delete(pageTracker)
    hasAuthoritativeTotal = !pageReconciled.countInvalidated
    totalFlows = pageReconciled.total
    expect(hasAuthoritativeTotal).toBe(false)
    expect(totalFlows).toBe(101)

    // 2. Count refresh 1 returns 103 (already includes B and C), while B ('second-flow') arrives in flight;
    //    Count refresh 2 returns 103, while C ('third-flow') arrives in flight.
    let countCall = 0
    const ctx = {
      isCurrentRequest: () => true,
      getAuthoritativeDerivedTotal: () => authoritativeDerivedTotal,
      registerTracker: (t: Map<string, number>) => activeTrackers.add(t),
      unregisterTracker: (t: Map<string, number>) => activeTrackers.delete(t),
      fetchCount: async () => {
        countCall++
        if (countCall === 1) {
          emitLive(flow({ id: 'second-flow', status_code: undefined }))
          return { data: { count: 103 }, error: null }
        }
        if (countCall === 2) {
          emitLive(flow({ id: 'third-flow', status_code: undefined }))
          return { data: { count: 103 }, error: null }
        }
        return { data: { count: 103 }, error: null }
      },
      onCountUpdated: (refreshed: { total: number; countInvalidated: boolean }) => {
        hasAuthoritativeTotal = !refreshed.countInvalidated
        if (!refreshed.countInvalidated) {
          authoritativeDerivedTotal = refreshed.total
        }
        totalFlows = refreshed.total
      },
      scheduleQuietRetry: (retryAttempt: number) => {
        scheduledQuietRetries.push(retryAttempt)
      },
    }

    await runCountRefreshAttempts(ctx, 2)
    expect(countCall).toBe(2)
    expect(totalFlows).toBe(103)
    expect(hasAuthoritativeTotal).toBe(false)
    expect(scheduledQuietRetries).toEqual([0])

    // 3. Quiet retry runs after quiet interval (no overlapping events) and settles authoritatively at 103
    await runCountRefreshAttempts(ctx, 2, scheduledQuietRetries[0] + 1)
    expect(countCall).toBe(3)
    expect(totalFlows).toBe(103)
    expect(hasAuthoritativeTotal).toBe(true)
    expect(authoritativeDerivedTotal).toBe(103)

    // 4. Completions for A, B, and C do not change the authoritative 103 count
    emitLive(flow({ id: 'new-flow', status_code: 200 }))
    emitLive(flow({ id: 'second-flow', status_code: 200 }))
    emitLive(flow({ id: 'third-flow', status_code: 200 }))
    expect(totalFlows).toBe(103)
  })

  it('recovers from a provisional overcount (when no prior baseline exists) via quiet retry after exhausting immediate attempts', async () => {
    const activeTrackers = new Set<Map<string, number>>()
    let totalFlows = 101
    let authoritativeDerivedTotal: number | null = null
    let hasAuthoritativeTotal = false
    const scheduledQuietRetries: number[] = []

    let countCall = 0
    const ctx = {
      isCurrentRequest: () => true,
      getAuthoritativeDerivedTotal: () => authoritativeDerivedTotal,
      registerTracker: (t: Map<string, number>) => activeTrackers.add(t),
      unregisterTracker: (t: Map<string, number>) => activeTrackers.delete(t),
      fetchCount: async () => {
        countCall++
        if (countCall <= 2) {
          for (const tracker of activeTrackers) {
            tracker.set(`overlap-${countCall}`, 1)
          }
        }
        return { data: { count: 103 }, error: null }
      },
      onCountUpdated: (refreshed: { total: number; countInvalidated: boolean }) => {
        hasAuthoritativeTotal = !refreshed.countInvalidated
        if (!refreshed.countInvalidated) {
          authoritativeDerivedTotal = refreshed.total
        }
        totalFlows = refreshed.total
      },
      scheduleQuietRetry: (retryAttempt: number) => {
        scheduledQuietRetries.push(retryAttempt)
      },
    }

    // Immediate attempts 1 and 2 both overlap and provisionally overcount to 104 without a baseline
    await runCountRefreshAttempts(ctx, 2)
    expect(countCall).toBe(2)
    expect(totalFlows).toBe(104)
    expect(hasAuthoritativeTotal).toBe(false)
    expect(scheduledQuietRetries).toEqual([0])

    // Quiet retry (attempt 3) runs without overlap and corrects totalFlows to 103
    await runCountRefreshAttempts(ctx, 2, scheduledQuietRetries[0] + 1)
    expect(countCall).toBe(3)
    expect(totalFlows).toBe(103)
    expect(hasAuthoritativeTotal).toBe(true)
    expect(authoritativeDerivedTotal).toBe(103)
  })

  it('computes bounded exponential backoff for quiet count refresh retries', () => {
    expect(computeQuietRefreshDelay(0)).toBe(80)
    expect(computeQuietRefreshDelay(1)).toBe(160)
    expect(computeQuietRefreshDelay(10)).toBe(1000)
  })
})
