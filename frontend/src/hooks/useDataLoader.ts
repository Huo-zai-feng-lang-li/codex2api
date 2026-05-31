import { useCallback, useEffect, useRef, useState } from 'react'
import { getErrorMessage } from '../utils/error'

export interface LoadOptions {
  silent?: boolean
}

interface UseDataLoaderOptions<T> {
  initialData: T
  load: (options?: LoadOptions) => Promise<T>
  onError?: (message: string, error: unknown) => void
}

export function useDataLoader<T>({ initialData, load, onError }: UseDataLoaderOptions<T>) {
  const [data, setData] = useState<T>(initialData)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const activeRequestId = useRef(0)
  const inFlight = useRef<Promise<T | null> | null>(null)

  const run = useCallback(async (options: LoadOptions = {}) => {
    const { silent = false } = options
    if (silent && inFlight.current) {
      return inFlight.current
    }

    if (!silent) {
      setLoading(true)
      setError(null)
    }

    const requestId = activeRequestId.current + 1
    activeRequestId.current = requestId

    const request = (async () => {
      const nextData = await load(options)
      if (activeRequestId.current === requestId) {
        setData(nextData)
        setError(null)
      }
      return nextData
    })()

    const trackedRequest = request.catch((err) => {
      if (activeRequestId.current === requestId) {
        const message = getErrorMessage(err)
        if (!silent) {
          setError(message)
        }
        onError?.(message, err)
      }
      return null
    }).finally(() => {
      if (inFlight.current === trackedRequest) {
        inFlight.current = null
      }
      if (!silent && activeRequestId.current === requestId) {
        setLoading(false)
      }
    })

    inFlight.current = trackedRequest
    return inFlight.current
  }, [load, onError])

  useEffect(() => {
    void run()
  }, [run])

  const reload = useCallback(() => run(), [run])
  const reloadSilently = useCallback(() => run({ silent: true }), [run])

  return {
    data,
    setData,
    loading,
    error,
    reload,
    reloadSilently,
  }
}
