import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const dashboardSource = readFileSync(new URL('./Dashboard.tsx', import.meta.url), 'utf8')
const hookSource = readFileSync(new URL('../hooks/useActiveRequestsStream.ts', import.meta.url), 'utf8')
const apiSource = readFileSync(new URL('../api.ts', import.meta.url), 'utf8')

assert.match(
  hookSource,
  /refreshActiveRequests/,
  'useActiveRequestsStream should expose a refreshActiveRequests callback for polling fallback',
)

assert.match(
  apiSource,
  /getActiveRequestsSnapshot/,
  'api should expose a lightweight active request snapshot fetcher',
)

assert.match(
  dashboardSource,
  /ACTIVE_REQUESTS_REFRESH_INTERVAL_MS\s*=\s*3_000/,
  'Dashboard should use a 3 second active request refresh interval',
)

assert.match(
  dashboardSource,
  /enabled:\s*activeRequests\.length\s*>\s*0/,
  'Dashboard active request polling should run only while active requests exist',
)

assert.match(
  dashboardSource,
  /useVisiblePolling\(\s*refreshActiveRequests,\s*ACTIVE_REQUESTS_REFRESH_INTERVAL_MS/s,
  'Dashboard should wire visible polling to the active request refresh callback',
)

console.log('Dashboard active request visible polling contract verified')
