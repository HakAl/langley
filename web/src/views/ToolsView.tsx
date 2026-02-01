import type { ToolStats } from '../types'
import { formatCost } from '../utils'

interface ToolsViewProps {
  tools: ToolStats[]
  selectedIndex: number
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
}

export function ToolsView({ tools, selectedIndex, timeRange, onTimeRangeChange }: ToolsViewProps) {
  return (
    <div className="tools-view">
      <div className="filters">
        <select
          aria-label="Time range"
          value={timeRange == null ? 'all' : String(timeRange)}
          onChange={(e) => onTimeRangeChange(e.target.value === 'all' ? null : Number(e.target.value))}
        >
          <option value="1">Last 24 hours</option>
          <option value="7">Last 7 days</option>
          <option value="30">Last 30 days</option>
          <option value="90">Last 90 days</option>
          <option value="all">All time</option>
        </select>
      </div>

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
                tabIndex={index === selectedIndex ? 0 : -1}
                aria-selected={index === selectedIndex}
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
}
