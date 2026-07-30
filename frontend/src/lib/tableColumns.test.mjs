import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import fs from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

async function loadModule(name) {
  const source = fs.readFileSync(new URL(name, import.meta.url), 'utf8')
  const compiled = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 },
  }).outputText
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
}

const columns = [
  { key: 'time', labelKey: 'time' },
  { key: 'status', labelKey: 'status' },
  { key: 'actions', labelKey: 'actions', hideable: false },
]

test('migrates legacy visibility and keeps all columns ordered', async () => {
  const { normalizeTableColumnPreferences } = await loadModule('./tableColumns.ts')
  assert.deepEqual(normalizeTableColumnPreferences(columns, { time: false, status: true }), {
    order: ['time', 'status', 'actions'],
    visibility: { time: false, status: true, actions: true },
  })
})

test('repairs duplicate unknown and missing order entries', async () => {
  const { normalizeTableColumnPreferences } = await loadModule('./tableColumns.ts')
  assert.deepEqual(normalizeTableColumnPreferences(columns, {
    order: ['status', 'unknown', 'status'],
    visibility: { time: true, status: false, actions: false },
  }), {
    order: ['status', 'time', 'actions'],
    visibility: { time: true, status: false, actions: true },
  })
})

test('restores the first data column when migrated preferences hide every data column', async () => {
  const { normalizeTableColumnPreferences } = await loadModule('./tableColumns.ts')
  const expected = {
    order: ['time', 'status', 'actions'],
    visibility: { time: true, status: false, actions: true },
  }
  assert.deepEqual(normalizeTableColumnPreferences(columns, { time: false, status: false }), expected)
  assert.deepEqual(normalizeTableColumnPreferences(columns, {
    order: ['time', 'status', 'actions'],
    visibility: { time: false, status: false, actions: false },
  }), expected)
})

test('ignores malformed visibility values instead of coercing them', async () => {
  const { normalizeTableColumnPreferences } = await loadModule('./tableColumns.ts')
  assert.deepEqual(normalizeTableColumnPreferences(columns, {
    time: 0,
    status: null,
    actions: false,
  }), {
    order: ['time', 'status', 'actions'],
    visibility: { time: true, status: true, actions: true },
  })
  assert.deepEqual(normalizeTableColumnPreferences(columns, {
    order: ['status', 'time', 'actions'],
    visibility: null,
    time: false,
  }), {
    order: ['status', 'time', 'actions'],
    visibility: { time: true, status: true, actions: true },
  })
})

test('prevents hiding a locked column or the final visible hideable column', async () => {
  const { toggleTableColumn } = await loadModule('./tableColumns.ts')
  const initial = { order: ['time', 'status', 'actions'], visibility: { time: true, status: false, actions: true } }
  assert.deepEqual(toggleTableColumn(columns, initial, 'actions'), initial)
  assert.deepEqual(toggleTableColumn(columns, initial, 'time'), initial)
})

test('moves a column to the requested position without changing visibility', async () => {
  const { moveTableColumn } = await loadModule('./tableColumns.ts')
  const initial = { order: ['time', 'status', 'actions'], visibility: { time: false, status: true, actions: true } }
  assert.deepEqual(moveTableColumn(initial, 'actions', 'time'), {
    order: ['actions', 'time', 'status'],
    visibility: initial.visibility,
  })
})
