import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const source = fs.readFileSync(new URL('./Usage.tsx', import.meta.url), 'utf8')

test('drives usage table headers and cells from persisted ordered columns', () => {
  assert.match(source, /usePersistedTableColumns\(USAGE_VISIBLE_COLUMNS_KEY/)
  assert.match(source, /<ColumnSettingsDropdown/)
  assert.match(source, /visibleColumns\.map\(\(column\)\s*=>\s*\(/)
  assert.match(source, /visibleColumns\.map\(\(column\)\s*=>\s*renderUsageCell\(column\.key, log\)\)/)
})
