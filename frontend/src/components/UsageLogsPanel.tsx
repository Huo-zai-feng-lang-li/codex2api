import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api'
import { formatAccountIdentity } from '../lib/utils'
import { formatBeijingTime } from '../utils/time'
import type { APIKeyRow, UsageLog } from '../types'
import { useConfirmDialog } from '../hooks/useConfirmDialog'
import { useToast } from '../hooks/useToast'
import { useVisiblePolling } from '../hooks/useVisiblePolling'
import { DEFAULT_PAGE_SIZE_OPTIONS, usePersistedPageSize } from '../hooks/usePersistedPageSize'
import { usePersistedTableColumns } from '../hooks/usePersistedTableColumns'
import type { TableColumnDefinition } from '../lib/tableColumns'
import Pagination from './Pagination'
import StateShell from './StateShell'
import ColumnSettingsDropdown from './ColumnSettingsDropdown'
import UsageRangeSelector, {
  resolveUsageRangeISO,
  type CustomRange,
  type UsageTimeRangeKey,
} from './UsageRangeSelector'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { Brain, DatabaseZap, Image as ImageIcon, Info, RefreshCw, Search, X, Zap } from 'lucide-react'

type UsageTableColumn = 'status' | 'model' | 'account' | 'apiKey' | 'endpoint' | 'type' | 'token' | 'cost' | 'cached' | 'firstToken' | 'duration' | 'time'

const USAGE_COLUMN_DEFINITIONS: readonly TableColumnDefinition<UsageTableColumn>[] = [
  { key: 'status', labelKey: 'usage.tableStatus' },
  { key: 'model', labelKey: 'usage.tableModel' },
  { key: 'account', labelKey: 'usage.tableAccount' },
  { key: 'apiKey', labelKey: 'usage.tableApiKey' },
  { key: 'endpoint', labelKey: 'usage.tableEndpoint' },
  { key: 'type', labelKey: 'usage.tableType' },
  { key: 'token', labelKey: 'usage.tableToken' },
  { key: 'cost', labelKey: 'usage.tableCost' },
  { key: 'cached', labelKey: 'usage.tableCached' },
  { key: 'firstToken', labelKey: 'usage.tableFirstToken' },
  { key: 'duration', labelKey: 'usage.tableDuration' },
  { key: 'time', labelKey: 'usage.tableTime' },
]

const USAGE_VISIBLE_COLUMNS_KEY = 'codex2api:usage:visible-columns'
const usageTableHeadClass = 'text-[12px] font-semibold'
const usageTableTextClass = 'text-[14px]'
const usageTableMonoClass = 'font-mono text-[13px] tabular-nums'
const usageTableBadgeClass = 'text-[13px]'
const REQUEST_LOGS_ACTIVE_REFRESH_INTERVAL_MS = 3_000

interface UsageLogsPanelProps {
  autoRefreshWhen?: boolean
  headerAddon?: ReactNode
}

export default function UsageLogsPanel({ autoRefreshWhen = false, headerAddon }: UsageLogsPanelProps) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const { confirm, confirmDialog } = useConfirmDialog()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = usePersistedPageSize('usage_logs', 20, DEFAULT_PAGE_SIZE_OPTIONS)
  const [timeRange, setTimeRange] = useState<UsageTimeRangeKey>('1h')
  const [customRange, setCustomRange] = useState<CustomRange | null>(null)
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [logsTotal, setLogsTotal] = useState(0)
  const [logsLoading, setLogsLoading] = useState(false)
  const [clearing, setClearing] = useState(false)
  const [searchInput, setSearchInput] = useState('')
  const [searchEmail, setSearchEmail] = useState('')
  const [filterModel, setFilterModel] = useState('')
  const [filterEndpoint, setFilterEndpoint] = useState('')
  const [filterApiKeyId, setFilterApiKeyId] = useState('')
  const [filterFast, setFilterFast] = useState('')
  const [filterStream, setFilterStream] = useState<'' | 'true' | 'false'>('')
  const [apiKeys, setAPIKeys] = useState<APIKeyRow[]>([])
  const [modelOptions, setModelOptions] = useState<string[]>([])
  const [apiKeyLoadFailed, setAPIKeyLoadFailed] = useState(false)
  const searchTimer = useRef<ReturnType<typeof setTimeout>>(null)
  const { preferences, setPreferences, visibleColumns } = usePersistedTableColumns(USAGE_VISIBLE_COLUMNS_KEY, USAGE_COLUMN_DEFINITIONS)

  const fetchLogs = useCallback(async () => {
    const { start, end } = resolveUsageRangeISO(timeRange, customRange)
    const response = await api.getUsageLogsPaged({
      start,
      end,
      page,
      pageSize,
      email: searchEmail || undefined,
      model: filterModel || undefined,
      endpoint: filterEndpoint || undefined,
      apiKeyId: filterApiKeyId || undefined,
      fast: filterFast || undefined,
      stream: filterStream || undefined,
    })
    setLogs(response.logs ?? [])
    setLogsTotal(response.total ?? 0)
  }, [timeRange, customRange, page, pageSize, searchEmail, filterModel, filterEndpoint, filterApiKeyId, filterFast, filterStream])

  const loadLogs = useCallback(async () => {
    setLogsLoading(true)
    try {
      await fetchLogs()
    } catch {
      // Keep this panel isolated from the dashboard page-level error boundary.
    } finally {
      setLogsLoading(false)
    }
  }, [fetchLogs])

  const refreshLogsSilently = useCallback(async () => {
    try {
      await fetchLogs()
    } catch {
      // Keep background refresh silent; manual refresh still exposes visible loading.
    }
  }, [fetchLogs])

  useEffect(() => {
    const refreshWhenVisible = () => {
      if (document.visibilityState === 'visible') void loadLogs()
    }

    refreshWhenVisible()
    document.addEventListener('visibilitychange', refreshWhenVisible)
    return () => document.removeEventListener('visibilitychange', refreshWhenVisible)
  }, [loadLogs])

  useVisiblePolling(
    refreshLogsSilently,
    REQUEST_LOGS_ACTIVE_REFRESH_INTERVAL_MS,
    { enabled: autoRefreshWhen, immediateOnVisible: false },
  )

  useEffect(() => {
    let active = true
    void api.getAPIKeys().then((response) => {
      if (!active) return
      setAPIKeys(response.keys ?? [])
      setAPIKeyLoadFailed(false)
    }).catch(() => {
      if (!active) return
      setAPIKeys([])
      setAPIKeyLoadFailed(true)
    })
    void api.getModels().then((response) => {
      if (!active) return
      const models = response.items && response.items.length > 0
        ? response.items.filter((item) => item.enabled).map((item) => item.id)
        : response.models ?? []
      setModelOptions(models)
    }).catch(() => {
      if (active) setModelOptions([])
    })
    return () => {
      active = false
      if (searchTimer.current) clearTimeout(searchTimer.current)
    }
  }, [])

  const totalPages = Math.max(1, Math.ceil(logsTotal / pageSize))
  const currentPage = Math.min(page, totalPages)
  useEffect(() => {
    if (page > totalPages) setPage(totalPages)
  }, [page, totalPages])

  const handleSearchChange = (value: string) => {
    setSearchInput(value)
    if (searchTimer.current) clearTimeout(searchTimer.current)
    searchTimer.current = setTimeout(() => {
      setSearchEmail(value)
      setPage(1)
    }, 400)
  }

  const handleRangeChange = (nextRange: UsageTimeRangeKey, nextCustomRange: CustomRange | null) => {
    setTimeRange(nextRange)
    setCustomRange(nextCustomRange)
    setPage(1)
  }

  const clearFilters = () => {
    setSearchInput('')
    setSearchEmail('')
    setFilterModel('')
    setFilterEndpoint('')
    setFilterApiKeyId('')
    setFilterStream('')
    setFilterFast('')
    setPage(1)
  }

  const clearLogs = async () => {
    const confirmed = await confirm({
      title: t('usage.clearLogsTitle'),
      description: t('usage.clearLogsDesc'),
      confirmText: t('usage.clearLogsConfirm'),
      tone: 'destructive',
      confirmVariant: 'destructive',
    })
    if (!confirmed) return
    setClearing(true)
    try {
      await api.clearUsageLogs()
      showToast(t('usage.clearLogsSuccess'))
      setPage(1)
      await loadLogs()
    } catch {
      showToast(t('usage.clearLogsFailed'), 'error')
    } finally {
      setClearing(false)
    }
  }

  const hasActiveFilters = Boolean(searchInput || filterModel || filterEndpoint || filterApiKeyId || filterStream || filterFast)
  const showAPIKeyFilter = !apiKeyLoadFailed && apiKeys.length > 0
  const apiKeyOptions = [
    { label: t('usage.allApiKeys'), value: '' },
    ...apiKeys.map((apiKey) => ({ label: formatAPIKeyOptionLabel(apiKey), value: String(apiKey.id) })),
  ]
  const renderers = createUsageCellRenderers(t)

  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-4 flex flex-wrap items-center gap-3 overflow-visible max-lg:overflow-x-auto">
          <h3 className="whitespace-nowrap text-base font-semibold leading-8 text-foreground">{t('usage.requestLogs')}</h3>
          {headerAddon}
          <UsageRangeSelector value={timeRange} customRange={customRange} onChange={handleRangeChange} />
          <div className="ml-auto flex shrink-0 items-center gap-2">
            <span className="whitespace-nowrap text-xs text-muted-foreground">
              {logsLoading ? t('common.loading') : t('usage.recordsCount', { count: logsTotal })}
            </span>
            <Button variant="outline" size="sm" disabled={logsLoading} onClick={() => void loadLogs()}>
              <RefreshCw className={`size-3.5 ${logsLoading ? 'animate-spin' : ''}`} />
              {t('common.refresh')}
            </Button>
            <Button variant="destructive" size="sm" disabled={clearing || logs.length === 0} onClick={() => void clearLogs()}>
              {clearing ? t('usage.clearingLogs') : t('usage.clearLogs')}
            </Button>
          </div>
        </div>

        <div className="toolbar-surface mb-4 flex items-center gap-2 overflow-visible whitespace-nowrap max-lg:overflow-x-auto">
          <div className="relative w-60 shrink-0 max-sm:w-full">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input className="h-8 rounded-lg pl-8 text-[13px]" placeholder={t('usage.searchEmail')} value={searchInput} onChange={(event) => handleSearchChange(event.target.value)} />
          </div>
          <Select className="w-36 shrink-0" compact value={filterModel} onValueChange={(value) => { setFilterModel(value); setPage(1) }} placeholder={t('usage.allModels')} options={[{ label: t('usage.allModels'), value: '' }, ...modelOptions.map((model) => ({ label: model, value: model }))]} />
          <Select className="w-44 shrink-0" compact value={filterEndpoint} onValueChange={(value) => { setFilterEndpoint(value); setPage(1) }} placeholder={t('usage.allEndpoints')} options={endpointOptions(t)} />
          {showAPIKeyFilter && <Select className="w-48 shrink-0" compact value={filterApiKeyId} onValueChange={(value) => { setFilterApiKeyId(value); setPage(1) }} placeholder={t('usage.allApiKeys')} options={apiKeyOptions} />}
          <Select className="w-28 shrink-0" compact value={filterStream} onValueChange={(value) => { setFilterStream(value as '' | 'true' | 'false'); setPage(1) }} placeholder={t('usage.allTypes')} options={[{ label: t('usage.allTypes'), value: '' }, { label: 'Stream', value: 'true' }, { label: 'Sync', value: 'false' }]} />
          <button type="button" onClick={() => { setFilterFast(filterFast === 'true' ? '' : 'true'); setPage(1) }} className={`inline-flex h-8 shrink-0 items-center gap-1 whitespace-nowrap rounded-lg border px-2.5 text-[13px] font-medium transition-colors ${filterFast === 'true' ? 'border-blue-500/40 bg-blue-500/12 text-blue-600 dark:bg-blue-500/20 dark:text-blue-400' : 'border-border bg-background text-muted-foreground hover:bg-muted/50 hover:text-foreground'}`}><Zap className="size-3.5" />Fast</button>
          {hasActiveFilters && <button type="button" onClick={clearFilters} className="inline-flex h-8 shrink-0 items-center gap-1 whitespace-nowrap rounded-lg border border-border bg-background px-2.5 text-[13px] text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"><X className="size-3.5" />{t('usage.clearFilters')}</button>}
          <div className="ml-auto shrink-0"><ColumnSettingsDropdown definitions={USAGE_COLUMN_DEFINITIONS} preferences={preferences} onChange={setPreferences} /></div>
        </div>

        <StateShell variant="section" isEmpty={logs.length === 0} emptyTitle={t('usage.emptyTitle')} emptyDescription={hasActiveFilters ? t('usage.emptyFilteredDesc') : t('usage.emptyDesc')}>
          <div className="data-table-shell">
            <TooltipProvider>
              <Table>
                <TableHeader><TableRow>{visibleColumns.map((column) => <TableHead key={column.key} className={usageTableHeadClass}>{t(column.labelKey)}</TableHead>)}</TableRow></TableHeader>
                <TableBody>{logs.map((log) => <TableRow key={log.id}>{visibleColumns.map((column) => renderers[column.key](log))}</TableRow>)}</TableBody>
              </Table>
            </TooltipProvider>
          </div>
          <Pagination page={currentPage} totalPages={totalPages} onPageChange={setPage} totalItems={logsTotal} pageSize={pageSize} pageSizeOptions={DEFAULT_PAGE_SIZE_OPTIONS} onPageSizeChange={(value) => { setPageSize(value); setPage(1) }} />
        </StateShell>
      </CardContent>
      {confirmDialog}
    </Card>
  )
}

function endpointOptions(t: ReturnType<typeof useTranslation>['t']) {
  return [
    { label: t('usage.allEndpoints'), value: '' },
    ...['/v1/chat/completions', '/v1/responses', '/v1/images/generations', '/v1/images/edits', '/v1/messages'].map((value) => ({ label: value, value })),
  ]
}

function createUsageCellRenderers(t: ReturnType<typeof useTranslation>['t']): Record<UsageTableColumn, (log: UsageLog) => ReactNode> {
  return {
    status: (log) => <TableCell key="status"><StatusCodeBadge log={log} /></TableCell>,
    model: (log) => <TableCell key="model"><div className="flex flex-wrap items-center gap-1.5"><Badge variant="outline" className={usageTableBadgeClass}>{log.model || '-'}</Badge>{log.effective_model && log.effective_model !== log.model && <Badge variant="outline" className="border-transparent bg-blue-500/10 text-[11px] font-medium text-blue-600 dark:bg-blue-500/20 dark:text-blue-400">→ {log.effective_model}</Badge>}{log.reasoning_effort && <Badge variant="outline" className={`border-transparent text-[11px] font-medium ${reasoningEffortClass(log.reasoning_effort)}`}>{log.reasoning_effort}</Badge>}{isImageUsageLog(log) && <ImageUsageBadge log={log} />}{(log.service_tier === 'fast' || log.service_tier === 'priority') && <Badge variant="outline" className="gap-0.5 border-transparent bg-blue-500/12 text-[11px] font-semibold text-blue-600 dark:bg-blue-500/20 dark:text-blue-400"><Zap className="size-3" />Fast</Badge>}</div></TableCell>,
    account: (log) => <TableCell key="account" className={`${usageTableTextClass} text-muted-foreground`}>{formatAccountIdentity(log)}</TableCell>,
    apiKey: (log) => { const label = formatUsageAPIKeyLabel(log.api_key_name, log.api_key_masked) || t('usage.unknownApiKey'); return <TableCell key="apiKey" className={`${usageTableTextClass} text-muted-foreground`}><span className="block max-w-[180px] truncate whitespace-nowrap font-mono text-[12px]" title={label}>{label}</span></TableCell> },
    endpoint: (log) => <TableCell key="endpoint"><div className={`${usageTableMonoClass} leading-relaxed`}><span className="text-muted-foreground">{log.inbound_endpoint || log.endpoint || '-'}</span>{log.upstream_endpoint && log.upstream_endpoint !== log.inbound_endpoint && <span className="text-muted-foreground"> → {log.upstream_endpoint}</span>}</div></TableCell>,
    type: (log) => <TableCell key="type"><Badge variant="outline" className={usageTableBadgeClass} style={{ background: log.stream ? 'rgba(99, 102, 241, 0.12)' : 'rgba(107, 114, 128, 0.12)', color: log.stream ? '#6366f1' : '#6b7280', borderColor: 'transparent' }}>{log.stream ? 'stream' : 'sync'}</Badge></TableCell>,
    token: (log) => <TableCell key="token">{log.status_code < 400 && (log.input_tokens > 0 || log.output_tokens > 0) ? <div className={`${usageTableMonoClass} inline-flex flex-wrap items-center`}><span className="text-blue-500">↓{formatTokens(log.input_tokens, true)}</span><span className="mx-1 text-border">|</span><span className="text-emerald-500">↑{formatTokens(log.output_tokens, true)}</span>{log.reasoning_tokens > 0 && <><span className="mx-1 text-border">|</span><span className="inline-flex items-center gap-0.5 text-amber-500"><Brain className="size-3.5 shrink-0" /><span>{formatTokens(log.reasoning_tokens, true)}</span></span></>}</div> : <span className={`${usageTableMonoClass} text-muted-foreground`}>-</span>}</TableCell>,
    cost: (log) => <TableCell key="cost"><UsageCostCell log={log} /></TableCell>,
    cached: (log) => <TableCell key="cached">{log.cached_tokens > 0 ? <Badge variant="outline" className={`${usageTableBadgeClass} gap-1 border-transparent bg-indigo-500/10 text-indigo-600 dark:bg-indigo-500/20 dark:text-indigo-400`}><DatabaseZap className="size-3.5" />{formatTokens(log.cached_tokens, true)}</Badge> : <span className={`${usageTableMonoClass} text-muted-foreground`}>-</span>}</TableCell>,
    firstToken: (log) => <TableCell key="firstToken">{log.first_token_ms > 0 ? <span className={`${usageTableMonoClass} ${log.first_token_ms > 5000 ? 'text-red-500' : log.first_token_ms > 2000 ? 'text-amber-500' : 'text-emerald-500'}`}>{log.first_token_ms > 1000 ? `${(log.first_token_ms / 1000).toFixed(1)}s` : `${log.first_token_ms}ms`}</span> : <span className={`${usageTableMonoClass} text-muted-foreground`}>-</span>}</TableCell>,
    duration: (log) => <TableCell key="duration"><span className={`${usageTableMonoClass} ${log.duration_ms > 30000 ? 'text-red-500' : log.duration_ms > 10000 ? 'text-amber-500' : 'text-muted-foreground'}`}>{log.duration_ms > 1000 ? `${(log.duration_ms / 1000).toFixed(1)}s` : `${log.duration_ms}ms`}</span></TableCell>,
    time: (log) => <TableCell key="time" className={`${usageTableMonoClass} whitespace-nowrap text-muted-foreground`}>{formatBeijingTime(log.created_at)}</TableCell>,
  }
}

function reasoningEffortClass(effort: string): string {
  if (effort === 'xhigh' || effort === 'high') return 'bg-red-500/12 text-red-600 dark:bg-red-500/20 dark:text-red-400'
  if (effort === 'medium') return 'bg-amber-500/12 text-amber-600 dark:bg-amber-500/20 dark:text-amber-400'
  return 'bg-emerald-500/12 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-400'
}

function formatTokens(value?: number | null, showFullNumbers = false): string {
  const numericValue = Number(value ?? 0)
  if (!Number.isFinite(numericValue)) return '0'
  const roundedValue = Math.round(numericValue)
  if (showFullNumbers) return roundedValue.toLocaleString()
  const unit = [{ value: 1e12, suffix: 'T' }, { value: 1e9, suffix: 'B' }, { value: 1e6, suffix: 'M' }, { value: 1e3, suffix: 'K' }].find((item) => Math.abs(numericValue) >= item.value)
  if (!unit) return roundedValue.toLocaleString()
  const scaled = numericValue / unit.value
  const digits = Math.abs(scaled) >= 100 ? 0 : Math.abs(scaled) >= 10 ? 1 : 2
  return `${scaled.toFixed(digits).replace(/\.0+$/, '').replace(/(\.\d*?)0+$/, '$1')}${unit.suffix}`
}

function getStatusBadgeClassName(statusCode: number): string {
  if (statusCode === 200) return 'border-transparent bg-emerald-500/14 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300'
  if (statusCode === 401 || statusCode >= 500) return 'border-transparent bg-red-500/14 text-red-600 dark:bg-red-500/20 dark:text-red-300'
  if (statusCode === 429 || statusCode >= 400) return 'border-transparent bg-amber-500/14 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300'
  return 'border-transparent bg-slate-500/14 text-slate-600 dark:bg-slate-500/20 dark:text-slate-300'
}

function StatusCodeBadge({ log }: { log: UsageLog }) {
  const { t } = useTranslation()
  const badge = <Badge variant="outline" className={`${usageTableBadgeClass} ${getStatusBadgeClassName(log.status_code)} ${log.status_code !== 200 ? 'cursor-help ring-1 ring-inset ring-current/10' : ''}`}>{log.status_code}</Badge>
  if (log.status_code === 200) return badge
  const message = log.error_message?.trim() || t('usage.statusErrorEmpty')
  return <Tooltip><TooltipTrigger asChild><span tabIndex={0} aria-label={`${log.status_code} ${message}`} className="inline-flex focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">{badge}</span></TooltipTrigger><TooltipContent side="right" sideOffset={8} className="max-w-[360px] rounded-lg border border-slate-700 bg-slate-950 px-3 py-2.5 text-xs text-slate-50 shadow-xl"><div className="space-y-1.5"><div className="font-semibold text-slate-300">{t('usage.statusErrorDetails')}</div><div className="font-geist-mono text-[11px] tabular-nums text-slate-400">HTTP {log.status_code}</div><div className="whitespace-pre-wrap break-words leading-relaxed text-slate-50">{message}</div></div></TooltipContent></Tooltip>
}

function formatAPIKeyOptionLabel(apiKey: APIKeyRow): string { return apiKey.name ? `${apiKey.name} · ${apiKey.key}` : apiKey.key }
function formatUsageAPIKeyLabel(name?: string, maskedKey?: string): string { const trimmedName = name?.trim() ?? ''; if (trimmedName) return trimmedName; const key = maskedKey?.trim() ?? ''; return key.length > 8 ? `${key.slice(0, 4)}...${key.slice(-4)}` : key }
function isImageUsageLog(log: UsageLog): boolean { const endpoint = log.inbound_endpoint || log.endpoint || ''; return endpoint.includes('/images/') || log.model?.startsWith('gpt-image-') || (log.image_count ?? 0) > 0 }
function formatImageBytes(bytes?: number | null): string { if (!bytes || bytes <= 0) return ''; if (bytes < 1024) return `${bytes} B`; if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`; return `${(bytes / 1024 / 1024).toFixed(2)} MB` }
function imageResolution(log: UsageLog): string { return log.image_width > 0 && log.image_height > 0 ? `${log.image_width}×${log.image_height}` : log.image_size || '' }

function ImageUsageBadge({ log }: { log: UsageLog }) {
  const { t } = useTranslation()
  const rows = [{ label: t('usage.imageTooltipCount'), value: log.image_count > 0 ? String(log.image_count) : '' }, { label: t('usage.imageTooltipResolution'), value: imageResolution(log) }, { label: t('usage.imageTooltipBytes'), value: formatImageBytes(log.image_bytes) }, { label: t('usage.imageTooltipFormat'), value: log.image_format?.toUpperCase() || '' }, { label: t('usage.imageTooltipRequestSize'), value: log.image_size || '' }].filter((row) => row.value)
  const title = rows.length > 0 ? rows.map((row) => `${row.label}: ${row.value}`).join('\n') : t('usage.imageTooltipNoDetails')
  return <Tooltip><TooltipTrigger asChild><span aria-label={title} tabIndex={0} className="inline-flex w-fit shrink-0 cursor-help items-center justify-center gap-0.5 whitespace-nowrap rounded-full border border-transparent bg-cyan-500/12 px-2 py-0.5 text-[11px] font-semibold text-cyan-700 dark:bg-cyan-500/20 dark:text-cyan-300"><ImageIcon className="size-3" />{t('usage.imageRequest')}</span></TooltipTrigger><TooltipContent side="top" sideOffset={6} className="max-w-64 p-2.5"><div className="space-y-1.5"><div className="font-semibold">{t('usage.imageTooltipTitle')}</div>{rows.length > 0 ? rows.map((row) => <div key={row.label} className="flex min-w-44 items-center justify-between gap-4"><span className="text-background/70">{row.label}</span><span className="font-geist-mono tabular-nums">{row.value}</span></div>) : <div className="text-background/70">{t('usage.imageTooltipNoDetails')}</div>}</div></TooltipContent></Tooltip>
}

function safeNumber(value?: number | null): number { return typeof value === 'number' && Number.isFinite(value) ? value : 0 }
function formatUSD(value?: number | null, digits = 6): string { return `$${safeNumber(value).toFixed(digits)}` }
function formatTokenPricePerMillion(value?: number | null): string { return `$${safeNumber(value).toFixed(4)} / 1M Token` }

function UsageCostCell({ log }: { log: UsageLog }) {
  const { t } = useTranslation()
  const accountBilled = safeNumber(log.account_billed)
  const userBilled = safeNumber(log.user_billed)
  const totalCost = safeNumber(log.total_cost)
  const displayCost = userBilled > 0 ? userBilled : accountBilled
  const longContextThreshold = safeNumber(log.long_context_threshold)
  const hasCostContext = log.status_code < 400 && (accountBilled > 0 || userBilled > 0 || totalCost > 0 || log.input_tokens > 0 || log.output_tokens > 0 || log.cached_tokens > 0)
  if (!hasCostContext) return <span className={`${usageTableMonoClass} text-muted-foreground`}>-</span>
  return <Tooltip><TooltipTrigger asChild><button type="button" className="group inline-flex cursor-help items-center gap-1.5 rounded-md px-1.5 py-1 text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"><span className="text-[13px] font-semibold leading-none tabular-nums text-emerald-600 antialiased dark:text-emerald-400">{formatUSD(displayCost)}</span><Info className="size-3.5 shrink-0 text-muted-foreground transition-colors group-hover:text-blue-500" /></button></TooltipTrigger><TooltipContent side="right" sideOffset={8} className="w-96 max-w-none whitespace-nowrap rounded-lg border border-slate-700 bg-slate-950 px-3 py-2.5 text-xs text-slate-50 shadow-xl"><div className="space-y-1.5"><div className="mb-1 text-xs font-semibold text-slate-300">{t('usage.costDetails')}</div>{log.input_cost > 0 && <CostTooltipRow label={t('usage.inputCost')} value={formatUSD(log.input_cost)} />}{log.output_cost > 0 && <CostTooltipRow label={t('usage.outputCost')} value={formatUSD(log.output_cost)} />}{log.cached_tokens > 0 && <CostTooltipRow label={t('usage.cacheReadCost')} value={formatUSD(log.cache_read_cost)} />}{log.input_tokens > 0 && <CostTooltipRow label={t('usage.inputUnitPrice')} value={formatTokenPricePerMillion(log.input_price_per_mtoken)} valueClassName="text-sky-300" />}{log.output_tokens > 0 && <CostTooltipRow label={t('usage.outputUnitPrice')} value={formatTokenPricePerMillion(log.output_price_per_mtoken)} valueClassName="text-violet-300" />}{log.cached_tokens > 0 && log.cache_read_price_per_mtoken > 0 && <CostTooltipRow label={t('usage.cacheReadUnitPrice')} value={formatTokenPricePerMillion(log.cache_read_price_per_mtoken)} valueClassName="text-cyan-300" />}<CostTooltipRow label={t('usage.billingTier')} value={log.service_tier === 'fast' || log.service_tier === 'priority' ? t('usage.billingTierFast') : t('usage.billingTierStandard')} valueClassName={log.service_tier === 'fast' || log.service_tier === 'priority' ? 'text-amber-300' : 'text-slate-200'} />{log.long_context && longContextThreshold > 0 && <CostTooltipRow label={t('usage.billingContext')} value={t('usage.billingContextLong', { input: formatTokens(log.input_tokens, true), threshold: formatTokens(longContextThreshold, true) })} valueClassName="text-orange-300" />}</div></TooltipContent></Tooltip>
}

function CostTooltipRow({ label, value, valueClassName = 'font-medium text-white' }: { label: string; value: string; valueClassName?: string }) { return <div className="flex items-center justify-between gap-6"><span className="text-slate-400">{label}</span><span className={`font-geist-mono tabular-nums ${valueClassName}`}>{value}</span></div> }
