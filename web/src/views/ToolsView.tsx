import type { ToolStats, ToolInvocation } from '../types'
import { TimeRangeSelect } from '../components/TimeRangeSelect'
import { formatTime, formatDate, formatDuration } from '../utils'

interface ToolsViewProps {
  tools: ToolStats[]
  selectedIndex: number
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
  onToolSelect: (toolName: string) => void
  loading?: boolean
  // Drill-down state
  selectedTool: string | null
  invocations: ToolInvocation[]
  invocationsTotal: number
  invocationsLoading: boolean
  invocationSelectedIndex: number
  onBack: () => void
  onInvocationSelect: (invocation: ToolInvocation) => void
}

export function ToolsView({
  tools, selectedIndex, timeRange, onTimeRangeChange, onToolSelect, loading,
  selectedTool, invocations, invocationsTotal, invocationsLoading, invocationSelectedIndex,
  onBack, onInvocationSelect,
}: ToolsViewProps) {
  // Invocations list mode
  if (selectedTool) {
    return (
      <div className="tools-view">
        <div className="filters">
          <div className="drill-down-header">
            <button className="secondary-btn" onClick={onBack} aria-label="Back to tools list">&larr; Tools</button>
            <span className="drill-down-title">{selectedTool}</span>
            <span className="text-muted">({invocationsTotal} invocations)</span>
          </div>
        </div>

        {invocationsLoading && invocations.length === 0 ? (
          <div className="view-loading">Loading invocations&hellip;</div>
        ) : invocations.length === 0 ? (
          <div className="empty-state">
            <h2>No invocations found</h2>
            <p>No invocations for {selectedTool} in the selected time range.</p>
          </div>
        ) : (
          <table className="data-table" aria-label="Tool invocations">
            <thead>
              <tr>
                <th>Tool Use ID</th>
                <th>Status</th>
                <th>Duration</th>
                <th>Flow</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {invocations.map((inv, index) => {
                const statusLabel = inv.success == null ? 'Pending' : inv.success ? 'OK' : 'Fail'
                const statusClass = inv.success == null ? '' : inv.success ? 'success' : 'error'
                return (
                  <tr
                    key={inv.id}
                    className={index === invocationSelectedIndex ? 'keyboard-selected' : ''}
                    tabIndex={index === invocationSelectedIndex ? 0 : -1}
                    aria-selected={index === invocationSelectedIndex}
                    onClick={() => onInvocationSelect(inv)}
                    ref={el => {
                      if (index === invocationSelectedIndex && el) {
                        el.scrollIntoView({ block: 'nearest' })
                      }
                    }}
                  >
                    <td className="tool-use-id">{inv.tool_use_id ? inv.tool_use_id.slice(0, 12) + '\u2026' : '\u2014'}</td>
                    <td className={statusClass}>{statusLabel}</td>
                    <td>{formatDuration(inv.duration_ms)}</td>
                    <td className="text-muted">{inv.flow_id.slice(0, 8)}&hellip;</td>
                    <td>{formatDate(inv.timestamp)} {formatTime(inv.timestamp)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    )
  }

  // Aggregate stats mode (default)
  return (
    <div className="tools-view">
      <div className="filters">
        <TimeRangeSelect timeRange={timeRange} onTimeRangeChange={onTimeRangeChange} />
      </div>

      {loading && tools.length === 0 ? (
        <div className="view-loading">Loading tools&hellip;</div>
      ) : tools.length === 0 ? (
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
            </tr>
          </thead>
          <tbody>
            {tools.map((tool, index) => (
              <tr
                key={tool.tool_name}
                className={index === selectedIndex ? 'keyboard-selected' : ''}
                tabIndex={index === selectedIndex ? 0 : -1}
                aria-selected={index === selectedIndex}
                onClick={() => onToolSelect(tool.tool_name)}
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
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
