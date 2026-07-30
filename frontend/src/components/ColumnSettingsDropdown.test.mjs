import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const source = fs.readFileSync(new URL('./ColumnSettingsDropdown.tsx', import.meta.url), 'utf8')

test('uses disclosure semantics and complete popover dismissal behavior', () => {
  assert.doesNotMatch(source, /role="menu"|aria-haspopup="menu"/)
  assert.match(source, /aria-controls=\{panelId\}/)
  assert.match(source, /role="group"/)
  assert.match(source, /event\.key === 'Escape'/)
  assert.match(source, /document\.addEventListener\('pointerdown'/)
  assert.match(source, /triggerRef\.current\?\.focus\(\)/)
})
