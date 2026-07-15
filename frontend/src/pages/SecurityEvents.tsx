import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Download, Eye, FileText, RefreshCw, Save, Search, ShieldCheck, Trash2, X } from 'lucide-react'
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
import { completeSecurityRuleEvidence, formatSecurityRawBody } from '../utils/securityRawBody'
import type { SecurityCapture, SecurityEvent, SystemSettings } from '../types'
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
  captureReason: string
  direction: string
  action: string
  sourceType: string
  toolCall: string
  endpoint: string
  model: string
  accountId: string
  baseUrl: string
  requestId: string
  start: string
  end: string
  q: string
}

type SecurityView = 'events' | 'captures' | 'requestPrompt' | 'responsePrompt'

type PromptSettingsForm = {
  requestEnabled: boolean
  requestPrompt: string
  responseEnabled: boolean
  responsePrompt: string
}

type PromptSettingsKind = 'request' | 'response'

type SecurityRule = {
  rule_id?: string
  evidence?: string
  field?: string
  match?: string
}

const emptyFilters: SecurityEventFilters = {
  riskLevel: '',
  captureReason: '',
  direction: '',
  action: '',
  sourceType: '',
  toolCall: '',
  endpoint: '',
  model: '',
  accountId: '',
  baseUrl: '',
  requestId: '',
  start: '',
  end: '',
  q: '',
}

const tableHeadClass = 'text-[12px] font-semibold'
const tableTextClass = 'text-[14px]'
const monoClass = 'font-geist-mono text-[12px] tabular-nums'
const loadingDelayMs = 220
const previewToneClass: Record<SecurityPreviewSummary['tone'], string> = {
  error: 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300',
  json: 'border-slate-500/25 bg-slate-500/10 text-slate-700 dark:text-slate-300',
  text: 'border-border bg-muted text-muted-foreground',
  tool: 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300',
}

const emptyPromptSettings: PromptSettingsForm = {
  requestEnabled: false,
  requestPrompt: '',
  responseEnabled: false,
  responsePrompt: '',
}

function toPromptSettings(settings: SystemSettings): PromptSettingsForm {
  return {
    requestEnabled: Boolean(settings.proxy_request_system_prompt_enabled),
    requestPrompt: settings.proxy_request_system_prompt || '',
    responseEnabled: Boolean(settings.proxy_response_rewrite_enabled),
    responsePrompt: settings.proxy_response_rewrite_prompt || '',
  }
}

function useDelayedLoading(loading: boolean) {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    if (!loading) {
      setVisible(false)
      return
    }

    const timer = window.setTimeout(() => setVisible(true), loadingDelayMs)
    return () => window.clearTimeout(timer)
  }, [loading])

  return visible
}

function parseRules(raw: string): SecurityRule[] {
  if (!raw.trim()) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.map(normalizeRule).filter((item): item is SecurityRule => Boolean(item))
  } catch {
    return [{ rule_id: raw }]
  }
}

function normalizeRule(item: unknown): SecurityRule | null {
  if (!item || typeof item !== 'object') return null
  const record = item as Record<string, unknown>
  return {
    rule_id: readRuleString(record.rule_id),
    evidence: readRuleString(record.evidence),
    field: readRuleString(record.field),
    match: readRuleString(record.match),
  }
}

function readRuleString(value: unknown) {
  return typeof value === 'string' ? value : ''
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

function shortHash(hash: string) {
  if (!hash) return '-'
  if (hash.length <= 18) return hash
  return `${hash.slice(0, 10)}...${hash.slice(-6)}`
}

function highlightSearchText(body: string, query: string) {
  const needle = query.trim().toLowerCase()
  if (!needle) return body
  const index = body.toLowerCase().indexOf(needle)
  if (index < 0) return body
  const start = Math.max(0, index - 1200)
  const end = Math.min(body.length, index + needle.length + 2200)
  const prefix = start > 0 ? '...\n' : ''
  const suffix = end < body.length ? '\n...' : ''
  return `${prefix}${body.slice(start, end)}${suffix}`
}

function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = bytes
  let index = 0
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
  }
  return `${value >= 10 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`
}

async function copyBody(body: string, onToast: (message: string, type?: 'success' | 'error') => void, successMessage: string, errorMessage: string) {
  try {
    await navigator.clipboard.writeText(body)
    onToast(successMessage, 'success')
  } catch {
    onToast(errorMessage, 'error')
  }
}

function downloadBody(filename: string, body: string) {
  const blob = new Blob([body], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
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
  const [view, setView] = useState<SecurityView>('events')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = usePersistedPageSize('security_events', 20, DEFAULT_PAGE_SIZE_OPTIONS)
  const [capturePage, setCapturePage] = useState(1)
  const [capturePageSize, setCapturePageSize] = usePersistedPageSize('security_captures', 20, DEFAULT_PAGE_SIZE_OPTIONS)
  const [filters, setFilters] = useState<SecurityEventFilters>(emptyFilters)
  const [selectedEvent, setSelectedEvent] = useState<SecurityEvent | null>(null)
  const [selectedCapture, setSelectedCapture] = useState<SecurityCapture | null>(null)
  const [clearing, setClearing] = useState(false)
  const [suppressingId, setSuppressingId] = useState<number | null>(null)
  const [promptSettings, setPromptSettings] = useState<PromptSettingsForm>(emptyPromptSettings)
  const [promptSettingsLoading, setPromptSettingsLoading] = useState(false)
  const [promptSettingsError, setPromptSettingsError] = useState('')
  const [savingPromptKind, setSavingPromptKind] = useState<PromptSettingsKind | null>(null)
  const isPromptView = view === 'requestPrompt' || view === 'responsePrompt'

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

  const loadCaptures = useCallback(async () => {
    const result = await api.getSecurityCaptures({
      page: capturePage,
      pageSize: capturePageSize,
      captureReason: filters.captureReason,
      direction: filters.direction,
      endpoint: filters.endpoint,
      model: filters.model,
      accountId: filters.accountId,
      baseUrl: filters.baseUrl,
      sourceType: filters.sourceType,
      toolCall: filters.toolCall,
      requestId: filters.requestId,
      start: filters.start,
      end: filters.end,
      q: filters.q,
    })
    return {
      captures: result.captures ?? [],
      total: result.total ?? 0,
    }
  }, [capturePage, capturePageSize, filters.accountId, filters.baseUrl, filters.captureReason, filters.direction, filters.end, filters.endpoint, filters.model, filters.q, filters.requestId, filters.sourceType, filters.start, filters.toolCall])

  const handleLoadError = useCallback((message: string) => {
    showToast(message, 'error')
  }, [showToast])

  const { data, loading, error, reload } = useDataLoader({
    initialData: { events: [] as SecurityEvent[], total: 0 },
    load: loadEvents,
    onError: handleLoadError,
  })
  const { data: captureData, loading: captureLoading, error: captureError, reload: reloadCaptures } = useDataLoader({
    initialData: { captures: [] as SecurityCapture[], total: 0 },
    load: loadCaptures,
    onError: handleLoadError,
  })
  const showEventsLoading = useDelayedLoading(loading && data.events.length === 0)
  const showCaptureLoading = useDelayedLoading(captureLoading && captureData.captures.length === 0)

  const totalPages = Math.max(1, Math.ceil(data.total / pageSize))
  const captureTotalPages = Math.max(1, Math.ceil(captureData.total / capturePageSize))
  const activeTotal = view === 'events' ? data.total : view === 'captures' ? captureData.total : 0
  const hasFilters = useMemo(() => Object.values(filters).some(Boolean), [filters])

  const loadPromptSettings = useCallback(async () => {
    setPromptSettingsLoading(true)
    setPromptSettingsError('')
    try {
      const settings = await api.getSettings()
      setPromptSettings(toPromptSettings(settings))
    } catch (err) {
      const message = getErrorMessage(err)
      setPromptSettingsError(message)
      showToast(message, 'error')
    } finally {
      setPromptSettingsLoading(false)
    }
  }, [showToast])
  const activeReload = isPromptView ? () => { void loadPromptSettings() } : view === 'events' ? reload : reloadCaptures

  useEffect(() => {
    if (isPromptView) {
      void loadPromptSettings()
    }
  }, [isPromptView, loadPromptSettings])

  const updateFilter = <K extends keyof SecurityEventFilters>(key: K, value: SecurityEventFilters[K]) => {
    setPage(1)
    setCapturePage(1)
    setFilters((current) => ({ ...current, [key]: value }))
  }

  const clearFilters = () => {
    setPage(1)
    setCapturePage(1)
    setFilters(emptyFilters)
  }

  const switchView = (next: SecurityView) => {
    setView(next)
    setPage(1)
    setCapturePage(1)
    setFilters((current) => {
      if (next === 'captures') return { ...current, riskLevel: '', action: '' }
      if (next === 'events') return { ...current, captureReason: '', requestId: '' }
      return current
    })
  }

  const updatePromptSettings = <K extends keyof PromptSettingsForm>(key: K, value: PromptSettingsForm[K]) => {
    setPromptSettings((current) => ({ ...current, [key]: value }))
  }

  const savePromptSettings = async (kind: PromptSettingsKind) => {
    setSavingPromptKind(kind)
    setPromptSettingsError('')
    try {
      const payload: Partial<SystemSettings> = kind === 'request'
        ? {
            proxy_request_system_prompt_enabled: promptSettings.requestEnabled,
            proxy_request_system_prompt: promptSettings.requestPrompt,
          }
        : {
            proxy_response_rewrite_enabled: promptSettings.responseEnabled,
            proxy_response_rewrite_prompt: promptSettings.responsePrompt,
          }
      const settings = await api.updateSettings(payload)
      setPromptSettings(toPromptSettings(settings))
      showToast(t('securityEvents.promptSaveSuccess'), 'success')
    } catch (err) {
      const message = getErrorMessage(err)
      setPromptSettingsError(message)
      showToast(message, 'error')
    } finally {
      setSavingPromptKind(null)
    }
  }

  const handleClearEvents = async () => {
    if (!window.confirm(t('securityEvents.clearConfirm'))) return
    setClearing(true)
    try {
      await api.clearSecurityEvents()
      showToast(t('securityEvents.clearSuccess'), 'success')
      setPage(1)
      setCapturePage(1)
      await reload()
      await reloadCaptures()
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
        onRefresh={activeReload}
        actions={!isPromptView ? (
          <Button variant="outline" onClick={() => void handleClearEvents()} disabled={clearing || activeTotal === 0}>
            {clearing ? <RefreshCw className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
            {t('securityEvents.clear')}
          </Button>
        ) : undefined}
        actionMeta={!isPromptView ? t('securityEvents.total', { count: activeTotal }) : undefined}
      />

      <div className="flex w-fit flex-wrap rounded-lg border border-border bg-card p-1 shadow-sm">
        <Button
          type="button"
          size="sm"
          variant={view === 'events' ? 'secondary' : 'ghost'}
          onClick={() => switchView('events')}
        >
          <ShieldCheck className="size-4" />
          {t('securityEvents.eventsView')}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={view === 'captures' ? 'secondary' : 'ghost'}
          onClick={() => switchView('captures')}
        >
          <FileText className="size-4" />
          {t('securityEvents.capturesView')}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={view === 'requestPrompt' ? 'secondary' : 'ghost'}
          onClick={() => switchView('requestPrompt')}
        >
          <FileText className="size-4" />
          {t('securityEvents.requestPromptView')}
        </Button>
        <Button
          type="button"
          size="sm"
          variant={view === 'responsePrompt' ? 'secondary' : 'ghost'}
          onClick={() => switchView('responsePrompt')}
        >
          <FileText className="size-4" />
          {t('securityEvents.responsePromptView')}
        </Button>
      </div>

      {!isPromptView ? (
      <div className="rounded-lg border border-border bg-card/80 p-4 shadow-sm">
        <div className="grid grid-cols-[repeat(auto-fit,minmax(160px,1fr))] gap-3">
          {view === 'events' ? (
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
          ) : (
            <Select
              value={filters.captureReason}
              onValueChange={(value) => updateFilter('captureReason', value)}
              placeholder={t('securityEvents.captureReason')}
              options={[
                { label: t('securityEvents.allCaptureReasons'), value: '' },
                { label: t('securityEvents.captureReasonValue.hit'), value: 'hit' },
                { label: t('securityEvents.captureReasonValue.full'), value: 'full' },
              ]}
            />
          )}
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
          {view === 'events' ? (
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
          ) : (
            <Input
              value={filters.requestId}
              placeholder={t('securityEvents.requestId')}
              onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('requestId', e.target.value)}
            />
          )}
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
              placeholder={view === 'events' ? t('securityEvents.search') : t('securityEvents.searchRawBody')}
              onChange={(e: ChangeEvent<HTMLInputElement>) => updateFilter('q', e.target.value)}
            />
            {hasFilters ? (
              <Button type="button" variant="outline" onClick={clearFilters} aria-label={t('securityEvents.clearFilters')}>
                <X className="size-4" />
              </Button>
            ) : (
              <Button type="button" variant="outline" onClick={() => void activeReload()} aria-label={t('securityEvents.searchButton')}>
                <Search className="size-4" />
              </Button>
            )}
          </div>
        </div>
      </div>
      ) : null}

      {view === 'events' ? (
      <StateShell
        loading={showEventsLoading}
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
                              {rule.field ? (
                                <Badge className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                                  {`${t('securityEvents.evidenceField')}: ${rule.field}`}
                                </Badge>
                              ) : null}
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
      ) : view === 'captures' ? (
      <StateShell
        loading={showCaptureLoading}
        error={captureError}
        isEmpty={!captureLoading && !captureError && captureData.captures.length === 0}
        onRetry={reloadCaptures}
        loadingTitle={t('securityEvents.captureLoadingTitle')}
        loadingDescription={t('securityEvents.captureLoadingDesc')}
        emptyTitle={t('securityEvents.captureEmptyTitle')}
        emptyDescription={t('securityEvents.captureEmptyDesc')}
      >
        <CaptureTable
          captures={captureData.captures}
          page={capturePage}
          totalPages={captureTotalPages}
          totalItems={captureData.total}
          pageSize={capturePageSize}
          onOpen={setSelectedCapture}
          onPageChange={setCapturePage}
          onPageSizeChange={(next) => {
            setCapturePage(1)
            setCapturePageSize(next)
          }}
        />
      </StateShell>
      ) : (
        <PromptSettingsPanel
          kind={view === 'requestPrompt' ? 'request' : 'response'}
          form={promptSettings}
          loading={promptSettingsLoading}
          error={promptSettingsError}
          saving={savingPromptKind === (view === 'requestPrompt' ? 'request' : 'response')}
          onChange={updatePromptSettings}
          onSave={savePromptSettings}
          onRetry={loadPromptSettings}
        />
      )}
      <SecurityPreviewDialog event={selectedEvent} onClose={() => setSelectedEvent(null)} />
      <SecurityCaptureDialog capture={selectedCapture} onClose={() => setSelectedCapture(null)} />
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

function PromptSettingsPanel({
  kind,
  form,
  loading,
  error,
  saving,
  onChange,
  onSave,
  onRetry,
}: {
  kind: PromptSettingsKind
  form: PromptSettingsForm
  loading: boolean
  error: string
  saving: boolean
  onChange: <K extends keyof PromptSettingsForm>(key: K, value: PromptSettingsForm[K]) => void
  onSave: (kind: PromptSettingsKind) => Promise<void>
  onRetry: () => Promise<void>
}) {
  const { t } = useTranslation()
  const isRequest = kind === 'request'
  const enabledKey = isRequest ? 'requestEnabled' : 'responseEnabled'
  const promptKey = isRequest ? 'requestPrompt' : 'responsePrompt'
  const enabled = form[enabledKey]
  const prompt = form[promptKey]

  return (
    <section className="rounded-lg border border-border bg-card/80 p-4 shadow-sm">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[15px] font-semibold text-foreground">
            {t(isRequest ? 'securityEvents.requestPromptTitle' : 'securityEvents.responsePromptTitle')}
          </div>
          <div className="mt-1 max-w-3xl text-[13px] leading-5 text-muted-foreground">
            {t(isRequest ? 'securityEvents.requestPromptDesc' : 'securityEvents.responsePromptDesc')}
          </div>
        </div>
        <Button
          type="button"
          variant="outline"
          onClick={() => void onRetry()}
          disabled={loading || saving}
        >
          <RefreshCw className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
          {t('common.refresh')}
        </Button>
      </div>

      {error ? (
        <div className="mb-4 rounded-md border border-red-500/25 bg-red-500/10 px-3 py-2 text-[13px] text-red-700 dark:text-red-300">
          {error}
        </div>
      ) : null}

      <div className="space-y-4">
        <label className="flex items-center gap-2 text-[13px] font-medium text-foreground">
          <input
            type="checkbox"
            checked={enabled}
            disabled={loading || saving}
            className="size-4 rounded border-border accent-primary"
            onChange={(event: ChangeEvent<HTMLInputElement>) => onChange(enabledKey, event.target.checked)}
          />
          {t('securityEvents.promptEnabled')}
        </label>

        <textarea
          value={prompt}
          disabled={loading || saving}
          aria-label={t(isRequest ? 'securityEvents.requestPromptTitle' : 'securityEvents.responsePromptTitle')}
          spellCheck={false}
          className="min-h-[280px] w-full min-w-0 resize-y rounded-md border border-input bg-transparent px-3 py-2 font-geist-mono text-[13px] leading-5 shadow-xs outline-none transition-[color,box-shadow] placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60"
          placeholder={t(isRequest ? 'securityEvents.requestPromptPlaceholder' : 'securityEvents.responsePromptPlaceholder')}
          onChange={(event: ChangeEvent<HTMLTextAreaElement>) => onChange(promptKey, event.target.value)}
        />

        <div className="flex justify-end">
          <Button
            type="button"
            onClick={() => void onSave(kind)}
            disabled={loading || saving}
          >
            {saving ? <RefreshCw className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
            {saving ? t('securityEvents.promptSaving') : t('securityEvents.promptSave')}
          </Button>
        </div>
      </div>
    </section>
  )
}

function CaptureTable({
  captures,
  page,
  totalPages,
  totalItems,
  pageSize,
  onOpen,
  onPageChange,
  onPageSizeChange,
}: {
  captures: SecurityCapture[]
  page: number
  totalPages: number
  totalItems: number
  pageSize: number
  onOpen: (capture: SecurityCapture) => void
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-card/80 shadow-sm">
      <div className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className={tableHeadClass}>{t('securityEvents.time')}</TableHead>
              <TableHead className={tableHeadClass}>{t('securityEvents.captureReason')}</TableHead>
              <TableHead className={tableHeadClass}>{t('securityEvents.target')}</TableHead>
              <TableHead className={tableHeadClass}>{t('securityEvents.metadata')}</TableHead>
              <TableHead className={tableHeadClass}>{t('securityEvents.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {captures.map((capture) => (
              <TableRow key={capture.id}>
                <TableCell className="min-w-[150px] align-top">
                  <div className={monoClass}>{formatBeijingTime(capture.created_at)}</div>
                  <div className="mt-1 text-[12px] text-muted-foreground">{formatRelativeTime(capture.created_at)}</div>
                </TableCell>
                <TableCell className="min-w-[150px] align-top">
                  <div className="flex flex-wrap gap-1.5">
                    <Badge className={capture.capture_reason === 'full' ? 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300' : 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300'}>
                      {t(`securityEvents.captureReasonValue.${capture.capture_reason}`, capture.capture_reason)}
                    </Badge>
                    <Badge className="border-border bg-muted text-muted-foreground">
                      {t(`securityEvents.directionValue.${capture.direction}`, capture.direction || '-')}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell className="min-w-[220px] align-top">
                  <div className={tableTextClass}>{capture.endpoint || '-'}</div>
                  <div className="mt-1 truncate text-[12px] text-muted-foreground">{capture.model || '-'}</div>
                  <div className="mt-1 truncate text-[12px] text-muted-foreground">{capture.account_name || (capture.account_id ? `ID ${capture.account_id}` : '-')}</div>
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <Badge className="border-border bg-muted text-muted-foreground">
                      {t(`securityEvents.sourceValue.${capture.source_type}`, capture.source_type || '-')}
                    </Badge>
                    {capture.tool_call ? <Badge className="border-border bg-muted text-muted-foreground">tool</Badge> : null}
                  </div>
                </TableCell>
                <TableCell className="min-w-[300px] align-top">
                  <div className="space-y-1 text-[12px] text-muted-foreground">
                    <div className="flex flex-wrap gap-1.5">
                      <Badge className="border-border bg-muted text-muted-foreground">{formatBytes(capture.body_bytes)}</Badge>
                      {capture.stream ? <Badge className="border-border bg-muted text-muted-foreground">stream</Badge> : null}
                      {capture.truncated ? <Badge className="border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300">truncated</Badge> : null}
                    </div>
                    <div className="truncate">{t('securityEvents.requestId')}: {capture.request_id || '-'}</div>
                    <div className="truncate">{t('securityEvents.baseUrl')}: {capture.base_url || '-'}</div>
                    <div className="truncate">{t('securityEvents.hash')}: {shortHash(capture.body_hash || '')}</div>
                    <div>{t('securityEvents.expiresAt')}: {capture.expires_at ? formatBeijingTime(capture.expires_at) : '-'}</div>
                  </div>
                </TableCell>
                <TableCell className="min-w-[120px] align-top">
                  <Button type="button" size="sm" variant="outline" onClick={() => onOpen(capture)}>
                    <Eye className="size-3.5" />
                    {t('securityEvents.viewDetails')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <div className="border-t border-border p-4">
        <Pagination
          page={page}
          totalPages={totalPages}
          totalItems={totalItems}
          pageSize={pageSize}
          onPageChange={onPageChange}
          onPageSizeChange={onPageSizeChange}
          pageSizeOptions={DEFAULT_PAGE_SIZE_OPTIONS}
        />
      </div>
    </div>
  )
}

function SecurityPreviewDialog({ event, onClose }: { event: SecurityEvent | null; onClose: () => void }) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  const summary = event ? summarizeSecurityPreview(event.preview, event.scanner_error) : null
  const rules = event ? parseRules(event.rules) : []
  const hints = event ? parseStringList(event.false_positive_hints) : []
  const loadCaptures = useCallback(async () => {
    if (!event) return { captures: [] as SecurityCapture[], total: 0 }
    const result = await api.getSecurityEventCaptures(event.id)
    return { captures: result.captures ?? [], total: result.total ?? 0 }
  }, [event])
  const { data: captureData, loading: capturesLoading } = useDataLoader({
    initialData: { captures: [] as SecurityCapture[], total: 0 },
    load: loadCaptures,
  })
  const evidenceBody = captureData.captures.find((capture) => Boolean(capture.body))?.body || event?.preview || ''

  return (
    <Dialog open={Boolean(event)} onOpenChange={(open) => { if (!open) onClose() }}>
      {event && summary ? (
        <DialogContent className="!flex !h-[min(920px,calc(100dvh-1rem))] !w-[min(1500px,calc(100vw-1rem))] !max-w-none flex-col gap-0 overflow-hidden p-0">
          <DialogHeader className="shrink-0 border-b border-border px-5 pb-4 pr-12 pt-5">
            <DialogTitle>{t('securityEvents.previewDetailTitle', { id: event.id })}</DialogTitle>
            <DialogDescription>{formatBeijingTime(event.created_at)}</DialogDescription>
            <div className="flex flex-wrap gap-1.5 pt-1">
              <Badge className={riskClass(event.risk_level)}>{t(`securityEvents.riskValue.${event.risk_level}`, event.risk_level)}</Badge>
              <Badge className={actionClass(event.action)}>{t(`securityEvents.actionValue.${event.action}`, event.action)}</Badge>
              <Badge className={previewToneClass[summary.tone]}>{t(`securityEvents.previewTone.${summary.tone}`)}</Badge>
              {event.tool_call ? <Badge className="border-border bg-muted text-muted-foreground">tool</Badge> : null}
            </div>
          </DialogHeader>

          <div className="grid min-h-0 flex-1 gap-4 overflow-hidden p-4 lg:grid-cols-[minmax(320px,0.8fr)_minmax(0,1.2fr)]">
            <aside className="min-h-0 space-y-4 overflow-auto pr-1">
              <EventVerdictPanel event={event} summary={summary} />
              <DetailPanel title={t('securityEvents.hitEvidence')}>
                <RuleEvidenceList rules={rules} body={evidenceBody} />
                {hints.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {hints.map((hint, index) => (
                      <Badge key={`${event.id}-dialog-hint-${index}`} className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                        {hint}
                      </Badge>
                    ))}
                  </div>
                ) : null}
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
              <DetailPanel title={t('securityEvents.rawPreview')}>
                <pre className="max-h-[220px] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-background p-3 font-geist-mono text-[12px] leading-relaxed text-foreground">
                  {summary.prettyRaw}
                </pre>
              </DetailPanel>
            </aside>

            <section className="min-h-0 overflow-auto rounded-lg border border-border bg-muted/15 p-4">
              <div className="mb-3 flex items-center justify-between gap-3">
                <div>
                  <div className="text-[13px] font-semibold text-foreground">{t('securityEvents.rawCaptures')}</div>
                  <div className="mt-1 text-[12px] text-muted-foreground">{t('securityEvents.rawCapturesDesc')}</div>
                </div>
                <Badge className="border-border bg-background text-muted-foreground">{String(captureData.total)}</Badge>
              </div>
              {capturesLoading ? (
                <div className="text-[12px] text-muted-foreground">{t('securityEvents.captureLoadingTitle')}</div>
              ) : captureData.captures.length > 0 ? (
                <div className="space-y-3">
                  {captureData.captures.map((capture) => (
                    <RawBodyPanel key={capture.id} capture={capture} onToast={showToast} compact />
                  ))}
                </div>
              ) : (
                <div className="text-[12px] text-muted-foreground">{t('securityEvents.noRawCaptures')}</div>
              )}
            </section>
          </div>

          <DialogFooter className="shrink-0 border-t border-border px-5 py-4">
            <DialogClose asChild>
              <Button type="button" variant="outline">{t('common.close')}</Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      ) : null}
    </Dialog>
  )
}

function EventVerdictPanel({ event, summary }: { event: SecurityEvent; summary: SecurityPreviewSummary }) {
  const { t } = useTranslation()
  return (
    <DetailPanel title={t('securityEvents.alertSummary')}>
      <div className="flex flex-wrap gap-1.5">
        <Badge className={riskClass(event.risk_level)}>{t(`securityEvents.riskValue.${event.risk_level}`, event.risk_level)}</Badge>
        <Badge className={actionClass(event.action)}>{t(`securityEvents.actionValue.${event.action}`, event.action)}</Badge>
        <Badge className="border-border bg-background text-muted-foreground">
          {t('securityEvents.scoreConfidence', { score: event.risk_score, confidence: event.confidence })}
        </Badge>
      </div>
      <DetailRow label={t('securityEvents.previewTitle')} value={summary.title} mono />
      <DetailRow label={t('securityEvents.previewSubtitle')} value={summary.subtitle} mono />
      <DetailRow label={t('securityEvents.direction')} value={t(`securityEvents.directionValue.${event.direction}`, event.direction)} />
      <DetailRow label={t('securityEvents.account')} value={event.account_name || (event.account_id ? `ID ${event.account_id}` : '-')} />
      <DetailRow label={t('securityEvents.baseUrl')} value={event.base_url || '-'} mono />
      <DetailRow label={t('securityEvents.requestId')} value={event.request_id || '-'} mono />
      <DetailRow label={t('securityEvents.hash')} value={event.content_hash || '-'} mono />
    </DetailPanel>
  )
}

function RuleEvidenceList({ rules, body }: { rules: SecurityRule[]; body: string }) {
  const { t } = useTranslation()
  if (rules.length === 0) return <div className="text-[12px] text-muted-foreground">{t('securityEvents.noRuleHit')}</div>
  return (
    <div className="space-y-2">
      {rules.map((rule, index) => {
        const evidence = completeSecurityRuleEvidence(rule, body)
        return (
          <div key={`${evidence.ruleId}-${index}`} className="rounded-md border border-border bg-background p-3">
            <div className="mb-2 flex flex-wrap gap-1.5">
              <Badge className="border-border bg-muted text-muted-foreground">{evidence.ruleId || '-'}</Badge>
              <Badge className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                {`${t('securityEvents.evidenceField')}: ${evidence.field || 'body'}`}
              </Badge>
            </div>
            {evidence.reason ? <EvidenceRow label={t('securityEvents.evidenceReason')} value={evidence.reason} /> : null}
            {evidence.match ? <EvidenceRow label={t('securityEvents.evidenceMatch')} value={evidence.match} /> : null}
          </div>
        )
      })}
    </div>
  )
}

function EvidenceRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid min-w-0 grid-cols-[78px_minmax(0,1fr)] gap-2 text-[12px] leading-5">
      <div className="text-muted-foreground">{label}</div>
      <pre className="max-h-[120px] overflow-auto whitespace-pre-wrap break-words font-geist-mono text-[11px] leading-relaxed text-foreground">{value || '-'}</pre>
    </div>
  )
}

function SecurityCaptureDialog({ capture, onClose }: { capture: SecurityCapture | null; onClose: () => void }) {
  const { t } = useTranslation()
  const { showToast } = useToast()
  return (
    <Dialog open={Boolean(capture)} onOpenChange={(open) => { if (!open) onClose() }}>
      {capture ? (
        <DialogContent className="!flex !h-[min(920px,calc(100dvh-1rem))] !w-[min(1500px,calc(100vw-1rem))] !max-w-none flex-col gap-0 overflow-hidden p-0">
          <DialogHeader className="shrink-0 border-b border-border px-5 pb-4 pr-12 pt-5">
            <DialogTitle>{t('securityEvents.captureDetailTitle', { id: capture.id })}</DialogTitle>
            <DialogDescription>{formatBeijingTime(capture.created_at)}</DialogDescription>
          </DialogHeader>
          <div className="grid min-h-0 flex-1 gap-4 overflow-hidden p-4 lg:grid-cols-[minmax(320px,0.7fr)_minmax(0,1.3fr)]">
            <div className="min-h-0 space-y-4 overflow-auto">
              <CaptureEvidencePanel capture={capture} />
              <DetailPanel title={t('securityEvents.metadata')}>
                <DetailRow label={t('securityEvents.captureReason')} value={t(`securityEvents.captureReasonValue.${capture.capture_reason}`, capture.capture_reason)} />
                <DetailRow label={t('securityEvents.direction')} value={t(`securityEvents.directionValue.${capture.direction}`, capture.direction)} />
                <DetailRow label={t('securityEvents.endpoint')} value={capture.endpoint || '-'} mono />
                <DetailRow label={t('securityEvents.model')} value={capture.model || '-'} mono />
                <DetailRow label={t('securityEvents.sourceType')} value={t(`securityEvents.sourceValue.${capture.source_type}`, capture.source_type || '-')} />
                <DetailRow label={t('securityEvents.baseUrl')} value={capture.base_url || '-'} mono />
                <DetailRow label={t('securityEvents.toolCall')} value={capture.tool_call ? t('securityEvents.yes') : t('securityEvents.no')} />
                <DetailRow label={t('securityEvents.requestId')} value={capture.request_id || '-'} mono />
                <DetailRow label={t('securityEvents.bodyBytes')} value={formatBytes(capture.body_bytes)} mono />
                <DetailRow label={t('securityEvents.hash')} value={capture.body_hash || '-'} mono />
                <DetailRow label={t('securityEvents.expiresAt')} value={capture.expires_at ? formatBeijingTime(capture.expires_at) : '-'} mono />
              </DetailPanel>
            </div>
            <RawBodyPanel capture={capture} onToast={showToast} />
          </div>
          <DialogFooter className="shrink-0 border-t border-border px-5 py-4">
            <DialogClose asChild>
              <Button type="button" variant="outline">{t('common.close')}</Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      ) : null}
    </Dialog>
  )
}

function CaptureEvidencePanel({ capture }: { capture: SecurityCapture }) {
  const { t } = useTranslation()
  const rules = parseRules(capture.event_rules || '')
  const hints = parseStringList(capture.event_false_positive_hints || '')
  const hasEvidence = capture.security_event_id > 0 || rules.length > 0 || Boolean(capture.event_preview || capture.event_scanner_error)
  return (
    <DetailPanel title={t('securityEvents.hitEvidence')}>
      {hasEvidence ? (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-1.5">
            {capture.event_risk_level ? <Badge className={riskClass(capture.event_risk_level)}>{t(`securityEvents.riskValue.${capture.event_risk_level}`, capture.event_risk_level)}</Badge> : null}
            {capture.event_action ? <Badge className={actionClass(capture.event_action)}>{t(`securityEvents.actionValue.${capture.event_action}`, capture.event_action)}</Badge> : null}
            {capture.event_risk_score || capture.event_confidence ? (
              <Badge className="border-border bg-muted text-muted-foreground">
                {t('securityEvents.scoreConfidence', { score: capture.event_risk_score || 0, confidence: capture.event_confidence || 0 })}
              </Badge>
            ) : null}
          </div>
          <div className="space-y-2">
            <div className="text-[12px] text-muted-foreground">{t('securityEvents.rules')}</div>
            <RuleEvidenceList rules={rules} body={capture.body} />
          </div>
          {capture.event_preview ? (
            <DetailRow label={t('securityEvents.preview')} value={capture.event_preview} mono />
          ) : null}
          {capture.event_scanner_error ? (
            <DetailRow label={t('securityEvents.scannerError')} value={capture.event_scanner_error} mono />
          ) : null}
          {hints.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {hints.map((hint, index) => (
                <Badge key={`${capture.id}-capture-hint-${index}`} className="border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300">
                  {hint}
                </Badge>
              ))}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="text-[12px] text-muted-foreground">{t('securityEvents.noRuleHit')}</div>
      )}
    </DetailPanel>
  )
}

function RawBodyPanel({ capture, onToast, compact = false }: { capture: SecurityCapture; onToast: (message: string, type?: 'success' | 'error') => void; compact?: boolean }) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [formatted, setFormatted] = useState(true)
  const hasCaptureError = Boolean(capture.capture_error?.trim())
  const formattedBody = useMemo(() => formatSecurityRawBody(capture.body), [capture.body])
  const body = formatted ? formattedBody.text : capture.body
  const visibleBody = query ? highlightSearchText(body, query) : body
  const filename = `security-capture-${capture.id}-${capture.direction || 'body'}.txt`
  const preClass = formatted ? 'whitespace-pre' : 'whitespace-pre-wrap break-words'
  return (
    <section className={compact ? 'rounded-md border border-border bg-background p-3' : 'flex min-h-0 flex-col rounded-lg border border-border bg-muted/20 p-4'}>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <div className="text-[12px] font-semibold text-foreground">{t('securityEvents.rawBody')}</div>
            <Badge className="border-border bg-muted text-muted-foreground">{t(`securityEvents.formatKind.${formattedBody.kind}`, formattedBody.kind)}</Badge>
            {formattedBody.folded ? (
              <Badge className="border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300">{t('securityEvents.foldedPayload')}</Badge>
            ) : null}
          </div>
          <div className="mt-1 break-all text-[11px] text-muted-foreground">
            {capture.direction} · {formatBytes(capture.body_bytes)} · {capture.body_hash || '-'}
          </div>
        </div>
        {!hasCaptureError ? (
          <div className="flex shrink-0 flex-wrap gap-2">
            <Button type="button" size="sm" variant="outline" onClick={() => setFormatted((value) => !value)}>
              <FileText className="size-3.5" />
              {formatted ? t('securityEvents.rawText') : t('securityEvents.reviewFormat')}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => void copyBody(capture.body, onToast, t('securityEvents.copySuccess'), t('securityEvents.copyFailed'))}>
              <Copy className="size-3.5" />
              {t('securityEvents.copy')}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => downloadBody(filename, capture.body)}>
              <Download className="size-3.5" />
              {t('securityEvents.download')}
            </Button>
          </div>
        ) : null}
      </div>
      {hasCaptureError ? (
        <div className="rounded-md border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-[12px] leading-5 text-amber-800 dark:text-amber-200">
          <div className="font-semibold">{t('securityEvents.rawUnavailable')}</div>
          <div className="mt-1 font-geist-mono">{capture.capture_error}</div>
        </div>
      ) : (
        <Input
          value={query}
          placeholder={t('securityEvents.searchRawBody')}
          className="mb-3"
          onChange={(e: ChangeEvent<HTMLInputElement>) => setQuery(e.target.value)}
        />
      )}
      {!hasCaptureError && formattedBody.folded && formatted ? (
        <div className="mb-3 rounded-md border border-amber-500/20 bg-amber-500/10 px-3 py-2 text-[12px] leading-5 text-amber-800 dark:text-amber-200">
          {t('securityEvents.foldedPayloadHint')}
        </div>
      ) : null}
      {!hasCaptureError ? (
        <pre className={`${compact ? 'max-h-[280px]' : 'min-h-[320px] flex-1'} overflow-auto ${preClass} rounded-md border border-border bg-background p-3 font-geist-mono text-[12px] leading-relaxed text-foreground`}>
          {visibleBody}
        </pre>
      ) : null}
    </section>
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
