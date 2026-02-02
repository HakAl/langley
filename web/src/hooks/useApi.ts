import { useCallback } from 'react'
import type { Anomaly, ApiResult, CostPeriod, Flow, Settings, Stats, TaskSummary, ToolInvocation, ToolStats } from '../types'

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

  const timeParams = useCallback((days?: number) => {
    const end = new Date()
    const start = days != null
      ? new Date(end.getTime() - days * 24 * 60 * 60 * 1000)
      : new Date(0)
    return new URLSearchParams({ start: start.toISOString(), end: end.toISOString() })
  }, [])

  const fetchFlows = useCallback(async (days?: number) => {
    const params = new URLSearchParams({ limit: '50' })
    if (days != null) {
      const end = new Date()
      const start = new Date(end.getTime() - days * 24 * 60 * 60 * 1000)
      params.set('start_time', start.toISOString())
      params.set('end_time', end.toISOString())
    }
    return apiFetch<Flow[]>(`/api/flows?${params}`)
  }, [apiFetch])

  const fetchStats = useCallback(async (days?: number) => {
    return apiFetch<Stats>(`/api/stats?${timeParams(days ?? 30)}`)
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
    return apiFetch<CostPeriod[]>(`/api/analytics/cost/daily?${timeParams(days ?? 30)}`)
  }, [apiFetch, timeParams])

  const fetchSettings = useCallback(async () => {
    return apiFetch<Settings>('/api/settings')
  }, [apiFetch])

  const updateSettings = useCallback(async (newSettings: Partial<Settings>): Promise<ApiResult<Settings>> => {
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(newSettings)
      })
      if (res.ok) return { data: await res.json(), error: null }
      const text = await res.text().catch(() => '')
      return { data: null, error: text || `HTTP ${res.status}` }
    } catch (err) {
      return { data: null, error: err instanceof Error ? err.message : 'Network error' }
    }
  }, [])

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
    fetchFlowDetail,
    fetchFlowCount,
  }
}
