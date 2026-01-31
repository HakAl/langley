import { useEffect } from 'react'
import type { View, Flow, TaskSummary, Anomaly } from '../types'

interface NavigableItems {
  flows: Flow[]
  tasks: TaskSummary[]
  tools: unknown[]
  anomalies: Anomaly[]
}

interface UseKeyboardNavOptions {
  view: View
  selectedIndex: number
  selectedFlow: Flow | null
  showHelp: boolean
  items: NavigableItems
  setSelectedIndex: (fn: (i: number) => number) => void
  setShowHelp: (fn: (h: boolean) => boolean) => void
  clearSelectedFlow: () => void
  navigateTo: (v: View) => void
  onEnter: (item: unknown) => void
}

export function useKeyboardNav({
  view,
  selectedIndex,
  selectedFlow,
  showHelp,
  items,
  setSelectedIndex,
  setShowHelp,
  clearSelectedFlow,
  navigateTo,
  onEnter,
}: UseKeyboardNavOptions) {
  useEffect(() => {
    const getItems = (): unknown[] => {
      switch (view) {
        case 'flows': return items.flows
        case 'tasks': return items.tasks
        case 'tools': return items.tools
        case 'anomalies': return items.anomalies
        default: return []
      }
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        if (e.key === 'Escape') (e.target as HTMLElement).blur()
        return
      }

      const list = getItems()

      switch (e.key) {
        case 'j':
          if (list.length > 0) setSelectedIndex(i => Math.min(i + 1, list.length - 1))
          break
        case 'k':
          if (list.length > 0) setSelectedIndex(i => Math.max(i - 1, 0))
          break
        case 'Enter':
          if (list.length > 0 && selectedIndex < list.length) onEnter(list[selectedIndex])
          break
        case '/':
          if (view === 'flows') e.preventDefault()
          break
        case 'Escape':
          if (showHelp) setShowHelp(() => false)
          else if (selectedFlow) clearSelectedFlow()
          break
        case '?':
          setShowHelp(h => !h)
          break
        case '1': navigateTo('flows'); break
        case '2': navigateTo('analytics'); break
        case '3': navigateTo('tasks'); break
        case '4': navigateTo('tools'); break
        case '5': navigateTo('anomalies'); break
        case '6': navigateTo('settings'); break
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [view, selectedIndex, selectedFlow, showHelp, items, setSelectedIndex, setShowHelp, clearSelectedFlow, navigateTo, onEnter])
}
