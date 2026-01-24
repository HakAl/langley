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

interface Settings {
  idle_gap_minutes: number
}

function App() {
  const [token, setToken] = useState(() => localStorage.getItem('langley_token') || '')
  const [tokenInput, setTokenInput] = useState(token)
  const [flows, setFlows] = useState<Flow[]>([])
  const [selectedFlow, setSelectedFlow] = useState<Flow | null>(null)
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [view, setView] = useState<View>('flows')
  const [stats, setStats] = useState<Stats | null>(null)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const [tools, setTools] = useState<ToolStats[]>([])
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])
  const [dailyCosts, setDailyCosts] = useState<CostPeriod[]>([])
  const [settings, setSettings] = useState<Settings | null>(null)
  const [settingsSaving, setSettingsSaving] = useState(false)
  const [idleGapInput, setIdleGapInput] = useState(5)
  const wsRef = useRef<WebSocket | null>(null)

  // Filters
  const [hostFilter, setHostFilter] = useState('')
  const [taskFilter, setTaskFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'success' | 'error'>('all')

  // API helper
  const apiFetch = useCallback(async (path: string) => {
    if (!token) return null
    try {
      const res = await fetch(path, {
        headers: { 'Authorization': `Bearer ${token}` }
      })
      if (res.ok) return res.json()
      if (res.status === 401) setError('Invalid token')
      return null
    } catch {
      return null
    }
  }, [token])

  // WebSocket connection
  const connect = useCallback(() => {
    if (!token) return
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    const ws = new WebSocket(`ws://${window.location.host}/ws?token=${token}`)

    ws.onopen = () => {
      setConnected(true)
      setError(null)
    }

    ws.onclose = () => {
      setConnected(false)
      setTimeout(() => connect(), 3000)
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
  }, [token])

  // Fetch data
  const fetchFlows = useCallback(async () => {
    const data = await apiFetch('/api/flows?limit=50')
    if (data) setFlows(data)
  }, [apiFetch])

  const fetchStats = useCallback(async () => {
    const data = await apiFetch('/api/stats')
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
    const data = await apiFetch('/api/analytics/cost/daily')
    if (data) setDailyCosts(data)
  }, [apiFetch])

  const fetchSettings = useCallback(async () => {
    const data = await apiFetch('/api/settings')
    if (data) setSettings(data)
  }, [apiFetch])

  const updateSettings = useCallback(async (newSettings: Partial<Settings>) => {
    if (!token) return false
    setSettingsSaving(true)
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: {
          'Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json'
        },
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
  }, [token])

  const fetchFlowDetail = useCallback(async (id: string) => {
    const data = await apiFetch(`/api/flows/${id}`)
    if (data) setSelectedFlow(data)
  }, [apiFetch])

  // Initial load
  useEffect(() => {
    if (token) {
      fetchFlows()
      fetchStats()
      connect()
    }
    return () => {
      wsRef.current?.close()
    }
  }, [token, fetchFlows, fetchStats, connect])

  // Load view-specific data
  useEffect(() => {
    if (!token) return
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
  }, [view, token, fetchStats, fetchDailyCosts, fetchTasks, fetchTools, fetchAnomalies, fetchSettings])

  const handleSetToken = () => {
    localStorage.setItem('langley_token', tokenInput)
    setToken(tokenInput)
  }

  // Update idle gap input when settings load
  useEffect(() => {
    if (settings) setIdleGapInput(settings.idle_gap_minutes)
  }, [settings])

  // Filter flows
  const filteredFlows = flows.filter(flow => {
    if (hostFilter && !flow.host.toLowerCase().includes(hostFilter.toLowerCase())) return false
    if (taskFilter && flow.task_id !== taskFilter) return false
    if (statusFilter === 'success' && flow.status_code && flow.status_code >= 400) return false
    if (statusFilter === 'error' && flow.status_code && flow.status_code < 400) return false
    return true
  })

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
      <button className={view === 'flows' ? 'active' : ''} onClick={() => setView('flows')}>Flows</button>
      <button className={view === 'analytics' ? 'active' : ''} onClick={() => setView('analytics')}>Analytics</button>
      <button className={view === 'tasks' ? 'active' : ''} onClick={() => setView('tasks')}>Tasks</button>
      <button className={view === 'tools' ? 'active' : ''} onClick={() => setView('tools')}>Tools</button>
      <button className={view === 'anomalies' ? 'active' : ''} onClick={() => setView('anomalies')}>
        Anomalies {anomalies.length > 0 && <span className="badge error">{anomalies.length}</span>}
      </button>
      <button className={view === 'settings' ? 'active' : ''} onClick={() => setView('settings')}>
        Settings
      </button>
    </nav>
  )

  // Render flows view
  const renderFlows = () => (
    <div className="flows-view">
      <div className="filters">
        <input
          type="text"
          placeholder="Filter by host..."
          value={hostFilter}
          onChange={(e) => setHostFilter(e.target.value)}
        />
        <input
          type="text"
          placeholder="Filter by task ID..."
          value={taskFilter}
          onChange={(e) => setTaskFilter(e.target.value)}
        />
        <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as 'all' | 'success' | 'error')}>
          <option value="all">All Status</option>
          <option value="success">Success (2xx/3xx)</option>
          <option value="error">Errors (4xx/5xx)</option>
        </select>
      </div>

      <div className="flow-list">
        {filteredFlows.length === 0 ? (
          <div className="empty-state">
            <h2>No flows captured yet</h2>
            <p>Configure your client to use the proxy and traffic will appear here.</p>
          </div>
        ) : (
          filteredFlows.map(flow => (
            <div
              key={flow.id}
              className={`flow-item ${selectedFlow?.id === flow.id ? 'selected' : ''}`}
              onClick={() => fetchFlowDetail(flow.id)}
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
        <div className="chart-bars">
          {dailyCosts.map((day, i) => {
            const maxCost = Math.max(...dailyCosts.map(d => d.total_cost), 0.01)
            const height = (day.total_cost / maxCost) * 100
            return (
              <div key={i} className="bar-container">
                <div className="bar" style={{ height: `${height}%` }}>
                  <span className="bar-value">${day.total_cost.toFixed(2)}</span>
                </div>
                <span className="bar-label">{day.period.slice(5)}</span>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )

  // Render tasks view
  const renderTasks = () => (
    <div className="tasks-view">
      <table className="data-table">
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
          {tasks.map(task => (
            <tr key={task.task_id} onClick={() => { setTaskFilter(task.task_id); setView('flows') }}>
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
    </div>
  )

  // Render tools view
  const renderTools = () => (
    <div className="tools-view">
      <table className="data-table">
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
          {tools.map(tool => (
            <tr key={tool.tool_name}>
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
        <div className="anomaly-list">
          {anomalies.map((anomaly, i) => (
            <div key={i} className={`anomaly-item ${getSeverityClass(anomaly.severity)}`}>
              <div className="anomaly-header">
                <span className={`severity-badge ${anomaly.severity}`}>{anomaly.severity}</span>
                <span className="anomaly-type">{anomaly.type.replace(/_/g, ' ')}</span>
                <span className="anomaly-time">{formatTime(anomaly.timestamp)}</span>
              </div>
              <div className="anomaly-desc">{anomaly.description}</div>
              <div className="anomaly-meta">
                Value: {anomaly.value.toFixed(2)} | Threshold: {anomaly.threshold.toFixed(2)}
                {anomaly.flow_id && (
                  <button className="link-btn" onClick={() => { fetchFlowDetail(anomaly.flow_id); setView('flows') }}>
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
      // Settings saved successfully
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
          disabled={settingsSaving || idleGapInput === settings?.idle_gap_minutes}
        >
          {settingsSaving ? 'Saving...' : 'Save Settings'}
        </button>
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
        <h1>Langley</h1>
        {renderNav()}
        <div className="status">
          <span className={`status-dot ${connected ? 'connected' : ''}`}></span>
          <span>{connected ? 'Connected' : 'Disconnected'}</span>
        </div>
      </header>

      <div className="container">
        {!token && (
          <div className="token-input">
            <input
              type="text"
              placeholder="Enter auth token (langley_...)"
              value={tokenInput}
              onChange={(e) => setTokenInput(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleSetToken()}
            />
            <button onClick={handleSetToken}>Connect</button>
          </div>
        )}

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
    </div>
  )
}

export default App
