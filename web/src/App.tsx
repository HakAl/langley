import { useState, useEffect, useCallback, useRef } from 'react'

// Types
interface Flow {
  id: string
  host: string
  method: string
  path: string
  url?: string
  status_code?: number
  status_text?: string
  is_sse: boolean
  timestamp: string
  duration_ms?: number
  task_id?: string
  task_source?: string
  model?: string
  input_tokens?: number
  output_tokens?: number
  cache_creation_tokens?: number
  cache_read_tokens?: number
  total_cost?: number
  cost_source?: string
  provider?: string
  flow_integrity?: string
  events_dropped_count?: number
  request_body?: string
  response_body?: string
  request_headers?: Record<string, string[]>
  response_headers?: Record<string, string[]>
}

interface WSMessage {
  type: string
  timestamp: string
  data: Flow
}

interface Stats {
  status: string
  total_flows: number
  all_time_flows: number
  total_cost: number
  total_tokens_in: number
  total_tokens_out: number
  total_tasks: number
  total_tool_calls: number
  avg_cost_per_flow: number
}

interface TaskSummary {
  task_id: string
  flow_count: number
  total_tokens_in: number
  total_tokens_out: number
  total_cost: number
  first_seen: string
  last_seen: string
  duration_ms: number
}

interface ToolStats {
  tool_name: string
  invocation_count: number
  success_count: number
  failure_count: number
  success_rate: number
  total_cost: number
  avg_duration_ms: number
}

interface Anomaly {
  type: string
  flow_id: string
  task_id?: string
  timestamp: string
  severity: string
  description: string
  value: number
  threshold: number
}

interface CostPeriod {
  period: string
  flow_count: number
  total_cost: number
  total_tokens_in: number
  total_tokens_out: number
}

type View = 'flows' | 'analytics' | 'tasks' | 'tools' | 'anomalies' | 'settings'

const VALID_VIEWS: View[] = ['flows', 'analytics', 'tasks', 'tools', 'anomalies', 'settings']
const DEFAULT_VIEW: View = 'flows'

function parseHash(hash: string): View {
  const raw = hash.replace(/^#\/?/, '')
  return VALID_VIEWS.includes(raw as View) ? (raw as View) : DEFAULT_VIEW
}

function useHashRoute(): [View, (v: View) => void] {
  const [view, setView] = useState<View>(() => parseHash(window.location.hash))

  const navigateTo = useCallback((v: View) => {
    setView(v)
    window.history.pushState(null, '', `#${v}`)
  }, [])

  useEffect(() => {
    const onPopState = () => setView(parseHash(window.location.hash))
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  return [view, navigateTo]
}

interface Settings {
  idle_gap_minutes: number
}

// Theme utilities
const getInitialTheme = (): 'dark' | 'light' => {
  const stored = localStorage.getItem('langley_theme')
  if (stored === 'dark' || stored === 'light') return stored
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

function App() {
  const [theme, setTheme] = useState<'dark' | 'light'>(getInitialTheme)
  const [flows, setFlows] = useState<Flow[]>([])
  const [selectedFlow, setSelectedFlow] = useState<Flow | null>(null)
  const [connected, setConnected] = useState(false)
  const [initialLoading, setInitialLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [view, navigateTo] = useHashRoute()
  const [stats, setStats] = useState<Stats | null>(null)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const [tools, setTools] = useState<ToolStats[]>([])
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])
  const [dailyCosts, setDailyCosts] = useState<CostPeriod[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const [settingsSaving, setSettingsSaving] = useState(false)
  const [settingsSaved, setSettingsSaved] = useState(false)
  const [idleGapInput, setIdleGapInput] = useState(5)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)
  const reconnectAttemptsRef = useRef(0)
  const MAX_RECONNECT_ATTEMPTS = 5

  // Keyboard navigation
  const [selectedIndex, setSelectedIndex] = useState(0)
  const [showHelp, setShowHelp] = useState(false)
  const [exportConfig, setExportConfig] = useState<{format: string, rowCount: number} | null>(null)
  const hostFilterRef = useRef<HTMLInputElement>(null)
  const helpCloseRef = useRef<HTMLButtonElement>(null)

  // Filters
  const [hostFilter, setHostFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'success' | 'error'>('all')

  // API helper - uses HTTP-only cookie for auth (auto-set by server)
  const apiFetch = useCallback(async (path: string) => {
    try {
      const res = await fetch(path, {
        credentials: 'include' // Send cookies
      })
      if (res.ok) return res.json()
      if (res.status === 401 || res.status === 403) {
        setError('Authentication failed - ensure you are accessing from localhost')
      }
      return null
    } catch {
      return null
    }
  }, [])

  // WebSocket connection - uses HTTP-only cookie for auth (browser sends automatically)
  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    // Clear any pending reconnect timeout
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
      reconnectTimeoutRef.current = null
    }

    // Browser automatically sends cookies for same-origin WebSocket connections
    const ws = new WebSocket(`ws://${window.location.host}/ws`)

    ws.onopen = () => {
      setConnected(true)
      setError(null)
      reconnectAttemptsRef.current = 0 // Reset on successful connection
    }

    ws.onclose = () => {
      setConnected(false)
      wsRef.current = null

      // Exponential backoff with max attempts
      if (reconnectAttemptsRef.current < MAX_RECONNECT_ATTEMPTS) {
        const delay = Math.min(1000 * Math.pow(2, reconnectAttemptsRef.current), 30000)
        reconnectAttemptsRef.current++
        reconnectTimeoutRef.current = window.setTimeout(() => connect(), delay)
      } else {
        setError('Connection lost. Refresh to reconnect.')
      }
    }

    ws.onerror = () => {
      setError('WebSocket connection failed')
      setConnected(false)
    }

    ws.onmessage = (event) => {
      try {
        const lines = event.data.split('\n')
        for (const line of lines) {
          if (!line.trim()) continue
          const msg: WSMessage = JSON.parse(line)
          if (msg.type === 'flow_start' || msg.type === 'flow_complete' || msg.type === 'flow_update') {
            setFlows(prev => {
              const existing = prev.findIndex(f => f.id === msg.data.id)
              if (existing >= 0) {
                const updated = [...prev]
                updated[existing] = msg.data
                return updated
              }
              return [msg.data, ...prev].slice(0, 100)
            })
          }
        }
      } catch (e) {
        console.error('Failed to parse message:', e)
      }
    }

    wsRef.current = ws
  }, [])

  // Fetch data
  const fetchFlows = useCallback(async () => {
    const data = await apiFetch('/api/flows?limit=50')
    if (data) setFlows(data)
  }, [apiFetch])

  const fetchStats = useCallback(async () => {
    // Fetch stats for last 30 days to match daily cost chart
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    const data = await apiFetch(`/api/stats?${params}`)
    if (data) setStats(data)
  }, [apiFetch])

  const fetchTasks = useCallback(async () => {
    const data = await apiFetch('/api/analytics/tasks')
    if (data) setTasks(data)
  }, [apiFetch])

  const fetchTools = useCallback(async () => {
    const data = await apiFetch('/api/analytics/tools')
    if (data) setTools(data)
  }, [apiFetch])

  const fetchAnomalies = useCallback(async () => {
    const data = await apiFetch('/api/analytics/anomalies')
    if (data) setAnomalies(data)
  }, [apiFetch])

  const fetchDailyCosts = useCallback(async () => {
    // Fetch last 30 days of cost data
    const end = new Date()
    const start = new Date(end.getTime() - 30 * 24 * 60 * 60 * 1000)
    const params = new URLSearchParams({
      start: start.toISOString(),
      end: end.toISOString()
    })
    const data = await apiFetch(`/api/analytics/cost/daily?${params}`)
    if (data) setDailyCosts(data)
  }, [apiFetch])

  const fetchSettings = useCallback(async () => {
    const data = await apiFetch('/api/settings')
    if (data) setSettings(data)
  }, [apiFetch])

  const updateSettings = useCallback(async (newSettings: Partial<Settings>) => {
    setSettingsSaving(true)
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify(newSettings)
      })
      if (res.ok) {
        const data = await res.json()
        setSettings(data)
        return true
      }
      return false
    } catch {
      return false
    } finally {
      setSettingsSaving(false)
    }
  }, [])

  const fetchFlowDetail = useCallback(async (id: string) => {
    const data = await apiFetch(`/api/flows/${id}`)
    if (data) setSelectedFlow(data)
  }, [apiFetch])

  // startExport is defined after filteredFlows below

  const confirmExport = useCallback(async () => {
    if (!exportConfig) return
    const { format } = exportConfig

    // Build export URL with current filters
    const params = new URLSearchParams()
    params.set('format', format)
    if (hostFilter) params.set('host', hostFilter)
    if (taskFilter) params.set('task_id', taskFilter)

    try {
      const response = await fetch(`/api/flows/export?${params.toString()}`, {
        credentials: 'include'
      })
      if (!response.ok) throw new Error('Export failed')

      // Get filename from Content-Disposition header or generate one
      const disposition = response.headers.get('Content-Disposition')
      const filenameMatch = disposition?.match(/filename="(.+)"/)
      const filename = filenameMatch?.[1] || `flows.${format}`

      // Create blob and trigger download
      const blob = await response.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('Export failed:', err)
    } finally {
      setExportConfig(null)
    }
  }, [exportConfig, hostFilter, taskFilter])

  // Initial load - auto-connect on mount
  // Must await an API call first to set the auth cookie before WebSocket connects
  useEffect(() => {
    fetchFlows().then(() => { setInitialLoading(false); connect() })
    fetchStats()
    return () => {
      // Clear pending reconnect timeout
      if (reconnectTimeoutRef.current) {
        clearTimeout(reconnectTimeoutRef.current)
        reconnectTimeoutRef.current = null
      }
      wsRef.current?.close()
    }
  }, [fetchFlows, fetchStats, connect])

  // Load view-specific data
  useEffect(() => {
    if (view === 'analytics') {
      fetchStats()
      fetchDailyCosts()
    } else if (view === 'tasks') {
      fetchTasks()
    } else if (view === 'tools') {
      fetchTools()
    } else if (view === 'anomalies') {
      fetchAnomalies()
    } else if (view === 'settings') {
      fetchSettings()
    }
  }, [view, fetchStats, fetchDailyCosts, fetchTasks, fetchTools, fetchAnomalies, fetchSettings])

  // Update idle gap input when settings load
  useEffect(() => {
    if (settings) setIdleGapInput(settings.idle_gap_minutes)
  }, [settings])

  // Apply theme to document
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('langley_theme', theme)
  }, [theme])

  const toggleTheme = () => setTheme(t => t === 'dark' ? 'light' : 'dark')

  // Settings validation
  const isIdleGapValid = idleGapInput >= 1 && idleGapInput <= 60

  // Filter flows
  const filteredFlows = flows.filter(flow => {
    if (hostFilter && !flow.host.toLowerCase().includes(hostFilter.toLowerCase())) return false
    if (taskFilter && flow.task_id !== taskFilter) return false
    if (statusFilter === 'success' && flow.status_code && flow.status_code >= 400) return false
    if (statusFilter === 'error' && flow.status_code && flow.status_code < 400) return false
    return true
  })

  // Export handlers (after filteredFlows is defined)
  const startExport = useCallback(async (format: string) => {
    // Fetch actual count from server (client-side list is capped at 50)
    const params = new URLSearchParams()
    if (hostFilter) params.set('host', hostFilter)
    if (taskFilter) params.set('task_id', taskFilter)

    const countData = await apiFetch(`/api/flows/count?${params.toString()}`)
    const rowCount = countData?.count ?? filteredFlows.length

    setExportConfig({ format, rowCount })
  }, [apiFetch, hostFilter, taskFilter, filteredFlows.length])

  // Get current navigable list based on view
  const getNavigableItems = useCallback(() => {
    switch (view) {
      case 'flows': return filteredFlows
      case 'tasks': return tasks
      case 'tools': return tools
      case 'anomalies': return anomalies
      default: return []
    }
  }, [view, filteredFlows, tasks, tools, anomalies])

  // Reset selection when view or filtered items change
  useEffect(() => {
    setSelectedIndex(0)
  }, [view, filteredFlows.length, tasks.length, tools.length, anomalies.length])

  // Keyboard shortcuts
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Don't capture when typing in inputs
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        // Escape blurs inputs
        if (e.key === 'Escape') {
          (e.target as HTMLElement).blur()
        }
        return
      }

      const items = getNavigableItems()

      switch (e.key) {
        case 'j':
          if (items.length > 0) {
            setSelectedIndex(i => Math.min(i + 1, items.length - 1))
          }
          break
        case 'k':
          if (items.length > 0) {
            setSelectedIndex(i => Math.max(i - 1, 0))
          }
          break
        case 'Enter':
          if (items.length > 0 && selectedIndex < items.length) {
            const item = items[selectedIndex]
            if (view === 'flows' && 'id' in item) {
              fetchFlowDetail((item as Flow).id)
            } else if (view === 'tasks' && 'task_id' in item) {
              setTaskFilter((item as TaskSummary).task_id)
              navigateTo('flows')
            } else if (view === 'anomalies' && 'flow_id' in item) {
              const anomaly = item as Anomaly
              if (anomaly.flow_id) {
                fetchFlowDetail(anomaly.flow_id)
                navigateTo('flows')
              }
            }
          }
          break
        case '/':
          if (view === 'flows') {
            e.preventDefault() // Prevent Firefox quick-find
            hostFilterRef.current?.focus()
          }
          break
        case 'Escape':
          if (showHelp) {
            setShowHelp(false)
          } else if (selectedFlow) {
            setSelectedFlow(null)
          }
          break
        case '?':
          setShowHelp(h => !h)
          break
        case '1':
          navigateTo('flows')
          break
        case '2':
          navigateTo('analytics')
          break
        case '3':
          navigateTo('tasks')
          break
        case '4':
          navigateTo('tools')
          break
        case '5':
          navigateTo('anomalies')
          break
        case '6':
          navigateTo('settings')
          break
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [view, selectedIndex, selectedFlow, showHelp, getNavigableItems, fetchFlowDetail])

  // Focus trap for help overlay
  useEffect(() => {
    if (!showHelp) return
    helpCloseRef.current?.focus()
    const modal = helpCloseRef.current?.closest('.help-modal') as HTMLElement | null
    if (!modal) return
    const trap = (e: KeyboardEvent) => {
      if (e.key !== 'Tab') return
      const focusable = modal.querySelectorAll<HTMLElement>('button, [href], [tabindex]:not([tabindex="-1"])')
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', trap)
    return () => document.removeEventListener('keydown', trap)
  }, [showHelp])

  const formatTime = (timestamp: string) => new Date(timestamp).toLocaleTimeString()
  const formatDate = (timestamp: string) => new Date(timestamp).toLocaleDateString()
  const formatCost = (cost?: number) => cost != null ? `$${cost.toFixed(4)}` : null
  const formatDuration = (ms?: number) => ms != null ? `${(ms / 1000).toFixed(2)}s` : null

  const getStatusClass = (code?: number) => {
    if (!code) return ''
    if (code < 300) return 'success'
    if (code < 400) return 'redirect'
    return 'error'
  }

  const getSeverityClass = (severity: string) => {
    switch (severity) {
      case 'critical': return 'error'
      case 'warning': return 'warning'
      default: return 'info'
    }
  }

  // Render navigation
  const renderNav = () => (
    <nav className="nav">
      <button className={view === 'flows' ? 'active' : ''} aria-current={view === 'flows' ? 'page' : undefined} onClick={() => navigateTo('flows')}>Flows</button>
      <button className={view === 'analytics' ? 'active' : ''} aria-current={view === 'analytics' ? 'page' : undefined} onClick={() => navigateTo('analytics')}>Analytics</button>
      <button className={view === 'tasks' ? 'active' : ''} aria-current={view === 'tasks' ? 'page' : undefined} onClick={() => navigateTo('tasks')}>Tasks</button>
      <button className={view === 'tools' ? 'active' : ''} aria-current={view === 'tools' ? 'page' : undefined} onClick={() => navigateTo('tools')}>Tools</button>
      <button className={view === 'anomalies' ? 'active' : ''} aria-current={view === 'anomalies' ? 'page' : undefined} onClick={() => navigateTo('anomalies')}>
        Anomalies {anomalies.length > 0 && <span className="badge error">{anomalies.length}</span>}
      </button>
      <button className={view === 'settings' ? 'active' : ''} aria-current={view === 'settings' ? 'page' : undefined} onClick={() => navigateTo('settings')}>
        Settings
      </button>
    </nav>
  )

  // Render flows view
  const renderFlows = () => (
    <div className="flows-view">
      <div className="filters">
        <div className="filter-input-wrapper">
          <input
            ref={hostFilterRef}
            type="text"
            placeholder="Filter by host..."
            value={hostFilter}
            onChange={(e) => setHostFilter(e.target.value)}
          />
          {hostFilter && (
            <button className="filter-clear-btn" onClick={() => setHostFilter('')} aria-label="Clear host filter">&times;</button>
          )}
        </div>
        <div className="filter-input-wrapper">
          <input
            type="text"
            placeholder="Filter by task ID..."
            value={taskFilter}
            onChange={(e) => setTaskFilter(e.target.value)}
          />
          {taskFilter && (
            <button className="filter-clear-btn" onClick={() => setTaskFilter('')} aria-label="Clear task ID filter">&times;</button>
          )}
        </div>
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as 'all' | 'success' | 'error')}>
          <option value="all">All Status</option>
          <option value="success">Success (2xx/3xx)</option>
          <option value="error">Errors (4xx/5xx)</option>
        </select>
        <div className="export-dropdown">
          <select
            onChange={(e) => {
              if (e.target.value) {
                startExport(e.target.value)
                e.target.value = '' // Reset after selection
              }
            }}
            defaultValue=""
          >
            <option value="" disabled>Export...</option>
            <option value="ndjson">NDJSON</option>
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
        </div>
      </div>

      {stats && stats.total_flows > filteredFlows.length && (
        <div className="flow-count">Showing {filteredFlows.length} of {stats.total_flows.toLocaleString()} flows</div>
      )}

      <div className="flow-list" role="listbox" aria-label="Flow list" aria-activedescendant={filteredFlows.length > 0 ? `flow-${selectedIndex}` : undefined}>
        {filteredFlows.length === 0 ? (
          <div className="empty-state">
            {initialLoading ? (
              <p>Loading flows...</p>
            ) : (
              <>
                <h2>No flows captured yet</h2>
                <p>Configure your client to use the proxy and traffic will appear here.</p>
              </>
            )}
          </div>
        ) : (
          filteredFlows.map((flow, index) => (
            <div
              key={flow.id}
              id={`flow-${index}`}
              role="option"
              aria-selected={index === selectedIndex}
              tabIndex={index === selectedIndex ? 0 : -1}
              className={`flow-item ${selectedFlow?.id === flow.id ? 'selected' : ''} ${index === selectedIndex ? 'keyboard-selected' : ''}`}
              onClick={() => fetchFlowDetail(flow.id)}
              ref={el => {
                if (index === selectedIndex && el) {
                  el.scrollIntoView({ block: 'nearest' })
                }
              }}
            >
              <span className={`method ${flow.method}`}>{flow.method}</span>
              <div className="host-path">
                <span className="host">
                  {flow.host}
                  {flow.is_sse && <span className="badge sse">SSE</span>}
                </span>
                <span className="path">{flow.path}</span>
              </div>
              <span className={`status-code ${getStatusClass(flow.status_code)}`}>
                {flow.status_code || '...'}
              </span>
              <div className="tokens">
                {flow.input_tokens != null && (
                  <span className="token-count">
                    <span style={{ color: 'var(--accent)' }}>↓</span>
                    {flow.input_tokens.toLocaleString()}
                  </span>
                )}
                {flow.output_tokens != null && (
                  <span className="token-count">
                    <span style={{ color: 'var(--success)' }}>↑</span>
                    {flow.output_tokens.toLocaleString()}
                  </span>
                )}
                {flow.total_cost != null && (
                  <span className="cost">{formatCost(flow.total_cost)}</span>
                )}
              </div>
              <span className="timestamp">{formatTime(flow.timestamp)}</span>
            </div>
          ))
        )}
      </div>
    </div>
  )

  // Render flow detail
  const renderFlowDetail = () => {
    if (!selectedFlow) return null

    return (
      <div className="flow-detail">
        <div className="detail-header">
          <h2>{selectedFlow.method} {selectedFlow.path}</h2>
          <button className="close-btn" onClick={() => setSelectedFlow(null)}>×</button>
        </div>

        <div className="detail-grid">
          <div className="detail-section">
            <h3>Request</h3>
            <div className="detail-row"><strong>URL:</strong> {selectedFlow.url}</div>
            <div className="detail-row"><strong>Host:</strong> {selectedFlow.host}</div>
            <div className="detail-row"><strong>Time:</strong> {formatDate(selectedFlow.timestamp)} {formatTime(selectedFlow.timestamp)}</div>
            {selectedFlow.task_id && (
              <div className="detail-row"><strong>Task:</strong> {selectedFlow.task_id} ({selectedFlow.task_source})</div>
            )}
            {selectedFlow.request_headers && (
              <div className="headers">
                <h4>Headers</h4>
                {Object.entries(selectedFlow.request_headers).map(([k, v]) => (
                  <div key={k} className="header-row">{k}: {v.join(', ')}</div>
                ))}
              </div>
            )}
          </div>

          <div className="detail-section">
            <h3>Response</h3>
            <div className="detail-row"><strong>Status:</strong> <span className={getStatusClass(selectedFlow.status_code)}>{selectedFlow.status_code} {selectedFlow.status_text}</span></div>
            <div className="detail-row"><strong>Duration:</strong> {formatDuration(selectedFlow.duration_ms)}</div>
            <div className="detail-row"><strong>Provider:</strong> {selectedFlow.provider}</div>
            {selectedFlow.model && <div className="detail-row"><strong>Model:</strong> {selectedFlow.model}</div>}
            {selectedFlow.flow_integrity !== 'complete' && (
              <div className="detail-row warning"><strong>Integrity:</strong> {selectedFlow.flow_integrity}</div>
            )}
          </div>

          <div className="detail-section">
            <h3>Usage</h3>
            {selectedFlow.input_tokens != null && <div className="detail-row"><strong>Input:</strong> {selectedFlow.input_tokens.toLocaleString()} tokens</div>}
            {selectedFlow.output_tokens != null && <div className="detail-row"><strong>Output:</strong> {selectedFlow.output_tokens.toLocaleString()} tokens</div>}
            {selectedFlow.cache_creation_tokens != null && selectedFlow.cache_creation_tokens > 0 && (
              <div className="detail-row"><strong>Cache Created:</strong> {selectedFlow.cache_creation_tokens.toLocaleString()} tokens</div>
            )}
            {selectedFlow.cache_read_tokens != null && selectedFlow.cache_read_tokens > 0 && (
              <div className="detail-row"><strong>Cache Read:</strong> {selectedFlow.cache_read_tokens.toLocaleString()} tokens</div>
            )}
            {selectedFlow.total_cost != null && (
              <div className="detail-row cost-row"><strong>Cost:</strong> {formatCost(selectedFlow.total_cost)} ({selectedFlow.cost_source})</div>
            )}
          </div>
        </div>

        {selectedFlow.response_body && (
          <div className="body-section">
            <h3>Response Body</h3>
            <pre className="body-content">{selectedFlow.response_body}</pre>
          </div>
        )}
      </div>
    )
  }

  // Render analytics view
  const renderAnalytics = () => (
    <div className="analytics-view">
      {stats && (
        <div className="stats-grid">
          <div className="stat-card">
            <div className="stat-value">{stats.total_flows}</div>
            <div className="stat-label">Total Flows</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">${stats.total_cost.toFixed(2)}</div>
            <div className="stat-label">Total Cost</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{(stats.total_tokens_in + stats.total_tokens_out).toLocaleString()}</div>
            <div className="stat-label">Total Tokens</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{stats.total_tasks}</div>
            <div className="stat-label">Tasks</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">{stats.total_tool_calls}</div>
            <div className="stat-label">Tool Calls</div>
          </div>
          <div className="stat-card">
            <div className="stat-value">${stats.avg_cost_per_flow.toFixed(4)}</div>
            <div className="stat-label">Avg Cost/Flow</div>
          </div>
        </div>
      )}

      <div className="chart-section">
        <h3>Daily Cost</h3>
        {dailyCosts.length === 0 || dailyCosts.every(d => d.total_cost === 0) ? (
          <div className="chart-empty">
            <p>No cost data for this period</p>
            <p className="chart-empty-hint">Cost tracking requires LLM API traffic with token usage</p>
          </div>
        ) : (
          <div className="chart-bars">
            {dailyCosts.map((day, i) => {
              const maxCost = Math.max(...dailyCosts.map(d => d.total_cost), 0.01)
              const height = (day.total_cost / maxCost) * 100
              return (
                <div key={i} className="bar-container">
                  <span className="bar-value">${day.total_cost.toFixed(2)}</span>
                  <div className="bar" style={{ height: `${height}%` }}></div>
                  <span className="bar-label">{day.period.slice(5)}</span>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )

  // Render tasks view
  const renderTasks = () => (
    <div className="tasks-view">
      {tasks.length === 0 ? (
        <div className="empty-state">
          <h2>No tasks tracked yet</h2>
          <p>Tasks appear when API traffic includes task identifiers.</p>
        </div>
      ) : (
        <table className="data-table" aria-label="Task list">
          <thead>
            <tr>
              <th>Task ID</th>
              <th>Flows</th>
              <th>Tokens In</th>
              <th>Tokens Out</th>
              <th>Cost</th>
              <th>Duration</th>
              <th>Last Seen</th>
            </tr>
          </thead>
          <tbody>
            {tasks.map((task, index) => (
              <tr
                key={task.task_id}
                className={index === selectedIndex ? 'keyboard-selected' : ''}
                onClick={() => { setTaskFilter(task.task_id); navigateTo('flows') }}
                ref={el => {
                  if (index === selectedIndex && el) {
                    el.scrollIntoView({ block: 'nearest' })
                  }
                }}
              >
                <td className="task-id">{task.task_id.slice(0, 8)}...</td>
                <td>{task.flow_count}</td>
                <td>{task.total_tokens_in.toLocaleString()}</td>
                <td>{task.total_tokens_out.toLocaleString()}</td>
                <td className="cost">{formatCost(task.total_cost)}</td>
                <td>{formatDuration(task.duration_ms)}</td>
                <td>{formatTime(task.last_seen)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )

  // Render tools view
  const renderTools = () => (
    <div className="tools-view">
      {tools.length === 0 ? (
        <div className="empty-state">
          <h2>No tool invocations tracked yet</h2>
          <p>Tool usage data appears when API traffic includes tool calls.</p>
        </div>
      ) : (
        <table className="data-table" aria-label="Tool list">
          <thead>
            <tr>
              <th>Tool</th>
              <th>Invocations</th>
              <th>Success Rate</th>
              <th>Avg Duration</th>
              <th>Total Cost</th>
            </tr>
          </thead>
          <tbody>
            {tools.map((tool, index) => (
              <tr
                key={tool.tool_name}
                className={index === selectedIndex ? 'keyboard-selected' : ''}
                ref={el => {
                  if (index === selectedIndex && el) {
                    el.scrollIntoView({ block: 'nearest' })
                  }
                }}
              >
                <td className="tool-name">{tool.tool_name}</td>
                <td>{tool.invocation_count}</td>
                <td className={tool.success_rate >= 90 ? 'success' : tool.success_rate >= 70 ? 'warning' : 'error'}>
                  {tool.success_rate.toFixed(1)}%
                </td>
                <td>{tool.avg_duration_ms.toFixed(0)}ms</td>
                <td className="cost">{formatCost(tool.total_cost)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )

  // Render anomalies view
  const renderAnomalies = () => (
    <div className="anomalies-view">
      {anomalies.length === 0 ? (
        <div className="empty-state">
          <h2>No anomalies detected</h2>
          <p>Everything looks normal!</p>
        </div>
      ) : (
        <div className="anomaly-list" role="listbox" aria-label="Anomaly list" aria-activedescendant={`anomaly-${selectedIndex}`}>
          {anomalies.map((anomaly, index) => (
            <div
              key={index}
              id={`anomaly-${index}`}
              role="option"
              aria-selected={index === selectedIndex}
              tabIndex={index === selectedIndex ? 0 : -1}
              className={`anomaly-item ${getSeverityClass(anomaly.severity)} ${index === selectedIndex ? 'keyboard-selected' : ''}`}
              ref={el => {
                if (index === selectedIndex && el) {
                  el.scrollIntoView({ block: 'nearest' })
                }
              }}
            >
              <div className="anomaly-header">
                <span className={`severity-badge ${anomaly.severity}`}>{anomaly.severity}</span>
                <span className="anomaly-type">{anomaly.type.replace(/_/g, ' ')}</span>
                <span className="anomaly-time">{formatTime(anomaly.timestamp)}</span>
              </div>
              <div className="anomaly-desc">{anomaly.description}</div>
              <div className="anomaly-meta">
                Value: {anomaly.value.toFixed(2)} | Threshold: {anomaly.threshold.toFixed(2)}
                {anomaly.flow_id && (
                  <button className="link-btn" onClick={() => { fetchFlowDetail(anomaly.flow_id); navigateTo('flows') }}>
                    View Flow
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )

  // Handle settings save
  const handleSaveSettings = async () => {
    const success = await updateSettings({ idle_gap_minutes: idleGapInput })
    if (success) {
      setSettingsSaved(true)
      setTimeout(() => setSettingsSaved(false), 3000)
    }
  }

  // Render settings view
  const renderSettings = () => (
    <div className="settings-view">
      <h2>Settings</h2>

      <div className="settings-section">
        <h3>Task Grouping</h3>
        <p className="settings-description">
          Langley groups API calls into tasks based on timing. When there's a gap of inactivity,
          a new task is started.
        </p>

        <div className="setting-row">
          <label htmlFor="idle-gap">Idle Gap (minutes)</label>
          <div className="setting-input-group">
            <input
              id="idle-gap"
              type="number"
              min={1}
              max={60}
              value={idleGapInput}
              onChange={(e) => setIdleGapInput(parseInt(e.target.value) || 1)}
            />
            <span className="setting-hint">1-60 minutes</span>
            {!isIdleGapValid && <span className="setting-error" role="alert">Value must be between 1 and 60</span>}
          </div>
          <p className="setting-help">
            Minutes of inactivity before starting a new task. Lower values create more granular tasks.
          </p>
        </div>
      </div>

      <div className="settings-actions">
        <button
          className="primary-btn"
          onClick={handleSaveSettings}
          disabled={settingsSaving || !isIdleGapValid || idleGapInput === settings?.idle_gap_minutes}
        >
          {settingsSaving ? 'Saving...' : 'Save Settings'}
        </button>
        {settingsSaved && <span className="settings-saved" role="status">Settings saved</span>}
        {idleGapInput !== settings?.idle_gap_minutes && (
          <button
            className="secondary-btn"
            onClick={() => setIdleGapInput(settings?.idle_gap_minutes ?? 5)}
          >
            Reset
          </button>
        )}
      </div>

      <div className="settings-info">
        <h3>Note</h3>
        <p>
          Changes to task grouping settings apply to new API calls only. Existing tasks
          are not affected.
        </p>
      </div>
    </div>
  )

  return (
    <div className="app">
      <header>
        <div className="header-brand">
          <img src="/eagle.svg" alt="" className="header-logo" />
          <h1>Langley</h1>
        </div>
        {renderNav()}
        <div className="header-right">
          <button className="theme-toggle" onClick={toggleTheme} title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}>
            {theme === 'dark' ? '☀' : '☽'}
          </button>
          <div className="status">
            <span className={`status-dot ${connected ? 'connected' : ''}`}></span>
            <span>{connected ? 'Connected' : 'Disconnected'}</span>
          </div>
        </div>
      </header>

      <div className="container">
        {error && <div className="error-banner">{error}</div>}

        <div className="main-content">
          <div className="content-area">
            {view === 'flows' && renderFlows()}
            {view === 'analytics' && renderAnalytics()}
            {view === 'tasks' && renderTasks()}
            {view === 'tools' && renderTools()}
            {view === 'anomalies' && renderAnomalies()}
            {view === 'settings' && renderSettings()}
          </div>

          {selectedFlow && view === 'flows' && renderFlowDetail()}
        </div>
      </div>

      {showHelp && (
        <div className="help-overlay" onClick={() => setShowHelp(false)}>
          <div className="help-modal" role="dialog" aria-modal="true" aria-labelledby="help-title" onClick={e => e.stopPropagation()}>
            <div className="help-header">
              <h2 id="help-title">Keyboard Shortcuts</h2>
              <button ref={helpCloseRef} className="close-btn" onClick={() => setShowHelp(false)} aria-label="Close">×</button>
            </div>
            <table className="help-table">
              <tbody>
                <tr><td><kbd>j</kbd> / <kbd>k</kbd></td><td>Navigate down / up</td></tr>
                <tr><td><kbd>Enter</kbd></td><td>Select item</td></tr>
                <tr><td><kbd>/</kbd></td><td>Focus search (flows view)</td></tr>
                <tr><td><kbd>Escape</kbd></td><td>Close panel / blur input</td></tr>
                <tr><td><kbd>1</kbd>-<kbd>6</kbd></td><td>Switch views</td></tr>
                <tr><td><kbd>?</kbd></td><td>Toggle this help</td></tr>
              </tbody>
            </table>
          </div>
        </div>
      )}

      {exportConfig && (
        <div className="help-overlay" onClick={() => setExportConfig(null)}>
          <div className="help-modal export-modal" onClick={e => e.stopPropagation()}>
            <div className="help-header">
              <h2>Export Flows</h2>
              <button className="close-btn" onClick={() => setExportConfig(null)}>×</button>
            </div>
            <div className="export-confirm-body">
              <p>
                Export <strong>{exportConfig.rowCount}</strong> flow{exportConfig.rowCount !== 1 ? 's' : ''} as <strong>{exportConfig.format.toUpperCase()}</strong>
              </p>
              {(exportConfig.format === 'csv' || exportConfig.format === 'json') && exportConfig.rowCount > 10000 && (
                <p className="warning">{exportConfig.format.toUpperCase()} exports are limited to 10,000 rows.</p>
              )}
            </div>
            <div className="export-confirm-actions">
              <button className="secondary-btn" onClick={() => setExportConfig(null)}>Cancel</button>
              <button className="primary-btn" onClick={confirmExport}>Download</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

export default App
