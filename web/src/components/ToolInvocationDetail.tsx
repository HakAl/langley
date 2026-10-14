import { useState, useMemo, useRef, useEffect } from 'react'
import type { ToolInvocation } from '../types'
import { formatTime, formatDate, formatDuration } from '../utils'

interface ToolInvocationDetailProps {
  invocation: ToolInvocation
  onClose: () => void
  onViewFlow: (flowId: string) => void
}

const LINE_LIMIT = 20

function formatJSON(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

type JsonTab = 'input' | 'result'

function JsonViewer({ input, result }: { input?: string | null; result?: string | null }) {
  const hasInput = !!input
  const hasResult = !!result
  if (!hasInput && !hasResult) return <div className="body-empty">No input/result captured</div>

  const defaultTab: JsonTab = hasInput ? 'input' : 'result'
  const [activeTab, setActiveTab] = useState<JsonTab>(defaultTab)
  const [expanded, setExpanded] = useState(false)

  function switchTab(tab: JsonTab) {
    setActiveTab(tab)
    setExpanded(false)
  }

  const raw = activeTab === 'input' ? input : result
  const formatted = useMemo(() => (raw ? formatJSON(raw) : ''), [raw])
  const lines = formatted.split('\n')
  const needsCollapse = lines.length > LINE_LIMIT
  const isCollapsed = needsCollapse && !expanded

  function handleCopy() {
    if (raw) navigator.clipboard.writeText(raw)
  }

  return (
    <div className="body-viewer">
      <div className="body-tabs" role="tablist">
        <button
          role="tab"
          aria-selected={activeTab === 'input'}
          className={`body-tab${activeTab === 'input' ? ' body-tab-active' : ''}`}
          onClick={() => switchTab('input')}
          disabled={!hasInput}
        >
          Input
        </button>
        <button
          role="tab"
          aria-selected={activeTab === 'result'}
          className={`body-tab${activeTab === 'result' ? ' body-tab-active' : ''}`}
          onClick={() => switchTab('result')}
          disabled={!hasResult}
        >
          Result
        </button>
      </div>

      {raw ? (
        <>
          <div className="body-toolbar">
            <div />
            <button className="secondary-btn body-copy-btn" onClick={handleCopy}>Copy</button>
          </div>
          <div className={`body-content${isCollapsed ? ' body-collapsed' : ''}`}>
            <pre>{isCollapsed ? lines.slice(0, LINE_LIMIT).join('\n') : formatted}</pre>
          </div>
          {needsCollapse && (
            <button className="secondary-btn body-expand-btn" onClick={() => setExpanded(e => !e)}>
              {expanded ? 'Collapse' : `Show all (${lines.length} lines)`}
            </button>
          )}
        </>
      ) : (
        <div className="body-empty">No {activeTab} captured</div>
      )}
    </div>
  )
}

export function ToolInvocationDetail({ invocation, onClose, onViewFlow }: ToolInvocationDetailProps) {
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => { closeRef.current?.focus() }, [])

  const statusLabel = invocation.success == null ? 'Pending' : invocation.success ? 'Success' : 'Failed'
  const statusClass = invocation.success == null ? '' : invocation.success ? 'success' : 'error'

  return (
    <div className="flow-detail">
      <div className="detail-header">
        <h2>{invocation.tool_name}</h2>
        <button ref={closeRef} className="close-btn" onClick={onClose} aria-label="Close invocation detail">&times;</button>
      </div>

      <div className="detail-grid">
        <div className="detail-section">
          <h3>Invocation</h3>
          {invocation.tool_use_id && (
            <div className="detail-row"><strong>Tool Use ID:</strong> <code className="tool-use-id">{invocation.tool_use_id}</code></div>
          )}
          <div className="detail-row"><strong>Time:</strong> {formatDate(invocation.timestamp)} {formatTime(invocation.timestamp)}</div>
          <div className="detail-row"><strong>Duration:</strong> {formatDuration(invocation.duration_ms)}</div>
          <div className="detail-row"><strong>Status:</strong> <span className={statusClass}>{statusLabel}</span></div>
          {invocation.error_message && (
            <div className="detail-row error"><strong>Error:</strong> {invocation.error_message}</div>
          )}
        </div>

        <div className="detail-section">
          <h3>Context</h3>
          <div className="detail-row">
            <strong>Flow:</strong>{' '}
            <button className="link-btn" onClick={() => onViewFlow(invocation.flow_id)}>
              {invocation.flow_id.slice(0, 8)}&hellip;
            </button>
          </div>
          {invocation.task_id && (
            <div className="detail-row"><strong>Task:</strong> {invocation.task_id}</div>
          )}
        </div>
      </div>

      <JsonViewer input={invocation.tool_input} result={invocation.tool_result} />
    </div>
  )
}
