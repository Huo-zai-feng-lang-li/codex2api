export const METRIC_ANIMATION_INTERVAL_MS = 32

interface MetricAnimationClock {
  now: () => number
  setInterval: (callback: () => void, intervalMs: number) => number
  clearInterval: (timerId: number) => void
}

interface StartMetricAnimationOptions {
  from: number
  to: number
  durationMs: number
  onUpdate: (value: number) => void
  clock?: MetricAnimationClock
}

const browserClock: MetricAnimationClock = {
  now: () => performance.now(),
  setInterval: (callback, intervalMs) => window.setInterval(callback, intervalMs),
  clearInterval: (timerId) => window.clearInterval(timerId),
}

export function resolveMetricAnimationStart(currentValue: number | undefined, resetToZero: boolean): number {
  return resetToZero ? 0 : currentValue ?? 0
}

export function resolveMetricSizingValue(
  currentValue: number | undefined,
  targetValue: number | undefined,
  format: (value?: number) => string,
): number | undefined {
  if (currentValue === undefined && targetValue === undefined) return undefined

  const from = currentValue ?? 0
  const to = targetValue ?? from
  let sizingValue = from
  let maxTextWeight = -1

  for (let index = 0; index <= 32; index++) {
    const value = from + (to - from) * (index / 32)
    const textWeight = Array.from(format(value)).reduce(
      (weight, character) => weight + (character.codePointAt(0)! > 0xff ? 2 : 1),
      0,
    )
    if (textWeight > maxTextWeight) {
      sizingValue = value
      maxTextWeight = textWeight
    }
  }

  return sizingValue
}

export function startMetricAnimation({
  from,
  to,
  durationMs,
  onUpdate,
  clock = browserClock,
}: StartMetricAnimationOptions): () => void {
  if (from === to || durationMs <= 0) {
    onUpdate(to)
    return () => {}
  }

  const startedAt = clock.now()
  let active = true
  let timerId = 0

  const tick = () => {
    const progress = Math.min(Math.max((clock.now() - startedAt) / durationMs, 0), 1)
    onUpdate(progress === 1 ? to : from + (to - from) * progress)
    if (progress === 1) {
      active = false
      clock.clearInterval(timerId)
    }
  }

  onUpdate(from)
  timerId = clock.setInterval(tick, METRIC_ANIMATION_INTERVAL_MS)
  return () => {
    if (active) clock.clearInterval(timerId)
    active = false
  }
}
