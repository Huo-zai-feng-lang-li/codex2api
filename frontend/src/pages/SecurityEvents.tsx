import { useCallback, useMemo, useState, type ChangeEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, RefreshCw, Search, ShieldCheck, Trash2, X } from 'lucide-react'
import { api } from '../api'
import PageHeader from '../components/PageHeader'
import Pagination from '../components/Pagination'
import StateShell from '../components/StateShell'
import { DEFAULT_PAGE_SIZE_OPTIONS, usePersistedPageSize } from '../hooks/usePersistedPageSize'
import { useDataLoader } from '../hooks/useDataLoader'
import { useToast } from '../hooks/useToast'
import { formatBeijingTime, formatRelativeTime } from '../utils/time'
import { getErrorMessage } from '../utils/error'
import { summarizeSecurityPreview, type SecurityPreviewSummary } from '../utils/securityPreview'
import type { SecurityEvent } from '../types'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

type SecurityEventFilters = {
  riskLevel: string
  direction: string
  action: string
  sourceType: string
  toolCall: string
  endpoint: string
  model: string
  accountId: string
  baseUrl: string
  start: string
  end: string
  q: string
}

type SecurityRule = {
  rule_id?: string
  evidence?: string
}

const emptyFilters: SecurityEventFilters = {
  riskLevel: '',
  direction: '',
  action: '',
  sourceType: '',
  toolCall: '',
  endpoint: '',
  model: '',
  accountId: '',
  baseUrl: '',
  start: '',
  end: '',
  q: '',
}

const tableHeadClass = 'text-[12px] font-semibold'
const tableTextClass = 'text-[14px]'
const monoClass = 'font-geist-mono text-[12px] tabular-nums'
const previewToneClass: Record<SecurityPreviewSummary['tone'], string> = {
  error: 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300',
  json: 'border-slate-500/25 bg-slate-500/10 text-slate-700 dark:text-slate-300',
  text: 'border-border bg-muted text-muted-foreground',
  tool: 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300',
}

function parseRules(raw: string): SecurityRule[] {
  if (!raw.trim()) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((item): item is SecurityRule => item && typeof item === 'object')
  } catch {
    return [{ rule_id: raw }]
  }
}

function parseStringList(raw: string): string[] {
  if (!raw.trim()) return []
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : []
  } catch {
    return [raw]
  }
}

function riskClass(level: string) {
  switch (level) {
    case 'critical':
      return 'border-red-500/35 bg-red-500/12 text-red-700 dark:text-red-300'
    case 'high':
      return 'border-orange-500/35 bg-orange-500/12 text-orange-700 dark:text-orange-300'
    case 'medium':
      return 'border-amber-500/35 bg-amber-500/12 text-amber-700 dark:text-amber-300'
    case 'low':
      return 'border-sky-500/35 bg-sky-500/12 text-sky-700 dark:text-sky-300'
    default:
      return 'border-border bg-muted text-muted-foreground'
  }
}

function actionClass(action: string) {
  if (action === 'block') return 'border-red-500/35 bg-red-500/12 text-red-700 dark:text-red-300'
  if (action === 'warn') return 'border-amber-500/35 bg-amber-500/12 text-amber-700 dark:text-amber-300'
  return 'border-border bg-muted text-muted-foreground'
}

function Badge({ children, className = '' }: { children: string; className?: string }) {
  return (
    <span className={`inline-flex max-w-full items-center rounded-md border px-2 py-0.5 text-[12px] font-medium ${className}`}>
      <span className="truncate">{children}</span>
    </span>
  )
}

export default function SecurityEvents() {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = usePersistedPageSize('security_events', 20, DEFAULT_PAGE_SIZE_OPTIONS)
  const [filters, setFilters] = useState<SecurityEventFilters>(emptyFilters)
  const [selectedEvent, setSelectedEvent] = useState<SecurityEvent | null>(null)
  const [clearing, setClearing] = useState(false)
  const [suppressingId, setSuppressingId] = useState<number | null>(null)

  const loadEvents = useCallback(async () => {
    const result = await api.getSecurityEvents({
      page,
      pageSize,
      riskLevel: filters.riskLevel,
      direction: filters.direction,
      action: filters.action,
      sourceType: filters.sourceType,
      toolCall: filters.toolCall,
      endpoint: filters.endpoint,
      model: filters.model,
      accountId: filters.accountId,
      baseUrl: filters.baseUrl,
      start: filters.start,
      end: filters.end,
      q: filters.q,
    })
    return {
      events: result.events ?? [],
      total: result.total ?? 0,
    }
  }, [filters, page, pageSize])

  const handleLoadError = useCallback((message: string) => {
    showToast(message, 'error')
  }, [showToast])

  const { data, loading, error, reload } = useDataLoader({
    initialData: { events: [] as SecurityEvent[], total: 0 },
    load: loadEvents,
    onError: handleLoadError,
  })

  const totalPages = Math.max(1, Math.ceil(data.total / pageSize))
  const hasFilters = useMemo(() => Object.values(filters).some(Boolean), [filters])

  const updateFilter = <K extends keyof SecurityEventFilters>(key: K, value: SecurityEventFilters[K]) => {
    setPage(1)
    setFilters((current) => ({ ...current, [key]: value }))
  }

  const clearFilters = () => {
    setPage(1)
    setFilters(emptyFilters)
  }

  const handleClearEvents = async () => {
    if (!window.confirm(t('securityEvents.clearConfirm'))) return
    setClearing(true)
    try {
      await api.clearSecurityEvents()
      showToast(t('securityEvents.clearSuccess'), 'success')
      setPage(1)
      await reload()
    } catch (err) {
      showToast(getErrorMessage(err), 'error')
    } finally {
      setClearing(false)
    }
  }

  const handleSuppressEvent = async (event: SecurityEvent, ruleID: string) => {
    if (!ruleID || !window.confirm(t('securityEvents.suppressConfirm'))) return
    setSuppressingId(event.id)
    try {
      await api.suppressSecurityEvent(event.id, { rule_id: ruleID })
      showToast(t('securityEvents.suppressSuccess'), 'success')
      await reload()
    } catch (err) {
      showToast(getErrorMessage(err), 'error')
    } finally {
      setSuppressingId(null)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title={t('securityEvents.title')}
        description={t('securityEvents.description')}
        onRefresh={reload}
        actions={
          <Button variant="outline" onClick={() => void handleClearEvents()} disabled={clearing || data.total === 0}>
            {clearing ? <RefreshCw className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
            {t('securityEvents.clear')}
          </Button>
        }
        actionMeta={t('securityEvents.total', { count: data.total })}
      />

      <div className="rounded-lg border border-border bg-card/80 p-4 shadow-sm">
        <div className="grid grid-cols-[repeat(auto-fit,minmax(160px,1fr))] gap-3">
          <Select
            value={filters.riskLevel}
            onValueChange={(value) => updateFilter('riskLevel', value)}
            placeholder={t('securityEvents.riskLevel')}
            options={[
              { label: t('securityEvents.allRisk'), value: '' },
              { label: t('securityEvents.riskCritical'), value: 'critical' },
              { label: t('securityEvents.riskHigh'), value: 'high' },
              { label: t('securityEvents.riskMedium'), value: 'medium' },
              { label: t('securityEvents.riskLow'), value: 'low' },
            ]}
          />
          <Select
            value={filters.direction}
            onValueChange={(value) => updateFilter('direction', value)}
            placeholder={t('securityEvents.direction')}
            options={[
              { label: t('securityEvents.allDirections'), value: '' },
              { label: t('securityEvents.directionRequest'), value: 'request' },
              { label: t('securityEvents.directionResponse'), value: 'response' },
              { label: t('securityEvents.directionSource'), value: 'source' },
            ]}
          />
          <Select
            value={filters.action}
            onValueChange={(value) => updateFilter('action', value)}
            placeholder={t('securityEvents.action')}
            options={[
              { label: t('securityEvents.allActions'), value: '' },
              { label: t('securityEvents.actionWarn'), value: 'warn' },
              { label: t('securityEvents.actionBlock'), value: 'block' },
              { label: t('securityEvents.actionAllow'), value: 'allow' },
            ]}
          />
          <Select
            value={filters.sourceType}
            onValueChange={(value) => updateFilter('sourceType', value)}
            placeholder={t('securityEvents.sourceType')}
            options={[
              { label: t('securityEvents.allSources'), value: '' },
              { label: t('securityEvents.sourceOfficial'), value: 'official' },
              { label: t('securityEvents.sourceThirdParty'), value: 'third_party' },
              { label: t('securityEvents.sourceUnknown'), value: 'unknown' },
            ]}
          />
          <Select
            value={filters.toolCall}
            onValueChange={(value) => updateFilter('toolCall', value)}
            placeholder={t('securityEvents.toolCall')}
            options={[
              { label: t('securityEvents.allToolCalls'), value: '' },
              { label: t('securityEvents.yes'), value: 'true' },
              { label: t('securityEvents.no'), value: 'false' },
            ]}
          />
          <Input
            value={filters.endpoint}
            placeholder={t('securityEvents.endpoint')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('endpoint', e.target.value)}
          />
          <Input
            value={filters.model}
            placeholder={t('securityEvents.model')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('model', e.target.value)}
          />
          <Input
            value={filters.accountId}
            placeholder={t('securityEvents.accountId')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('accountId', e.target.value)}
          />
          <Input
            value={filters.baseUrl}
            placeholder={t('securityEvents.baseUrl')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('baseUrl', e.target.value)}
          />
          <Input
            type="date"
            value={filters.start}
            aria-label={t('securityEvents.startDate')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('start', e.target.value)}
          />
          <Input
            type="date"
            value={filters.end}
            aria-label={t('securityEvents.endDate')}
            onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('end', e.target.value)}
          />
          <div className="flex min-w-0 gap-2">
            <Input
              value={filters.q}
              placeholder={t('securityEvents.search')}
              onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('q', e.target.value)}
            />
            {hasFilters ? (
              <Button type="button" variant="outline" onClick={clearFilters} aria-label={t('securityEvents.clearFilters')}>
                <X className="size-4" />
              </Button>
            ) : (
              <Button type="button" variant="outline" onClick={() => void reload()} aria-label={t('securityEvents.searchButton')}>
                <Search className="size-4" />
              </Button>
            )}
          </div>
        </div>
      </div>

      <StateShell
        loading={loading}
        error={error}
        isEmpty={!loading && !error && data.events.length === 0}
        onRetry={reload}
        loadingTitle={t('securityEvents.loadingTitle')}
        loadingDescription={t('securityEvents.loadingDesc')}
        emptyTitle={t('securityEvents.emptyTitle')}
        emptyDescription={t('securityEvents.emptyDesc')}
      >
        <div className="overflow-hidden rounded-lg border border-border bg-card/80 shadow-sm">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className={tableHeadClass}>{t('securityEvents.time')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.risk')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.target')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.rules')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.preview')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.metadata')}</TableHead>
                  <TableHead className={tableHeadClass}>{t('securityEvents.actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.events.map((event) => {
                  const rules = parseRules(event.rules)
                  const hints = parseStringList(event.false_positive_hints)
                  const preview = summarizeSecurityPreview(event.preview, event.scanner_error)
                  const suppressRuleID = rules.find((rule) => rule.rule_id && rule.rule_id !== 'scanner_error')?.rule_id || ''
                  return (
                    <TableRow key={event.id}>
                      <TableCell className="min-w-[150px] align-top">
                        <div className={monoClass}>{formatBeijingTime(event.created_at)}</div>
                        <div className="mt-1 text-[12px] text-muted-foreground">{formatRelativeTime(event.created_at)}</div>
                      </TableCell>
                      <TableCell className="min-w-[150px] align-top">
                        <div className="flex flex-wrap gap-1.5">
                          <Badge className={riskClass(event.risk_level)}>{t(`securityEvents.riskValue.${event.risk_level}`, event.risk_level)}</Badge>
                          <Badge className={actionClass(event.action)}>{t(`securityEvents.actionValue.${event.action}`, event.action)}</Badge>
                        </div>
                        <div className="mt-2 text-[12px] text-muted-foreground">
                          {t('securityEvents.scoreConfidence', { score: event.risk_score, confidence: event.confidence })}
                        </div>
                      </TableCell>
                      <TableCell className="min-w-[220px] align-top">
                        <div className={tableTextClass}>{event.endpoint || '-'}</div>
                        <div className="mt-1 truncate text-[12px] text-muted-foreground">{event.model || '-'}</div>
                        <div className="mt-2 flex flex-wrap gap-1.5">
                          <Badge className="border-border bg-muted text-muted-foreground">{t(`securityEvents.directionValue.${event.direction}`, event.direction || '-')}</Badge>
                          <Badge className="border-border bg-muted text-muted-foreground">{t(`securityEvents.sourceValue.${event.source_type}`, event.source_type || '-')}</Badge>
                        </div>
                      </TableCell>
                      <TableCell className="min-w-[220px] align-top">
                        <div className="flex max-w-[260px] flex-wrap gap-1.5">
                          {rules.length > 0 ? rules.map((rule, index) => (
                            <div key={`${event.id}-${rule.rule_id}-${index}`} className="max-w-full space-y-1">
                              <Badge className="border-border bg-muted text-muted-foreground">
                                {rule.rule_id || '-'}
                              </Badge>
                              {rule.evidence ? (
                                <div className="max-w-[260px] truncate font-geist-mono text-[11px] text-muted-foreground">
                                  {rule.evidence}
                                </div>
                              ) : null}
                            </div>
                          )) : <span className="text-[12px] text-muted-foreground">-</span>}
                        </div>
                        {hints.length > 0 ? (
                          <div className="mt-2 flex max-w-[260px] flex-wrap gap-1.5">
                            {hints.map((hint, index) => (
                              <Badge key={`${event.id}-hint-${index}`} className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                                {hint}
                              </Badge>
                            ))}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="min-w-[280px] max-w-[420px] align-top">
                        <SecurityPreviewCard summary={preview} onOpen={() => setSelectedEvent(event)} />
                      </TableCell>
                      <TableCell className="min-w-[220px] align-top">
                        <div className="space-y-1 text-[12px] text-muted-foreground">
                          <div>{t('securityEvents.account')}: {event.account_name || (event.account_id ? `ID ${event.account_id}` : '-')}</div>
                          <div className="truncate">{t('securityEvents.baseUrl')}: {event.base_url || '-'}</div>
                          <div className="truncate">{t('securityEvents.requestId')}: {event.request_id || '-'}</div>
                          <div className="truncate">{t('securityEvents.hash')}: {event.content_hash || '-'}</div>
                          <div className="flex flex-wrap gap-1.5 pt-1">
                            {event.stream ? <Badge className="border-border bg-muted text-muted-foreground">stream</Badge> : null}
                            {event.tool_call ? <Badge className="border-border bg-muted text-muted-foreground">tool</Badge> : null}
                          </div>
                        </div>
                      </TableCell>
                      <TableCell className="min-w-[120px] align-top">
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          disabled={!suppressRuleID || suppressingId === event.id}
                          onClick={() => void handleSuppressEvent(event, suppressRuleID)}
                        >
                          {suppressingId === event.id ? <RefreshCw className="size-3.5 animate-spin" /> : <ShieldCheck className="size-3.5" />}
                          {t('securityEvents.suppress')}
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </div>
          <div className="border-t border-border p-4">
            <Pagination
              page={page}
              totalPages={totalPages}
              totalItems={data.total}
              pageSize={pageSize}
              onPageChange={setPage}
              onPageSizeChange={(next) => {
                setPage(1)
                setPageSize(next)
              }}
              pageSizeOptions={DEFAULT_PAGE_SIZE_OPTIONS}
            />
          </div>
        </div>
      </StateShell>
      <SecurityPreviewDialog event={selectedEvent} onClose={() => setSelectedEvent(null)} />
    </div>
  )
}

function SecurityPreviewCard({ summary, onOpen }: { summary: SecurityPreviewSummary; onOpen: () => void }) {
  const { t } = useTranslation()
  const visibleFields = summary.fields.slice(0, 3)

  return (
    <div className="max-w-[420px] rounded-md border border-border bg-muted/25 p-2.5">
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate font-geist-mono text-[12px] font-semibold leading-5 text-foreground">
            {summary.title}
          </div>
          <div className="mt-1 flex flex-wrap gap-1.5">
            <Badge className={previewToneClass[summary.tone]}>{t(`securityEvents.previewTone.${summary.tone}`)}</Badge>
            <Badge className="border-border bg-muted text-muted-foreground">{summary.subtitle}</Badge>
          </div>
        </div>
        <Button type="button" size="sm" variant="outline" className="h-8 shrink-0 px-2.5" onClick={onOpen}>
          <Eye className="size-3.5" />
          {t('securityEvents.viewDetails')}
        </Button>
      </div>
      {visibleFields.length > 0 ? (
        <div className="mt-2 grid gap-1.5">
          {visibleFields.map((field) => (
            <div key={field.key} className="grid min-w-0 grid-cols-[72px_minmax(0,1fr)] gap-2 text-[11px] leading-5">
              <span className="text-muted-foreground">{t(`securityEvents.previewFields.${field.key}`)}</span>
              <span className="truncate font-geist-mono text-foreground">{field.value}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function SecurityPreviewDialog({ event, onClose }: { event: SecurityEvent | null; onClose: () => void }) {
  const { t } = useTranslation()
  const summary = event ? summarizeSecurityPreview(event.preview, event.scanner_error) : null
  const rules = event ? parseRules(event.rules) : []
  const hints = event ? parseStringList(event.false_positive_hints) : []

  return (
    <Dialog open={Boolean(event)} onOpenChange={(open) => { if (!open) onClose() }}>
      {event && summary ? (
        <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-5xl">
          <DialogHeader>
            <DialogTitle>{t('securityEvents.previewDetailTitle', { id: event.id })}</DialogTitle>
            <DialogDescription>{formatBeijingTime(event.created_at)}</DialogDescription>
          </DialogHeader>

          <div className="flex flex-wrap gap-1.5">
            <Badge className={riskClass(event.risk_level)}>{t(`securityEvents.riskValue.${event.risk_level}`, event.risk_level)}</Badge>
            <Badge className={actionClass(event.action)}>{t(`securityEvents.actionValue.${event.action}`, event.action)}</Badge>
            <Badge className={previewToneClass[summary.tone]}>{t(`securityEvents.previewTone.${summary.tone}`)}</Badge>
            {event.tool_call ? <Badge className="border-border bg-muted text-muted-foreground">tool</Badge> : null}
          </div>

          <div className="grid gap-4 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
            <div className="space-y-4">
              <DetailPanel title={t('securityEvents.previewSummary')}>
                <DetailRow label={t('securityEvents.previewTitle')} value={summary.title} mono />
                <DetailRow label={t('securityEvents.previewSubtitle')} value={summary.subtitle} mono />
                <DetailRow label={t('securityEvents.account')} value={event.account_name || (event.account_id ? `ID ${event.account_id}` : '-')} />
                <DetailRow label={t('securityEvents.baseUrl')} value={event.base_url || '-'} mono />
                <DetailRow label={t('securityEvents.requestId')} value={event.request_id || '-'} mono />
              </DetailPanel>

              {summary.fields.length > 0 ? (
                <DetailPanel title={t('securityEvents.structuredFields')}>
                  {summary.fields.map((field) => (
                    <DetailRow
                      key={field.key}
                      label={t(`securityEvents.previewFields.${field.key}`)}
                      value={field.value}
                      mono
                    />
                  ))}
                </DetailPanel>
              ) : null}
            </div>

            <div className="space-y-4">
              <DetailPanel title={t('securityEvents.rawPreview')}>
                <pre className="max-h-[360px] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/40 p-3 font-geist-mono text-[12px] leading-relaxed text-foreground">
                  {summary.prettyRaw}
                </pre>
              </DetailPanel>

              <DetailPanel title={t('securityEvents.rules')}>
                <div className="flex flex-wrap gap-1.5">
                  {rules.length > 0 ? rules.map((rule, index) => (
                    <Badge key={`${event.id}-dialog-rule-${rule.rule_id}-${index}`} className="border-border bg-muted text-muted-foreground">
                      {rule.rule_id || '-'}
                    </Badge>
                  )) : <span className="text-[12px] text-muted-foreground">-</span>}
                </div>
                {hints.length > 0 ? (
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {hints.map((hint, index) => (
                      <Badge key={`${event.id}-dialog-hint-${index}`} className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                        {hint}
                      </Badge>
                    ))}
                  </div>
                ) : null}
              </DetailPanel>
            </div>
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">{t('common.close')}</Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      ) : null}
    </Dialog>
  )
}

function DetailPanel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-border bg-muted/20 p-4">
      <div className="mb-3 text-[12px] font-semibold uppercase text-muted-foreground">{title}</div>
      <div className="space-y-2">{children}</div>
    </section>
  )
}

function DetailRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid min-w-0 grid-cols-[120px_minmax(0,1fr)] gap-3 text-[12px] leading-5">
      <div className="text-muted-foreground">{label}</div>
      <div className={`min-w-0 break-words ${mono ? 'font-geist-mono' : ''}`}>{value || '-'}</div>
    </div>
  )
}
