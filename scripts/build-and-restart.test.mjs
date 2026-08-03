import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('../build-and-restart.bat', import.meta.url), 'utf8')
const position = (pattern) => source.search(pattern)

test('builds replacement artifacts before stopping the running service', () => {
  const frontendBuild = position(/call npm run build/i)
  const backendBuild = position(/go build -o "%NEW_EXE%" \./i)
  const stopService = position(/\.Kill\(\)/i)

  assert.ok(frontendBuild >= 0)
  assert.match(source, /set "NEW_EXE=codex2api\.new\.exe"/i)
  assert.ok(backendBuild > frontendBuild)
  assert.ok(stopService > backendBuild)
})

test('checks in-flight requests before the forced replacement', () => {
  const preflight = position(/responses_memory\.inflight_requests/i)
  const stopService = position(/\.Kill\(\)/i)

  assert.ok(preflight >= 0)
  assert.ok(stopService > preflight)
})

test('backs up the old binary and rolls back when health verification fails', () => {
  assert.match(source, /codex2api\.previous\.exe/i)
  assert.match(source, /\[IO\.File\]::Replace/i)
  assert.match(source, /:rollback/i)
  assert.match(source, /health check failed[\s\S]*goto rollback/i)
  assert.match(source, /Move-Item[\s\S]*PREVIOUS_EXE[\s\S]*codex2api\.exe/i)
})

test('is automation-safe and returns explicit success or failure', () => {
  assert.doesNotMatch(source, /\bpause\b/i)
  assert.doesNotMatch(source, /\btaskkill\b/i)
  assert.match(source, /GetFullPath\(\$p\.Path\)[\s\S]*\.Kill\(\)/i)
  assert.match(source, /netstat[\s\S]*18080[\s\S]*ownerPid[\s\S]*servicePid/i)
  assert.match(source, /status\s*-eq\s*'ok'/i)
  assert.match(source, /:wait_health[\s\S]*Get-Process -Id/i)
  assert.match(source, /Previous service restored successfully[\s\S]*del \/Q "%NEW_PID_FILE%"/i)
  assert.match(source, /exit \/b 0/i)
  assert.match(source, /exit \/b 1/i)
})
