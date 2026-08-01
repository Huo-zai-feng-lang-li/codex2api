import { useEffect, useState } from 'react'
import { AdminUnauthorizedError, api } from '../api'
import type { RuntimeActiveRequest } from '../types'

const INITIAL_RECONNECT_DELAY_MS = 1_000
const MAX_RECONNECT_DELAY_MS = 15_000

export function useActiveRequestsStream() {
  const [requests, setRequests] = useState<RuntimeActiveRequest[]>([])

  useEffect(() => {
    let stopped = false
    let reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS
    let reconnectTimer: number | undefined
    let controller: AbortController | undefined

    const scheduleReconnect = () => {
      if (stopped) return
      reconnectTimer = window.setTimeout(connect, reconnectDelayMs)
      reconnectDelayMs = Math.min(reconnectDelayMs * 2, MAX_RECONNECT_DELAY_MS)
    }

    const connect = async () => {
      controller = new AbortController()
      try {
        await api.streamActiveRequests({
          signal: controller.signal,
          onSnapshot: (snapshot) => {
            if (stopped) return
            reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS
            setRequests(snapshot.active_request_details)
          },
        })
        scheduleReconnect()
      } catch (error) {
        if (stopped || controller.signal.aborted || error instanceof AdminUnauthorizedError) return
        scheduleReconnect()
      }
    }

    void connect()
    return () => {
      stopped = true
      controller?.abort()
      if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
    }
  }, [])

  return requests
}
