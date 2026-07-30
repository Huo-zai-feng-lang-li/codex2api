export interface TableColumnDefinition<Key extends string> {
  key: Key
  labelKey: string
  hideable?: boolean
}

export interface TableColumnPreferences<Key extends string> {
  order: Key[]
  visibility: Record<Key, boolean>
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

function isPreferenceShape(value: unknown): value is { order?: unknown; visibility?: unknown } {
  return isRecord(value) && ('order' in value || 'visibility' in value)
}

export function normalizeTableColumnPreferences<Key extends string>(
  definitions: readonly TableColumnDefinition<Key>[],
  stored?: unknown,
): TableColumnPreferences<Key> {
  const keys = definitions.map(({ key }) => key)
  const allowed = new Set<string>(keys)
  const preference = isPreferenceShape(stored) ? stored : undefined
  const storedOrder = Array.isArray(preference?.order) ? preference.order : []
  const order = storedOrder
    .filter((key): key is Key => typeof key === 'string' && allowed.has(key))
    .filter((key, index, values) => values.indexOf(key) === index)
  for (const key of keys) if (!order.includes(key)) order.push(key)

  const storedVisibility = preference
    ? isRecord(preference.visibility) ? preference.visibility : {}
    : isRecord(stored) ? stored : {}
  const visibility = Object.fromEntries(definitions.map((definition) => [
    definition.key,
    definition.hideable === false || typeof storedVisibility[definition.key] !== 'boolean'
      ? true
      : storedVisibility[definition.key],
  ])) as Record<Key, boolean>

  const hideableColumns = definitions.filter((definition) => definition.hideable !== false)
  if (hideableColumns.length > 0 && !hideableColumns.some((definition) => visibility[definition.key])) {
    visibility[hideableColumns[0].key] = true
  }

  return { order, visibility }
}

export function toggleTableColumn<Key extends string>(
  definitions: readonly TableColumnDefinition<Key>[],
  preferences: TableColumnPreferences<Key>,
  key: Key,
): TableColumnPreferences<Key> {
  const definition = definitions.find((column) => column.key === key)
  if (!definition || definition.hideable === false) return preferences
  const visibleHideableCount = definitions.filter((column) => column.hideable !== false && preferences.visibility[column.key]).length
  if (preferences.visibility[key] && visibleHideableCount === 1) return preferences
  return { ...preferences, visibility: { ...preferences.visibility, [key]: !preferences.visibility[key] } }
}

export function reorderTableColumn<Key extends string>(
  preferences: TableColumnPreferences<Key>,
  key: Key,
  targetIndex: number,
): TableColumnPreferences<Key> {
  const sourceIndex = preferences.order.indexOf(key)
  if (sourceIndex < 0) return preferences
  const order = [...preferences.order]
  order.splice(sourceIndex, 1)
  order.splice(Math.max(0, Math.min(targetIndex, order.length)), 0, key)
  return { ...preferences, order }
}

export function moveTableColumn<Key extends string>(
  preferences: TableColumnPreferences<Key>,
  key: Key,
  beforeKey: Key,
): TableColumnPreferences<Key> {
  if (key === beforeKey) return preferences
  const targetIndex = preferences.order.filter((candidate) => candidate !== key).indexOf(beforeKey)
  return targetIndex < 0 ? preferences : reorderTableColumn(preferences, key, targetIndex)
}

export function getOrderedVisibleColumns<Key extends string>(
  definitions: readonly TableColumnDefinition<Key>[],
  preferences: TableColumnPreferences<Key>,
): TableColumnDefinition<Key>[] {
  const byKey = new Map(definitions.map((definition) => [definition.key, definition]))
  return preferences.order.flatMap((key) => preferences.visibility[key] && byKey.has(key) ? [byKey.get(key)!] : [])
}
