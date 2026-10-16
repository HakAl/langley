import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import type { Anomaly } from '../types'
import { formatTime, getSeverityClass } from '../utils'
import { TimeRangeSelect } from '../components/TimeRangeSelect'
import { filterAnomalies, getAnomalyKey, loadDismissedAnomalyKeys, saveDismissedAnomalyKeys } from '../anomalies'

interface AnomaliesViewProps {
  anomalies: Anomaly[]
  selectedIndex: number
  timeRange: number | null
  onTimeRangeChange: (days: number | null) => void
  onViewFlow: (flowId: string) => void
  loading?: boolean
  /** When provided, dismissal state is controlled by the parent. */
  dismissedKeys?: ReadonlySet<string>
  showDismissed?: boolean
  onShowDismissedChange?: (show: boolean) => void
  onDismiss?: (key: string) => void
  onRestore?: (key: string) => void
  onRestoreAll?: () => void
}

export function AnomaliesView({
  anomalies, selectedIndex, timeRange, onTimeRangeChange, onViewFlow, loading,
  dismissedKeys: controlledKeys, showDismissed: controlledShowDismissed, onShowDismissedChange,
  onDismiss, onRestore, onRestoreAll,
}: AnomaliesViewProps) {
  const selectedItemRef = useRef<HTMLDivElement | null>(null)
  const [internalKeys, setInternalKeys] = useState<ReadonlySet<string>>(() => controlledKeys ? new Set() : loadDismissedAnomalyKeys())
  const [internalShowDismissed, setInternalShowDismissed] = useState(false)
  const dismissedKeys = controlledKeys ?? internalKeys
  const showDismissed = controlledShowDismissed ?? internalShowDismissed
  const toggleShowDismissed = () => {
    const next = !showDismissed
    if (onShowDismissedChange) {
      onShowDismissedChange(next)
    } else {
      setInternalShowDismissed(next)
    }
  }

  useEffect(() => {
    selectedItemRef.current?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  const updateInternal = (next: Set<string>) => {
    setInternalKeys(next)
    saveDismissedAnomalyKeys(next)
  }

  const handleDismiss = (key: string) => {
    if (controlledKeys) {
      onDismiss?.(key)
      return
    }
    updateInternal(new Set(internalKeys).add(key))
  }

  const handleRestore = (key: string) => {
    if (controlledKeys) {
      onRestore?.(key)
      return
    }
    const next = new Set(internalKeys)
    next.delete(key)
    updateInternal(next)
  }

  const handleRestoreAll = () => {
    if (controlledKeys) {
      onRestoreAll?.()
      return
    }
    // Only restore anomalies in the current list; keys for anomalies outside
    // the selected time range stay dismissed.
    const next = new Set(internalKeys)
    for (const a of anomalies) next.delete(getAnomalyKey(a))
    updateInternal(next)
  }

  // Keep Enter/Space on these buttons from reaching the global row-navigation handler
  const stopActivationKeys = (e: KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') e.stopPropagation()
  }

  const { visible, dismissedCount } = filterAnomalies(anomalies, dismissedKeys, showDismissed)

  return (
    <div className="anomalies-view">
      <div className="filters">
        <TimeRangeSelect timeRange={timeRange} onTimeRangeChange={onTimeRangeChange} />
        {dismissedCount > 0 && (
          <button className="secondary-btn" onClick={toggleShowDismissed} onKeyDown={stopActivationKeys} aria-pressed={showDismissed}>
            {showDismissed ? `Hide dismissed (${dismissedCount})` : `Show dismissed (${dismissedCount})`}
          </button>
        )}
        {showDismissed && dismissedCount > 0 && (
          <button className="link-btn" onClick={handleRestoreAll} onKeyDown={stopActivationKeys}>Restore all</button>
        )}
      </div>

      {loading && anomalies.length === 0 ? (
        <div className="view-loading">Loading anomalies&hellip;</div>
      ) : anomalies.length === 0 ? (
        <div className="empty-state">
          <h2>No anomalies detected</h2>
          <p>Everything looks normal!</p>
        </div>
      ) : visible.length === 0 ? (
        <div className="empty-state">
          <h2>All anomalies dismissed</h2>
          <p>Use &ldquo;Show dismissed&rdquo; to review them.</p>
        </div>
      ) : (
      <div className="anomaly-list" role="listbox" aria-label="Anomaly list" aria-activedescendant={`anomaly-${selectedIndex}`}>
        {visible.map((anomaly, index) => {
          const key = getAnomalyKey(anomaly)
          const isDismissed = dismissedKeys.has(key)
          return (
          <div
            key={`${key}#${index}`}
            id={`anomaly-${index}`}
            role="option"
            aria-selected={index === selectedIndex}
            tabIndex={index === selectedIndex ? 0 : -1}
            className={`anomaly-item ${getSeverityClass(anomaly.severity)} ${index === selectedIndex ? 'keyboard-selected' : ''} ${isDismissed ? 'dismissed' : ''}`}
            ref={index === selectedIndex ? selectedItemRef : null}
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
                <button className="link-btn" onClick={() => onViewFlow(anomaly.flow_id)} onKeyDown={stopActivationKeys}>
                  View Flow
                </button>
              )}
              {isDismissed ? (
                <button className="link-btn" onClick={() => handleRestore(key)} onKeyDown={stopActivationKeys} aria-label="Restore anomaly">Restore</button>
              ) : (
                <button className="link-btn" onClick={() => handleDismiss(key)} onKeyDown={stopActivationKeys} aria-label="Dismiss anomaly">Dismiss</button>
              )}
            </div>
          </div>
          )
        })}
      </div>
      )}
    </div>
  )
}
