import type { Anomaly } from '../types'
import { formatTime, getSeverityClass } from '../utils'

interface AnomaliesViewProps {
  anomalies: Anomaly[]
  selectedIndex: number
  onViewFlow: (flowId: string) => void
}

export function AnomaliesView({ anomalies, selectedIndex, onViewFlow }: AnomaliesViewProps) {
  if (anomalies.length === 0) {
    return (
      <div className="anomalies-view">
        <div className="empty-state">
          <h2>No anomalies detected</h2>
          <p>Everything looks normal!</p>
        </div>
      </div>
    )
  }

  return (
    <div className="anomalies-view">
      <div className="anomaly-list" role="listbox" aria-label="Anomaly list" aria-activedescendant={`anomaly-${selectedIndex}`}>
        {anomalies.map((anomaly, index) => (
          <div
            key={index}
            id={`anomaly-${index}`}
            role="option"
            aria-selected={index === selectedIndex}
            tabIndex={index === selectedIndex ? 0 : -1}
            className={`anomaly-item ${getSeverityClass(anomaly.severity)} ${index === selectedIndex ? 'keyboard-selected' : ''}`}
            ref={el => {
              if (index === selectedIndex && el) {
                el.scrollIntoView({ block: 'nearest' })
              }
            }}
          >
            <div className="anomaly-header">
              <span className={`severity-badge ${anomaly.severity}`}>{anomaly.severity}</span>
              <span className="anomaly-type">{anomaly.type.replace(/_/g, ' ')}</span>
              <span className="anomaly-time">{formatTime(anomaly.timestamp)}</span>
            </div>
            <div className="anomaly-desc">{anomaly.description}</div>
            <div className="anomaly-meta">
              Value: {anomaly.value.toFixed(2)} | Threshold: {anomaly.threshold.toFixed(2)}
              {anomaly.flow_id && (
                <button className="link-btn" onClick={() => onViewFlow(anomaly.flow_id)}>
                  View Flow
                </button>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
