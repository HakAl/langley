import type { Flow } from './types'

const MAX_FLOWS = 100

/**
 * Merge incoming REST flows with current in-memory flows.
 *
 * Heuristic: if the current (WS-updated) flow has `status_code` but the
 * incoming (REST) copy doesn't, WS raced ahead — keep the current version.
 * Otherwise the REST/DB copy is canonical.
 *
 * Flows present in current but absent from incoming (WS-only, not yet in DB)
 * are preserved.
 */
export function mergeFlows(current: Flow[], incoming: Flow[]): Flow[] {
  const currentById = new Map(current.map(f => [f.id, f]))

  const merged = new Map<string, Flow>()

  for (const inc of incoming) {
    const cur = currentById.get(inc.id)
    if (cur && cur.status_code != null && inc.status_code == null) {
      merged.set(cur.id, cur)
    } else {
      merged.set(inc.id, inc)
    }
  }

  // Preserve WS-only flows not present in REST response
  for (const [id, flow] of currentById) {
    if (!merged.has(id)) {
      merged.set(id, flow)
    }
  }

  return [...merged.values()]
    .sort((a, b) => b.timestamp.localeCompare(a.timestamp))
    .slice(0, MAX_FLOWS)
}
