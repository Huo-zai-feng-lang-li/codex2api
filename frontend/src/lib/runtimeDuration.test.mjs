import assert from 'node:assert/strict'
import { Buffer } from 'node:buffer'
import fs from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

async function loadModule(name) {
  const url = new URL(name, import.meta.url)
  const source = fs.readFileSync(url, 'utf8')
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText
  return import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`)
}

const { advanceRuntimeDuration } = await loadModule('./runtimeDuration.ts')

test('advances from the server snapshot duration without depending on wall-clock timestamps', () => {
  assert.equal(advanceRuntimeDuration(5_000, 10_000, 11_500), 6_500)
  assert.equal(advanceRuntimeDuration(5_000, 10_000, 9_000), 5_000)
})
