import { useState, useEffect, useCallback, useRef } from 'react'
import type { Flow, Stats, TaskSummary, ToolStats, ToolInvocation, Anomaly, CostPeriod, Settings, ApiResult } from './types'
import { mergeFlows } from './mergeFlows'
import { getInitialTheme } from './utils'
import { useHashRoute } from './hooks/useHashRoute'
import { useApi } from './hooks/useApi'
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

function App() {
  const [theme, setTheme] = useState<'dark' | 'light'>(getInitialTheme)
  const [flows, setFlows] = useState<Flow[]>([])
  const [totalFlows, setTotalFlows] = useState(0)
  const [page, setPage] = useState(0)
  const [selectedFlow, setSelectedFlow] = useState<Flow | null>(null)
  const [connected, setConnected] = useState(false)
  const [initialLoading, setInitialLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [viewError, setViewError] = useState<string | null>(null)
  const [view, navigateTo] = useHashRoute()
  const [stats, setStats] = useState<Stats | null>(null)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const [tools, setTools] = useState<ToolStats[]>([])
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])
  const [dailyCosts, setDailyCosts] = useState<CostPeriod[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const [viewLoading, setViewLoading] = useState(false)
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [showHelp, setShowHelp] = useState(false)
  const [exportConfig, setExportConfig] = useState<{ format: string; rowCount: number } | null>(null)
  const [hostFilter, setHostFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'success' | 'error'>('all')
  const [timeRange, setTimeRange] = useState<number | null>(30)
  // Tool drill-down state
  const [selectedTool, setSelectedTool] = useState<string | null>(null)
  const [toolInvocations, setToolInvocations] = useState<ToolInvocation[]>([])
  const [toolInvocationsTotal, setToolInvocationsTotal] = useState(0)
  const [toolInvocationsLoading, setToolInvocationsLoading] = useState(false)
  const [selectedToolInvocation, setSelectedToolInvocation] = useState<ToolInvocation | null>(null)
  const [invocationSelectedIndex, setInvocationSelectedIndex] = useState(0)
  const hostFilterRef = useRef<HTMLInputElement>(null)
  const pageRef = useRef(page)
  pageRef.current = page

  const api = useApi()

  // Build fetch params from current filter/page state
  const buildFetchParams = useCallback((pageOverride?: number): FlowFetchParams => ({
    days: timeRange ?? undefined,
    limit: PAGE_SIZE,
    offset: (pageOverride ?? page) * PAGE_SIZE,
    host: hostFilter || undefined,
    taskId: taskFilter || undefined,
    status: statusFilter !== 'all' ? statusFilter : undefined,
  }), [timeRange, page, hostFilter, taskFilter, statusFilter])

  const fetchFlowPage = useCallback(async (opts?: { page?: number; merge?: boolean }) => {
    const params = buildFetchParams(opts?.page)
    const { data, error } = await api.fetchFlows(params)
    if (data) {
      if (opts?.merge && (opts?.page ?? pageRef.current) === 0) {
        setFlows(prev => mergeFlows(prev, data.flows))
      } else {
        setFlows(data.flows)
      }
      setTotalFlows(data.total)
    }
    return { data, error }
  }, [api.fetchFlows, buildFetchParams])

  const handleFlowUpdate = useCallback((flow: Flow) => {
    // Only apply WS updates on page 0
    if (pageRef.current !== 0) {
      // Just bump total count for new flows
      setTotalFlows(prev => {
        // If this is a new flow (not an update), increment
        setFlows(currentFlows => {
          if (!currentFlows.find(f => f.id === flow.id)) {
            return currentFlows // don't modify the list on non-zero pages
          }
          // Update in place if it exists on current page
          return currentFlows.map(f => f.id === flow.id ? flow : f)
        })
        return prev
      })
      return
    }
    setFlows(prev => {
      const idx = prev.findIndex(f => f.id === flow.id)
      if (idx >= 0) { const u = [...prev]; u[idx] = flow; return u }
      return [flow, ...prev]
    })
    // New flow on page 0 means total went up
    setTotalFlows(prev => prev + 1)
  }, [])

  const handleReconnect = useCallback(() => {
    fetchFlowPage({ merge: true })
  }, [fetchFlowPage])

  const { connect, cleanup } = useWebSocket({
    onFlowUpdate: handleFlowUpdate,
    onConnectedChange: setConnected,
    onError: setError,
    onReconnect: handleReconnect,
  })

  // Reset page to 0 on filter change
  const handleHostFilterChange = useCallback((v: string) => { setHostFilter(v); setPage(0) }, [])
  const handleTaskFilterChange = useCallback((v: string) => { setTaskFilter(v); setPage(0) }, [])
  const handleStatusFilterChange = useCallback((v: 'all' | 'success' | 'error') => { setStatusFilter(v); setPage(0) }, [])
  const handleTimeRangeChange = useCallback((days: number | null) => { setTimeRange(days); setPage(0) }, [])
  const handlePageChange = useCallback((newPage: number) => { setPage(newPage); setSelectedIndex(0) }, [])

  // Initial load
  useEffect(() => {
    fetchFlowPage({ page: 0, merge: true }).then(({ error }) => { if (error) setViewError(error); setInitialLoading(false); connect() })
    api.fetchStats(timeRange ?? undefined).then(({ data }) => { if (data) setStats(data) })
    return cleanup
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Re-fetch flows when filters or page change
  useEffect(() => {
    if (view !== 'flows') return
    setViewLoading(true)
    fetchFlowPage().then(({ error }) => { if (error) setViewError(error) }).finally(() => setViewLoading(false))
  }, [page, hostFilter, taskFilter, statusFilter, timeRange, view]) // eslint-disable-line react-hooks/exhaustive-deps

  // View-specific data (non-flow views)
  useEffect(() => {
    setViewError(null)
    setViewLoading(true)
    const days = timeRange ?? undefined
    const showError = (error: string | null) => { if (error) setViewError(error) }
    const done = () => setViewLoading(false)
    if (view === 'flows') {
      // Handled by the filter/page effect above
      done()
    } else if (view === 'analytics') {
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

  // Reset selection on data change
  useEffect(() => { setSelectedIndex(0) }, [view, flows.length, tasks.length, tools.length, anomalies.length])

  const handleFlowSelect = useCallback(async (id: string) => {
    const { data } = await api.fetchFlowDetail(id)
    if (data) setSelectedFlow(data)
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
    items: { flows, tasks, tools: effectiveToolItems, anomalies },
    setSelectedIndex: effectiveSetSelectedIndex,
    setShowHelp,
    clearSelectedFlow: () => {
      if (selectedToolInvocation) {
        setSelectedToolInvocation(null)
      } else if (selectedTool && view === 'tools') {
        handleToolBack()
      } else {
        setSelectedFlow(null)
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
    const { data: countData } = await api.fetchFlowCount(params)
    setExportConfig({ format, rowCount: countData?.count ?? flows.length })
  }, [api.fetchFlowCount, hostFilter, taskFilter, statusFilter, flows.length])

  const handleConfirmExport = useCallback(async () => {
    if (!exportConfig) return
    const params = new URLSearchParams({ format: exportConfig.format })
    if (hostFilter) params.set('host', hostFilter)
    if (taskFilter) params.set('task_id', taskFilter)
    if (statusFilter !== 'all') params.set('status', statusFilter)
    try {
      const res = await fetch(`/api/flows/export?${params}`, { credentials: 'include' })
      if (!res.ok) throw new Error('Export failed')
      const match = res.headers.get('Content-Disposition')?.match(/filename="(.+)"/)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = Object.assign(document.createElement('a'), { href: url, download: match?.[1] || `flows.${exportConfig.format}` })
      document.body.appendChild(a); a.click(); document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (err) { console.error('Export failed:', err) }
    finally { setExportConfig(null) }
  }, [exportConfig, hostFilter, taskFilter, statusFilter])

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
        <Nav view={view} anomalyCount={anomalies.length} onNavigate={navigateTo} />
        <div className="header-right">
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
        <div className="main-content">
          <div className="content-area">
            {viewError && (
              <div className="error-banner" role="alert">
                <span className="error-banner-message">{viewError}</span>
                <button className="error-banner-dismiss" onClick={() => setViewError(null)} aria-label="Dismiss error">&times;</button>
              </div>
            )}
            {view === 'flows' && <FlowListView flows={flows} totalFlows={totalFlows} initialLoading={initialLoading} selectedFlowId={selectedFlow?.id ?? null} selectedIndex={selectedIndex} hostFilter={hostFilter} taskFilter={taskFilter} statusFilter={statusFilter} timeRange={timeRange} page={page} totalPages={totalPages} onHostFilterChange={handleHostFilterChange} onTaskFilterChange={handleTaskFilterChange} onStatusFilterChange={handleStatusFilterChange} onTimeRangeChange={handleTimeRangeChange} onPageChange={handlePageChange} onFlowSelect={handleFlowSelect} onStartExport={handleStartExport} hostFilterRef={hostFilterRef} />}
            {view === 'analytics' && <AnalyticsView stats={stats} dailyCosts={dailyCosts} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} loading={viewLoading} />}
            {view === 'tasks' && <TasksView tasks={tasks} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onTaskSelect={(id) => { setTaskFilter(id); setPage(0); navigateTo('flows') }} loading={viewLoading} />}
            {view === 'tools' && <ToolsView tools={tools} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onToolSelect={handleToolSelect} loading={viewLoading} selectedTool={selectedTool} invocations={toolInvocations} invocationsTotal={toolInvocationsTotal} invocationsLoading={toolInvocationsLoading} invocationSelectedIndex={invocationSelectedIndex} onBack={handleToolBack} onInvocationSelect={handleInvocationSelect} />}
            {view === 'anomalies' && <AnomaliesView anomalies={anomalies} selectedIndex={selectedIndex} timeRange={timeRange} onTimeRangeChange={handleTimeRangeChange} onViewFlow={async (id) => { await handleFlowSelect(id); navigateTo('flows') }} loading={viewLoading} />}
            {view === 'settings' && <SettingsView settings={settings} onSave={handleSaveSettings} />}
          </div>
          {selectedFlow && view === 'flows' && <>
            <div className="detail-overlay" onClick={() => setSelectedFlow(null)} />
            <FlowDetail flow={selectedFlow} onClose={() => setSelectedFlow(null)} />
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
