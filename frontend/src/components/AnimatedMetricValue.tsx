import { useEffect, useRef } from 'react'

interface AnimatedMetricValueProps {
  id: string
  value?: number
  format: (value?: number) => string
  durationMs?: number
  className?: string
}

const metricDisplayCache = new Map<string, number>()

export default function AnimatedMetricValue({
  id,
  value,
  format,
  durationMs = 1_000,
  className,
}: AnimatedMetricValueProps) {
  const targetValue = typeof value === 'number' && Number.isFinite(value) ? value : undefined
  const displayValueRef = useRef<number | undefined>(
    targetValue === undefined ? undefined : metricDisplayCache.get(id) ?? targetValue,
  )
  const valueNodeRef = useRef<HTMLSpanElement>(null)
  const formatRef = useRef(format)
  formatRef.current = format

  useEffect(() => {
    const valueNode = valueNodeRef.current
    const updateDisplay = (nextValue?: number) => {
      displayValueRef.current = nextValue
      if (nextValue === undefined) {
        metricDisplayCache.delete(id)
      } else {
        metricDisplayCache.set(id, nextValue)
      }
      if (valueNode) valueNode.textContent = formatRef.current(nextValue)
    }

    if (targetValue === undefined) {
      updateDisplay()
      return
    }

    const startValue = displayValueRef.current ?? metricDisplayCache.get(id)
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (startValue === undefined || startValue === targetValue || reduceMotion) {
      updateDisplay(targetValue)
      return
    }

    let startedAt: number | null = null
    let frameId = 0
    const animate = (now: number) => {
      startedAt ??= now
      const progress = Math.min((now - startedAt) / durationMs, 1)
      const easedProgress = 1 - (1 - progress) ** 3
      updateDisplay(startValue + (targetValue - startValue) * easedProgress)
      if (progress < 1) frameId = requestAnimationFrame(animate)
    }

    frameId = requestAnimationFrame(animate)
    return () => cancelAnimationFrame(frameId)
  }, [durationMs, id, targetValue])

  return (
    <span ref={valueNodeRef} className={className} data-metric-id={id}>
      {format(displayValueRef.current)}
    </span>
  )
}
