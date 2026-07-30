interface MetricAnimationClock {
  now: () => number
  requestFrame: (callback: () => void) => number
  cancelFrame: (frameId: number) => void
}

interface StartMetricAnimationOptions {
  from: number
  to: number
  durationMs: number
  step?: number
  onUpdate: (value: number) => void
  clock?: MetricAnimationClock
}

const browserClock: MetricAnimationClock = {
  now: () => performance.now(),
  requestFrame: (callback) => window.requestAnimationFrame(callback),
  cancelFrame: (frameId) => window.cancelAnimationFrame(frameId),
}

export function resolveMetricAnimationStart(currentValue: number | undefined, resetToZero: boolean): number {
  return resetToZero ? 0 : currentValue ?? 0
}

export function resolveTokenAnimationStep(value: number, locale: string): number {
  const absoluteValue = Math.abs(value)
  if (locale.toLowerCase().startsWith('zh')) {
    const unit = [1_0000_0000_0000, 1_0000_0000, 1_0000]
      .find((threshold) => absoluteValue >= threshold)
    if (!unit) return 1

    const scaledValue = absoluteValue / unit
    const fractionDigits = scaledValue >= 100 ? 0 : scaledValue >= 10 ? 1 : 2
    return unit / (10 ** fractionDigits)
  }

  const unit = [1_000_000_000_000, 1_000_000_000, 1_000_000, 1_000]
    .find((threshold) => absoluteValue >= threshold)
  return unit ? unit / 10 : 1
}

export function resolveMoneyAnimationStep(value: number): number {
  const absoluteValue = Math.abs(value)
  if (absoluteValue >= 100) return 0.1
  if (absoluteValue >= 1) return 0.01
  return 0.0001
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
    if (textWeight >= maxTextWeight) {
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
  step,
  onUpdate,
  clock = browserClock,
}: StartMetricAnimationOptions): () => void {
  if (from === to || durationMs <= 0) {
    onUpdate(to)
    return () => {}
  }

  const startedAt = clock.now()
  let active = true
  let frameId = 0
  let lastValue = from

  const tick = () => {
    if (!active) return
    const progress = Math.min(Math.max((clock.now() - startedAt) / durationMs, 0), 1)
    const interpolatedValue = progress === 1 ? to : from + (to - from) * progress
    const nextValue = progress === 1
      ? to
      : quantizeMetricValue(interpolatedValue, step, from, to)
    if (nextValue !== lastValue) {
      lastValue = nextValue
      onUpdate(nextValue)
    }
    if (progress === 1) active = false
    else frameId = clock.requestFrame(tick)
  }

  onUpdate(from)
  frameId = clock.requestFrame(tick)
  return () => {
    if (active) clock.cancelFrame(frameId)
    active = false
  }
}

function quantizeMetricValue(value: number, step: number | undefined, from: number, to: number): number {
  if (!step || !Number.isFinite(step) || step <= 0) return value

  const roundedValue = Math.round(value / step) * step
  const clampedValue = Math.min(Math.max(roundedValue, Math.min(from, to)), Math.max(from, to))
  return Number(clampedValue.toPrecision(15))
}
