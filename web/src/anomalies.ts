import type { Anomaly } from './types'

export const DISMISSED_ANOMALIES_STORAGE_KEY = 'langley_dismissed_anomalies'

function defaultStorage(): Storage | undefined {
  return typeof localStorage !== 'undefined' ? localStorage : undefined
}

/** Deterministic identity for an anomaly, stable across refetches. */
export function getAnomalyKey(a: Anomaly): string {
  return `${a.type}:${a.flow_id ?? ''}:${a.timestamp}`
}

/** Loads dismissed keys; returns an empty set on missing or corrupt data. */
export function loadDismissedAnomalyKeys(storage: Pick<Storage, 'getItem'> | undefined = defaultStorage()): Set<string> {
  try {
    const raw = storage?.getItem(DISMISSED_ANOMALIES_STORAGE_KEY)
    if (!raw) return new Set()
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return new Set()
    return new Set(parsed.filter((k): k is string => typeof k === 'string'))
  } catch {
    return new Set()
  }
}

export function saveDismissedAnomalyKeys(keys: Iterable<string>, storage: Pick<Storage, 'setItem'> | undefined = defaultStorage()): void {
  try {
    storage?.setItem(DISMISSED_ANOMALIES_STORAGE_KEY, JSON.stringify([...keys]))
  } catch {
    // Storage full or unavailable (e.g. private mode); dismissal stays in memory only.
  }
}

export function filterAnomalies(
  anomalies: Anomaly[],
  dismissedKeys: ReadonlySet<string>,
  showDismissed = false,
): { visible: Anomaly[]; activeCount: number; dismissedCount: number } {
  const active = anomalies.filter(a => !dismissedKeys.has(getAnomalyKey(a)))
  return {
    visible: showDismissed ? anomalies : active,
    activeCount: active.length,
    dismissedCount: anomalies.length - active.length,
  }
}
