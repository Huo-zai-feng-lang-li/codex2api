import type { ReactNode } from 'react'
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api'
import { getTimeRangeISO, getBucketConfig, type TimeRangeKey } from '../lib/timeRange'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import StatCard from '../components/StatCard'
import UsageStatsSummary from '../components/UsageStatsSummary'
import ActiveRequestsPanel from '../components/ActiveRequestsPanel'
import UsageLogsPanel from '../components/UsageLogsPanel'
import type { StatsResponse, UsageStats, ChartAggregation } from '../types'
import { useDataLoader } from '../hooks/useDataLoader'
import { useVisiblePolling } from '../hooks/useVisiblePolling'
import { useActiveRequestsStream } from '../hooks/useActiveRequestsStream'
import { Card, CardContent } from '@/components/ui/card'
import { Users, CheckCircle, Gauge, XCircle, Activity } from 'lucide-react'

const DashboardUsageCharts = lazy(() => import('../components/DashboardUsageCharts'))
const OperationsErrorsPanel = lazy(() => import('./OperationsErrors').then((module) => ({ default: module.OperationsErrorsPanel })))

const DASHBOARD_REFRESH_INTERVAL_MS = 15_000
const ACTIVE_REQUESTS_REFRESH_INTERVAL_MS = 3_000

type DashboardRequestTab = 'usage_logs' | 'error_details'

function ChartsSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
      {[0, 1, 2, 3].map((i) => (
        <Card key={i} className="py-0">
          <CardContent className="p-6">
            <div className="mb-5 space-y-2">
              <div className="h-4 w-32 rounded-md bg-muted animate-pulse" />
              <div className="h-3 w-48 rounded-md bg-muted/60 animate-pulse" />
            </div>
            <div className="h-[280px] flex items-end gap-2 px-4 pb-4">
              {[40, 65, 30, 80, 55, 70, 45, 60, 35, 75, 50, 68].map((h, j) => (
                <div
                  key={j}
                  className="flex-1 rounded-t-md bg-muted/50 animate-pulse"
                  style={{ height: `${h}%`, animationDelay: `${j * 80}ms` }}
                />
              ))}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

function RequestTabSkeleton() {
  return (
    <Card>
      <CardContent className="p-6">
        <div className="space-y-3">
          <div className="h-4 w-36 rounded-md bg-muted animate-pulse" />
          <div className="h-10 w-full rounded-md bg-muted/70 animate-pulse" />
          <div className="h-40 w-full rounded-md bg-muted/50 animate-pulse" />
        </div>
      </CardContent>
    </Card>
  )
}

function DashboardRequestTabs({
  activeTab,
  onTabChange,
}: {
  activeTab: DashboardRequestTab
  onTabChange: (tab: DashboardRequestTab) => void
}) {
  const { t } = useTranslation()
  const tabs: Array<{ key: DashboardRequestTab; label: string }> = [
    { key: 'usage_logs', label: t('dashboard.requestRecords') },
    { key: 'error_details', label: t('dashboard.errorDetails') },
  ]

  return (
    <div className="inline-flex shrink-0 rounded-lg border border-border bg-muted/50 p-0.5" role="tablist" aria-label={t('dashboard.requestDetailsTabs')}>
      {tabs.map((tab) => {
        const selected = activeTab === tab.key
        return (
          <button
            key={tab.key}
            type="button"
            role="tab"
            aria-selected={selected}
            onClick={() => onTabChange(tab.key)}
            className={`rounded-md px-2.5 py-1 text-xs font-medium transition-all duration-200 ${
              selected
                ? 'bg-background text-foreground shadow-sm'
                : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            {tab.label}
          </button>
        )
      })}
    </div>
  )
}

export default function Dashboard() {
  const { t } = useTranslation()
  const { requests: activeRequests, refreshActiveRequests } = useActiveRequestsStream()
  const [activeRequestTab, setActiveRequestTab] = useState<DashboardRequestTab>('usage_logs')
  const [timeRange, setTimeRange] = useState<TimeRangeKey>('1h')
  const [chartData, setChartData] = useState<ChartAggregation | null>(null)
  const [chartDataRange, setChartDataRange] = useState<TimeRangeKey | null>(null)
  const [chartRefreshedAt, setChartRefreshedAt] = useState<number | null>(null)
  const [chartLoading, setChartLoading] = useState(true)
  const chartAbort = useRef<AbortController | null>(null)

  const loadDashboardStats = useCallback(async () => {
    const [stats, usageStats] = await Promise.all([api.getStats(), api.getUsageStats()])
    return { stats, usageStats }
  }, [])

  const { data, loading, error, reload, reloadSilently } = useDataLoader<{
    stats: StatsResponse | null
    usageStats: UsageStats | null
  }>({
    initialData: { stats: null, usageStats: null },
    load: loadDashboardStats,
  })

  const loadChartData = useCallback(async () => {
    chartAbort.current?.abort()
    const controller = new AbortController()
    chartAbort.current = controller
    setChartLoading(true)
    try {
      const { start, end } = getTimeRangeISO(timeRange)
      const { bucketMinutes } = getBucketConfig(timeRange)
      const res = await api.getChartData({ start, end, bucketMinutes })
      if (!controller.signal.aborted) {
        setChartData(res)
        setChartDataRange(timeRange)
        setChartRefreshedAt(Date.now())
      }
    } catch {
      // 静默容错
    } finally {
      if (!controller.signal.aborted) setChartLoading(false)
    }
  }, [timeRange])

  useEffect(() => { void loadChartData() }, [loadChartData])

  useVisiblePolling(async () => {
    await Promise.all([reloadSilently(), loadChartData()])
  }, DASHBOARD_REFRESH_INTERVAL_MS, { enabled: timeRange === '1h' })

  useVisiblePolling(
    refreshActiveRequests,
    ACTIVE_REQUESTS_REFRESH_INTERVAL_MS,
    { enabled: activeRequests.length > 0, immediateOnVisible: true },
  )

  const { stats, usageStats } = data
  const total = stats?.total ?? 0
  const available = stats?.available ?? 0
  const rateLimited = stats?.rate_limited ?? 0
  const errorCount = stats?.error ?? 0
  const todayRequests = stats?.today_requests ?? 0
  const latencyLoading = chartLoading && chartData !== null && chartDataRange !== timeRange

  const icons: Record<string, ReactNode> = {
    total: <Users className="size-[22px]" />,
    available: <CheckCircle className="size-[22px]" />,
    rateLimited: <Gauge className="size-[22px]" />,
    error: <XCircle className="size-[22px]" />,
    requests: <Activity className="size-[22px]" />,
  }
  const requestTabs = <DashboardRequestTabs activeTab={activeRequestTab} onTabChange={setActiveRequestTab} />

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => { void reload(); void loadChartData() }}
      loadingTitle={t('dashboard.loadingTitle')}
      loadingDescription={t('dashboard.loadingDesc')}
      errorTitle={t('dashboard.errorTitle')}
    >
      <>
        <PageHeader
          title={t('dashboard.title')}
          description={t('dashboard.description')}
          onRefresh={() => { void reload(); void loadChartData() }}
        />

        {/* Account status */}
        <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-4 mb-6">
          <StatCard metricId="total" icon={icons.total} iconClass="blue" label={t('dashboard.totalAccounts')} value={total} />
          <StatCard metricId="available" icon={icons.available} iconClass="green" label={t('dashboard.available')} value={available} />
          <StatCard metricId="rate-limited" icon={icons.rateLimited} iconClass="amber" label={t('dashboard.rateLimited')} value={rateLimited} />
          <StatCard metricId="error" icon={icons.error} iconClass="red" label={t('dashboard.error')} value={errorCount} />
          <StatCard metricId="today-requests" icon={icons.requests} iconClass="purple" label={t('dashboard.todayRequests')} value={todayRequests} />
        </div>

        {/* Usage stats */}
        {usageStats && (
          <div className="space-y-6">
            <UsageStatsSummary
              stats={usageStats}
              firstTokenLatencyMs={chartData?.avg_first_token_ms}
              completionLatencyMs={chartData?.avg_duration_ms}
              latencyAnimationKey={chartDataRange ?? undefined}
              latencyLoading={latencyLoading}
            />
            <ActiveRequestsPanel requests={activeRequests} />

            {/* 稳定基底容器：设置 min-h 保底并配合淡入动画，避免空数据或高度变动引发布局跳跃 */}
            <div className="min-h-[440px] transition-all duration-200">
              {activeRequestTab === 'usage_logs' ? (
                <div key="usage_logs" className="animate-in fade-in duration-200">
                  <UsageLogsPanel
                    autoRefreshWhen={true}
                    headerAddon={requestTabs}
                  />
                </div>
              ) : (
                <div key="error_details" className="animate-in fade-in duration-200">
                  <Suspense fallback={<RequestTabSkeleton />}>
                    <OperationsErrorsPanel
                      autoRefresh={false}
                      headerAddon={requestTabs}
                    />
                  </Suspense>
                </div>
              )}
            </div>

            <Suspense fallback={<ChartsSkeleton />}>
              <DashboardUsageCharts
                chartData={chartData}
                refreshedAt={chartRefreshedAt}
                refreshIntervalMs={DASHBOARD_REFRESH_INTERVAL_MS}
                timeRange={timeRange}
                onTimeRangeChange={setTimeRange}
                loading={chartLoading}
              />
            </Suspense>
          </div>
        )}
      </>
    </StateShell>
  )
}
