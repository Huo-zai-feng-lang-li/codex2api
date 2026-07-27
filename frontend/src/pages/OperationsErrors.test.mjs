import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = fs.readFileSync(new URL('./OperationsErrors.tsx', import.meta.url), 'utf8')
const types = fs.readFileSync(new URL('../types.ts', import.meta.url), 'utf8')
const zh = JSON.parse(fs.readFileSync(new URL('../locales/zh.json', import.meta.url), 'utf8'))
const en = JSON.parse(fs.readFileSync(new URL('../locales/en.json', import.meta.url), 'utf8'))
const summaryModuleUrl = new URL('./operationsErrorSummary.ts', import.meta.url)

async function loadSummaryModule() {
  assert.equal(fs.existsSync(summaryModuleUrl), true, 'typed operations error summary module must exist')
  const moduleSource = fs.readFileSync(summaryModuleUrl, 'utf8')
  const output = ts.transpileModule(moduleSource, {
    compilerOptions: {
      module: ts.ModuleKind.ESNext,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText
  return import(`data:text/javascript;base64,${Buffer.from(output).toString('base64')}`)
}

test('declares request-level and retry error counters in the API type', () => {
  const summaryType = types.match(/export interface OpsErrorSummary \{([\s\S]*?)\n\}/)?.[1] ?? ''
  assert.match(summaryType, /\bterminal_errors:\s*number\b/)
  assert.match(summaryType, /\bretry_errors:\s*number\b/)
})

test('maps non-zero API counters to the four summary metrics at runtime', async () => {
  const { buildOpsErrorSummaryMetrics } = await loadSummaryModule()
  const summary = {
    total_errors: 22,
    terminal_errors: 11,
    retry_errors: 33,
    status_4xx: 45,
    status_5xx: 55,
    unauthorized: 401,
    rate_limited: 429,
    canceled: 499,
    timeouts: 66,
    retry_attempts: 44,
    avg_duration_ms: 77,
  }

  const metrics = buildOpsErrorSummaryMetrics(summary)
    .map(({ key, value, labelKey }) => ({ key, value, labelKey }))

  assert.deepEqual(metrics, [
    { key: 'terminal_errors', value: 11, labelKey: 'opsErrors.failedRequests' },
    { key: 'total_errors', value: 22, labelKey: 'opsErrors.attemptErrors' },
    { key: 'retry_errors', value: 33, labelKey: 'opsErrors.retryErrors' },
    { key: 'retry_attempts', value: 44, labelKey: 'opsErrors.retryAttempts' },
  ])
})

test('renders the four primary summary cards from the typed metric mapping', () => {
  assert.match(source, /buildOpsErrorSummaryMetrics\(data\.summary\)/)
  assert.match(source, /summaryMetrics\.map\(\(metric\)\s*=>/)
})

test('uses accurate Chinese and English labels for each error counter', () => {
  assert.equal(zh.opsErrors.failedRequests, '错误请求')
  assert.equal(zh.opsErrors.attemptErrors, '错误尝试')
  assert.equal(zh.opsErrors.retryErrors, '重试错误')
  assert.equal(zh.opsErrors.retryAttempts, '重试尝试')
  assert.equal(en.opsErrors.failedRequests, 'Failed Requests')
  assert.equal(en.opsErrors.attemptErrors, 'Attempt Errors')
  assert.equal(en.opsErrors.retryErrors, 'Retry Errors')
  assert.equal(en.opsErrors.retryAttempts, 'Retry Attempts')
})

test('keeps every error attempt returned by the detailed-list API', () => {
  assert.match(source, /api\.getOpsErrors\(/)
  assert.match(source, /logs:\s*pageResult\.logs\s*\?\?\s*\[\]/)
  assert.match(source, /data\.logs\.map\(\(log\)\s*=>/)
})
