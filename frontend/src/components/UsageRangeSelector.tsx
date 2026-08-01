import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { getTimeRangeISO, type TimeRangeKey } from '../lib/timeRange'
import { Button } from '@/components/ui/button'

const TIME_RANGE_OPTIONS: TimeRangeKey[] = ['1h', '6h', '24h', '7d', '30d']
const CUSTOM_RANGE_MAX_DAYS = 90
const CUSTOM_RANGE_MAX_MS = CUSTOM_RANGE_MAX_DAYS * 24 * 60 * 60 * 1000

export type UsageTimeRangeKey = TimeRangeKey | 'custom'

export interface CustomRange {
  start: string
  end: string
}

interface UsageRangeSelectorProps {
  value: UsageTimeRangeKey
  customRange: CustomRange | null
  onChange: (value: UsageTimeRangeKey, customRange: CustomRange | null) => void
  className?: string
}

export function resolveUsageRangeISO(
  range: UsageTimeRangeKey,
  custom: CustomRange | null,
): { start: string; end: string } {
  if (range === 'custom' && custom) {
    return { start: custom.start, end: custom.end }
  }
  return getTimeRangeISO(range === 'custom' ? '24h' : range)
}

export default function UsageRangeSelector({
  value,
  customRange,
  onChange,
  className = '',
}: UsageRangeSelectorProps) {
  const { t } = useTranslation()
  const customChipRef = useRef<HTMLButtonElement>(null)
  const [showCustomPopover, setShowCustomPopover] = useState(false)

  return (
    <div className={`relative inline-flex shrink-0 rounded-lg border border-border bg-muted/50 p-0.5 ${className}`}>
      {TIME_RANGE_OPTIONS.map((key) => (
        <button
          key={key}
          type="button"
          onClick={() => {
            onChange(key, customRange)
            setShowCustomPopover(false)
          }}
          className={`whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium transition-all duration-200 ${
            value === key
              ? 'border border-border bg-background text-foreground shadow-sm'
              : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          {t(`dashboard.timeRange${key.toUpperCase()}`)}
        </button>
      ))}
      <button
        ref={customChipRef}
        type="button"
        onClick={() => setShowCustomPopover((current) => !current)}
        className={`whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium transition-all duration-200 ${
          value === 'custom'
            ? 'border border-border bg-background text-foreground shadow-sm'
            : 'text-muted-foreground hover:text-foreground'
        }`}
      >
        {value === 'custom' && customRange
          ? t('usage.customRangeChipApplied')
          : t('usage.customRange')}
      </button>
      {showCustomPopover && (
        <CustomRangePopover
          anchorRef={customChipRef}
          initial={customRange}
          onCancel={() => setShowCustomPopover(false)}
          onApply={(range) => {
            onChange('custom', range)
            setShowCustomPopover(false)
          }}
        />
      )}
    </div>
  )
}

function dateToLocalInputValue(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function localInputValueToDate(value: string): Date | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

function dateToLocalRFC3339(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  const offset = date.getTimezoneOffset()
  const sign = offset <= 0 ? '+' : '-'
  const absoluteOffset = Math.abs(offset)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${sign}${pad(Math.floor(absoluteOffset / 60))}:${pad(absoluteOffset % 60)}`
}

function CustomRangePopover({
  anchorRef,
  initial,
  onApply,
  onCancel,
}: {
  anchorRef: React.RefObject<HTMLButtonElement | null>
  initial: CustomRange | null
  onApply: (range: CustomRange) => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const now = new Date()
  const defaultEnd = initial ? new Date(initial.end) : now
  const defaultStart = initial
    ? new Date(initial.start)
    : new Date(now.getTime() - 24 * 60 * 60 * 1000)
  const [startStr, setStartStr] = useState(dateToLocalInputValue(defaultStart))
  const [endStr, setEndStr] = useState(dateToLocalInputValue(defaultEnd))
  const [error, setError] = useState<string | null>(null)
  const popoverRef = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null)
  const popoverWidth = 320

  const recompute = useCallback(() => {
    const anchor = anchorRef.current
    if (!anchor) return
    const rect = anchor.getBoundingClientRect()
    const desiredLeft = rect.right - popoverWidth
    setPosition({
      top: rect.bottom + 6,
      left: Math.max(8, Math.min(window.innerWidth - popoverWidth - 8, desiredLeft)),
    })
  }, [anchorRef])

  useLayoutEffect(() => {
    recompute()
  }, [recompute])

  useEffect(() => {
    const handle = () => recompute()
    window.addEventListener('resize', handle)
    window.addEventListener('scroll', handle, true)
    return () => {
      window.removeEventListener('resize', handle)
      window.removeEventListener('scroll', handle, true)
    }
  }, [recompute])

  useEffect(() => {
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null
      if (!target || popoverRef.current?.contains(target) || anchorRef.current?.contains(target)) return
      onCancel()
    }
    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCancel()
    }
    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [anchorRef, onCancel])

  const handleApply = () => {
    const startDate = localInputValueToDate(startStr)
    const endDate = localInputValueToDate(endStr)
    if (!startDate || !endDate) {
      setError(t('usage.customRangeInvalid'))
      return
    }
    if (endDate.getTime() <= startDate.getTime()) {
      setError(t('usage.customRangeEndBeforeStart'))
      return
    }
    if (endDate.getTime() - startDate.getTime() > CUSTOM_RANGE_MAX_MS) {
      setError(t('usage.customRangeTooLong', { days: CUSTOM_RANGE_MAX_DAYS }))
      return
    }
    onApply({ start: dateToLocalRFC3339(startDate), end: dateToLocalRFC3339(endDate) })
  }

  if (!position) return null

  return createPortal(
    <div
      ref={popoverRef}
      style={{ position: 'fixed', top: position.top, left: position.left, width: popoverWidth }}
      className="z-[1000] rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-[0_18px_40px_hsl(222_30%_18%/0.18)]"
    >
      <div className="mb-2 text-xs font-semibold text-foreground">{t('usage.customRangeTitle')}</div>
      <div className="space-y-2">
        <label className="block text-[11px] text-muted-foreground">
          {t('usage.customRangeStart')}
          <input
            type="datetime-local"
            value={startStr}
            onChange={(event) => setStartStr(event.target.value)}
            className="mt-1 block w-full rounded-md border border-border bg-background px-2 py-1 text-xs"
          />
        </label>
        <label className="block text-[11px] text-muted-foreground">
          {t('usage.customRangeEnd')}
          <input
            type="datetime-local"
            value={endStr}
            onChange={(event) => setEndStr(event.target.value)}
            className="mt-1 block w-full rounded-md border border-border bg-background px-2 py-1 text-xs"
          />
        </label>
      </div>
      {error && <div className="mt-2 text-[11px] text-destructive">{error}</div>}
      <div className="mt-3 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t('common.cancel', { defaultValue: 'Cancel' })}
        </Button>
        <Button size="sm" onClick={handleApply}>{t('usage.customRangeApply')}</Button>
      </div>
    </div>,
    document.body,
  )
}
