import { useCallback, useEffect, useRef } from 'react'
import type { Flow } from '../types'
import { formatTime, formatCost, formatDuration, getStatusClass } from '../utils'
import { TimeRangeSelect } from '../components/TimeRangeSelect'
import { Pagination } from '../components/Pagination'

interface FlowListViewProps {
  flows: Flow[]
  totalFlows: number
  initialLoading: boolean
  selectedFlowId: string | null
  selectedIndex: number
  hostFilter: string
  taskFilter: string
  statusFilter: 'all' | 'success' | 'error'
  timeRange: number | null
  page: number
  totalPages: number
  onHostFilterChange: (value: string) => void
  onTaskFilterChange: (value: string) => void
  onStatusFilterChange: (value: 'all' | 'success' | 'error') => void
  onTimeRangeChange: (days: number | null) => void
  onPageChange: (page: number) => void
  onFlowSelect: (id: string) => void
  onStartExport: (format: string) => void
  exportMessage?: string | null
  hostFilterRef?: React.RefObject<HTMLInputElement | null>
}

const PAGE_SIZE = 50

export function FlowListView({
  flows,
  totalFlows,
  initialLoading,
  selectedFlowId,
  selectedIndex,
  hostFilter,
  taskFilter,
  statusFilter,
  onHostFilterChange,
  onTaskFilterChange,
  onStatusFilterChange,
  onTimeRangeChange,
  onPageChange,
  onFlowSelect,
  onStartExport,
  exportMessage,
  timeRange,
  page,
  totalPages,
  hostFilterRef,
}: FlowListViewProps) {
  const selectedItemRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    selectedItemRef.current?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  const handleExportChange = useCallback((e: React.ChangeEvent<HTMLSelectElement>) => {
    if (e.target.value) {
      onStartExport(e.target.value)
      e.target.value = ''
    }
  }, [onStartExport])

  const rangeStart = page * PAGE_SIZE + 1
  const rangeEnd = Math.min((page + 1) * PAGE_SIZE, totalFlows)

  return (
    <div className="flows-view">
      <div className="filters">
        <div className="filter-input-wrapper">
          <input
            ref={hostFilterRef}
            type="text"
            placeholder="Filter by host..."
            aria-label="Filter by host"
            value={hostFilter}
            onChange={(e) => onHostFilterChange(e.target.value)}
          />
          {hostFilter && (
            <button className="filter-clear-btn" onClick={() => onHostFilterChange('')} aria-label="Clear host filter">&times;</button>
          )}
        </div>
        <div className="filter-input-wrapper">
          <input
            type="text"
            placeholder="Filter by task ID..."
            aria-label="Filter by task ID"
            value={taskFilter}
            onChange={(e) => onTaskFilterChange(e.target.value)}
          />
          {taskFilter && (
            <button className="filter-clear-btn" onClick={() => onTaskFilterChange('')} aria-label="Clear task ID filter">&times;</button>
          )}
        </div>
        <select aria-label="Filter by status" value={statusFilter} onChange={(e) => onStatusFilterChange(e.target.value as 'all' | 'success' | 'error')}>
          <option value="all">All Status</option>
          <option value="success">Success (2xx/3xx)</option>
          <option value="error">Errors (4xx/5xx)</option>
        </select>
        <TimeRangeSelect timeRange={timeRange} onTimeRangeChange={onTimeRangeChange} />
        <div className="export-dropdown">
          <label htmlFor="export-flows-select" className="export-label">&darr; Export</label>
          <select
            id="export-flows-select"
            aria-label="Export flows"
            onChange={handleExportChange}
            defaultValue=""
          >
            <option value="" disabled>Export...</option>
            <option value="ndjson">NDJSON</option>
            <option value="json">JSON</option>
            <option value="csv">CSV</option>
          </select>
          {exportMessage && (
            <span className="export-toast" role="status">{exportMessage}</span>
          )}
        </div>
      </div>

      {totalFlows > 0 && (
        <div className="flow-count">Showing {rangeStart.toLocaleString()}&ndash;{rangeEnd.toLocaleString()} of {totalFlows.toLocaleString()} flows</div>
      )}

      <div className="flow-list" role="listbox" aria-label="Flow list" aria-activedescendant={flows.length > 0 ? `flow-${selectedIndex}` : undefined}>
        {flows.length === 0 ? (
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
          flows.map((flow, index) => (
            <div
              key={flow.id}
              id={`flow-${index}`}
              role="option"
              aria-selected={index === selectedIndex}
              tabIndex={index === selectedIndex ? 0 : -1}
              className={`flow-item ${selectedFlowId === flow.id ? 'selected' : ''} ${index === selectedIndex ? 'keyboard-selected' : ''}`}
              onClick={() => onFlowSelect(flow.id)}
              ref={index === selectedIndex ? selectedItemRef : null}
            >
              <span className={`method ${flow.method}`}>{flow.method}</span>
              <div className="host-path">
                <span className="host">
                  {flow.host}
                  {flow.is_sse && <span className="badge sse">SSE</span>}
                </span>
                <span className="path-line">
                  <span className="path">{flow.path}</span>
                  {flow.model && <span className="flow-model" title={flow.model}>{flow.model}</span>}
                </span>
              </div>
              <span className={`status-code ${getStatusClass(flow.status_code)}`}>
                {flow.status_code || '...'}
              </span>
              <div className="tokens">
                {flow.duration_ms != null && (
                  <span className="flow-duration" title="Duration">{formatDuration(flow.duration_ms)}</span>
                )}
                {flow.input_tokens != null && (
                  <span className="token-count">
                    <span className="token-arrow-in">↓</span>
                    {flow.input_tokens.toLocaleString()}
                  </span>
                )}
                {flow.output_tokens != null && (
                  <span className="token-count">
                    <span className="token-arrow-out">↑</span>
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

      <Pagination page={page} totalPages={totalPages} onPageChange={onPageChange} />
    </div>
  )
}
