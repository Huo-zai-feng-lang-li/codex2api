import type { OpsErrorSummary } from '../types'

export type OpsErrorSummaryMetricKey =
  | 'terminal_errors'
  | 'total_errors'
  | 'retry_errors'
  | 'retry_attempts'

export type OpsErrorSummaryLabelKey =
  | 'opsErrors.failedRequests'
  | 'opsErrors.attemptErrors'
  | 'opsErrors.retryErrors'
  | 'opsErrors.retryAttempts'

interface OpsErrorSummaryMetricDefinition {
  key: OpsErrorSummaryMetricKey
  labelKey: OpsErrorSummaryLabelKey
  tone: 'danger' | 'info'
}

const metricDefinitions = [
  { key: 'terminal_errors', labelKey: 'opsErrors.failedRequests', tone: 'danger' },
  { key: 'total_errors', labelKey: 'opsErrors.attemptErrors', tone: 'danger' },
  { key: 'retry_errors', labelKey: 'opsErrors.retryErrors', tone: 'danger' },
  { key: 'retry_attempts', labelKey: 'opsErrors.retryAttempts', tone: 'info' },
] as const satisfies readonly OpsErrorSummaryMetricDefinition[]

export function buildOpsErrorSummaryMetrics(summary: OpsErrorSummary | null | undefined) {
  return metricDefinitions.map((metric) => ({
    ...metric,
    value: summary?.[metric.key] ?? 0,
  }))
}
