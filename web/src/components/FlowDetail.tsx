import { useRef, useEffect } from 'react'
import type { Anomaly, Flow } from '../types'
import { formatTime, formatDate, formatCost, formatDuration, formatStatusText, formatTaskId, getStatusClass, getSeverityClass } from '../utils'
import { BodyViewer } from './BodyViewer'

interface FlowDetailProps {
  flow: Flow
  loading?: boolean
  anomalies?: Anomaly[]
  onClose: () => void
}

export function FlowDetail({ flow, loading, anomalies = [], onClose }: FlowDetailProps) {
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => { closeRef.current?.focus() }, [])

  const hasCache = (flow.cache_creation_tokens ?? 0) > 0 || (flow.cache_read_tokens ?? 0) > 0
  const integrityIssue = flow.flow_integrity && flow.flow_integrity !== 'complete'
  const statusReason = formatStatusText(flow.status_code, flow.status_text)

  return (
    <div className="flow-detail">
      <div className="detail-header">
        <h2 title={flow.url}>{flow.method} {flow.path}</h2>
        <button ref={closeRef} className="close-btn" onClick={onClose} aria-label="Close flow detail">×</button>
      </div>
      {loading && (
        <div className="detail-loading" role="status">Loading flow details&hellip;</div>
      )}

      <dl className="detail-summary-bar">
        <div className="summary-item">
          <dt>Status</dt>
          <dd><span className={`status-code ${getStatusClass(flow.status_code)}`}>{flow.status_code ?? '...'}</span>{statusReason ? ` ${statusReason}` : ''}</dd>
        </div>
        <div className="summary-item">
          <dt>Provider</dt>
          <dd>{flow.provider || flow.host}{flow.model && <span className="summary-model"> · {flow.model}</span>}</dd>
        </div>
        {flow.duration_ms != null && (
          <div className="summary-item">
            <dt>Duration</dt>
            <dd>{formatDuration(flow.duration_ms)}</dd>
          </div>
        )}
        {(flow.input_tokens != null || flow.output_tokens != null) && (
          <div className="summary-item">
            <dt>Tokens</dt>
            <dd>
              <span className="token-arrow-in">↓</span> {(flow.input_tokens ?? 0).toLocaleString()} in
              {' / '}
              <span className="token-arrow-out">↑</span> {(flow.output_tokens ?? 0).toLocaleString()} out
              {hasCache && (
                <span className="summary-cache">
                  {' '}(cache {(flow.cache_read_tokens ?? 0).toLocaleString()} read · {(flow.cache_creation_tokens ?? 0).toLocaleString()} created)
                </span>
              )}
            </dd>
          </div>
        )}
        {flow.total_cost != null && (
          <div className="summary-item">
            <dt>Cost</dt>
            <dd className="cost" title={flow.cost_source ? `Source: ${flow.cost_source}` : undefined}>{formatCost(flow.total_cost)}</dd>
          </div>
        )}
        <div className="summary-item">
          <dt>Time</dt>
          <dd>{formatDate(flow.timestamp)} {formatTime(flow.timestamp)}</dd>
        </div>
        {flow.task_id && (
          <div className="summary-item">
            <dt>Task</dt>
            <dd><code title={flow.task_id}>{formatTaskId(flow.task_id)}</code>{flow.task_source && <span className="summary-muted"> ({flow.task_source})</span>}</dd>
          </div>
        )}
      </dl>

      {(integrityIssue || anomalies.length > 0) && (
        <ul className="detail-alerts">
          {integrityIssue && (
            <li className="detail-alert warning">
              Integrity: {flow.flow_integrity}
              {flow.events_dropped_count ? ` (${flow.events_dropped_count.toLocaleString()} events dropped)` : ''}
            </li>
          )}
          {anomalies.map((a, i) => (
            <li key={`${a.type}-${i}`} className={`detail-alert ${getSeverityClass(a.severity)}`}>
              {a.type.replace(/_/g, ' ')}: {a.description}
            </li>
          ))}
        </ul>
      )}

      <BodyViewer
        requestBody={flow.request_body}
        responseBody={flow.response_body}
        requestTruncated={flow.request_body_truncated}
        responseTruncated={flow.response_body_truncated}
        requestHeaders={flow.request_headers}
        responseHeaders={flow.response_headers}
      />
    </div>
  )
}
