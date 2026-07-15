import type { ChangeEvent } from 'react'
import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Eye, EyeOff, SlidersHorizontal } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { cn } from '@/lib/utils'
import {
  buildProxyUrl,
  isValidProxyUrl,
  parseProxyUrl,
  SUPPORTED_PROXY_SCHEMES,
  type ProxyParts,
} from '../utils/proxyUrl'

interface ProxyUrlInputProps {
  value: string
  onChange: (value: string) => void
  label?: string
  placeholder?: string
  required?: boolean
  allowEmpty?: boolean
  error?: string
  onErrorChange?: (error: string) => void
  className?: string
  inputClassName?: string
}

function hasProxyParts(parts: ProxyParts): boolean {
  return Boolean(
    parts.host.trim() ||
      parts.port.trim() ||
      parts.username.trim() ||
      parts.password,
  )
}

export default function ProxyUrlInput({
  value,
  onChange,
  label,
  placeholder,
  required = false,
  allowEmpty = true,
  error,
  onErrorChange,
  className,
  inputClassName,
}: ProxyUrlInputProps) {
  const { t } = useTranslation()
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [showPassword, setShowPassword] = useState(false)
  const [parts, setParts] = useState<ProxyParts>(() => parseProxyUrl(value))
  const proxyInputId = useId().replace(/:/g, '')
  const credentialEditArmed = useRef({
    username: false,
    password: false,
  })

  useEffect(() => {
    if (advancedOpen) return
    setParts(parseProxyUrl(value))
  }, [advancedOpen, value])

  const schemeOptions = useMemo(
    () => SUPPORTED_PROXY_SCHEMES.map((scheme) => ({
      label: scheme,
      value: scheme,
    })),
    [],
  )

  const resolvedError =
    error ??
    (isValidProxyUrl(value, allowEmpty)
      ? ''
      : t('proxyInput.invalidUrl'))

  const emitValue = (nextValue: string) => {
    const trimmed = nextValue.trim()
    onChange(nextValue)
    onErrorChange?.(isValidProxyUrl(trimmed, allowEmpty) ? '' : t('proxyInput.invalidUrl'))
  }

  const updateParts = (next: ProxyParts) => {
    setParts(next)
    emitValue(buildProxyUrl(next))
  }

  const armCredentialEdit = (field: 'username' | 'password') => {
    credentialEditArmed.current[field] = true
  }

  const updateCredentialPart = (
    field: 'username' | 'password',
    nextValue: string,
  ) => {
    if (!credentialEditArmed.current[field]) {
      setParts((current) => ({ ...current }))
      return
    }
    updateParts({ ...parts, [field]: nextValue })
  }

  const handleUrlChange = (event: ChangeEvent<HTMLInputElement>) => {
    const nextValue = event.target.value
    emitValue(nextValue)
    credentialEditArmed.current = { username: false, password: false }
    setParts(parseProxyUrl(nextValue))
  }

  const helperText = value.trim()
    ? t('proxyInput.currentUrl')
    : allowEmpty
      ? t('proxyInput.emptyDirect')
      : t('proxyInput.required')

  return (
    <div className={cn('space-y-2', className)}>
      {label ? (
        <label className="block text-sm font-semibold text-muted-foreground">
          {label}
          {required ? ' *' : ''}
        </label>
      ) : null}

      <div className="flex gap-2">
        <Input
          placeholder={placeholder ?? t('proxyInput.urlPlaceholder')}
          value={value}
          onChange={handleUrlChange}
          aria-invalid={Boolean(resolvedError) || undefined}
          className={cn('font-mono', inputClassName)}
          autoComplete="off"
          data-1p-ignore="true"
          data-lpignore="true"
          name={`proxy-${proxyInputId}-url`}
        />
        <Button
          type="button"
          variant={advancedOpen ? 'secondary' : 'outline'}
          size="default"
          title={t('proxyInput.toggleAdvanced')}
          aria-label={t('proxyInput.toggleAdvanced')}
          onClick={() => {
            setAdvancedOpen((open) => {
              const nextOpen = !open
              credentialEditArmed.current = { username: false, password: false }
              if (nextOpen) setParts(parseProxyUrl(value))
              return nextOpen
            })
          }}
        >
          <SlidersHorizontal className="size-4" />
          {advancedOpen ? t('proxyInput.hideAdvanced') : t('proxyInput.showAdvanced')}
        </Button>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>{helperText}</span>
        {resolvedError ? <span className="font-medium text-destructive">{resolvedError}</span> : null}
      </div>

      {advancedOpen ? (
        <div className="rounded-md border border-border bg-muted/20 p-3">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-sm font-semibold text-foreground">
                {t('proxyInput.advancedTitle')}
              </div>
              <div className="text-xs text-muted-foreground">
                {t('proxyInput.advancedHint')}
              </div>
            </div>
          </div>
          <div className="grid gap-3 md:grid-cols-[120px_1fr_120px]">
            <label className="space-y-1.5">
              <span className="text-xs font-semibold text-muted-foreground">{t('proxyInput.scheme')} *</span>
              <Select
                compact
                value={parts.scheme}
                options={schemeOptions}
                onValueChange={(scheme) => updateParts({ ...parts, scheme: scheme as ProxyParts['scheme'] })}
              />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-semibold text-muted-foreground">{t('proxyInput.host')} *</span>
              <Input
                value={parts.host}
                onChange={(event) => updateParts({ ...parts, host: event.target.value })}
                placeholder="c489.fxip.cc"
              />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-semibold text-muted-foreground">{t('proxyInput.port')} *</span>
              <Input
                value={parts.port}
                onChange={(event) => updateParts({ ...parts, port: event.target.value.replace(/[^\d]/g, '') })}
                placeholder="9345"
                inputMode="numeric"
              />
            </label>
          </div>

          <div className="mt-3 grid gap-3 md:grid-cols-2">
            <label className="space-y-1.5">
              <span className="text-xs font-semibold text-muted-foreground">{t('proxyInput.username')}</span>
              <Input
                value={parts.username}
                onBeforeInput={() => armCredentialEdit('username')}
                onKeyDown={() => armCredentialEdit('username')}
                onPaste={() => armCredentialEdit('username')}
                onChange={(event) => updateCredentialPart('username', event.target.value)}
                placeholder={t('proxyInput.usernamePlaceholder')}
                autoComplete="one-time-code"
                data-1p-ignore="true"
                data-lpignore="true"
                name={`proxy-${proxyInputId}-auth-identity`}
              />
            </label>
            <label className="space-y-1.5">
              <span className="text-xs font-semibold text-muted-foreground">{t('proxyInput.password')}</span>
              <div className="flex gap-2">
                <Input
                  value={parts.password}
                  onBeforeInput={() => armCredentialEdit('password')}
                  onKeyDown={() => armCredentialEdit('password')}
                  onPaste={() => armCredentialEdit('password')}
                  onChange={(event) => updateCredentialPart('password', event.target.value)}
                  placeholder={t('proxyInput.passwordPlaceholder')}
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="one-time-code"
                  data-1p-ignore="true"
                  data-lpignore="true"
                  name={`proxy-${proxyInputId}-auth-secret`}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  title={showPassword ? t('proxyInput.hidePassword') : t('proxyInput.showPassword')}
                  aria-label={showPassword ? t('proxyInput.hidePassword') : t('proxyInput.showPassword')}
                  onClick={() => setShowPassword((visible) => !visible)}
                >
                  {showPassword ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                </Button>
              </div>
            </label>
          </div>

          <div className="mt-3 rounded-md bg-background px-3 py-2 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{t('proxyInput.preview')}</span>{' '}
            <span className="font-mono break-all">
              {hasProxyParts(parts) ? buildProxyUrl(parts) || '-' : t('proxyInput.emptyDirect')}
            </span>
          </div>
        </div>
      ) : null}
    </div>
  )
}
