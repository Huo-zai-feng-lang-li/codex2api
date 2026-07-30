import { useEffect, useId, useRef, useState, type DragEvent, type KeyboardEvent } from 'react'
import { ChevronDown, ChevronUp, GripVertical, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  reorderTableColumn,
  toggleTableColumn,
  type TableColumnDefinition,
  type TableColumnPreferences,
} from '../lib/tableColumns'

interface ColumnSettingsDropdownProps<Key extends string> {
  definitions: readonly TableColumnDefinition<Key>[]
  preferences: TableColumnPreferences<Key>
  onChange: (preferences: TableColumnPreferences<Key>) => void
}

export default function ColumnSettingsDropdown<Key extends string>({
  definitions,
  preferences,
  onChange,
}: ColumnSettingsDropdownProps<Key>) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draggedKey, setDraggedKey] = useState<Key | null>(null)
  const panelId = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const byKey = new Map(definitions.map((definition) => [definition.key, definition]))
  const ordered = preferences.order.flatMap((key) => byKey.has(key) ? [byKey.get(key)!] : [])

  useEffect(() => {
    if (!open) return
    panelRef.current?.querySelector<HTMLElement>('input:not([disabled]), button:not([disabled])')?.focus()
    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target as Node
      if (!panelRef.current?.contains(target) && !triggerRef.current?.contains(target)) setOpen(false)
    }
    document.addEventListener('pointerdown', closeOnOutsidePointer)
    return () => document.removeEventListener('pointerdown', closeOnOutsidePointer)
  }, [open])

  const closeOnEscape = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      setOpen(false)
      triggerRef.current?.focus()
    }
  }

  const dropAtPointer = (event: DragEvent<HTMLDivElement>, key: Key) => {
    event.preventDefault()
    if (draggedKey) {
      const sourceIndex = preferences.order.indexOf(draggedKey)
      const hoveredIndex = preferences.order.indexOf(key)
      const bounds = event.currentTarget.getBoundingClientRect()
      const insertAfter = event.clientY > bounds.top + bounds.height / 2
      const rawTargetIndex = hoveredIndex + (insertAfter ? 1 : 0)
      const targetIndex = sourceIndex < rawTargetIndex ? rawTargetIndex - 1 : rawTargetIndex
      onChange(reorderTableColumn(preferences, draggedKey, targetIndex))
    }
    setDraggedKey(null)
  }

  return (
    <div className="relative">
      <Button ref={triggerRef} type="button" variant="outline" size="sm" onClick={() => setOpen((value) => !value)} aria-controls={panelId} aria-expanded={open}>
        <SlidersHorizontal className="size-3.5" />
        {t('accounts.columnSettings')}
      </Button>
      {open && (
        <div ref={panelRef} id={panelId} role="group" aria-label={t('accounts.columnSettings')} onKeyDown={closeOnEscape} className="absolute right-0 z-20 mt-2 w-64 rounded-lg border border-border bg-popover p-2 text-popover-foreground shadow-lg">
          <div className="mb-1 px-2 py-1 text-[11px] font-semibold uppercase text-muted-foreground">{t('accounts.columnSettings')}</div>
          {ordered.map((column, index) => (
            <div
              key={column.key}
              draggable
              onDragStart={(event) => {
                event.dataTransfer.effectAllowed = 'move'
                setDraggedKey(column.key)
              }}
              onDragEnd={() => setDraggedKey(null)}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => dropAtPointer(event, column.key)}
              className="flex items-center gap-1 rounded-md px-1 py-1 hover:bg-muted"
            >
              <GripVertical className="size-4 shrink-0 cursor-grab text-muted-foreground" aria-hidden="true" />
              <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 px-1 text-[13px]">
                <input
                  type="checkbox"
                  className="size-3.5 rounded border-border"
                  checked={preferences.visibility[column.key]}
                  disabled={column.hideable === false}
                  onChange={() => onChange(toggleTableColumn(definitions, preferences, column.key))}
                />
                <span className="truncate">{t(column.labelKey)}</span>
              </label>
              <Button type="button" variant="ghost" size="icon-xs" disabled={index === 0} aria-label={`${t(column.labelKey)} ↑`} onClick={() => onChange(reorderTableColumn(preferences, column.key, index - 1))}>
                <ChevronUp className="size-3.5" />
              </Button>
              <Button type="button" variant="ghost" size="icon-xs" disabled={index === ordered.length - 1} aria-label={`${t(column.labelKey)} ↓`} onClick={() => onChange(reorderTableColumn(preferences, column.key, index + 1))}>
                <ChevronDown className="size-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
