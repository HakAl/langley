import { useState, useEffect, useCallback, useRef } from 'react'

interface Flow {
  id: string
  host: string
  method: string
  path: string
  status_code?: number
  is_sse: boolean
  timestamp: string
  duration_ms?: number
  task_id?: string
  task_source?: string
  model?: string
  input_tokens?: number
  output_tokens?: number
  total_cost?: number
}

interface WSMessage {
  type: string
  timestamp: string
  data: Flow
}

function App() {
  const [token, setToken] = useState(() => localStorage.getItem('langley_token') || '')
  const [tokenInput, setTokenInput] = useState(token)
  const [flows, setFlows] = useState<Flow[]>([])
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const connect = useCallback(() => {
    if (!token) return
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    const ws = new WebSocket(`ws://${window.location.host}/ws?token=${token}`)

    ws.onopen = () => {
      setConnected(true)
      setError(null)
      console.log('WebSocket connected')
    }

    ws.onclose = () => {
      setConnected(false)
      console.log('WebSocket disconnected')
      // Reconnect after 3 seconds
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
              return [msg.data, ...prev].slice(0, 100) // Keep last 100
            })
          }
        }
      } catch (e) {
        console.error('Failed to parse message:', e)
      }
    }

    wsRef.current = ws
  }, [token])

  const fetchFlows = useCallback(async () => {
    if (!token) return

    try {
      const res = await fetch('/api/flows?limit=50', {
        headers: { 'Authorization': `Bearer ${token}` }
      })
      if (res.ok) {
        const data = await res.json()
        setFlows(data)
        setError(null)
      } else if (res.status === 401) {
        setError('Invalid token')
      }
    } catch (e) {
      setError('Failed to fetch flows')
    }
  }, [token])

  useEffect(() => {
    if (token) {
      fetchFlows()
      connect()
    }
    return () => {
      wsRef.current?.close()
    }
  }, [token, fetchFlows, connect])

  const handleSetToken = () => {
    localStorage.setItem('langley_token', tokenInput)
    setToken(tokenInput)
  }

  const formatTime = (timestamp: string) => {
    return new Date(timestamp).toLocaleTimeString()
  }

  const formatCost = (cost?: number) => {
    if (cost === undefined || cost === null) return null
    return `$${cost.toFixed(4)}`
  }

  const getStatusClass = (code?: number) => {
    if (!code) return ''
    if (code < 300) return 'success'
    if (code < 400) return 'redirect'
    return 'error'
  }

  return (
    <div>
      <header>
        <h1>Langley</h1>
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

        {error && (
          <div style={{ color: 'var(--error)', marginBottom: '1rem' }}>
            {error}
          </div>
        )}

        <div className="flow-list">
          {flows.length === 0 ? (
            <div className="empty-state">
              <h2>No flows captured yet</h2>
              <p>Configure your client to use the proxy and traffic will appear here.</p>
            </div>
          ) : (
            flows.map(flow => (
              <div key={flow.id} className="flow-item">
                <span className={`method ${flow.method}`}>{flow.method}</span>
                <div className="host-path">
                  <span className="host">
                    {flow.host}
                    {flow.is_sse && <span className="badge sse" style={{ marginLeft: '0.5rem' }}>SSE</span>}
                  </span>
                  <span className="path">{flow.path}</span>
                </div>
                <span className={`status-code ${getStatusClass(flow.status_code)}`}>
                  {flow.status_code || '...'}
                </span>
                <div className="tokens">
                  {flow.input_tokens && (
                    <span className="token-count">
                      <span style={{ color: 'var(--accent)' }}>↓</span>
                      {flow.input_tokens.toLocaleString()}
                    </span>
                  )}
                  {flow.output_tokens && (
                    <span className="token-count">
                      <span style={{ color: 'var(--success)' }}>↑</span>
                      {flow.output_tokens.toLocaleString()}
                    </span>
                  )}
                  {flow.total_cost !== undefined && flow.total_cost !== null && (
                    <span className="cost">{formatCost(flow.total_cost)}</span>
                  )}
                </div>
                <span className="timestamp">{formatTime(flow.timestamp)}</span>
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  )
}

export default App
