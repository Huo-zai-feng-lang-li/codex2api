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

const rawBody = await loadModule('./securityRawBody.ts')

test('formats nested JSON strings and folds opaque base64 payloads for review', () => {
  const formatted = rawBody.formatSecurityRawBody(JSON.stringify({
    type: 'response.output_item.done',
    item: {
      type: 'function_call',
      arguments: '{"url":"http://127.0.0.1:18080/admin/security-events"}',
    },
    image: 'A'.repeat(900),
  }))

  assert.equal(formatted.kind, 'json')
  assert.match(formatted.text, /"arguments": \{/)
  assert.match(formatted.text, /"url": "http:\/\/127\.0\.0\.1:18080\/admin\/security-events"/)
  assert.match(formatted.text, /base64-like payload/)
  assert.equal(formatted.folded, true)
})

test('keeps long prose readable instead of folding it as base64', () => {
  const prose = 'This upstream response is long but still normal reviewable text. '.repeat(80)
  const formatted = rawBody.formatSecurityRawBody(prose)

  assert.equal(formatted.kind, 'text')
  assert.equal(formatted.folded, false)
  assert.equal(formatted.text, prose)
})

test('infers rule field and match from legacy captures without field metadata', () => {
  const body = JSON.stringify({
    model: 'gpt-5',
    messages: [{
      role: 'user',
      content: 'OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNOP',
    }],
  })

  const evidence = rawBody.completeSecurityRuleEvidence({
    rule_id: 'dlp_token',
    evidence: 'request contains a real-looking access token',
  }, body)

  assert.equal(evidence.field, '$.messages[0].content')
  assert.match(evidence.match, /sk-proj-abcdefghijklmnopqrstuvwxyz/)
})
