import type { TaskSummary } from '../types'
import { formatTime, formatCost, formatDuration } from '../utils'
import { TimeRangeSelect } from '../components/TimeRangeSelect'

interface TasksViewProps {
  tasks: TaskSummary[]
  selectedIndex: number
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
  onTaskSelect: (taskId: string) => void
  loading?: boolean
}

export function TasksView({ tasks, selectedIndex, timeRange, onTimeRangeChange, onTaskSelect, loading }: TasksViewProps) {
  return (
    <div className="tasks-view">
      <div className="filters">
        <TimeRangeSelect timeRange={timeRange} onTimeRangeChange={onTimeRangeChange} />
      </div>

      {loading && tasks.length === 0 ? (
        <div className="view-loading">Loading tasks&hellip;</div>
      ) : tasks.length === 0 ? (
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
              role="row"
              className={index === selectedIndex ? 'keyboard-selected' : ''}
              tabIndex={index === selectedIndex ? 0 : -1}
              aria-selected={index === selectedIndex}
              onClick={() => onTaskSelect(task.task_id)}
              onKeyDown={e => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onTaskSelect(task.task_id)
                }
              }}
              ref={el => {
                if (index === selectedIndex && el) {
                  el.scrollIntoView({ block: 'nearest' })
                }
              }}
            >
              <td className="task-id" title={task.task_id}>{task.task_id}</td>
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
}
