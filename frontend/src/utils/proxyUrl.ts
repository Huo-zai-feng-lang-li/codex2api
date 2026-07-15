export type ProxyScheme = 'http' | 'https' | 'socks5' | 'socks5h'

export const DEFAULT_PROXY_URL = 'http://127.0.0.1:51081'

export interface ProxyParts {
  scheme: ProxyScheme
  host: string
  port: string
  username: string
  password: string
}

const DEFAULT_PROXY_SCHEME: ProxyScheme = 'http'
const SUPPORTED_PROXY_SCHEMES: ProxyScheme[] = ['http', 'https', 'socks5', 'socks5h']

function normalizeProxyScheme(value: string | undefined): ProxyScheme {
  const normalized = (value || '').replace(/:$/, '').toLowerCase()
  return SUPPORTED_PROXY_SCHEMES.includes(normalized as ProxyScheme)
    ? (normalized as ProxyScheme)
    : DEFAULT_PROXY_SCHEME
}

function extractAuthority(value: string): string {
  const schemeIndex = value.indexOf('://')
  if (schemeIndex < 0) return ''
  return value
    .slice(schemeIndex + 3)
    .split(/[/?#]/, 1)[0]
}

function extractExplicitPort(value: string): string {
  const authority = extractAuthority(value)
  const hostPort = authority.includes('@')
    ? authority.slice(authority.lastIndexOf('@') + 1)
    : authority

  if (hostPort.startsWith('[')) {
    return hostPort.match(/^\[[^\]]+\]:(\d+)$/)?.[1] ?? ''
  }

  return hostPort.match(/:(\d+)$/)?.[1] ?? ''
}

function hasCompleteCredentials(parsed: URL): boolean {
  return (
    (parsed.username === '' && parsed.password === '') ||
    (parsed.username !== '' && parsed.password !== '')
  )
}

export function emptyProxyParts(scheme: ProxyScheme = DEFAULT_PROXY_SCHEME): ProxyParts {
  return {
    scheme,
    host: '',
    port: '',
    username: '',
    password: '',
  }
}

export function parseProxyUrl(value: string): ProxyParts {
  const trimmed = value.trim()
  if (!trimmed) return emptyProxyParts()

  try {
    const parsed = new URL(trimmed)
    return {
      scheme: normalizeProxyScheme(parsed.protocol),
      host: parsed.hostname.replace(/^\[(.*)\]$/, '$1'),
      port: parsed.port || extractExplicitPort(trimmed),
      username: decodeURIComponent(parsed.username),
      password: decodeURIComponent(parsed.password),
    }
  } catch {
    return emptyProxyParts()
  }
}

export function buildProxyUrl(parts: ProxyParts): string {
  const scheme = normalizeProxyScheme(parts.scheme)
  const host = parts.host.trim()
  const port = parts.port.trim()
  if (!host) return ''

  const auth =
    parts.username.trim() || parts.password
      ? `${encodeURIComponent(parts.username.trim())}:${encodeURIComponent(parts.password)}@`
      : ''
  const wrappedHost = host.includes(':') && !host.startsWith('[') && !host.endsWith(']') ? `[${host}]` : host
  const portPart = port ? `:${port}` : ''
  return `${scheme}://${auth}${wrappedHost}${portPart}`
}

export function isValidProxyUrl(value: string, allowEmpty = true): boolean {
  const trimmed = value.trim()
  if (!trimmed) return allowEmpty

  try {
    const parsed = new URL(trimmed)
    const scheme = parsed.protocol.replace(/:$/, '').toLowerCase()
    return (
      Boolean(parsed.hostname) &&
      Boolean(parsed.port || extractExplicitPort(trimmed)) &&
      SUPPORTED_PROXY_SCHEMES.includes(scheme as ProxyScheme) &&
      hasCompleteCredentials(parsed)
    )
  } catch {
    return false
  }
}

export { SUPPORTED_PROXY_SCHEMES }
