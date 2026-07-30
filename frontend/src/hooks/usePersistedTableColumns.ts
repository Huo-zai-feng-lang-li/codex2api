import { useEffect, useMemo, useState } from 'react'
import {
  getOrderedVisibleColumns,
  normalizeTableColumnPreferences,
  type TableColumnDefinition,
  type TableColumnPreferences,
} from '../lib/tableColumns'

export function usePersistedTableColumns<Key extends string>(
  storageKey: string,
  definitions: readonly TableColumnDefinition<Key>[],
) {
  const [preferences, setPreferences] = useState<TableColumnPreferences<Key>>(() => {
    try {
      const stored = localStorage.getItem(storageKey)
      return normalizeTableColumnPreferences(definitions, stored ? JSON.parse(stored) : undefined)
    } catch {
      return normalizeTableColumnPreferences(definitions)
    }
  })

  useEffect(() => {
    try { localStorage.setItem(storageKey, JSON.stringify(preferences)) } catch { /* ignore storage failures */ }
  }, [preferences, storageKey])

  const visibleColumns = useMemo(
    () => getOrderedVisibleColumns(definitions, preferences),
    [definitions, preferences],
  )
  return { preferences, setPreferences, visibleColumns }
}
