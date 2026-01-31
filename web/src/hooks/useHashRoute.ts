import { useState, useEffect, useCallback } from 'react'
import type { View } from '../types'
import { parseHash } from '../utils'

export function useHashRoute(): [View, (v: View) => void] {
  const [view, setView] = useState<View>(() => parseHash(window.location.hash))

  const navigateTo = useCallback((v: View) => {
    setView(v)
    window.history.pushState(null, '', `#${v}`)
  }, [])

  useEffect(() => {
    const onPopState = () => setView(parseHash(window.location.hash))
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  return [view, navigateTo]
}
