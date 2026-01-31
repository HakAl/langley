import type { View } from '../types'

interface NavProps {
  view: View
  anomalyCount: number
  onNavigate: (v: View) => void
}

export function Nav({ view, anomalyCount, onNavigate }: NavProps) {
  return (
    <nav className="nav">
      <button className={view === 'flows' ? 'active' : ''} aria-current={view === 'flows' ? 'page' : undefined} onClick={() => onNavigate('flows')}>Flows</button>
      <button className={view === 'analytics' ? 'active' : ''} aria-current={view === 'analytics' ? 'page' : undefined} onClick={() => onNavigate('analytics')}>Analytics</button>
      <button className={view === 'tasks' ? 'active' : ''} aria-current={view === 'tasks' ? 'page' : undefined} onClick={() => onNavigate('tasks')}>Tasks</button>
      <button className={view === 'tools' ? 'active' : ''} aria-current={view === 'tools' ? 'page' : undefined} onClick={() => onNavigate('tools')}>Tools</button>
      <button className={view === 'anomalies' ? 'active' : ''} aria-current={view === 'anomalies' ? 'page' : undefined} onClick={() => onNavigate('anomalies')}>
        Anomalies {anomalyCount > 0 && <span className="badge error">{anomalyCount}</span>}
      </button>
      <button className={view === 'settings' ? 'active' : ''} aria-current={view === 'settings' ? 'page' : undefined} onClick={() => onNavigate('settings')}>
        Settings
      </button>
    </nav>
  )
}
