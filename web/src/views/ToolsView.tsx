import type { ToolStats } from '../types'
import { formatCost } from '../utils'

interface ToolsViewProps {
  tools: ToolStats[]
  selectedIndex: number
}

export function ToolsView({ tools, selectedIndex }: ToolsViewProps) {
  if (tools.length === 0) {
    return (
      <div className="tools-view">
        <div className="empty-state">
          <h2>No tool invocations tracked yet</h2>
          <p>Tool usage data appears when API traffic includes tool calls.</p>
        </div>
      </div>
    )
  }

  return (
    <div className="tools-view">
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
    </div>
  )
}
