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

const { createSSEParser } = await loadModule('./sse.ts')

test('parses a snapshot split across arbitrary chunks', () => {
  const events = []
  const parser = createSSEParser((event) => events.push(event))

  parser.push('event: snap')
  parser.push('shot\nid: 17\ndata: {"active_requests":1,')
  parser.push('"active_request_details":[]}\n\n')

  assert.deepEqual(events, [{
    event: 'snapshot',
    id: '17',
    data: '{"active_requests":1,"active_request_details":[]}',
  }])
})

test('supports CRLF, comments, multiline data, and retry fields', () => {
  const events = []
  const parser = createSSEParser((event) => events.push(event))

  parser.push(': heartbeat\r\nevent: snapshot\r\nid: 18\r\nretry: 2500\r\ndata: first\r\ndata: second\r\n\r\n')

  assert.deepEqual(events, [{
    event: 'snapshot',
    id: '18',
    retry: 2500,
    data: 'first\nsecond',
  }])
})

test('ignores invalid retry and dispatches a final complete frame on finish', () => {
  const events = []
  const parser = createSSEParser((event) => events.push(event))

  parser.push('event: snapshot\nretry: nope\ndata: {}')
  parser.finish()

  assert.deepEqual(events, [{ event: 'snapshot', data: '{}' }])
})
