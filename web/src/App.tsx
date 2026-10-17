import { useState, useEffect, useCallback, useRef } from 'react'
import type { ReactNode } from 'react'
import type { CaptureStatus, Flow, Stats, TaskSummary, ToolStats, ToolInvocation, Anomaly, CostPeriod, Settings, ApiResult } from './types'
import {
  applyFlowUpdate,
  applyLiveDeltaToTotal,
  computeQuietRefreshDelay,
  isCustomFlowSort,
  MAX_COUNT_REFRESH_ATTEMPTS,
  pruneExpiredFlows,
  reconcileFlowPage,
  runCountRefreshAttempts,
} from './mergeFlows'
import type { FlowFilterCriteria, FlowSortDir } from './mergeFlows'
import { filterAnomalies, getAnomalyKey, loadDismissedAnomalyKeys, saveDismissedAnomalyKeys } from './anomalies'
import { getInitialTheme } from './utils'
import { useHashRoute } from './hooks/useHashRoute'
import { setFlowQueryParams, useApi } from './hooks/useApi'
import type { FlowFetchParams } from './hooks/useApi'
import { useWebSocket } from './hooks/useWebSocket'
import { useKeyboardNav } from './hooks/useKeyboardNav'
import { Nav } from './components/Nav'
import { FlowDetail } from './components/FlowDetail'
import { ToolInvocationDetail } from './components/ToolInvocationDetail'
import { HelpModal } from './components/HelpModal'
import { ExportModal } from './components/ExportModal'
import { ErrorBoundary } from './components/ErrorBoundary'
import { FlowListView } from './views/FlowListView'
import { AnalyticsView } from './views/AnalyticsView'
import { TasksView } from './views/TasksView'
import { ToolsView } from './views/ToolsView'
import { AnomaliesView } from './views/AnomaliesView'
import { SettingsView } from './views/SettingsView'

const PAGE_SIZE = 50
const MS_PER_DAY = 86_400_000
const MAX_TIMEOUT_MS = 2_147_483_647

function buildFlowCountSearchParams(params: FlowFetchParams): URLSearchParams {
  const qs = new URLSearchParams()
  if (params.host) qs.set('host', params.host)
  if (params.taskId) qs.set('task_id', params.taskId)
  if (params.status && params.status !== 'all') qs.set('status', params.status)
  setFlowQueryParams(qs, params)
  if (params.days != null) {
    const end = new Date()
    const start = new Date(end.getTime() - params.days * 24 * 60 * 60 * 1000)
    qs.set('start_time', start.toISOString())
    qs.set('end_time', end.toISOString())
  }
  return qs
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

interface CaptureControlProps {
  status: CaptureStatus
  pending: boolean
  onSetMode: (enabled: boolean, force: boolean) => void
}

function CaptureControl({ status, pending, onSetMode }: CaptureControlProps) {
  const button = (label: string, ariaLabel: string, enabled: boolean, force: boolean, primary = false) => (
    <button
      type="button"
      className={`${primary ? 'primary-btn' : 'secondary-btn'} capture-btn`}
      aria-label={ariaLabel}
      disabled={pending}
      onClick={() => onSetMode(enabled, force)}
    >
      {label}
    </button>
  )

  let label: string
  let detail: ReactNode = null
  let actions: ReactNode
  switch (status.state) {
    case 'passthrough':
      label = 'Passthrough'
      detail = <span className="capture-detail">TLS payloads not decrypted or stored</span>
      actions = button('Start Capture', 'Start capture', true, false, true)
      break
    case 'enabling':
      label = `Capturing (${plural(status.draining_passthrough_tunnels, 'active connection')} will switch after current request)`
      actions = <>
        {button('Force Switch', 'Force switch active connections', true, true, true)}
        {button('Pause Capture', 'Pause capture', false, false)}
      </>
      break
    case 'capturing': {
      const hosts = status.intercept_hosts.join(', ')
      const scope = `Intercepting ${hosts || 'no hosts'}; all other hosts tunnel through untouched`
      label = 'Capturing'
      detail = (
        <span className="capture-detail capture-scope" title={scope}>
          Intercepting {plural(status.intercept_hosts.length, 'host pattern')}
        </span>
      )
      actions = button('Pause Capture', 'Pause capture', false, false)
      break
    }
    case 'stopping':
      label = `Stopping capture — finishing ${plural(status.draining_mitm_requests, 'active request')}…`
      actions = <>
        {button('Force Stop', 'Force stop active requests', false, true, true)}
        {button('Resume Capture', 'Resume capture', true, false)}
      </>
      break
  }

  return (
    <div className="capture-control" data-state={status.state} aria-busy={pending}>
      <div className="capture-status" role="status">
        <span className="status-dot capture-dot" aria-hidden="true"></span>
        <span className="capture-label">{label}</span>
        {detail}
      </div>
      <div className="capture-actions">{actions}</div>
    </div>
  )
}

function App() {
  const [theme, setTheme] = useState<'dark' | 'light'>(getInitialTheme)
  const [flows, setFlows] = useState<Flow[]>([])
  const [totalFlows, setTotalFlows] = useState(0)
  const [page, setPage] = useState(0)
  const [selectedFlow, setSelectedFlow] = useState<Flow | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [connected, setConnected] = useState(false)
  const [captureStatus, setCaptureStatus] = useState<CaptureStatus | null>(null)
  const [captureActionPending, setCaptureActionPending] = useState(false)
  const [initialLoading, setInitialLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [viewError, setViewError] = useState<string | null>(null)
  const [view, navigateTo] = useHashRoute()
  const [stats, setStats] = useState<Stats | null>(null)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const [tools, setTools] = useState<ToolStats[]>([])
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])
  const [dismissedAnomalyKeys, setDismissedAnomalyKeys] = useState<ReadonlySet<string>>(loadDismissedAnomalyKeys)
  const [showDismissedAnomalies, setShowDismissedAnomalies] = useState(false)
  const [dailyCosts, setDailyCosts] = useState<CostPeriod[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const [draftIdleGap, setDraftIdleGap] = useState<number | null>(null)
  const [viewLoading, setViewLoading] = useState(false)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [showHelp, setShowHelp] = useState(false)
  const [exportConfig, setExportConfig] = useState<{ format: string; rowCount: number } | null>(null)
  const [exportMessage, setExportMessage] = useState<string | null>(null)
  const exportTimeoutRef = useRef<number | null>(null)
  const [hostFilter, setHostFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'success' | 'error'>('all')
  const [timeRange, setTimeRange] = useState<number | null>(1)
  const [methodFilter, setMethodFilter] = useState('')
  const [pathFilter, setPathFilter] = useState('')
  const [modelFilter, setModelFilter] = useState('')
  const [sortBy, setSortBy] = useState('timestamp')
  const [sortDir, setSortDir] = useState<FlowSortDir>('desc')
  // Tool drill-down state
  const [selectedTool, setSelectedTool] = useState<string | null>(null)
  const [toolInvocations, setToolInvocations] = useState<ToolInvocation[]>([])
  const [toolInvocationsTotal, setToolInvocationsTotal] = useState(0)
  const [toolInvocationsLoading, setToolInvocationsLoading] = useState(false)
  const [selectedToolInvocation, setSelectedToolInvocation] = useState<ToolInvocation | null>(null)
  const [invocationSelectedIndex, setInvocationSelectedIndex] = useState(0)
  const [expiryTick, setExpiryTick] = useState(0)
  const [offPagePollTick, setOffPagePollTick] = useState(0)
  const hostFilterRef = useRef<HTMLInputElement>(null)
  const pageRef = useRef(page)
  pageRef.current = page
  const flowsRef = useRef<Flow[]>(flows)
  flowsRef.current = flows
  const totalFlowsRef = useRef(totalFlows)
  totalFlowsRef.current = totalFlows
  const hasAuthoritativeTotalRef = useRef(false)
  const authoritativeDerivedTotalRef = useRef<number | null>(null)
  const pendingQuietRefreshRef = useRef<{
    requestId: number
    params: FlowFetchParams
    retryAttempt: number
    timerId: number
  } | null>(null)
  const knownFlowIdsRef = useRef<Set<string>>(new Set())
  const restFlowIdsRef = useRef<Set<string>>(new Set())
  const activeFetchTrackersRef = useRef<Set<Map<string, number>>>(new Set())
  const flowPageRequestIdRef = useRef(0)
  const foregroundNavIdRef = useRef(0)
  const pollFailureCountRef = useRef(0)
  const viewRef = useRef(view)
  viewRef.current = view
  const filterCriteriaRef = useRef({
    host: hostFilter, taskId: taskFilter, status: statusFilter, days: timeRange,
    method: methodFilter, path: pathFilter, model: modelFilter, sortBy, sortDir,
  })
  filterCriteriaRef.current = {
    host: hostFilter, taskId: taskFilter, status: statusFilter, days: timeRange,
    method: methodFilter, path: pathFilter, model: modelFilter, sortBy, sortDir,
  }

  const api = useApi()

  // Live filter criteria for client-side matching, read from refs so WebSocket closures stay current
  const currentFilterCriteria = useCallback((): FlowFilterCriteria => {
    const { host, taskId, status, days, method, path, model, sortBy, sortDir } = filterCriteriaRef.current
    return {
      host: host || undefined,
      taskId: taskId || undefined,
      status,
      days,
      method: method || undefined,
      path: path || undefined,
      model: model || undefined,
      sortBy,
      sortDir,
    }
  }, [])

  const pendingPageRefreshTimerRef = useRef<number | null>(null)
  const flowRefreshErrorRef = useRef(false)

  const clearPendingQuietRefresh = useCallback(() => {
    if (pendingQuietRefreshRef.current !== null) {
      window.clearTimeout(pendingQuietRefreshRef.current.timerId)
      pendingQuietRefreshRef.current = null
    }
    if (pendingPageRefreshTimerRef.current !== null) {
      window.clearTimeout(pendingPageRefreshTimerRef.current)
      pendingPageRefreshTimerRef.current = null
    }
  }, [])

  const clampPageToTotal = useCallback((total: number): boolean => {
    const maxPage = Math.max(0, Math.ceil(total / PAGE_SIZE) - 1)
    if (pageRef.current > maxPage) {
      pageRef.current = maxPage
      if (viewRef.current === 'flows') {
        foregroundNavIdRef.current += 1
        setViewLoading(true)
      }
      setPage(maxPage)
      setSelectedIndex(0)
      return true
    }
    return false
  }, [])

  // Build fetch params from current filter/page refs so reconnect closures always use live filters
  const buildFetchParams = useCallback((pageOverride?: number): FlowFetchParams => {
    const { host, taskId, status, days, method, path, model, sortBy, sortDir } = filterCriteriaRef.current
    const customSort = sortBy !== 'timestamp' || sortDir !== 'desc'
    return {
      days: days ?? undefined,
      limit: PAGE_SIZE,
      offset: (pageOverride ?? pageRef.current) * PAGE_SIZE,
      host: host || undefined,
      taskId: taskId || undefined,
      status: status !== 'all' ? status : undefined,
      method: method || undefined,
      path: path || undefined,
      model: model || undefined,
      sortBy: customSort ? sortBy : undefined,
      sortDir: customSort ? sortDir : undefined,
    }
  }, [])

  // When a page fetch overlaps with an off-page live event, its embedded total
  // cannot prove whether the server snapshot ran before or after that event.
  // Obtain a fresh authoritative count, retrying up to MAX_COUNT_REFRESH_ATTEMPTS
  // immediately and scheduling a quiet/backoff recovery refresh if overlap persists.
  const refreshAuthoritativeCount = useCallback(async (
    requestId: number,
    params: FlowFetchParams,
    quietRetryAttempt?: number,
  ) => {
    await runCountRefreshAttempts(
      {
        isCurrentRequest: () => flowPageRequestIdRef.current === requestId,
        getAuthoritativeDerivedTotal: () => authoritativeDerivedTotalRef.current,
        registerTracker: (tracker) => activeFetchTrackersRef.current.add(tracker),
        unregisterTracker: (tracker) => activeFetchTrackersRef.current.delete(tracker),
        fetchCount: () => api.fetchFlowCount(buildFlowCountSearchParams(params)),
        onCountUpdated: (refreshed) => {
          hasAuthoritativeTotalRef.current = !refreshed.countInvalidated
          if (!refreshed.countInvalidated) {
            authoritativeDerivedTotalRef.current = refreshed.total
            clearPendingQuietRefresh()
          }
          totalFlowsRef.current = refreshed.total
          setTotalFlows(refreshed.total)
          if (clampPageToTotal(refreshed.total)) {
            clearPendingQuietRefresh()
          }
        },
        scheduleQuietRetry: (retryAttempt) => {
          clearPendingQuietRefresh()
          const delay = computeQuietRefreshDelay(retryAttempt)
          const timerId = window.setTimeout(() => {
            pendingQuietRefreshRef.current = null
            void refreshAuthoritativeCount(requestId, params, retryAttempt + 1)
          }, delay)
          pendingQuietRefreshRef.current = { requestId, params, retryAttempt, timerId }
        },
      },
      MAX_COUNT_REFRESH_ATTEMPTS,
      quietRetryAttempt,
    )
  }, [api.fetchFlowCount, clampPageToTotal, clearPendingQuietRefresh])

  const applyDeltaToTotals = useCallback((totalDelta: number, deltaById?: ReadonlyMap<string, number>) => {
    let hasIdDelta = false
    if (deltaById) {
      for (const [, d] of deltaById.entries()) {
        if (d !== 0) {
          hasIdDelta = true
          break
        }
      }
      if (hasIdDelta) {
        for (const tracker of activeFetchTrackersRef.current) {
          for (const [id, d] of deltaById.entries()) {
            if (d !== 0) {
              tracker.set(id, (tracker.get(id) ?? 0) + d)
            }
          }
        }
      }
    }
    if (totalDelta === 0 && !hasIdDelta) return
    if (totalDelta !== 0) {
      const updated = applyLiveDeltaToTotal({
        currentTotal: totalFlowsRef.current,
        authoritativeDerivedTotal: authoritativeDerivedTotalRef.current,
        hasAuthoritativeTotal: hasAuthoritativeTotalRef.current,
        totalDelta,
      })
      authoritativeDerivedTotalRef.current = updated.authoritativeDerivedTotal
      totalFlowsRef.current = updated.total
      setTotalFlows(updated.total)
      if (clampPageToTotal(updated.total)) {
        clearPendingQuietRefresh()
        return
      }
    }
    if (pendingQuietRefreshRef.current !== null) {
      const { requestId, params, retryAttempt } = pendingQuietRefreshRef.current
      clearPendingQuietRefresh()
      const delay = computeQuietRefreshDelay(retryAttempt)
      const timerId = window.setTimeout(() => {
        pendingQuietRefreshRef.current = null
        void refreshAuthoritativeCount(requestId, params, retryAttempt + 1)
      }, delay)
      pendingQuietRefreshRef.current = { requestId, params, retryAttempt, timerId }
    }
  }, [clampPageToTotal, clearPendingQuietRefresh, refreshAuthoritativeCount])

  const fetchFlowPage = useCallback(async (opts?: { page?: number; merge?: boolean }) => {
    const requestId = ++flowPageRequestIdRef.current
    const navIdAtStart = foregroundNavIdRef.current
    const pageAtStart = opts?.page ?? pageRef.current
    clearPendingQuietRefresh()
    const preFetchTotal = authoritativeDerivedTotalRef.current
    const tracker = new Map<string, number>()
    activeFetchTrackersRef.current.add(tracker)
    const params = buildFetchParams(opts?.page)
    try {
      const { data, error } = await api.fetchFlows(params)
      const stale = flowPageRequestIdRef.current !== requestId
      if (data && !stale) {
        pollFailureCountRef.current = 0
        const shouldMerge = Boolean(opts?.merge && pageAtStart === 0)
        const reconciled = reconcileFlowPage({
          currentFlows: flowsRef.current,
          incomingFlows: data.flows,
          incomingTotal: data.total,
          preFetchTotal,
          merge: shouldMerge,
          knownIds: knownFlowIdsRef.current,
          restIds: restFlowIdsRef.current,
          inFlightDeltas: tracker,
          sortBy: params.sortBy,
          sortDir: params.sortDir,
          pageSize: PAGE_SIZE,
        })
        flowsRef.current = reconciled.flows
        hasAuthoritativeTotalRef.current = !reconciled.countInvalidated
        if (!reconciled.countInvalidated) {
          authoritativeDerivedTotalRef.current = reconciled.total
        }
        totalFlowsRef.current = reconciled.total
        setFlows(reconciled.flows)
        setTotalFlows(reconciled.total)
        const clamped = clampPageToTotal(reconciled.total)
        if (!clamped) {
          if (
            foregroundNavIdRef.current === navIdAtStart &&
            pageRef.current === pageAtStart &&
            viewRef.current === 'flows'
          ) {
            setViewLoading(false)
          }
          if (reconciled.countInvalidated) {
            void refreshAuthoritativeCount(requestId, params)
          }
        }
      }
      return { data, error, stale }
    } finally {
      activeFetchTrackersRef.current.delete(tracker)
    }
  }, [api.fetchFlows, buildFetchParams, clampPageToTotal, clearPendingQuietRefresh, refreshAuthoritativeCount])

  const schedulePageRefresh = useCallback((attempt = 0) => {
    if (pendingPageRefreshTimerRef.current !== null) {
      window.clearTimeout(pendingPageRefreshTimerRef.current)
      pendingPageRefreshTimerRef.current = null
    }
    if (viewRef.current !== 'flows') return
    const navIdAtStart = foregroundNavIdRef.current
    void fetchFlowPage({ page: pageRef.current }).then(({ error, stale }) => {
      if (stale || viewRef.current !== 'flows' || foregroundNavIdRef.current !== navIdAtStart) return
      if (!error) {
        if (flowRefreshErrorRef.current) {
          flowRefreshErrorRef.current = false
          setViewError(null)
        }
        return
      }
      if (attempt < 3) {
        const delay = Math.min(200 * (2 ** attempt), 2000)
        pendingPageRefreshTimerRef.current = window.setTimeout(() => {
          pendingPageRefreshTimerRef.current = null
          schedulePageRefresh(attempt + 1)
        }, delay)
      } else {
        flowRefreshErrorRef.current = true
        setViewError(error)
      }
    })
  }, [fetchFlowPage])

  const handleFlowUpdate = useCallback((flow: Flow) => {
    const result = applyFlowUpdate(
      flowsRef.current,
      flow,
      pageRef.current,
      currentFilterCriteria(),
      knownFlowIdsRef.current,
      PAGE_SIZE,
    )
    flowsRef.current = result.flows
    setFlows(result.flows)
    if (result.totalDelta !== 0 || result.deltaById.size > 0) {
      applyDeltaToTotals(result.totalDelta, result.deltaById)
    } else {
      clampPageToTotal(totalFlowsRef.current)
    }
    if (result.needsPageRefresh && viewRef.current === 'flows') {
      schedulePageRefresh(0)
    }
  }, [applyDeltaToTotals, clampPageToTotal, currentFilterCriteria, schedulePageRefresh])

  // Every authoritative capture update bumps the version; a response from a request
  // issued before a newer update is stale and must not overwrite it.
  const captureVersionRef = useRef(0)
  const applyCaptureStatus = useCallback((status: CaptureStatus) => {
    captureVersionRef.current += 1
    setCaptureStatus(status)
  }, [])

  const refreshCaptureStatus = useCallback(async () => {
    const version = captureVersionRef.current
    const { data } = await api.fetchCaptureStatus()
    if (data && captureVersionRef.current === version) applyCaptureStatus(data)
  }, [api.fetchCaptureStatus, applyCaptureStatus])

  const captureActionPendingRef = useRef(false)
  const handleSetCaptureMode = useCallback(async (enabled: boolean, force: boolean) => {
    if (captureActionPendingRef.current) return
    captureActionPendingRef.current = true
    setCaptureActionPending(true)
    const version = captureVersionRef.current
    try {
      const { data, error } = await api.setCaptureMode(enabled, force)
      if (error) {
        setError(error)
      } else if (data && captureVersionRef.current === version) {
        applyCaptureStatus(data)
      }
    } finally {
      captureActionPendingRef.current = false
      setCaptureActionPending(false)
    }
  }, [api.setCaptureMode, applyCaptureStatus])

  const handleReconnect = useCallback(() => {
    fetchFlowPage({ merge: true })
    void refreshCaptureStatus()
  }, [fetchFlowPage, refreshCaptureStatus])

  const { connect, cleanup } = useWebSocket({
    onFlowUpdate: handleFlowUpdate,
    onConnectedChange: setConnected,
    onError: setError,
    onReconnect: handleReconnect,
    onCaptureStateChange: applyCaptureStatus,
  })

  // Reset page to 0 on filter change; ignore no-op selections so viewLoading never gets stuck
  const changeFlowListFilter = useCallback(<K extends keyof typeof filterCriteriaRef.current>(
    key: K,
    value: (typeof filterCriteriaRef.current)[K],
    setState: (v: (typeof filterCriteriaRef.current)[K]) => void,
  ) => {
    if (value === filterCriteriaRef.current[key] && pageRef.current === 0) return
    filterCriteriaRef.current = { ...filterCriteriaRef.current, [key]: value }
    pageRef.current = 0
    if (viewRef.current === 'flows') {
      foregroundNavIdRef.current += 1
      setViewLoading(true)
    }
    setState(value)
    setPage(0)
  }, [])
  const handleHostFilterChange = useCallback((v: string) => changeFlowListFilter('host', v, setHostFilter), [changeFlowListFilter])
  const handleTaskFilterChange = useCallback((v: string) => changeFlowListFilter('taskId', v, setTaskFilter), [changeFlowListFilter])
  const handleStatusFilterChange = useCallback((v: 'all' | 'success' | 'error') => changeFlowListFilter('status', v, setStatusFilter), [changeFlowListFilter])
  const handleMethodFilterChange = useCallback((v: string) => changeFlowListFilter('method', v, setMethodFilter), [changeFlowListFilter])
  const handlePathFilterChange = useCallback((v: string) => changeFlowListFilter('path', v, setPathFilter), [changeFlowListFilter])
  const handleModelFilterChange = useCallback((v: string) => changeFlowListFilter('model', v, setModelFilter), [changeFlowListFilter])
  const handleSortChange = useCallback((nextSortBy: string, nextSortDir: FlowSortDir) => {
    const cur = filterCriteriaRef.current
    if (nextSortBy === cur.sortBy && nextSortDir === cur.sortDir && pageRef.current === 0) return
    filterCriteriaRef.current = { ...cur, sortBy: nextSortBy, sortDir: nextSortDir }
    pageRef.current = 0
    if (viewRef.current === 'flows') {
      foregroundNavIdRef.current += 1
      setViewLoading(true)
    }
    setSortBy(nextSortBy)
    setSortDir(nextSortDir)
    setPage(0)
    setSelectedIndex(0)
  }, [])
  const handleTimeRangeChange = useCallback((days: number | null) => {
    const daysChanged = days !== filterCriteriaRef.current.days
    if (!daysChanged && pageRef.current === 0) return
    filterCriteriaRef.current = { ...filterCriteriaRef.current, days }
    pageRef.current = 0
    if (daysChanged || viewRef.current === 'flows') {
      foregroundNavIdRef.current += 1
      setViewLoading(true)
    }
    setTimeRange(days)
    setPage(0)
  }, [])
  const handlePageChange = useCallback((newPage: number) => {
    if (newPage === pageRef.current) return
    pageRef.current = newPage
    if (viewRef.current === 'flows') {
      foregroundNavIdRef.current += 1
      setViewLoading(true)
    }
    setPage(newPage)
    setSelectedIndex(0)
  }, [])

  // Initial load
  useEffect(() => {
    fetchFlowPage({ page: 0, merge: true }).then(({ error }) => { if (error) setViewError(error); setInitialLoading(false); connect() })
    api.fetchStats(timeRange ?? undefined).then(({ data }) => { if (data) setStats(data) })
    void refreshCaptureStatus()
    return () => {
      clearPendingQuietRefresh()
      cleanup()
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Known IDs and in-flight deltas are only meaningful for the filter set they were counted under
  useEffect(() => {
    clearPendingQuietRefresh()
    knownFlowIdsRef.current.clear()
    restFlowIdsRef.current.clear()
    hasAuthoritativeTotalRef.current = false
    authoritativeDerivedTotalRef.current = null
    totalFlowsRef.current = 0
    pollFailureCountRef.current = 0
    for (const tracker of activeFetchTrackersRef.current) {
      tracker.clear()
    }
  }, [hostFilter, taskFilter, statusFilter, timeRange, methodFilter, pathFilter, modelFilter, clearPendingQuietRefresh])

  // Re-fetch flows when filters or page change
  useEffect(() => {
    if (view !== 'flows') return
    const navId = ++foregroundNavIdRef.current
    pollFailureCountRef.current = 0
    flowRefreshErrorRef.current = false
    setViewError(null)
    setViewLoading(true)
    fetchFlowPage({ page }).then(({ error, stale }) => {
      if (!stale && viewRef.current === 'flows' && foregroundNavIdRef.current === navId && error) {
        setViewError(error)
      }
    }).finally(() => {
      if (foregroundNavIdRef.current === navId && pageRef.current === page && viewRef.current === 'flows') {
        setViewLoading(false)
      }
    })
  }, [page, hostFilter, taskFilter, statusFilter, timeRange, methodFilter, pathFilter, modelFilter, sortBy, sortDir, view]) // eslint-disable-line react-hooks/exhaustive-deps

  // Expire displayed flows locally as the rolling time window advances
  useEffect(() => {
    if (view !== 'flows' || initialLoading || viewLoading || timeRange == null || flows.length === 0) return
    const windowMs = timeRange * MS_PER_DAY
    let nextExpiryMs = Infinity
    for (const f of flows) {
      const flowMs = Date.parse(f.timestamp)
      if (!Number.isNaN(flowMs)) {
        nextExpiryMs = Math.min(nextExpiryMs, flowMs + windowMs)
      }
    }
    if (!Number.isFinite(nextExpiryMs)) return

    const delay = Math.min(MAX_TIMEOUT_MS, Math.max(0, nextExpiryMs - Date.now() + 10))
    const timerId = window.setTimeout(() => {
      const pruned = pruneExpiredFlows(
        flowsRef.current,
        currentFilterCriteria(),
        knownFlowIdsRef.current,
      )
      if (pruned.totalDelta !== 0) {
        pollFailureCountRef.current = 0
        flowsRef.current = pruned.flows
        setFlows(pruned.flows)
        applyDeltaToTotals(pruned.totalDelta, pruned.deltaById)
      } else {
        setExpiryTick((t) => t + 1)
      }
      clampPageToTotal(totalFlowsRef.current)
    }, delay)
    return () => window.clearTimeout(timerId)
  }, [view, initialLoading, viewLoading, timeRange, flows, expiryTick, applyDeltaToTotals, clampPageToTotal, currentFilterCriteria])

  // Reconcile off-page rolling-window totals and membership when older pages exist
  useEffect(() => {
    if (view !== 'flows' || initialLoading || viewLoading || timeRange == null) return
    const hasOffPageFlows =
      (totalFlows > page * PAGE_SIZE + flows.length && flows.length >= PAGE_SIZE) ||
      (flows.length === 0 && totalFlows > page * PAGE_SIZE) ||
      (page > 0 && isCustomFlowSort(sortBy, sortDir))
    if (!hasOffPageFlows || pollFailureCountRef.current >= 3) return

    let cancelled = false
    const timerId = window.setTimeout(async () => {
      if (cancelled) return
      const pruned = pruneExpiredFlows(
        flowsRef.current,
        currentFilterCriteria(),
        knownFlowIdsRef.current,
      )
      if (pruned.totalDelta !== 0) {
        pollFailureCountRef.current = 0
        flowsRef.current = pruned.flows
        setFlows(pruned.flows)
        applyDeltaToTotals(pruned.totalDelta, pruned.deltaById)
      }
      if (clampPageToTotal(totalFlowsRef.current)) {
        return
      }

      const currentPage = pageRef.current
      const stillHasOffPage =
        (totalFlowsRef.current > currentPage * PAGE_SIZE + flowsRef.current.length &&
          flowsRef.current.length >= PAGE_SIZE) ||
        (flowsRef.current.length === 0 && totalFlowsRef.current > currentPage * PAGE_SIZE) ||
        (currentPage > 0 && isCustomFlowSort(filterCriteriaRef.current.sortBy, filterCriteriaRef.current.sortDir))

      if (
        !stillHasOffPage ||
        activeFetchTrackersRef.current.size > 0 ||
        pendingQuietRefreshRef.current !== null
      ) {
        if (stillHasOffPage && !cancelled) {
          setOffPagePollTick((t) => t + 1)
        }
        return
      }

      let lastError: string | null = null
      for (let attempt = 0; attempt < 2; attempt++) {
        if (cancelled) return
        const { error, stale } = await fetchFlowPage({ page: currentPage })
        lastError = error
        if (!error || stale) break
      }
      if (lastError) {
        pollFailureCountRef.current += 1
      }
      if (!cancelled) {
        setOffPagePollTick((t) => t + 1)
      }
    }, 1000)

    return () => {
      cancelled = true
      window.clearTimeout(timerId)
    }
  }, [view, initialLoading, viewLoading, timeRange, page, flows.length, totalFlows, sortBy, sortDir, offPagePollTick, applyDeltaToTotals, clampPageToTotal, fetchFlowPage])

  // View-specific data (non-flow views)
  useEffect(() => {
    flowRefreshErrorRef.current = false
    setViewError(null)
    if (view === 'flows') {
      // Handled by the filter/page effect above
      return
    }
    if (pendingPageRefreshTimerRef.current !== null) {
      window.clearTimeout(pendingPageRefreshTimerRef.current)
      pendingPageRefreshTimerRef.current = null
    }
    const navId = ++foregroundNavIdRef.current
    setViewLoading(true)
    const days = timeRange ?? undefined
    const showError = (error: string | null) => { if (foregroundNavIdRef.current === navId && error) setViewError(error) }
    const done = () => { if (foregroundNavIdRef.current === navId) setViewLoading(false) }
    if (view === 'analytics') {
      Promise.all([
        api.fetchStats(days).then(({ data, error }) => { if (data) setStats(data); showError(error) }),
        api.fetchDailyCosts(days).then(({ data, error }) => { if (data) setDailyCosts(data); showError(error) }),
      ]).finally(done)
    } else if (view === 'tasks') {
      api.fetchTasks(days).then(({ data, error }) => { if (data) setTasks(data); showError(error) }).finally(done)
    } else if (view === 'tools') {
      api.fetchTools(days).then(({ data, error }) => { if (data) setTools(data); showError(error) }).finally(done)
    } else if (view === 'anomalies') {
      api.fetchAnomalies(days).then(({ data, error }) => { if (data) setAnomalies(data); showError(error) }).finally(done)
    } else if (view === 'settings') {
      api.fetchSettings().then(({ data, error }) => { if (data) setSettings(data); showError(error) }).finally(done)
    } else {
      done()
    }
  }, [view, timeRange, api.fetchStats, api.fetchDailyCosts, api.fetchTasks, api.fetchTools, api.fetchAnomalies, api.fetchSettings])

  // Fetch tool invocations when drill-down is active
  useEffect(() => {
    if (!selectedTool) return
    setToolInvocationsLoading(true)
    setInvocationSelectedIndex(0)
    api.fetchToolInvocations(selectedTool, timeRange ?? undefined).then(({ data, error }) => {
      if (data) {
        setToolInvocations(data.items)
        setToolInvocationsTotal(data.total)
      }
      if (error) setViewError(error)
    }).finally(() => setToolInvocationsLoading(false))
  }, [selectedTool, timeRange, api.fetchToolInvocations])

  // Clear tool drill-down when leaving tools view
  useEffect(() => {
    if (view !== 'tools') {
      setSelectedTool(null)
      setSelectedToolInvocation(null)
    }
  }, [view])

  // Theme
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('langley_theme', theme)
  }, [theme])

  // Warn before unloading if settings have unsaved changes
  useEffect(() => {
    if (draftIdleGap === null) return
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault()
    }
    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [draftIdleGap])

  useEffect(() => {
    return () => {
      if (exportTimeoutRef.current !== null) window.clearTimeout(exportTimeoutRef.current)
    }
  }, [])

  const detailOpen = (!!selectedFlow && view === 'flows') || (!!selectedToolInvocation && view === 'tools')

  const { visible: visibleAnomalies, activeCount: activeAnomalyCount } = filterAnomalies(
    anomalies,
    dismissedAnomalyKeys,
    showDismissedAnomalies
  )

  const handleDismissAnomaly = useCallback((key: string) => {
    setDismissedAnomalyKeys(prev => {
      const next = new Set(prev)
      next.add(key)
      saveDismissedAnomalyKeys(next)
      return next
    })
  }, [])

  const handleRestoreAnomaly = useCallback((key: string) => {
    setDismissedAnomalyKeys(prev => {
      const next = new Set(prev)
      next.delete(key)
      saveDismissedAnomalyKeys(next)
      return next
    })
  }, [])

  const handleRestoreAllAnomalies = useCallback(() => {
    setDismissedAnomalyKeys(() => {
      const next = new Set<string>()
      saveDismissedAnomalyKeys(next)
      return next
    })
  }, [])

  // Reset selection on data change
  useEffect(() => { setSelectedIndex(0) }, [view, flows.length, tasks.length, tools.length, visibleAnomalies.length])

  const detailRequestIdRef = useRef(0)

  const closeFlowDetail = useCallback(() => {
    // Invalidate any in-flight detail request so it can't reopen the panel
    detailRequestIdRef.current += 1
    setSelectedFlow(null)
    setDetailLoading(false)
  }, [])

  const handleFlowSelect = useCallback(async (id: string) => {
    const requestId = ++detailRequestIdRef.current
    const preview = flowsRef.current.find(f => f.id === id)
    if (preview) {
      setSelectedFlow(prev => (prev?.id === id ? prev : preview))
    }
    setDetailLoading(true)
    try {
      const { data } = await api.fetchFlowDetail(id)
      if (detailRequestIdRef.current === requestId && data) {
        setSelectedFlow(data)
      }
    } finally {
      if (detailRequestIdRef.current === requestId) {
        setDetailLoading(false)
      }
    }
  }, [api.fetchFlowDetail])

  const handleToolSelect = useCallback((toolName: string) => {
    setSelectedTool(toolName)
    setToolInvocations([])
    setToolInvocationsTotal(0)
    setSelectedToolInvocation(null)
    setInvocationSelectedIndex(0)
  }, [])

  const handleToolBack = useCallback(() => {
    setSelectedTool(null)
    setSelectedToolInvocation(null)
    setToolInvocations([])
    setToolInvocationsTotal(0)
  }, [])

  const handleInvocationSelect = useCallback(async (inv: ToolInvocation) => {
    // Fetch full detail to get input/result
    const { data } = await api.fetchToolInvocationDetail(inv.id)
    if (data) setSelectedToolInvocation(data)
  }, [api.fetchToolInvocationDetail])

  const handleKeyboardEnter = useCallback(async (item: unknown) => {
    const rec = item as Record<string, unknown>
    if (view === 'flows' && rec.id) {
      handleFlowSelect(rec.id as string)
    } else if (view === 'tasks' && rec.task_id) {
      setTaskFilter(rec.task_id as string); setPage(0); navigateTo('flows')
    } else if (view === 'tools' && !selectedTool && rec.tool_name) {
      handleToolSelect(rec.tool_name as string)
    } else if (view === 'tools' && selectedTool) {
      handleInvocationSelect(item as ToolInvocation)
    } else if (view === 'anomalies' && rec.flow_id) {
      await handleFlowSelect(rec.flow_id as string); navigateTo('flows')
    }
  }, [view, selectedTool, handleFlowSelect, handleToolSelect, handleInvocationSelect, navigateTo])

  // When in tool drill-down, use invocations as the items list and invocationSelectedIndex
  const [sortedTasks, setSortedTasks] = useState<TaskSummary[]>([])
  const effectiveTasks = sortedTasks.length === tasks.length && sortedTasks.length > 0 ? sortedTasks : tasks
  const effectiveToolItems = selectedTool ? toolInvocations : tools
  const effectiveSelectedIndex = (view === 'tools' && selectedTool) ? invocationSelectedIndex : selectedIndex
  const effectiveSetSelectedIndex = (view === 'tools' && selectedTool)
    ? (fn: (i: number) => number) => setInvocationSelectedIndex(fn)
    : setSelectedIndex

  useKeyboardNav({
    view,
    selectedIndex: effectiveSelectedIndex,
    selectedFlow: selectedToolInvocation ? { id: 'tool-inv' } as Flow : selectedTool ? { id: 'tool-drill' } as Flow : selectedFlow,
    showHelp,
    items: { flows, tasks: effectiveTasks, tools: effectiveToolItems, anomalies: visibleAnomalies },
    setSelectedIndex: effectiveSetSelectedIndex,
    setShowHelp,
    clearSelectedFlow: () => {
      if (selectedToolInvocation) {
        setSelectedToolInvocation(null)
      } else if (selectedTool && view === 'tools') {
        handleToolBack()
      } else {
        closeFlowDetail()
      }
    },
    navigateTo, onEnter: handleKeyboardEnter,
    focusSearch: () => hostFilterRef.current?.focus(),
  })

  const totalPages = Math.max(1, Math.ceil(totalFlows / PAGE_SIZE))

  const handleStartExport = useCallback(async (format: string) => {
    const params = new URLSearchParams()
    if (hostFilter) params.set('host', hostFilter)
    if (taskFilter) params.set('task_id', taskFilter)
    if (statusFilter !== 'all') params.set('status', statusFilter)
    setFlowQueryParams(params, { method: methodFilter, path: pathFilter, model: modelFilter })
    const { data: countData } = await api.fetchFlowCount(params)
    setExportConfig({ format, rowCount: countData?.count ?? flows.length })
  }, [api.fetchFlowCount, hostFilter, taskFilter, statusFilter, methodFilter, pathFilter, modelFilter, flows.length])

  const handleConfirmExport = useCallback(async () => {
    if (!exportConfig) return
    const params = new URLSearchParams({ format: exportConfig.format })
    if (hostFilter) params.set('host', hostFilter)
    if (taskFilter) params.set('task_id', taskFilter)
    if (statusFilter !== 'all') params.set('status', statusFilter)
    setFlowQueryParams(params, { method: methodFilter, path: pathFilter, model: modelFilter, sortBy, sortDir })
    try {
      const res = await fetch(`/api/flows/export?${params}`, { credentials: 'include' })
      if (!res.ok) throw new Error('Export failed')
      const match = res.headers.get('Content-Disposition')?.match(/filename="(.+)"/)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = Object.assign(document.createElement('a'), { href: url, download: match?.[1] || `flows.${exportConfig.format}` })
      document.body.appendChild(a); a.click(); document.body.removeChild(a)
      URL.revokeObjectURL(url)
      setExportMessage(`Exported ${exportConfig.rowCount} flows as ${exportConfig.format.toUpperCase()}`)
      if (exportTimeoutRef.current !== null) window.clearTimeout(exportTimeoutRef.current)
      exportTimeoutRef.current = window.setTimeout(() => setExportMessage(null), 3000)
    } catch (err) { console.error('Export failed:', err) }
    finally { setExportConfig(null) }
  }, [exportConfig, hostFilter, taskFilter, statusFilter, methodFilter, pathFilter, modelFilter, sortBy, sortDir])

  const handleSaveSettings = useCallback(async (s: Partial<Settings>): Promise<ApiResult<Settings>> => {
    const result = await api.updateSettings(s)
    if (result.data) setSettings(result.data)
    return result
  }, [api.updateSettings])

  return (
    <ErrorBoundary>
    <div className="app">
      <header>
        <div className="header-brand">
          <img src="/eagle.svg" alt="" className="header-logo" />
          <h1>Langley</h1>
        </div>
        <Nav view={view} anomalyCount={activeAnomalyCount} onNavigate={navigateTo} />
        <div className="header-right">
          {captureStatus && <CaptureControl status={captureStatus} pending={captureActionPending} onSetMode={handleSetCaptureMode} />}
          <button className="theme-toggle" onClick={() => setTheme(t => t === 'dark' ? 'light' : 'dark')} title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`} aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}>
            {theme === 'dark' ? '☀' : '☽'}
          </button>
          <div className="status">
            <span className={`status-dot ${connected ? 'connected' : ''}`}></span>
            <span>{connected ? 'Connected' : 'Disconnected'}</span>
          </div>
        </div>
      </header>

      <div className="container">
        {error && (
          <div className="error-banner" role="alert">
            <span className="error-banner-message">{error}</span>
            <button className="error-banner-dismiss" onClick={() => setError(null)} aria-label="Dismiss error">&times;</button>
          </div>
        )}
        <div className={`main-content${detailOpen ? ' has-detail' : ''}`}>
          <div className="content-area">
            {viewError && (
              <div className="error-banner" role="alert">
                <span className="error-banner-message">{viewError}</span>
                <button className="error-banner-dismiss" onClick={() => setViewError(null)} aria-label="Dismiss error">&times;</button>
              </div>
            )}
            {view === 'flows' && <FlowListView flows={flows} totalFlows={totalFlows} initialLoading={initialLoading} selectedFlowId={selectedFlow?.id ?? null} selectedIndex={selectedIndex} hostFilter={hostFilter} taskFilter={taskFilter} statusFilter={statusFilter} timeRange={timeRange} page={page} totalPages={totalPages} onHostFilterChange={handleHostFilterChange} onTaskFilterChange={handleTaskFilterChange} onStatusFilterChange={handleStatusFilterChange} methodFilter={methodFilter} pathFilter={pathFilter} modelFilter={modelFilter} sortBy={sortBy} sortDir={sortDir} onMethodFilterChange={handleMethodFilterChange} onPathFilterChange={handlePathFilterChange} onModelFilterChange={handleModelFilterChange} onSortChange={handleSortChange} onTimeRangeChange={handleTimeRangeChange} onPageChange={handlePageChange} onFlowSelect={handleFlowSelect} onStartExport={handleStartExport} exportMessage={exportMessage} hostFilterRef={hostFilterRef} />}
            {view === 'analytics' && <AnalyticsView stats={stats} dailyCosts={dailyCosts} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} loading={viewLoading} />}
            {view === 'tasks' && <TasksView tasks={tasks} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onTaskSelect={(id) => { setTaskFilter(id); setPage(0); navigateTo('flows') }} onSortedTasksChange={setSortedTasks} loading={viewLoading} />}
            {view === 'tools' && <ToolsView tools={tools} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onToolSelect={handleToolSelect} loading={viewLoading} selectedTool={selectedTool} invocations={toolInvocations} invocationsTotal={toolInvocationsTotal} invocationsLoading={toolInvocationsLoading} invocationSelectedIndex={invocationSelectedIndex} onBack={handleToolBack} onInvocationSelect={handleInvocationSelect} />}
            {view === 'anomalies' && <AnomaliesView anomalies={anomalies} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onViewFlow={async (id) => { await handleFlowSelect(id); navigateTo('flows') }} loading={viewLoading} dismissedKeys={dismissedAnomalyKeys} showDismissed={showDismissedAnomalies} onShowDismissedChange={setShowDismissedAnomalies} onDismiss={handleDismissAnomaly} onRestore={handleRestoreAnomaly} onRestoreAll={handleRestoreAllAnomalies} />}
            {view === 'settings' && <SettingsView settings={settings} onSave={handleSaveSettings} loading={viewLoading} draftIdleGap={draftIdleGap} onDraftChange={setDraftIdleGap} />}
          </div>
          {selectedFlow && view === 'flows' && <>
            <div className="detail-overlay" onClick={closeFlowDetail} />
            <FlowDetail flow={selectedFlow} onClose={closeFlowDetail} loading={detailLoading} anomalies={anomalies.filter(a => a.flow_id === selectedFlow.id && !dismissedAnomalyKeys.has(getAnomalyKey(a)))} />
          </>}
          {selectedToolInvocation && view === 'tools' && <>
            <div className="detail-overlay" onClick={() => setSelectedToolInvocation(null)} />
            <ToolInvocationDetail
              invocation={selectedToolInvocation}
              onClose={() => setSelectedToolInvocation(null)}
              onViewFlow={async (flowId) => {
                await handleFlowSelect(flowId)
                setSelectedToolInvocation(null)
                setSelectedTool(null)
                navigateTo('flows')
              }}
            />
          </>}
        </div>
      </div>

      {showHelp && <HelpModal onClose={() => setShowHelp(false)} />}
      {exportConfig && <ExportModal format={exportConfig.format} rowCount={exportConfig.rowCount} onConfirm={handleConfirmExport} onCancel={() => setExportConfig(null)} />}
    </div>
    </ErrorBoundary>
  )
}

export default App
