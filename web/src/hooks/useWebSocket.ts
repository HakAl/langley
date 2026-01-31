import { useCallback, useRef } from 'react'
import type { Flow, WSMessage } from '../types'

const MAX_RECONNECT_ATTEMPTS = 5

interface UseWebSocketOptions {
  onFlowUpdate: (flow: Flow) => void
  onConnectedChange: (connected: boolean) => void
  onError: (error: string | null) => void
}

export function useWebSocket({ onFlowUpdate, onConnectedChange, onError }: UseWebSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimeoutRef = useRef<number | null>(null)
  const reconnectAttemptsRef = useRef(0)

  const connect = useCallback(() => {
    if (wsRef.current?.readyState === WebSocket.OPEN) return

    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
      reconnectTimeoutRef.current = null
    }

    const ws = new WebSocket(`ws://${window.location.host}/ws`)

    ws.onopen = () => {
      onConnectedChange(true)
      onError(null)
      reconnectAttemptsRef.current = 0
    }

    ws.onclose = () => {
      onConnectedChange(false)
      wsRef.current = null

      if (reconnectAttemptsRef.current < MAX_RECONNECT_ATTEMPTS) {
        const delay = Math.min(1000 * Math.pow(2, reconnectAttemptsRef.current), 30000)
        reconnectAttemptsRef.current++
        reconnectTimeoutRef.current = window.setTimeout(() => connect(), delay)
      } else {
        onError('Connection lost. Refresh to reconnect.')
      }
    }

    ws.onerror = () => {
      onError('WebSocket connection failed')
      onConnectedChange(false)
    }

    ws.onmessage = (event) => {
      try {
        const lines = event.data.split('\n')
        for (const line of lines) {
          if (!line.trim()) continue
          const msg: WSMessage = JSON.parse(line)
          if (msg.type === 'flow_start' || msg.type === 'flow_complete' || msg.type === 'flow_update') {
            onFlowUpdate(msg.data)
          }
        }
      } catch (e) {
        console.error('Failed to parse message:', e)
      }
    }

    wsRef.current = ws
  }, [onFlowUpdate, onConnectedChange, onError])

  const cleanup = useCallback(() => {
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
      reconnectTimeoutRef.current = null
    }
    wsRef.current?.close()
  }, [])

  return { connect, cleanup }
}
