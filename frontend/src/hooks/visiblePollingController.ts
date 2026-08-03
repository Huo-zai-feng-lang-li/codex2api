interface VisiblePollingControllerOptions {
  intervalMs: number
  immediateOnVisible: boolean
  isVisible: () => boolean
  run: () => void
  setTimer: (callback: () => void, intervalMs: number) => number
  clearTimer: (timer: number) => void
}

export function createVisiblePollingController(options: VisiblePollingControllerOptions) {
  let timer: number | undefined

  const stopPolling = () => {
    if (timer === undefined) return
    options.clearTimer(timer)
    timer = undefined
  }

  const startPolling = () => {
    stopPolling()
    if (!options.isVisible()) return
    timer = options.setTimer(options.run, options.intervalMs)
  }

  const handleVisibilityChange = () => {
    if (!options.isVisible()) {
      stopPolling()
      return
    }
    if (options.immediateOnVisible) options.run()
    startPolling()
  }

  startPolling()
  return { handleVisibilityChange, dispose: stopPolling }
}
