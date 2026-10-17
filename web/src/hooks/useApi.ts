import { useCallback } from 'react'
import type { Anomaly, ApiResult, CaptureStatus, CostPeriod, Flow, FlowListResponse, Settings, Stats, TaskSummary, ToolInvocation, ToolStats } from '../types'

export interface FlowFetchParams {
  days?: number
  limit?: number
  offset?: number
  host?: string
  taskId?: string
  status?: string
  method?: string
  path?: string
  model?: string
  sortBy?: string
  sortDir?: 'asc' | 'desc'
}

/** Set the method/path/model/sort query params shared by list, count, and export. */
export function setFlowQueryParams(qs: URLSearchParams, params: Pick<FlowFetchParams, 'method' | 'path' | 'model' | 'sortBy' | 'sortDir'>): void {
  if (params.method) qs.set('method', params.method)
  if (params.path) qs.set('path', params.path)
  if (params.model) qs.set('model', params.model)
  if (params.sortBy) qs.set('sort_by', params.sortBy)
  if (params.sortDir) qs.set('sort_dir', params.sortDir)
}

/** Window used by stats and daily-cost endpoints when no time range is selected. */
export const DEFAULT_ANALYTICS_DAYS = 30

export function useApi() {
  const apiFetch = useCallback(async <T,>(path: string): Promise<ApiResult<T>> => {
    try {
      const res = await fetch(path, { credentials: 'include' })
      if (res.ok) return { data: await res.json(), error: null }
      const text = await res.text().catch(() => '')
      return { data: null, error: text || `HTTP ${res.status}` }
    } catch (err) {
      return { data: null, error: err instanceof Error ? err.message : 'Network error' }
    }
  }, [])

  const apiSend = useCallback(async <T,>(method: 'POST' | 'PUT', path: string, body: unknown): Promise<ApiResult<T>> => {
    try {
      const res = await fetch(path, {
        method,
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(body)
      })
      if (res.ok) return { data: await res.json(), error: null }
      const text = await res.text().catch(() => '')
      return { data: null, error: text || `HTTP ${res.status}` }
    } catch (err) {
      return { data: null, error: err instanceof Error ? err.message : 'Network error' }
    }
  }, [])

  const timeParams = useCallback((days?: number) => {
    const end = new Date()
    const start = days != null
      ? new Date(end.getTime() - days * 24 * 60 * 60 * 1000)
      : new Date(0)
    return new URLSearchParams({ start: start.toISOString(), end: end.toISOString() })
  }, [])

  const fetchFlows = useCallback(async (params: FlowFetchParams = {}) => {
    const qs = new URLSearchParams()
    qs.set('limit', String(params.limit ?? 50))
    if (params.offset) qs.set('offset', String(params.offset))
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
    return apiFetch<FlowListResponse>(`/api/flows?${qs}`)
  }, [apiFetch])

  const fetchStats = useCallback(async (days?: number) => {
    return apiFetch<Stats>(`/api/stats?${timeParams(days)}`)
  }, [apiFetch, timeParams])

  const fetchTasks = useCallback(async (days?: number) => {
    return apiFetch<TaskSummary[]>(`/api/analytics/tasks?${timeParams(days)}`)
  }, [apiFetch, timeParams])

  const fetchTools = useCallback(async (days?: number) => {
    return apiFetch<ToolStats[]>(`/api/analytics/tools?${timeParams(days)}`)
  }, [apiFetch, timeParams])

  const fetchAnomalies = useCallback(async (days?: number) => {
    if (days != null) {
      const since = new Date(Date.now() - days * 24 * 60 * 60 * 1000)
      return apiFetch<Anomaly[]>(`/api/analytics/anomalies?since=${since.toISOString()}`)
    }
    return apiFetch<Anomaly[]>('/api/analytics/anomalies')
  }, [apiFetch])

  const fetchDailyCosts = useCallback(async (days?: number) => {
    return apiFetch<CostPeriod[]>(`/api/analytics/cost/daily?${timeParams(days)}`)
  }, [apiFetch, timeParams])

  const fetchSettings = useCallback(async () => {
    return apiFetch<Settings>('/api/settings')
  }, [apiFetch])

  const updateSettings = useCallback(async (newSettings: Partial<Settings>): Promise<ApiResult<Settings>> => {
    return apiSend<Settings>('PUT', '/api/settings', newSettings)
  }, [apiSend])

  const fetchCaptureStatus = useCallback(async (): Promise<ApiResult<CaptureStatus>> => {
    return apiFetch<CaptureStatus>('/api/capture')
  }, [apiFetch])

  const setCaptureMode = useCallback(async (enabled: boolean, force?: boolean): Promise<ApiResult<CaptureStatus>> => {
    return apiSend<CaptureStatus>('POST', '/api/capture', { enabled, force: Boolean(force) })
  }, [apiSend])

  const fetchToolInvocations = useCallback(async (toolName: string, days?: number) => {
    const params = timeParams(days)
    params.set('limit', '50')
    return apiFetch<{ items: ToolInvocation[]; total: number }>(`/api/analytics/tools/${encodeURIComponent(toolName)}/invocations?${params}`)
  }, [apiFetch, timeParams])

  const fetchToolInvocationDetail = useCallback(async (id: string) => {
    return apiFetch<ToolInvocation>(`/api/analytics/tool-invocations/${encodeURIComponent(id)}`)
  }, [apiFetch])

  const fetchFlowDetail = useCallback(async (id: string) => {
    return apiFetch<Flow>(`/api/flows/${id}`)
  }, [apiFetch])

  const fetchFlowCount = useCallback(async (params: URLSearchParams) => {
    return apiFetch<{ count: number }>(`/api/flows/count?${params.toString()}`)
  }, [apiFetch])

  return {
    apiFetch,
    fetchFlows,
    fetchStats,
    fetchTasks,
    fetchTools,
    fetchToolInvocations,
    fetchToolInvocationDetail,
    fetchAnomalies,
    fetchDailyCosts,
    fetchSettings,
    updateSettings,
    fetchCaptureStatus,
    setCaptureMode,
    fetchFlowDetail,
    fetchFlowCount,
  }
}
