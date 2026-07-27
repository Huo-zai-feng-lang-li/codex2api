import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const moduleUrl = new URL('./accountAvailability.ts', import.meta.url)

async function loadModule() {
  assert.equal(fs.existsSync(moduleUrl), true, 'typed account availability module must exist')
  const output = ts.transpileModule(fs.readFileSync(moduleUrl, 'utf8'), {
    compilerOptions: {
      module: ts.ModuleKind.ESNext,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText
  return import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`)
}

test('normal accounts require the backend availability truth', async () => {
  const { isNormalAccount } = await loadModule()

  assert.equal(isNormalAccount({ status: 'active', enabled: true, is_available: true }), true)
  assert.equal(isNormalAccount({ status: 'ready', enabled: true, is_available: true }), true)
  assert.equal(isNormalAccount({ status: 'active', enabled: true, is_available: false }), false)
})

test('backend availability remains authoritative during mixed-snapshot updates', async () => {
  const { isNormalAccount } = await loadModule()
  const account = {
    status: 'active',
    enabled: true,
    is_available: true,
    model_cooldowns: [{ model: 'gpt-5.4', reason: 'rate_limited_model', remaining_seconds: 60 }],
  }

  assert.equal(isNormalAccount(account), true)
})

test('legacy responses still exclude active model cooldowns', async () => {
  const { isNormalAccount } = await loadModule()
  const account = {
    status: 'active',
    enabled: true,
    model_cooldowns: [{ model: 'gpt-5.4', reason: 'rate_limited_model', remaining_seconds: 60 }],
  }

  assert.equal(isNormalAccount(account), false)
})

test('normal count and normal rows share exactly one predicate', async () => {
  const { countNormalAccounts, isNormalAccount } = await loadModule()
  const accounts = [
    { id: 1, status: 'active', enabled: true, is_available: true },
    { id: 2, status: 'active', enabled: true, is_available: false },
    { id: 3, status: 'rate_limited', enabled: true, is_available: false },
    { id: 4, status: 'error', enabled: true, is_available: false },
    { id: 5, status: 'active', enabled: false, is_available: false },
  ]

  const rows = accounts.filter(isNormalAccount)
  assert.equal(countNormalAccounts(accounts), rows.length)
  assert.deepEqual(rows.map((account) => account.id), [1])
})
