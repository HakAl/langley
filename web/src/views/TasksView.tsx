import type { TaskSummary } from '../types'
import { formatTime, formatCost, formatDuration } from '../utils'

interface TasksViewProps {
  tasks: TaskSummary[]
  selectedIndex: number
  onTaskSelect: (taskId: string) => void
}

export function TasksView({ tasks, selectedIndex, onTaskSelect }: TasksViewProps) {
  if (tasks.length === 0) {
    return (
      <div className="tasks-view">
        <div className="empty-state">
          <h2>No tasks tracked yet</h2>
          <p>Tasks appear when API traffic includes task identifiers.</p>
        </div>
      </div>
    )
  }

  return (
    <div className="tasks-view">
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
              className={index === selectedIndex ? 'keyboard-selected' : ''}
              onClick={() => onTaskSelect(task.task_id)}
              ref={el => {
                if (index === selectedIndex && el) {
                  el.scrollIntoView({ block: 'nearest' })
                }
              }}
            >
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
}
