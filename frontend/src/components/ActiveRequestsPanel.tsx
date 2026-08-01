import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { RuntimeActiveRequest } from '../types'
import { advanceRuntimeDuration } from '../lib/runtimeDuration'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

interface ActiveRequestsPanelProps {
  requests: RuntimeActiveRequest[]
  className?: string
}

export default function ActiveRequestsPanel({ requests, className = '' }: ActiveRequestsPanelProps) {
  const { t } = useTranslation()
  const nowMs = useActiveRequestClock(requests.length > 0)
  const snapshotAtMs = useRequestSnapshotBaseline(requests)
  return (
    <Card className={className}>
      <CardContent className="p-5">
        <div className="mb-4 flex items-center justify-between gap-4">
          <div>
            <h3 className="text-base font-semibold text-foreground">{t('runtime.activeRequestDetails')}</h3>
            <p className="mt-1 text-sm text-muted-foreground">{t('runtime.activeRequestDetailsDesc')}</p>
          </div>
          <Badge variant={requests.length > 0 ? 'default' : 'secondary'} className="shrink-0">
            {t('runtime.activeRequestCount', { count: requests.length })}
          </Badge>
        </div>

        {requests.length === 0 ? (
          <div className="rounded-md border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
            {t('runtime.noActiveRequests')}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('runtime.account')}</TableHead>
                <TableHead>{t('runtime.apiKey')}</TableHead>
                <TableHead>{t('runtime.model')}</TableHead>
                <TableHead>{t('runtime.endpoint')}</TableHead>
                <TableHead>{t('runtime.mode')}</TableHead>
                <TableHead className="text-right">{t('runtime.duration')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {requests.map((request) => (
                <TableRow key={request.id}>
                  <TableCell className="max-w-[220px] truncate font-mono text-[12px]" title={formatRuntimeAccountLabel(request)}>
                    {formatRuntimeAccountLabel(request)}
                  </TableCell>
                  <TableCell className="max-w-[180px] truncate font-mono text-[12px]" title={formatRuntimeAPIKeyLabel(request)}>
                    {formatRuntimeAPIKeyLabel(request)}
                  </TableCell>
                  <TableCell className="max-w-[180px] truncate font-mono text-[12px]" title={request.effective_model || request.model || '-'}>
                    {request.effective_model || request.model || '-'}
                  </TableCell>
                  <TableCell className="max-w-[220px] truncate font-mono text-[12px]" title={formatRuntimeEndpoint(request)}>
                    {formatRuntimeEndpoint(request)}
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{request.stream ? t('runtime.streamMode') : t('runtime.syncMode')}</Badge>
                  </TableCell>
                  <TableCell className="text-right font-mono text-[12px]">
                    {formatDurationMs(advanceRuntimeDuration(request.duration_ms, snapshotAtMs, nowMs))}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}

function useActiveRequestClock(enabled: boolean): number {
  const [nowMs, setNowMs] = useState(Date.now)

  useEffect(() => {
    setNowMs(Date.now())
    if (!enabled) return
    const timer = window.setInterval(() => setNowMs(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [enabled])

  return nowMs
}

function useRequestSnapshotBaseline(requests: RuntimeActiveRequest[]): number {
  const snapshot = useRef({ requests, receivedAtMs: Date.now() })
  if (snapshot.current.requests !== requests) {
    snapshot.current = { requests, receivedAtMs: Date.now() }
  }
  return snapshot.current.receivedAtMs
}

function formatRuntimeAccountLabel(request: RuntimeActiveRequest): string {
  const name = request.account_name?.trim()
  if (name) return name
  const email = request.account_email?.trim()
  if (email) return email
  return `#${request.account_id}`
}

function formatRuntimeAPIKeyLabel(request: RuntimeActiveRequest): string {
  const name = request.api_key_name?.trim()
  if (name) return name
  const masked = request.api_key_masked?.trim()
  if (masked) return masked
  return '-'
}

function formatRuntimeEndpoint(request: RuntimeActiveRequest): string {
  const inbound = request.endpoint || '-'
  const upstream = request.upstream_endpoint || ''
  if (!upstream || upstream === inbound) return inbound
  return `${inbound} -> ${upstream}`
}

function formatDurationMs(ms: number): string {
  if (ms < 1000) return `${Math.max(0, ms)}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  const minutes = Math.floor(ms / 60000)
  const seconds = Math.floor((ms % 60000) / 1000)
  return `${minutes}m ${seconds}s`
}
