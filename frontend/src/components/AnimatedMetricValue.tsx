import { useEffect, useRef } from 'react'
import {
  resolveMetricAnimationStart,
  resolveMetricSizingValue,
  startMetricAnimation,
} from '../lib/metricAnimation'

interface AnimatedMetricValueProps {
  id: string
  value?: number
  format: (value?: number) => string
  durationMs?: number
  animationKey?: string
  className?: string
}

const metricDisplayCache = new Map<string, number>()

export default function AnimatedMetricValue({
  id,
  value,
  format,
  durationMs = 3_000,
  animationKey,
  className,
}: AnimatedMetricValueProps) {
  const targetValue = typeof value === 'number' && Number.isFinite(value) ? value : undefined
  const displayValueRef = useRef<number | undefined>(
    targetValue === undefined ? undefined : metricDisplayCache.get(id) ?? 0,
  )
  const animationKeyRef = useRef(animationKey)
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

    const resetToZero = animationKey !== undefined && animationKeyRef.current !== animationKey
    animationKeyRef.current = animationKey
    const startValue = resolveMetricAnimationStart(
      displayValueRef.current ?? metricDisplayCache.get(id),
      resetToZero,
    )
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (startValue === targetValue || reduceMotion) {
      updateDisplay(targetValue)
      return
    }

    updateDisplay(startValue)
    return startMetricAnimation({
      from: startValue,
      to: targetValue,
      durationMs,
      onUpdate: updateDisplay,
    })
  }, [animationKey, durationMs, id, targetValue])

  const resetForSizing = animationKey !== undefined && animationKeyRef.current !== animationKey
  const sizingValue = resolveMetricSizingValue(
    resetForSizing ? 0 : displayValueRef.current,
    targetValue,
    format,
  )
  const displayText = format(displayValueRef.current)

  return (
    <span className={`inline-grid align-baseline ${className ?? ''}`}>
      <span
        aria-hidden="true"
        className="invisible col-start-1 row-start-1 whitespace-nowrap text-right tabular-nums"
      >
        {format(sizingValue)}
      </span>
      <span
        ref={valueNodeRef}
        className="col-start-1 row-start-1 whitespace-nowrap text-right tabular-nums"
        data-metric-id={id}
      >
        {displayText}
      </span>
    </span>
  )
}
