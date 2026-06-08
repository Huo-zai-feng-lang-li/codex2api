export type SecurityPreviewFieldKey =
  | 'eventType'
  | 'itemType'
  | 'toolName'
  | 'status'
  | 'command'
  | 'workdir'
  | 'timeout'
  | 'callId'
  | 'turnId'
  | 'outputIndex'
  | 'sequenceNumber'

export type SecurityPreviewTone = 'error' | 'json' | 'text' | 'tool'

export type SecurityPreviewField = {
  key: SecurityPreviewFieldKey
  value: string
}

export type SecurityPreviewSummary = {
  title: string
  subtitle: string
  raw: string
  prettyRaw: string
  tone: SecurityPreviewTone
  fields: SecurityPreviewField[]
}

const fallbackPreview = '-'

export function summarizeSecurityPreview(preview: string, scannerError = ''): SecurityPreviewSummary {
  const raw = (scannerError || preview || '').trim()
  if (!raw) return textSummary(fallbackPreview)
  if (scannerError.trim()) return textSummary(raw, 'error')

  const parsed = parseJSON(raw)
  if (!parsed.ok || !isRecord(parsed.value)) return textSummary(raw)

  const item = isRecord(parsed.value.item) ? parsed.value.item : undefined
  const args = readArguments(item)
  const fields = collectFields(parsed.value, item, args)
  const toolName = readString(item?.name)
  const command = readString(args?.command)
  const eventType = readString(parsed.value.type)
  const itemType = readString(item?.type)
  const isTool = itemType === 'function_call' || Boolean(toolName || command)

  return {
    title: command ? `${toolName || 'tool'}: ${command}` : toolName ? `${itemType || 'tool'}: ${toolName}` : eventType || itemType || 'JSON',
    subtitle: isTool ? eventType || itemType || 'tool_call' : itemType || eventType || 'JSON',
    raw,
    prettyRaw: JSON.stringify(parsed.value, null, 2),
    tone: isTool ? 'tool' : 'json',
    fields,
  }
}

function textSummary(raw: string, tone: SecurityPreviewTone = 'text'): SecurityPreviewSummary {
  return {
    title: firstLine(raw),
    subtitle: tone === 'error' ? 'scanner_error' : 'text',
    raw,
    prettyRaw: raw,
    tone,
    fields: [],
  }
}

function collectFields(
  root: Record<string, unknown>,
  item: Record<string, unknown> | undefined,
  args: Record<string, unknown> | undefined,
): SecurityPreviewField[] {
  const fields: SecurityPreviewField[] = []
  pushField(fields, 'eventType', readString(root.type))
  pushField(fields, 'itemType', readString(item?.type))
  pushField(fields, 'toolName', readString(item?.name))
  pushField(fields, 'status', readString(item?.status))
  pushField(fields, 'command', readString(args?.command))
  pushField(fields, 'workdir', readString(args?.workdir))
  pushField(fields, 'timeout', readNumber(args?.timeout_ms))
  pushField(fields, 'callId', readString(item?.call_id))
  pushField(fields, 'turnId', readString(isRecord(item?.metadata) ? item.metadata.turn_id : undefined))
  pushField(fields, 'outputIndex', readNumber(root.output_index))
  pushField(fields, 'sequenceNumber', readNumber(root.sequence_number))
  return fields
}

function readArguments(item: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
  const raw = item?.arguments
  if (isRecord(raw)) return raw
  if (typeof raw !== 'string' || !raw.trim()) return undefined
  const parsed = parseJSON(raw)
  return parsed.ok && isRecord(parsed.value) ? parsed.value : { command: raw }
}

function pushField(fields: SecurityPreviewField[], key: SecurityPreviewFieldKey, value: string) {
  if (value) fields.push({ key, value })
}

function firstLine(raw: string): string {
  const line = raw.split(/\r?\n/, 1)[0]?.trim() || fallbackPreview
  return line.length > 120 ? `${line.slice(0, 117)}...` : line
}

function readString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function readNumber(value: unknown): string {
  return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function parseJSON(raw: string): { ok: true; value: unknown } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(raw) as unknown }
  } catch {
    return { ok: false }
  }
}
