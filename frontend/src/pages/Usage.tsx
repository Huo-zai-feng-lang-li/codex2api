import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Cell, Pie, PieChart, ResponsiveContainer, Tooltip as RechartsTooltip } from 'recharts'
import { api } from '../api'
import { chartInitialDimensions } from '../lib/chartDimensions'
import PageHeader from '../components/PageHeader'
import StateShell from '../components/StateShell'
import UsageRangeSelector, { resolveUsageRangeISO, type CustomRange, type UsageTimeRangeKey } from '../components/UsageRangeSelector'
import { useDataLoader } from '../hooks/useDataLoader'
import { useVisiblePolling } from '../hooks/useVisiblePolling'
import type { SystemSettings, UsageAPIKeyStat, UsageEndpointStat, UsageFeatureStats, UsageModelStat, UsageStats } from '../types'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Activity, Box, Clock, Zap, AlertTriangle, CircleDollarSign, BarChart3, KeyRound, Route } from 'lucide-react'

function formatTokens(value?: number | null, showFullNumbers = false): string {
  if (value === undefined || value === null) return '0'
  const numericValue = Number(value)
  if (!Number.isFinite(numericValue)) return '0'
  const roundedValue = Math.round(numericValue)
  if (showFullNumbers) return roundedValue.toLocaleString()

  const absValue = Math.abs(numericValue)
  const units = [
    { value: 1_000_000_000_000, suffix: 'T' },
    { value: 1_000_000_000, suffix: 'B' },
    { value: 1_000_000, suffix: 'M' },
    { value: 1_000, suffix: 'K' },
  ]
  const unit = units.find((item) => absValue >= item.value)
  if (!unit) return roundedValue.toLocaleString()

  const scaled = numericValue / unit.value
  const fractionDigits = Math.abs(scaled) >= 100 ? 0 : Math.abs(scaled) >= 10 ? 1 : 2
  const compact = scaled
    .toFixed(fractionDigits)
    .replace(/\.0+$/, '')
    .replace(/(\.\d*?)0+$/, '$1')
  return `${compact}${unit.suffix}`
}

const USAGE_ANALYSIS_VISIBILITY_KEY = 'usage_analysis_visible'
const usageStatCardContentClass = 'flex min-w-0 flex-col gap-1.5 p-3'
const usageStatValueClass = 'min-w-0 break-words text-[20px] font-bold leading-tight tabular-nums sm:text-[22px]'

function getInitialAnalysisVisibility(): boolean {
  try {
    return window.localStorage.getItem(USAGE_ANALYSIS_VISIBILITY_KEY) !== 'false'
  } catch {
    return true
  }
}

function persistAnalysisVisibility(visible: boolean) {
  try {
    window.localStorage.setItem(USAGE_ANALYSIS_VISIBILITY_KEY, visible ? 'true' : 'false')
  } catch {}
}

function safeNumber(value?: number | null): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function formatUSD(value?: number | null, digits = 6): string {
  return `$${safeNumber(value).toFixed(digits)}`
}

function formatCostCardValue(value?: number | null): string {
  const amount = safeNumber(value)
  if (amount >= 100) {
    return `$${amount.toLocaleString(undefined, { maximumFractionDigits: 2 })}`
  }
  if (amount >= 1) {
    return `$${amount.toFixed(2)}`
  }
  if (amount >= 0.01) {
    return `$${amount.toFixed(4)}`
  }
  return `$${amount.toFixed(6)}`
}

function formatPercent(value: number, total: number): string {
  if (total <= 0) return '0.0%'
  return `${((value / total) * 100).toFixed(1)}%`
}

interface ModelPieDatum {
  model: string
  value: number
  requests: number
  amount: number
  share: number
}

function buildModelPieData(stats: UsageModelStat[], useAmount: boolean, otherLabel: string): ModelPieDatum[] {
  const base = stats
    .map((item) => ({
      model: item.model || 'unknown',
      value: useAmount ? safeNumber(item.user_billed) : safeNumber(item.requests),
      requests: safeNumber(item.requests),
      amount: safeNumber(item.user_billed),
      share: 0,
    }))
    .filter((item) => item.value > 0)

  const total = base.reduce((sum, item) => sum + item.value, 0)
  if (total <= 0) return []

  const visible = base.slice(0, 4)
  const overflow = base.slice(4)
  if (overflow.length > 0) {
    visible.push({
      model: otherLabel,
      value: overflow.reduce((sum, item) => sum + item.value, 0),
      requests: overflow.reduce((sum, item) => sum + item.requests, 0),
      amount: overflow.reduce((sum, item) => sum + item.amount, 0),
      share: 0,
    })
  }

  return visible.map((item) => ({
    ...item,
    share: (item.value / total) * 100,
  }))
}

function ModelSharePie({
  stats,
  showFullUsageNumbers,
}: {
  stats: UsageModelStat[]
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  const totalAmount = stats.reduce((sum, item) => sum + safeNumber(item.user_billed), 0)
  const totalRequests = stats.reduce((sum, item) => sum + safeNumber(item.requests), 0)
  const useAmount = totalAmount > 0
  const pieData = buildModelPieData(stats, useAmount, t('usage.modelStatsOther'))
  const centerValue = useAmount ? formatCostCardValue(totalAmount) : formatTokens(totalRequests, showFullUsageNumbers)
  const metricLabel = useAmount ? t('usage.modelPieAmount') : t('usage.modelPieRequests')

  if (pieData.length === 0) {
    return (
      <div className={modelPieShellClass}>
        <div className="flex min-h-[150px] flex-1 items-center justify-center px-3 text-center text-sm text-muted-foreground">
          {t('usage.noModelStats')}
        </div>
      </div>
    )
  }

  return (
    <div className={modelPieShellClass}>
      <div className="mb-1.5 flex items-start justify-between gap-3">
        <div>
          <div className="text-[13px] font-semibold text-foreground">{t('usage.modelPieTitle')}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">{metricLabel}</div>
        </div>
      </div>
      <div className="relative h-[150px] max-xl:h-[140px]">
        <ResponsiveContainer width="100%" height="100%" initialDimension={chartInitialDimensions.usagePie}>
          <PieChart>
            <Pie
              data={pieData}
              dataKey="value"
              nameKey="model"
              cx="50%"
              cy="50%"
              innerRadius="54%"
              outerRadius="78%"
              paddingAngle={0}
              stroke="none"
              strokeWidth={0}
            >
              {pieData.map((_, index) => (
                <Cell key={index} fill={modelPieColors[index % modelPieColors.length]} />
              ))}
            </Pie>
            <RechartsTooltip
              formatter={(value, name) => [
                useAmount ? formatCostCardValue(Number(value ?? 0)) : formatTokens(Number(value ?? 0), showFullUsageNumbers),
                String(name ?? ''),
              ]}
              contentStyle={{
                backgroundColor: 'var(--color-card)',
                border: '1px solid var(--color-border)',
                borderRadius: 12,
                boxShadow: '0 16px 36px rgba(15, 23, 42, 0.14)',
                fontSize: 12,
              }}
              itemStyle={{ color: 'var(--color-foreground)' }}
            />
          </PieChart>
        </ResponsiveContainer>
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
          <div className="max-w-[112px] text-center">
            <div className="text-[11px] font-medium text-muted-foreground">{metricLabel}</div>
            <div className="mt-0.5 truncate font-geist-mono text-[13px] font-semibold tabular-nums text-foreground">
              {centerValue}
            </div>
          </div>
        </div>
      </div>
      <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 max-sm:grid-cols-1">
        {pieData.map((item, index) => (
          <div key={`${item.model}-${index}`} className="flex items-center gap-2 text-xs">
            <span className="size-2 shrink-0 rounded-full" style={{ background: modelPieColors[index % modelPieColors.length] }} />
            <span className="min-w-0 flex-1 truncate font-medium text-foreground" title={item.model}>{item.model}</span>
            <span className="shrink-0 font-geist-mono tabular-nums text-muted-foreground">{item.share.toFixed(1)}%</span>
          </div>
        ))}
      </div>
    </div>
  )
}

function ModelStatsPanel({
  stats,
  showFullUsageNumbers,
}: {
  stats: UsageModelStat[]
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  const totalRequests = stats.reduce((sum, item) => sum + safeNumber(item.requests), 0)
  const maxRequests = Math.max(1, ...stats.map((item) => safeNumber(item.requests)))

  return (
    <Card className="py-0">
      <CardContent className="flex flex-col p-4">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h3 className="text-base font-semibold text-foreground">{t('usage.modelStatsTitle')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('usage.modelStatsDesc')}</p>
          </div>
          <div className="size-10 flex shrink-0 items-center justify-center rounded-xl bg-blue-500/12 text-blue-600 dark:bg-blue-500/20 dark:text-blue-300">
            <BarChart3 className="size-[18px]" />
          </div>
        </div>

        {stats.length === 0 ? (
          <div className="rounded-lg border border-dashed border-border bg-muted/30 px-3 py-8 text-center text-sm text-muted-foreground">
            {t('usage.noModelStats')}
          </div>
        ) : (
          <div className="grid grid-cols-[minmax(0,1fr)_minmax(220px,260px)] gap-4 max-lg:grid-cols-1">
            <div className="space-y-2.5">
              {stats.slice(0, 5).map((item) => {
                const share = totalRequests > 0 ? (item.requests / totalRequests) * 100 : 0
                const width = `${Math.max(4, Math.min(100, (item.requests / maxRequests) * 100))}%`
                return (
                  <div key={item.model} className="space-y-1">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <div className="truncate font-geist-mono text-[13px] font-semibold leading-tight text-foreground" title={item.model}>
                          {item.model}
                        </div>
                        <div className="mt-0.5 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-xs text-muted-foreground">
                          <span>{t('usage.modelStatsRequests')}: {formatTokens(item.requests, showFullUsageNumbers)}</span>
                          <span>{t('usage.modelStatsTokens')}: {formatTokens(item.tokens, showFullUsageNumbers)}</span>
                          {item.error_count > 0 && (
                            <span className="text-amber-600 dark:text-amber-400">{t('usage.modelStatsErrors')}: {formatTokens(item.error_count, showFullUsageNumbers)}</span>
                          )}
                        </div>
                      </div>
                      <div className="shrink-0 text-right">
                        <div className="font-geist-mono text-[13px] font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">
                          {formatCostCardValue(item.user_billed)}
                        </div>
                        <div className="mt-0.5 text-xs text-muted-foreground">{share.toFixed(1)}%</div>
                      </div>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                      <div className="h-full rounded-full bg-blue-500/70" style={{ width }} />
                    </div>
                  </div>
                )
              })}
            </div>
            <ModelSharePie stats={stats} showFullUsageNumbers={showFullUsageNumbers} />
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function FeatureStatsPanel({
  stats,
  totalRequests,
  showFullUsageNumbers,
}: {
  stats?: UsageFeatureStats
  totalRequests: number
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  const safeStats = stats ?? {
    stream_requests: 0,
    sync_requests: 0,
    fast_requests: 0,
    cache_hit_requests: 0,
    reasoning_requests: 0,
    image_requests: 0,
    retry_requests: 0,
    error_requests: 0,
  }
  const items = [
    { label: t('usage.featureStream'), value: safeStats.stream_requests, color: '#6366f1' },
    { label: t('usage.featureSync'), value: safeStats.sync_requests, color: '#64748b' },
    { label: t('usage.featureFast'), value: safeStats.fast_requests, color: '#3b82f6' },
    { label: t('usage.featureCache'), value: safeStats.cache_hit_requests, color: '#06b6d4' },
    { label: t('usage.featureReasoning'), value: safeStats.reasoning_requests, color: '#f59e0b' },
    { label: t('usage.featureImage'), value: safeStats.image_requests, color: '#d946ef' },
    { label: t('usage.featureRetry'), value: safeStats.retry_requests, color: '#f97316' },
    { label: t('usage.featureError'), value: safeStats.error_requests, color: '#ef4444' },
  ]

  return (
    <Card className="py-0">
      <CardContent className="flex h-full flex-col p-4">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h3 className="text-base font-semibold text-foreground">{t('usage.featureStatsTitle')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('usage.featureStatsDesc')}</p>
          </div>
          <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-cyan-500/12 text-cyan-600 dark:bg-cyan-500/20 dark:text-cyan-300">
            <Activity className="size-[18px]" />
          </div>
        </div>

        <div className="grid flex-1 grid-cols-2 gap-2 max-sm:grid-cols-1">
          {items.map((item) => {
            const pct = totalRequests > 0 ? (item.value / totalRequests) * 100 : 0
            return (
              <div
                key={item.label}
                className="group relative overflow-hidden rounded-lg border px-3 py-2.5 transition-colors"
                style={{
                  background: `color-mix(in srgb, ${item.color} 10%, transparent)`,
                  borderColor: `color-mix(in srgb, ${item.color} 28%, transparent)`,
                }}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-[12px] font-medium text-foreground/80">{item.label}</span>
                  <span className="font-geist-mono text-[10px] font-semibold tabular-nums text-foreground/60">
                    {pct.toFixed(1)}%
                  </span>
                </div>
                <div className="mt-0.5 font-geist-mono text-[20px] font-bold leading-tight tabular-nums text-foreground">
                  {formatTokens(item.value, showFullUsageNumbers)}
                </div>
                <div className="mt-1.5 h-[3px] overflow-hidden rounded-full bg-foreground/5">
                  <div
                    className="h-full rounded-full transition-all"
                    style={{ width: `${Math.min(100, pct)}%`, background: item.color }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}

function EndpointStatsPanel({
  stats,
  totalRequests,
  showFullUsageNumbers,
}: {
  stats: UsageEndpointStat[]
  totalRequests: number
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  return (
    <DistributionPanel
      title={t('usage.endpointStatsTitle')}
      description={t('usage.endpointStatsDesc')}
      emptyText={t('usage.noEndpointStats')}
      icon={<Route className="size-[18px]" />}
      items={stats.map((item) => ({
        key: item.endpoint,
        label: item.endpoint,
        requests: item.requests,
        tokens: item.tokens,
        errors: item.error_count,
      }))}
      totalRequests={totalRequests}
      showFullUsageNumbers={showFullUsageNumbers}
    />
  )
}

function APIKeyStatsPanel({
  stats,
  totalRequests,
  showFullUsageNumbers,
}: {
  stats: UsageAPIKeyStat[]
  totalRequests: number
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  return (
    <DistributionPanel
      title={t('usage.apiKeyStatsTitle')}
      description={t('usage.apiKeyStatsDesc')}
      emptyText={t('usage.noApiKeyStats')}
      icon={<KeyRound className="size-[18px]" />}
      items={stats.map((item) => ({
        key: `${item.api_key_id}-${item.label}`,
        label: item.label,
        requests: item.requests,
        tokens: item.tokens,
        errors: item.error_count,
      }))}
      limit={3}
      totalRequests={totalRequests}
      showFullUsageNumbers={showFullUsageNumbers}
    />
  )
}

function DistributionPanel({
  title,
  description,
  emptyText,
  icon,
  items,
  limit = 6,
  totalRequests,
  showFullUsageNumbers,
}: {
  title: string
  description: string
  emptyText: string
  icon: ReactNode
  items: Array<{ key: string; label: string; requests: number; tokens: number; errors: number }>
  limit?: number
  totalRequests: number
  showFullUsageNumbers: boolean
}) {
  const { t } = useTranslation()
  const maxRequests = Math.max(1, ...items.map((item) => safeNumber(item.requests)))

  return (
    <Card className="h-full py-0">
      <CardContent className="flex h-full flex-col p-4">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h3 className="text-base font-semibold text-foreground">{title}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{description}</p>
          </div>
          <div className="size-10 flex shrink-0 items-center justify-center rounded-xl bg-muted text-foreground">
            {icon}
          </div>
        </div>

        {items.length === 0 ? (
          <div className="flex min-h-[150px] flex-1 items-center justify-center rounded-lg border border-dashed border-border bg-muted/20 px-3 text-center text-sm text-muted-foreground">
            {emptyText}
          </div>
        ) : (
          <div className="space-y-3">
            {items.slice(0, limit).map((item) => {
              const width = `${Math.max(5, Math.min(100, (safeNumber(item.requests) / maxRequests) * 100))}%`
              return (
                <div key={item.key} className="space-y-1.5">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="truncate font-geist-mono text-[13px] font-semibold text-foreground" title={item.label}>
                        {item.label}
                      </div>
                      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                        <span>{t('usage.modelStatsRequests')}: {formatTokens(item.requests, showFullUsageNumbers)}</span>
                        <span>{t('usage.modelStatsTokens')}: {formatTokens(item.tokens, showFullUsageNumbers)}</span>
                        {item.errors > 0 && (
                          <span className="text-amber-600 dark:text-amber-400">{t('usage.modelStatsErrors')}: {formatTokens(item.errors, showFullUsageNumbers)}</span>
                        )}
                      </div>
                    </div>
                    <span className="shrink-0 font-geist-mono text-xs tabular-nums text-muted-foreground">
                      {formatPercent(item.requests, totalRequests)}
                    </span>
                  </div>
                  <div className="h-2 overflow-hidden rounded-full bg-muted">
                    <div className="h-full rounded-full bg-emerald-500/70" style={{ width }} />
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

const modelPieColors = [
  '#2563eb', '#059669', '#f59e0b', '#dc2626', '#7c3aed',
  '#0891b2', '#db2777', '#ea580c', '#4f46e5', '#16a34a',
  '#ca8a04', '#e11d48', '#0d9488', '#9333ea', '#65a30d',
  '#0284c7', '#c026d3', '#d97706', '#6366f1', '#14b8a6',
]
const modelPieShellClass = 'flex min-h-[196px] flex-col border-l border-border pl-4 max-lg:min-h-0 max-lg:border-l-0 max-lg:border-t max-lg:pl-0 max-lg:pt-3'

export default function Usage() {
  const { t } = useTranslation()
  const [timeRange, setTimeRange] = useState<UsageTimeRangeKey>('1h')
  const [customRange, setCustomRange] = useState<CustomRange | null>(null)
  const [showAnalysis, setShowAnalysis] = useState(getInitialAnalysisVisibility)

  const loadStatsOnly = useCallback(async () => {
    const { start, end } = resolveUsageRangeISO(timeRange, customRange)
    return api.getUsageStats({ start, end })
  }, [timeRange, customRange])

  // 统计按时间范围刷新；settings 是展示偏好，首次加载即可，避免 30 秒重复拉配置。
  const loadStats = useCallback(async () => {
    const [stats, settings] = await Promise.all([
      loadStatsOnly(),
      api.getSettings().catch((): SystemSettings | null => null),
    ])
    return { stats, settings }
  }, [loadStatsOnly])

  const { data, setData, loading, error, reload } = useDataLoader<{
    stats: UsageStats | null
    settings: SystemSettings | null
  }>({
    initialData: { stats: null, settings: null },
    load: loadStats,
  })

  useVisiblePolling(async () => {
    const stats = await loadStatsOnly()
    setData((current) => ({ ...current, stats }))
  }, 30000)

  useEffect(() => {
    persistAnalysisVisibility(showAnalysis)
  }, [showAnalysis])

  const { stats, settings } = data
  const showFullUsageNumbers = settings?.show_full_usage_numbers ?? false

  const totalRequests = stats?.total_requests ?? 0
  const totalTokens = stats?.total_tokens ?? 0
  const totalPromptTokens = stats?.total_prompt_tokens ?? 0
  const totalCompletionTokens = stats?.total_completion_tokens ?? 0
  const totalAccountBilled = stats?.total_account_billed ?? 0
  const totalUserBilled = stats?.total_user_billed ?? 0
  const todayRequests = stats?.today_requests ?? 0
  const todayUserBilled = stats?.today_user_billed ?? 0
  const modelStats = stats?.model_stats ?? []
  const featureStats = stats?.feature_stats
  const endpointStats = stats?.endpoint_stats ?? []
  const apiKeyStats = stats?.api_key_stats ?? []
  const rpm = stats?.rpm ?? 0
  const tpm = stats?.tpm ?? 0
  const errorRate = stats?.error_rate ?? 0
  const avgDurationMs = stats?.avg_duration_ms ?? 0
  const successRequests = totalRequests - Math.round(totalRequests * errorRate / 100)
  // 顶部 6 张卡片里的 today_* 字段联动顶部时间范围,标签也跟着改 —— 与下方请求记录的范围一致。
  // 后端在 GetUsageStats 收到 start/end 后,today_* 字段语义即"该区间统计"。
  const rangeLabel = timeRange === 'custom'
    ? t('usage.customRange')
    : t(`dashboard.timeRange${timeRange.toUpperCase()}`)
  const rangeRequestsLabel = t('usage.rangeRequests', { range: rangeLabel })
  const rangeCostLabel = t('usage.rangeCost', { range: rangeLabel })

  return (
    <StateShell
      variant="page"
      loading={loading}
      error={error}
      onRetry={() => { void reload() }}
      loadingTitle={t('usage.loadingTitle')}
      loadingDescription={t('usage.loadingDesc')}
      errorTitle={t('usage.errorTitle')}
    >
      <>
        <PageHeader
          title={t('usage.title')}
          description={t('usage.description')}
          onRefresh={() => { void reload() }}
          actions={
            <div className="flex items-center gap-2 overflow-x-auto">
              <UsageRangeSelector
                value={timeRange}
                customRange={customRange}
                onChange={(nextRange, nextCustomRange) => {
                  setTimeRange(nextRange)
                  setCustomRange(nextCustomRange)
                }}
              />
              <Button
                variant="outline"
                aria-pressed={showAnalysis}
                onClick={() => setShowAnalysis((v) => !v)}
              >
                <BarChart3 className="size-3.5" />
                {showAnalysis ? t('usage.hideAnalysis') : t('usage.showAnalysis')}
              </Button>
            </div>
          }
        />

        <div className="space-y-6">
        {/* Stat overview: 6 metrics in a single row */}
        <div className="grid grid-cols-1 gap-3 min-[560px]:grid-cols-2 md:grid-cols-3 xl:grid-cols-6">
          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">{t('usage.totalRequestsCard')}</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-primary/12 text-primary">
                  <Activity className="size-4" />
                </div>
              </div>
              <div className={usageStatValueClass}>
                {formatTokens(totalRequests, showFullUsageNumbers)}
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground leading-snug">
                <span className="text-[hsl(var(--success))]">● {t('usage.success')}: {formatTokens(successRequests, showFullUsageNumbers)}</span>
                <span>● {rangeRequestsLabel}: {formatTokens(todayRequests, showFullUsageNumbers)}</span>
              </div>
            </CardContent>
          </Card>

          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">{t('usage.totalTokensCard')}</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-[hsl(var(--info-bg))] text-[hsl(var(--info))]">
                  <Box className="size-4" />
                </div>
              </div>
              <div className={usageStatValueClass}>
                {formatTokens(totalTokens, showFullUsageNumbers)}
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground leading-snug">
                <span>{t('usage.inputTokens')}: {formatTokens(totalPromptTokens, showFullUsageNumbers)}</span>
                <span>{t('usage.outputTokens')}: {formatTokens(totalCompletionTokens, showFullUsageNumbers)}</span>
              </div>
            </CardContent>
          </Card>

          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">{t('usage.totalCostCard')}</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-emerald-500/12 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300">
                  <CircleDollarSign className="size-4" />
                </div>
              </div>
              <div className={`${usageStatValueClass} text-emerald-600 dark:text-emerald-400`}>
                {formatCostCardValue(totalUserBilled)}
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground leading-snug">
                <span>{rangeCostLabel}: {formatCostCardValue(todayUserBilled)}</span>
                <span>{t('usage.accountCost')}: {formatCostCardValue(totalAccountBilled)}</span>
              </div>
            </CardContent>
          </Card>

          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">RPM</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-[hsl(var(--success-bg))] text-[hsl(var(--success))]">
                  <Clock className="size-4" />
                </div>
              </div>
              <div className={usageStatValueClass}>
                {Math.round(rpm)}
              </div>
              <div className="text-[11px] text-muted-foreground leading-snug">{t('usage.rpmDesc')}</div>
            </CardContent>
          </Card>

          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">TPM</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-destructive/12 text-destructive">
                  <Zap className="size-4" />
                </div>
              </div>
              <div className={usageStatValueClass}>
                {formatTokens(tpm, showFullUsageNumbers)}
              </div>
              <div className="text-[11px] text-muted-foreground leading-snug">{t('usage.tpmDesc')}</div>
            </CardContent>
          </Card>

          <Card className="min-w-0 py-0">
            <CardContent className={usageStatCardContentClass}>
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] font-bold uppercase text-muted-foreground">{t('usage.errorRateCard')}</span>
                <div className="flex size-9 items-center justify-center rounded-lg bg-[hsl(36_72%_40%/0.12)] text-[hsl(36,72%,40%)]">
                  <AlertTriangle className="size-4" />
                </div>
              </div>
              <div className={usageStatValueClass}>
                {errorRate.toFixed(1)}%
              </div>
              <div className="text-[11px] text-muted-foreground leading-snug">{t('usage.avgLatencyInline', { value: Math.round(avgDurationMs) })}</div>
            </CardContent>
          </Card>
        </div>

        {showAnalysis && (
          <>
            <div className="grid grid-cols-[minmax(0,0.5fr)_minmax(360px,0.5fr)] gap-3 max-lg:grid-cols-1">
              <ModelStatsPanel stats={modelStats} showFullUsageNumbers={showFullUsageNumbers} />
              <FeatureStatsPanel stats={featureStats} totalRequests={totalRequests} showFullUsageNumbers={showFullUsageNumbers} />
            </div>

            <div className="grid grid-cols-2 gap-3 max-lg:grid-cols-1">
              <EndpointStatsPanel stats={endpointStats} totalRequests={totalRequests} showFullUsageNumbers={showFullUsageNumbers} />
              <APIKeyStatsPanel stats={apiKeyStats} totalRequests={totalRequests} showFullUsageNumbers={showFullUsageNumbers} />
            </div>
          </>
        )}

        </div>
      </>
    </StateShell>
  )
}

