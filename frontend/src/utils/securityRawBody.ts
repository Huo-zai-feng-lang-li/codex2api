export type SecurityRawBodyKind = 'json' | 'jsonl' | 'sse' | 'text'

export type SecurityRawBodyFormat = {
  text: string
  kind: SecurityRawBodyKind
  folded: boolean
}

export type SecurityRuleLike = {
  rule_id?: string
  evidence?: string
  field?: string
  match?: string
}

export type CompletedSecurityRuleEvidence = {
  ruleId: string
  reason: string
  field: string
  match: string
}

const tokenPattern = /\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{32,}|ghp_[A-Za-z0-9_]{30,}|xoxb-[A-Za-z0-9-]{20,}|AKIA[0-9A-Z]{16})\b/i
const dbURLPattern = /\b(?:postgres|postgresql|mysql|mongodb|redis):\/\/[^:\s/@]+:[^@\s]+@[^)\s'"<>]+/i
const envLinePattern = /^[A-Z][A-Z0-9_]{2,64}\s*=\s*\S+/gm
const jsonPathIdentifierPattern = /^[A-Za-z_][A-Za-z0-9_]*$/

const opaquePreviewChars = 240
const opaqueMinChars = 512

export function formatSecurityRawBody(body: string): SecurityRawBodyFormat {
  const sse = formatSSEBody(body)
  if (sse) return sse

  const json = parseJSON(body)
  if (json.ok) return formatJSONValue(json.value, 'json')

  const jsonl = formatJSONLines(body)
  if (jsonl) return jsonl

  if (isOpaquePayload(body)) {
    return { text: foldedOpaquePayload(body), kind: 'text', folded: true }
  }
  return { text: body, kind: 'text', folded: false }
}

export function completeSecurityRuleEvidence(rule: SecurityRuleLike, body: string): CompletedSecurityRuleEvidence {
  const inferred = inferRuleEvidence((rule.rule_id ?? '').trim(), body)
  return {
    ruleId: (rule.rule_id ?? '').trim(),
    reason: (rule.evidence ?? '').trim(),
    field: (rule.field ?? '').trim() || inferred.field || 'body',
    match: (rule.match ?? '').trim() || inferred.match,
  }
}

function formatSSEBody(body: string): SecurityRawBodyFormat | null {
  let folded = false
  let hasData = false
  const text = body.split(/\r?\n/).map((line) => {
    const trimmed = line.trimStart()
    if (!trimmed.startsWith('data:')) return line
    hasData = true
    const data = trimmed.slice(5).trim()
    if (!data || data === '[DONE]') return `data: ${data}`
    const json = parseJSON(data)
    if (!json.ok) return line
    const formatted = formatJSONValue(json.value, 'json')
    folded = folded || formatted.folded
    return `data:\n${formatted.text.split('\n').map((part) => `  ${part}`).join('\n')}`
  }).join('\n')
  return hasData ? { text, kind: 'sse', folded } : null
}

function formatJSONLines(body: string): SecurityRawBodyFormat | null {
  const lines = body.split(/\r?\n/).filter((line) => line.trim())
  if (lines.length <= 1) return null
  const formatted = lines.map((line) => {
    const json = parseJSON(line)
    return json.ok ? formatJSONValue(json.value, 'json') : null
  })
  if (formatted.some((item) => item === null)) return null
  return {
    text: formatted.map((item) => item?.text ?? '').join('\n'),
    kind: 'jsonl',
    folded: formatted.some((item) => Boolean(item?.folded)),
  }
}

function formatJSONValue(value: unknown, kind: SecurityRawBodyKind): SecurityRawBodyFormat {
  const reviewed = reviewJSONValue(value, 0)
  return {
    text: JSON.stringify(reviewed.value, null, 2),
    kind,
    folded: reviewed.folded,
  }
}

function reviewJSONValue(value: unknown, depth: number): { value: unknown; folded: boolean } {
  if (typeof value === 'string') return reviewJSONString(value, depth)
  if (Array.isArray(value)) return reviewJSONArray(value, depth)
  if (isRecord(value)) return reviewJSONObject(value, depth)
  return { value, folded: false }
}

function reviewJSONString(value: string, depth: number): { value: unknown; folded: boolean } {
  if (depth < 8 && isJSONLike(value)) {
    const parsed = parseJSON(value)
    if (parsed.ok) return reviewJSONValue(parsed.value, depth + 1)
  }
  if (isOpaquePayload(value)) {
    return { value: foldedOpaquePayload(value), folded: true }
  }
  return { value, folded: false }
}

function reviewJSONArray(values: unknown[], depth: number): { value: unknown[]; folded: boolean } {
  let folded = false
  const value = values.map((item) => {
    const reviewed = reviewJSONValue(item, depth)
    folded = folded || reviewed.folded
    return reviewed.value
  })
  return { value, folded }
}

function reviewJSONObject(value: Record<string, unknown>, depth: number): { value: Record<string, unknown>; folded: boolean } {
  let folded = false
  const result: Record<string, unknown> = {}
  Object.entries(value).forEach(([key, item]) => {
    const reviewed = reviewJSONValue(item, depth)
    folded = folded || reviewed.folded
    result[key] = reviewed.value
  })
  return { value: result, folded }
}

function foldedOpaquePayload(value: string) {
  const trimmed = value.trim()
  const preview = trimmed.slice(0, opaquePreviewChars)
  const suffix = trimmed.length > opaquePreviewChars ? '...' : ''
  return `[base64-like payload · ${formatChars(trimmed.length)} · use raw view or download for the full value]\n${preview}${suffix}`
}

function inferRuleEvidence(ruleID: string, body: string): { field: string; match: string } {
  const match = inferRuleMatch(ruleID, body)
  return { field: match ? locateEvidenceField(body, match) : 'body', match }
}

function inferRuleMatch(ruleID: string, body: string): string {
  if (ruleID === 'dlp_token') return tokenPattern.exec(body)?.[0] ?? ''
  if (ruleID === 'dlp_database_url') return dbURLPattern.exec(body)?.[0] ?? ''
  if (ruleID === 'dlp_private_key') return body.includes('PRIVATE KEY') ? 'PRIVATE KEY' : ''
  if (ruleID === 'dlp_env_bulk') return Array.from(body.matchAll(envLinePattern)).slice(0, 3).map((item) => item[0]).join('\n')
  if (ruleID === 'tool_call') return firstPresent(body.toLowerCase(), ['tool_calls', 'function_call'])
  if (ruleID === 'unknown_field') return firstPresent(body.toLowerCase(), ['x-injected', 'developer_override'])
  if (ruleID === 'response_injection') return injectionTerms(body.toLowerCase()).join(' + ')
  return ''
}

function locateEvidenceField(body: string, match: string): string {
  const needles = splitEvidenceNeedles(match)
  return locateJSONEvidencePath(body, needles) || locateSSEEvidencePath(body, needles) || 'body'
}

function locateSSEEvidencePath(body: string, needles: string[]) {
  let index = 0
  for (const line of body.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (!trimmed.startsWith('data:')) continue
    const data = trimmed.slice(5).trim()
    if (!data || data === '[DONE]') continue
    const path = locateJSONEvidencePath(data, needles)
    if (path) return `sse[${index}]${path.replace(/^\$/, '')}`
    index += 1
  }
  return ''
}

function locateJSONEvidencePath(raw: string, needles: string[]) {
  const parsed = parseJSON(raw)
  return parsed.ok ? locateEvidenceInValue(parsed.value, '$', needles) : ''
}

function locateEvidenceInValue(value: unknown, path: string, needles: string[]): string {
  if (typeof value === 'string') return containsNeedle(value, needles) ? path : ''
  if (Array.isArray(value)) return locateEvidenceInArray(value, path, needles)
  if (isRecord(value)) return locateEvidenceInObject(value, path, needles)
  return ''
}

function locateEvidenceInArray(values: unknown[], path: string, needles: string[]) {
  for (let index = 0; index < values.length; index += 1) {
    const found = locateEvidenceInValue(values[index], `${path}[${index}]`, needles)
    if (found) return found
  }
  return ''
}

function locateEvidenceInObject(value: Record<string, unknown>, path: string, needles: string[]) {
  for (const [key, item] of Object.entries(value)) {
    const childPath = jsonFieldPath(path, key)
    if (containsNeedle(key, needles)) return childPath
    const found = locateEvidenceInValue(item, childPath, needles)
    if (found) return found
  }
  return ''
}

function splitEvidenceNeedles(match: string) {
  return match.split(/\s+\+\s+|\r?\n/).map((item) => item.trim().toLowerCase()).filter(Boolean)
}

function injectionTerms(lower: string) {
  return [
    firstPresent(lower, ['ignore', 'bypass', 'disable', 'do not tell', 'without telling', '忽略', '绕过', '关闭', '不要告诉']),
    firstPresent(lower, ['upload', 'send', 'leak', 'copy', 'read', '上传', '发送', '泄露', '复制', '读取']),
    firstPresent(lower, ['source', 'api key', 'token', 'system prompt', 'environment', '源码', '密钥', '私钥', '环境变量', '系统提示']),
  ].filter(Boolean)
}

function firstPresent(text: string, values: string[]) {
  return values.find((value) => text.includes(value)) ?? ''
}

function containsNeedle(value: string, needles: string[]) {
  const lower = value.toLowerCase()
  return needles.some((needle) => lower.includes(needle))
}

function jsonFieldPath(parent: string, key: string) {
  return jsonPathIdentifierPattern.test(key) ? `${parent}.${key}` : `${parent}[${JSON.stringify(key)}]`
}

function isOpaquePayload(value: string) {
  const trimmed = value.trim()
  if (trimmed.length < opaqueMinChars) return false
  if (/^data:[^,]+;base64,/i.test(trimmed)) return true
  const whitespaceChars = trimmed.replace(/\S/g, '').length
  if (whitespaceChars / trimmed.length > 0.05) return false
  const normalized = trimmed.replace(/\s+/g, '')
  if (normalized.length < opaqueMinChars) return false
  const opaqueChars = normalized.replace(/[A-Za-z0-9+/_=-]/g, '').length
  return opaqueChars / normalized.length < 0.02
}

function isJSONLike(value: string) {
  const trimmed = value.trim()
  return (trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))
}

function parseJSON(raw: string): { ok: true; value: unknown } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(raw) as unknown }
  } catch {
    return { ok: false }
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function formatChars(chars: number) {
  if (chars >= 1024 * 1024) return `${(chars / 1024 / 1024).toFixed(1)}M chars`
  if (chars >= 1024) return `${(chars / 1024).toFixed(1)}K chars`
  return `${chars} chars`
}
