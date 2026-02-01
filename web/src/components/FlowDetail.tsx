import type { Flow } from '../types'
import { formatTime, formatDate, formatCost, formatDuration, getStatusClass } from '../utils'

interface FlowDetailProps {
  flow: Flow
  onClose: () => void
}

export function FlowDetail({ flow, onClose }: FlowDetailProps) {
  return (
    <div className="flow-detail">
      <div className="detail-header">
        <h2>{flow.method} {flow.path}</h2>
        <button className="close-btn" onClick={onClose} aria-label="Close flow detail">×</button>
      </div>

      <div className="detail-grid">
        <div className="detail-section">
          <h3>Request</h3>
          <div className="detail-row"><strong>URL:</strong> {flow.url}</div>
          <div className="detail-row"><strong>Host:</strong> {flow.host}</div>
          <div className="detail-row"><strong>Time:</strong> {formatDate(flow.timestamp)} {formatTime(flow.timestamp)}</div>
          {flow.task_id && (
            <div className="detail-row"><strong>Task:</strong> {flow.task_id} ({flow.task_source})</div>
          )}
          {flow.request_headers && (
            <div className="headers">
              <h4>Headers</h4>
              {Object.entries(flow.request_headers).map(([k, v]) => (
                <div key={k} className="header-row">{k}: {v.join(', ')}</div>
              ))}
            </div>
          )}
        </div>

        <div className="detail-section">
          <h3>Response</h3>
          <div className="detail-row"><strong>Status:</strong> <span className={getStatusClass(flow.status_code)}>{flow.status_code} {flow.status_text}</span></div>
          <div className="detail-row"><strong>Duration:</strong> {formatDuration(flow.duration_ms)}</div>
          <div className="detail-row"><strong>Provider:</strong> {flow.provider}</div>
          {flow.model && <div className="detail-row"><strong>Model:</strong> {flow.model}</div>}
          {flow.flow_integrity !== 'complete' && (
            <div className="detail-row warning"><strong>Integrity:</strong> {flow.flow_integrity}</div>
          )}
        </div>

        <div className="detail-section">
          <h3>Usage</h3>
          {flow.input_tokens != null && <div className="detail-row"><strong>Input:</strong> {flow.input_tokens.toLocaleString()} tokens</div>}
          {flow.output_tokens != null && <div className="detail-row"><strong>Output:</strong> {flow.output_tokens.toLocaleString()} tokens</div>}
          {flow.cache_creation_tokens != null && flow.cache_creation_tokens > 0 && (
            <div className="detail-row"><strong>Cache Created:</strong> {flow.cache_creation_tokens.toLocaleString()} tokens</div>
          )}
          {flow.cache_read_tokens != null && flow.cache_read_tokens > 0 && (
            <div className="detail-row"><strong>Cache Read:</strong> {flow.cache_read_tokens.toLocaleString()} tokens</div>
          )}
          {flow.total_cost != null && (
            <div className="detail-row cost-row"><strong>Cost:</strong> {formatCost(flow.total_cost)} ({flow.cost_source})</div>
          )}
        </div>
      </div>

      {flow.response_body && (
        <div className="body-section">
          <h3>Response Body</h3>
          <pre className="body-content">{flow.response_body}</pre>
        </div>
      )}
    </div>
  )
}
