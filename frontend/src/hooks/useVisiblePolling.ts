import { useCallback, useEffect, useRef } from 'react'

interface VisiblePollingOptions {
  enabled?: boolean
  immediateOnVisible?: boolean
  skipWhenBusy?: boolean
}

function isDocumentVisible() {
  return typeof document === 'undefined' || document.visibilityState === 'visible'
}

export function useVisiblePolling(
  callback: () => void | Promise<unknown>,
  intervalMs: number,
  options: VisiblePollingOptions = {},
) {
  const { enabled = true, immediateOnVisible = true, skipWhenBusy = true } = options
  const callbackRef = useRef(callback)
  const busyRef = useRef(false)

  useEffect(() => {
    callbackRef.current = callback
  }, [callback])

  const runIfVisible = useCallback(() => {
    if (!enabled || !isDocumentVisible()) return
    if (skipWhenBusy && busyRef.current) return

    const result = callbackRef.current()
    if (result && typeof (result as Promise<unknown>).finally === 'function') {
      busyRef.current = true
      void Promise.resolve(result).finally(() => {
        busyRef.current = false
      })
    }
  }, [enabled, skipWhenBusy])

  useEffect(() => {
    if (!enabled || intervalMs <= 0) return

    const timer = window.setInterval(runIfVisible, intervalMs)
    const handleVisible = () => {
      if (document.visibilityState === 'visible' && immediateOnVisible) {
        runIfVisible()
      }
    }
    const handleFocus = () => {
      if (immediateOnVisible) {
        runIfVisible()
      }
    }

    document.addEventListener('visibilitychange', handleVisible)
    window.addEventListener('focus', handleFocus)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', handleVisible)
      window.removeEventListener('focus', handleFocus)
    }
  }, [enabled, immediateOnVisible, intervalMs, runIfVisible])
}
