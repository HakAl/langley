import { useCallback } from 'react'
import type { Settings } from '../types'

export function useApi() {
  const apiFetch = useCallback(async (path: string) => {
    try {
      const res = await fetch(path, { credentials: 'include' })
      if (res.ok) return res.json()
      return null
    } catch {
      return null
    }
  }, [])

  const fetchFlows = useCallback(async () => {
    return apiFetch('/api/flows?limit=50')
  }, [apiFetch])

  const fetchStats = useCallback(async () => {
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    return apiFetch(`/api/stats?${params}`)
  }, [apiFetch])

  const fetchTasks = useCallback(async () => {
    return apiFetch('/api/analytics/tasks')
  }, [apiFetch])

  const fetchTools = useCallback(async () => {
    return apiFetch('/api/analytics/tools')
  }, [apiFetch])

  const fetchAnomalies = useCallback(async () => {
    return apiFetch('/api/analytics/anomalies')
  }, [apiFetch])

  const fetchDailyCosts = useCallback(async () => {
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    return apiFetch(`/api/analytics/cost/daily?${params}`)
  }, [apiFetch])

  const fetchSettings = useCallback(async () => {
    return apiFetch('/api/settings')
  }, [apiFetch])

  const updateSettings = useCallback(async (newSettings: Partial<Settings>): Promise<Settings | null> => {
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(newSettings)
      })
      if (res.ok) return res.json()
      return null
    } catch {
      return null
    }
  }, [])

  const fetchFlowDetail = useCallback(async (id: string) => {
    return apiFetch(`/api/flows/${id}`)
  }, [apiFetch])

  const fetchFlowCount = useCallback(async (params: URLSearchParams) => {
    return apiFetch(`/api/flows/count?${params.toString()}`)
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
