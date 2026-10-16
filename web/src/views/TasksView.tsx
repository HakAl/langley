import { useEffect, useMemo, useRef, useState } from 'react'
import type { TaskSummary } from '../types'
import {
  formatTime,
  formatCost,
  formatDuration,
  formatTaskId,
  sortTasks,
  type TaskSortField,
  type SortDirection,
} from '../utils'
import { TimeRangeSelect } from '../components/TimeRangeSelect'

interface TasksViewProps {
  tasks: TaskSummary[]
  selectedIndex: number
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
  onTaskSelect: (taskId: string) => void
  onSortedTasksChange?: (tasks: TaskSummary[]) => void
  loading?: boolean
}

const TASK_COLUMNS: ReadonlyArray<{
  field: TaskSortField
  label: string
  defaultDir: SortDirection
}> = [
  { field: 'task_id', label: 'Task ID', defaultDir: 'asc' },
  { field: 'flow_count', label: 'Flows', defaultDir: 'desc' },
  { field: 'total_tokens_in', label: 'Tokens In', defaultDir: 'desc' },
  { field: 'total_tokens_out', label: 'Tokens Out', defaultDir: 'desc' },
  { field: 'total_cost', label: 'Cost', defaultDir: 'desc' },
  { field: 'duration_ms', label: 'Duration', defaultDir: 'desc' },
  { field: 'last_seen', label: 'Last Seen', defaultDir: 'desc' },
]

export function TasksView({
  tasks,
  selectedIndex,
  timeRange,
  onTimeRangeChange,
  onTaskSelect,
  onSortedTasksChange,
  loading,
}: TasksViewProps) {
  const selectedRowRef = useRef<HTMLTableRowElement | null>(null)
  const [sortField, setSortField] = useState<TaskSortField>('last_seen')
  const [sortDir, setSortDir] = useState<SortDirection>('desc')

  const sortedTasks = useMemo(
    () => sortTasks(tasks, sortField, sortDir),
    [tasks, sortField, sortDir]
  )

  useEffect(() => {
    onSortedTasksChange?.(sortedTasks)
  }, [sortedTasks, onSortedTasksChange])

  useEffect(() => {
    selectedRowRef.current?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  const handleSort = (field: TaskSortField, defaultDir: SortDirection) => {
    if (sortField === field) {
      setSortDir(prev => (prev === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortField(field)
      setSortDir(defaultDir)
    }
  }

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
      <div className="data-table-container">
        <table className="data-table" aria-label="Task list">
          <thead>
            <tr>
              {TASK_COLUMNS.map(col => {
                const isActive = sortField === col.field
                return (
                  <th
                    key={col.field}
                    className="sortable-th"
                    aria-sort={isActive ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
                  >
                    <button
                      type="button"
                      className={`table-sort-btn${isActive ? ' active' : ''}`}
                      onClick={() => handleSort(col.field, col.defaultDir)}
                    >
                      <span>{col.label}</span>
                      <span className="sort-indicator" aria-hidden="true">
                        {isActive ? (sortDir === 'asc' ? '↑' : '↓') : '↕'}
                      </span>
                    </button>
                  </th>
                )
              })}
              <th></th>
            </tr>
          </thead>
          <tbody>
            {sortedTasks.map((task, index) => (
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
                    e.stopPropagation()
                    onTaskSelect(task.task_id)
                  }
                }}
                ref={index === selectedIndex ? selectedRowRef : null}
              >
                <td className="task-id" title={task.task_id}>{formatTaskId(task.task_id)}</td>
                <td>{task.flow_count}</td>
                <td>{task.total_tokens_in.toLocaleString()}</td>
                <td>{task.total_tokens_out.toLocaleString()}</td>
                <td className="cost">{formatCost(task.total_cost)}</td>
                <td>{formatDuration(task.duration_ms)}</td>
                <td>{formatTime(task.last_seen)}</td>
                <td className="row-action-cue" aria-hidden="true">View flows &rarr;</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      )}
    </div>
  )
}
