import assert from 'node:assert/strict'
import fs from 'node:fs'
import test from 'node:test'

const usageSource = fs.readFileSync(new URL('./Usage.tsx', import.meta.url), 'utf8')
const dashboardSource = fs.readFileSync(new URL('./Dashboard.tsx', import.meta.url), 'utf8')
const logsPanelSource = fs.readFileSync(new URL('../components/UsageLogsPanel.tsx', import.meta.url), 'utf8')
const rangeSelectorSource = fs.readFileSync(new URL('../components/UsageRangeSelector.tsx', import.meta.url), 'utf8')

test('keeps usage statistics range control independent from dashboard request logs', () => {
  assert.match(usageSource, /<UsageRangeSelector/)
  assert.doesNotMatch(usageSource, /getUsageLogsPaged|<UsageLogsPanel/)
  assert.match(rangeSelectorSource, /CustomRangePopover/)
  assert.match(rangeSelectorSource, /TIME_RANGE_OPTIONS/)
})

test('places the self-contained request log panel immediately below active requests', () => {
  assert.match(dashboardSource, /type DashboardRequestTab = 'usage_logs' \| 'error_details'/)
  assert.match(dashboardSource, /useState<DashboardRequestTab>\('usage_logs'\)/)
  assert.match(dashboardSource, /<ActiveRequestsPanel[^>]*\/>\s*<DashboardRequestTabs/s)
  assert.match(dashboardSource, /activeRequestTab === 'usage_logs'[\s\S]*<UsageLogsPanel\s*\/>/)
  assert.match(logsPanelSource, /api\.getUsageLogsPaged/)
  assert.match(logsPanelSource, /api\.getAPIKeys\(\)/)
  assert.match(logsPanelSource, /api\.getModels\(\)/)
  assert.match(logsPanelSource, /api\.clearUsageLogs\(\)/)
  assert.match(logsPanelSource, /usePersistedPageSize\('usage_logs'/)
  assert.match(logsPanelSource, /usePersistedTableColumns\(USAGE_VISIBLE_COLUMNS_KEY/)
  assert.match(logsPanelSource, /<UsageRangeSelector/)
  assert.match(logsPanelSource, /<Pagination/)
})

test('does not couple request log loading to dashboard polling', () => {
  const pollingBlock = dashboardSource.match(/useVisiblePolling\([\s\S]*?DASHBOARD_REFRESH_INTERVAL_MS[\s\S]*?\)/)?.[0] ?? ''
  assert.doesNotMatch(pollingBlock, /UsageLogs|loadLogs|getUsageLogsPaged/)
})

test('keeps operations error details lazy and manual inside dashboard tabs', () => {
  assert.match(dashboardSource, /const OperationsErrorsPanel = lazy\(/)
  assert.match(dashboardSource, /activeRequestTab === 'error_details'[\s\S]*<OperationsErrorsPanel\s+autoRefresh=\{false\}/)
  assert.doesNotMatch(dashboardSource, /getOpsErrors|getOpsErrorSummary/)
})
