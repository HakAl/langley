import { useCallback } from 'react'
import type { Anomaly, ApiResult, CostPeriod, Flow, Settings, Stats, TaskSummary, ToolStats } from '../types'

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

  const fetchFlows = useCallback(async () => {
    return apiFetch<Flow[]>('/api/flows?limit=50')
  }, [apiFetch])

  const fetchStats = useCallback(async () => {
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    return apiFetch<Stats>(`/api/stats?${params}`)
  }, [apiFetch])

  const fetchTasks = useCallback(async () => {
    return apiFetch<TaskSummary[]>('/api/analytics/tasks')
  }, [apiFetch])

  const fetchTools = useCallback(async (days?: number) => {
    const end = new Date()
    const start = days != null
      ? new Date(end.getTime() - days * 24 * 60 * 60 * 1000)
      : new Date(0)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    return apiFetch<ToolStats[]>(`/api/analytics/tools?${params}`)
  }, [apiFetch])

  const fetchAnomalies = useCallback(async () => {
    return apiFetch<Anomaly[]>('/api/analytics/anomalies')
  }, [apiFetch])

  const fetchDailyCosts = useCallback(async () => {
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    return apiFetch<CostPeriod[]>(`/api/analytics/cost/daily?${params}`)
  }, [apiFetch])

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
    fetchAnomalies,
    fetchDailyCosts,
    fetchSettings,
    updateSettings,
    fetchFlowDetail,
    fetchFlowCount,
  }
}
