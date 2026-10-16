import type { Flow } from './types'

/**
 * Merge incoming REST flows with current in-memory flows.
 *
 * Heuristic: if the current (WS-updated) flow has `status_code` but the
 * incoming (REST) copy doesn't, WS raced ahead — keep the current version.
 * Otherwise the REST/DB copy is canonical.
 *
 * Flows present in current but absent from incoming (WS-only, not yet in DB)
 * are preserved.
 */
export function mergeFlows(current: Flow[], incoming: Flow[]): Flow[] {
  const currentById = new Map(current.map(f => [f.id, f]))

  const merged = new Map<string, Flow>()

  for (const inc of incoming) {
    const cur = currentById.get(inc.id)
    if (cur && cur.status_code != null && inc.status_code == null) {
      merged.set(cur.id, cur)
    } else {
      merged.set(inc.id, inc)
    }
  }

  // Preserve WS-only flows not present in REST response
  for (const [id, flow] of currentById) {
    if (!merged.has(id)) {
      merged.set(id, flow)
    }
  }

  return [...merged.values()]
    .sort((a, b) => b.timestamp.localeCompare(a.timestamp))
}

const MS_PER_DAY = 86_400_000

export interface FlowFilterCriteria {
  host?: string
  taskId?: string
  status?: 'all' | 'success' | 'error'
  days?: number | null
  nowMs?: number
}

export function matchesFlowFilter(flow: Flow, filter?: FlowFilterCriteria): boolean {
  if (!filter) return true
  // Exact equality mirrors the server's ListFlows filters (host = ?, task_id = ?).
  if (filter.host && flow.host !== filter.host) {
    return false
  }
  if (filter.taskId && (flow.task_id ?? '') !== filter.taskId) {
    return false
  }
  if (filter.days != null) {
    const flowMs = Date.parse(flow.timestamp)
    const cutoffMs = (filter.nowMs ?? Date.now()) - filter.days * MS_PER_DAY
    if (!Number.isNaN(flowMs) && flowMs < cutoffMs) {
      return false
    }
  }
  if (filter.status === 'success') {
    return flow.status_code != null && flow.status_code < 400
  }
  if (filter.status === 'error') {
    return flow.status_code != null && flow.status_code >= 400
  }
  return true
}

export interface PruneExpiredFlowsResult {
  flows: Flow[]
  removedIds: string[]
  totalDelta: number
}

/**
 * Remove displayed flows that have aged out of the active time-range window.
 */
export function pruneExpiredFlows(
  currentFlows: Flow[],
  filter?: FlowFilterCriteria,
  knownIds?: Set<string>,
  excludeId?: string,
): PruneExpiredFlowsResult {
  if (!filter || filter.days == null || currentFlows.length === 0) {
    return { flows: currentFlows, removedIds: [], totalDelta: 0 }
  }
  const removedIds: string[] = []
  const kept = currentFlows.filter(f => {
    if (f.id === excludeId || matchesFlowFilter(f, filter)) return true
    removedIds.push(f.id)
    knownIds?.delete(f.id)
    return false
  })
  if (removedIds.length === 0) {
    return { flows: currentFlows, removedIds, totalDelta: 0 }
  }
  return {
    flows: kept,
    removedIds,
    totalDelta: -removedIds.length,
  }
}

export interface ApplyFlowUpdateResult {
  flows: Flow[]
  totalDelta: number
}

/**
 * Apply a single real-time WebSocket flow event to the current flow list.
 * Prevents double-counting when flow_start is followed by flow_complete/flow_update,
 * respects active filters (including time-range windows), and handles non-zero pagination pages cleanly.
 */
export function applyFlowUpdate(
  currentFlows: Flow[],
  incoming: Flow,
  page: number,
  filter?: FlowFilterCriteria,
  knownIds?: Set<string>,
): ApplyFlowUpdateResult {
  const pruned = pruneExpiredFlows(currentFlows, filter, knownIds, incoming.id)
  const baseFlows = pruned.flows
  const idx = baseFlows.findIndex(f => f.id === incoming.id)
  const matches = matchesFlowFilter(incoming, filter)
  const alreadyKnown = idx >= 0 || (knownIds?.has(incoming.id) ?? false)

  if (!matches) {
    if (idx >= 0) {
      knownIds?.delete(incoming.id)
      return {
        flows: baseFlows.filter(f => f.id !== incoming.id),
        totalDelta: pruned.totalDelta - 1,
      }
    }
    return { flows: baseFlows, totalDelta: pruned.totalDelta }
  }

  knownIds?.add(incoming.id)

  if (page !== 0) {
    if (idx >= 0) {
      const updated = [...baseFlows]
      updated[idx] = incoming
      return { flows: updated, totalDelta: pruned.totalDelta }
    }
    // Without a status filter, a completed flow was already counted at
    // flow_start or by an intervening REST page fetch's total.
    const hasStatusFilter = filter?.status === 'success' || filter?.status === 'error'
    const isCompletionOfStartedFlow = !hasStatusFilter && incoming.status_code != null
    return {
      flows: baseFlows,
      totalDelta: pruned.totalDelta + (alreadyKnown || isCompletionOfStartedFlow ? 0 : 1),
    }
  }

  if (idx >= 0) {
    const updated = [...baseFlows]
    updated[idx] = incoming
    return { flows: updated, totalDelta: pruned.totalDelta }
  }

  return {
    flows: [incoming, ...baseFlows],
    totalDelta: pruned.totalDelta + (alreadyKnown ? 0 : 1),
  }
}

export interface ReconcileFlowPageParams {
  currentFlows: Flow[]
  incomingFlows: Flow[]
  incomingTotal: number
  preFetchTotal?: number | null
  merge?: boolean
  knownIds: Set<string>
  inFlightDeltas?: ReadonlyMap<string, number>
}

export interface ReconcileFlowPageResult {
  flows: Flow[]
  total: number
  countInvalidated: boolean
}

/**
 * Reconcile a REST flow page response with live WebSocket state.
 *
 * - Unions returned flow IDs into knownIds so completions of flows that
 *   started before the REST request do not double-count.
 * - Flags `countInvalidated: true` whenever an off-page live event modified
 *   the count while the REST request was in flight, signaling the caller to
 *   obtain a fresh authoritative count.
 * - Computes an immediate provisional total from `preFetchTotal` (when a
 *   prior authoritative baseline exists for the active filter) so that:
 *   1. A delayed REST snapshot captured before an in-flight flow_start
 *      (incomingTotal === preFetchTotal) retains the live increment.
 *   2. A REST response whose total already includes the off-page flow_start
 *      (incomingTotal >= preFetchTotal + postSnapshotDelta) is not double-counted.
 */
export function reconcileFlowPage({
  currentFlows,
  incomingFlows,
  incomingTotal,
  preFetchTotal,
  merge = false,
  knownIds,
  inFlightDeltas,
}: ReconcileFlowPageParams): ReconcileFlowPageResult {
  const nextFlows = merge ? mergeFlows(currentFlows, incomingFlows) : incomingFlows
  const returnedIds = new Set(incomingFlows.map(f => f.id))

  for (const f of nextFlows) {
    knownIds.add(f.id)
  }

  let postSnapshotDelta = 0
  if (inFlightDeltas) {
    for (const [id, delta] of inFlightDeltas.entries()) {
      if (!returnedIds.has(id)) {
        postSnapshotDelta += delta
      }
    }
  }

  const countInvalidated = postSnapshotDelta !== 0
  let reconciledTotal = incomingTotal
  if (postSnapshotDelta !== 0) {
    if (preFetchTotal != null) {
      reconciledTotal = postSnapshotDelta > 0
        ? Math.max(incomingTotal, preFetchTotal + postSnapshotDelta)
        : Math.min(incomingTotal, preFetchTotal + postSnapshotDelta)
    } else {
      reconciledTotal = incomingTotal + postSnapshotDelta
    }
  }

  return {
    flows: nextFlows,
    total: Math.max(0, reconciledTotal),
    countInvalidated,
  }
}

export const MAX_COUNT_REFRESH_ATTEMPTS = 2
export const QUIET_COUNT_REFRESH_MS = 80
export const MAX_QUIET_COUNT_REFRESH_MS = 1000

export interface ApplyLiveDeltaParams {
  currentTotal: number
  authoritativeDerivedTotal: number | null
  hasAuthoritativeTotal: boolean
  totalDelta: number
}

export interface ApplyLiveDeltaResult {
  total: number
  authoritativeDerivedTotal: number | null
}

/**
 * Apply a non-zero live WebSocket count delta to the tracked totals.
 *
 * When `hasAuthoritativeTotal` is false, `currentTotal` may already reflect
 * an invalidated server count that ran ahead of WebSocket notifications;
 * advancing `authoritativeDerivedTotal` from the last non-overlapping baseline
 * prevents compounding `totalDelta` on top of an already-included ahead count.
 */
export function applyLiveDeltaToTotal({
  currentTotal,
  authoritativeDerivedTotal,
  hasAuthoritativeTotal,
  totalDelta,
}: ApplyLiveDeltaParams): ApplyLiveDeltaResult {
  const nextAuthoritativeDerived = authoritativeDerivedTotal != null
    ? Math.max(0, authoritativeDerivedTotal + totalDelta)
    : null

  if (!hasAuthoritativeTotal && nextAuthoritativeDerived != null) {
    const reconciled = totalDelta > 0
      ? Math.max(currentTotal, nextAuthoritativeDerived)
      : Math.min(currentTotal, nextAuthoritativeDerived)
    return {
      total: Math.max(0, reconciled),
      authoritativeDerivedTotal: nextAuthoritativeDerived,
    }
  }

  return {
    total: Math.max(0, currentTotal + totalDelta),
    authoritativeDerivedTotal: nextAuthoritativeDerived,
  }
}

export interface ReconcileCountRefreshParams {
  incomingCount: number
  preRefreshTotal?: number | null
  inFlightDeltas?: ReadonlyMap<string, number>
}

export interface ReconcileCountRefreshResult {
  total: number
  countInvalidated: boolean
}

/**
 * Reconcile a follow-up `/api/flows/count` response requested after an
 * overlapping page fetch invalidated the page response's count.
 */
export function reconcileCountRefresh({
  incomingCount,
  preRefreshTotal,
  inFlightDeltas,
}: ReconcileCountRefreshParams): ReconcileCountRefreshResult {
  let overlapDelta = 0
  if (inFlightDeltas) {
    for (const delta of inFlightDeltas.values()) {
      overlapDelta += delta
    }
  }

  if (overlapDelta === 0) {
    return {
      total: Math.max(0, incomingCount),
      countInvalidated: false,
    }
  }

  let reconciledTotal = incomingCount + overlapDelta
  if (preRefreshTotal != null) {
    reconciledTotal = overlapDelta > 0
      ? Math.max(incomingCount, preRefreshTotal + overlapDelta)
      : Math.min(incomingCount, preRefreshTotal + overlapDelta)
  }

  return {
    total: Math.max(0, reconciledTotal),
    countInvalidated: true,
  }
}

export function computeQuietRefreshDelay(retryAttempt: number): number {
  return Math.min(QUIET_COUNT_REFRESH_MS * (2 ** Math.max(0, retryAttempt)), MAX_QUIET_COUNT_REFRESH_MS)
}

export interface CountRefreshContext {
  isCurrentRequest: () => boolean
  getAuthoritativeDerivedTotal: () => number | null
  registerTracker: (tracker: Map<string, number>) => void
  unregisterTracker: (tracker: Map<string, number>) => void
  fetchCount: () => Promise<{ data: { count: number } | null; error: string | null }>
  onCountUpdated: (result: ReconcileCountRefreshResult) => void
  scheduleQuietRetry: (retryAttempt: number) => void
}

/**
 * Execute bounded immediate `/api/flows/count` refresh attempts after an
 * overlapping response, preserving invalidation and scheduling a quiet/backoff
 * retry if all immediate attempts also overlap with live events.
 */
export async function runCountRefreshAttempts(
  ctx: CountRefreshContext,
  maxImmediateAttempts = MAX_COUNT_REFRESH_ATTEMPTS,
  quietRetryAttempt?: number,
): Promise<void> {
  const attempts = quietRetryAttempt != null ? 1 : maxImmediateAttempts
  for (let attempt = 0; attempt < attempts; attempt++) {
    if (!ctx.isCurrentRequest()) return
    const preRefreshTotal = ctx.getAuthoritativeDerivedTotal()
    const countTracker = new Map<string, number>()
    ctx.registerTracker(countTracker)
    try {
      const { data: countData } = await ctx.fetchCount()
      if (!ctx.isCurrentRequest()) return
      if (typeof countData?.count !== 'number') return
      const refreshed = reconcileCountRefresh({
        incomingCount: countData.count,
        preRefreshTotal,
        inFlightDeltas: countTracker,
      })
      ctx.onCountUpdated(refreshed)
      if (!refreshed.countInvalidated) {
        return
      }
    } finally {
      ctx.unregisterTracker(countTracker)
    }
  }
  if (ctx.isCurrentRequest()) {
    ctx.scheduleQuietRetry(quietRetryAttempt ?? 0)
  }
}

