import { type CSSProperties, type PropsWithChildren, type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { LayoutDashboard, Users, Activity, Settings, Server, Languages, Globe, BookOpen, KeyRound, Image as ImageIcon, ShieldAlert, ShieldCheck, ExternalLink, ChevronLeft, Palette, Sun, Moon, LogOut, Radar } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { API_LOADING_EVENT, resetAdminAuthState } from '../api'
import { DEFAULT_SITE_LOGO, isBrandingVideo, useBranding } from '../branding'
import { CODEX2API_GITHUB_URL, useVersionCheck } from '../hooks/useVersionCheck'
import { useTheme } from '../hooks/useTheme'
import SecurityBanner from './SecurityBanner'

type NavDef = {
  id: string
  to: string
  labelKey: string
  icon: ReactNode
  end?: boolean
  activePrefix?: string
}

const NAV_ORDER_KEY = 'sidebar_nav_order'

const navDefs: NavDef[] = [
  { id: 'dashboard', to: '/', labelKey: 'nav.dashboard', icon: <LayoutDashboard className="size-[18px]" />, end: true },
  { id: 'accounts', to: '/accounts', labelKey: 'nav.accounts', icon: <Users className="size-[18px]" /> },
  { id: 'api-keys', to: '/api-keys', labelKey: 'nav.apiKeys', icon: <KeyRound className="size-[18px]" /> },
  { id: 'usage', to: '/usage', labelKey: 'nav.usage', icon: <Activity className="size-[18px]" /> },
  { id: 'ops', to: '/ops/overview', labelKey: 'nav.ops', icon: <Server className="size-[18px]" />, activePrefix: '/ops' },
  { id: 'proxies', to: '/proxies', labelKey: 'nav.proxies', icon: <Globe className="size-[18px]" /> },
  { id: 'images', to: '/images/studio', labelKey: 'nav.images', icon: <ImageIcon className="size-[18px]" />, activePrefix: '/images' },
  { id: 'prompt-filter', to: '/prompt-filter/overview', labelKey: 'nav.promptFilter', icon: <ShieldAlert className="size-[18px]" />, activePrefix: '/prompt-filter' },
  { id: 'security-events', to: '/security-events', labelKey: 'nav.securityEvents', icon: <ShieldCheck className="size-[18px]" /> },
  { id: 'subscriptions', to: '/subscriptions', labelKey: 'nav.subscriptions', icon: <Radar className="size-[18px]" /> },
  { id: 'theme', to: '/theme', labelKey: 'nav.theme', icon: <Palette className="size-[18px]" /> },
  { id: 'settings', to: '/settings', labelKey: 'nav.settings', icon: <Settings className="size-[18px]" /> },
  { id: 'docs', to: '/docs', labelKey: 'nav2.docs', icon: <BookOpen className="size-[18px]" /> },
]

function loadNavOrder(): NavDef[] {
  try {
    const saved = window.localStorage.getItem(NAV_ORDER_KEY)
    if (!saved) return navDefs
    const ids: string[] = JSON.parse(saved)
    const map = new Map(navDefs.map((d) => [d.id, d]))
    const ordered = ids.flatMap((id) => (map.has(id) ? [map.get(id)!] : []))
    const seen = new Set(ids)
    navDefs.forEach((d) => { if (!seen.has(d.id)) ordered.push(d) })
    return ordered
  } catch {
    return navDefs
  }
}

function saveNavOrder(defs: NavDef[]) {
  try {
    window.localStorage.setItem(NAV_ORDER_KEY, JSON.stringify(defs.map((d) => d.id)))
  } catch { /* ignore */ }
}

function GlobalLoadingBar() {
  const location = useLocation()
  const [apiLoading, setApiLoading] = useState(false)
  const [visible, setVisible] = useState(false)
  const [width, setWidth] = useState(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const handleApiLoading = (e: Event) => {
      const customEvt = e as CustomEvent<{ count: number }>
      setApiLoading((customEvt.detail?.count ?? 0) > 0)
    }
    window.addEventListener(API_LOADING_EVENT, handleApiLoading)
    return () => window.removeEventListener(API_LOADING_EVENT, handleApiLoading)
  }, [])

  // 监听路由 path 变动瞬间触发进度条
  useEffect(() => {
    setVisible(true)
    setWidth(25)
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => {
      setWidth(100)
      timerRef.current = setTimeout(() => {
        setVisible(false)
        setWidth(0)
      }, 250)
    }, 150)
  }, [location.pathname])

  // 监听 API 请求变动
  useEffect(() => {
    if (apiLoading) {
      setVisible(true)
      setWidth((prev) => (prev > 0 && prev < 85 ? prev : 20))
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(() => setWidth(85), 100)
    } else {
      setWidth(100)
      timerRef.current = setTimeout(() => {
        setVisible(false)
        setWidth(0)
      }, 300)
    }
    return () => { if (timerRef.current) clearTimeout(timerRef.current) }
  }, [apiLoading])

  if (!visible) return null
  return (
    <div aria-hidden className="pointer-events-none fixed inset-x-0 top-0 z-[300] h-[3px] overflow-hidden bg-primary/20">
      <div
        className="h-full bg-primary shadow-[0_0_10px_hsl(var(--primary))] transition-[width] ease-out"
        style={{ width: `${width}%`, transitionDuration: width === 100 ? '150ms' : '500ms' }}
      />
    </div>
  )
}

export default function Layout({ children }: PropsWithChildren) {
  const location = useLocation()
  const navigate = useNavigate()
  const { t, i18n } = useTranslation()
  const { hasUpdate, latestVersion } = useVersionCheck(location.pathname)
  const { siteName, siteLogo, backgroundImage, backgroundOpacity, backgroundBlur, backgroundGlassOpacity, backgroundGlassBlur } = useBranding()
  const { theme, toggle } = useTheme()
  const [spinning, setSpinning] = useState(false)
  const logoSrc = siteLogo || DEFAULT_SITE_LOGO
  const [navOrder, setNavOrder] = useState<NavDef[]>(() => loadNavOrder())
  const dragSrcId = useRef<string | null>(null)
  const hasDraggedRef = useRef<boolean>(false)
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)

  const handleDragStart = useCallback((id: string, e: React.DragEvent) => {
    dragSrcId.current = id
    hasDraggedRef.current = true
    setDraggingId(id)
    e.dataTransfer.effectAllowed = 'move'
    e.dataTransfer.setData('text/plain', id)
  }, [])

  const handleDragOver = useCallback((id: string, e: React.DragEvent) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setDragOverId(id)
  }, [])

  const handleDrop = useCallback((targetId: string, e: React.DragEvent) => {
    e.preventDefault()
    setDragOverId(null)
    setDraggingId(null)
    const srcId = dragSrcId.current
    if (!srcId || srcId === targetId) return
    setNavOrder((prev) => {
      const next = [...prev]
      const srcIdx = next.findIndex((d) => d.id === srcId)
      const tgtIdx = next.findIndex((d) => d.id === targetId)
      if (srcIdx < 0 || tgtIdx < 0) return prev
      const [item] = next.splice(srcIdx, 1)
      next.splice(tgtIdx, 0, item)
      saveNavOrder(next)
      return next
    })
  }, [])

  const handleDragEnd = useCallback(() => {
    dragSrcId.current = null
    setDraggingId(null)
    setDragOverId(null)
    setTimeout(() => {
      hasDraggedRef.current = false
    }, 50)
  }, [])

  const [showVersionPopover, setShowVersionPopover] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false
    try {
      return window.localStorage.getItem('sidebar_collapsed') === '1'
    } catch {
      return false
    }
  })
  const toggleSidebarCollapsed = () => {
    setSidebarCollapsed((prev) => {
      const next = !prev
      try {
        window.localStorage.setItem('sidebar_collapsed', next ? '1' : '0')
      } catch { /* ignore */ }
      return next
    })
  }

  const popoverRef = useRef<HTMLDivElement | null>(null)
  const buttonRef = useRef<HTMLDivElement | null>(null)
  const [popoverPos, setPopoverPos] = useState<{ top: number; left: number }>({ top: 0, left: 0 })

  useEffect(() => {
    if (!showVersionPopover) return
    function updatePos() {
      if (!buttonRef.current) return
      const rect = buttonRef.current.getBoundingClientRect()
      setPopoverPos({
        top: rect.top - 8,
        left: rect.left,
      })
    }
    updatePos()
    window.addEventListener('scroll', updatePos, true)
    window.addEventListener('resize', updatePos)
    return () => {
      window.removeEventListener('scroll', updatePos, true)
      window.removeEventListener('resize', updatePos)
    }
  }, [showVersionPopover])

  useEffect(() => {
    if (!showVersionPopover) return
    function handleClickOutside(e: MouseEvent) {
      if (
        popoverRef.current &&
        !popoverRef.current.contains(e.target as Node) &&
        buttonRef.current &&
        !buttonRef.current.contains(e.target as Node)
      ) {
        setShowVersionPopover(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [showVersionPopover])

  const releaseURL = latestVersion ? `${CODEX2API_GITHUB_URL}/releases/tag/v${latestVersion}` : null

  const isNavActive = useCallback(
    (item: NavDef) => {
      if (item.activePrefix) {
        return location.pathname.startsWith(item.activePrefix)
      }
      return item.end ? location.pathname === item.to : location.pathname.startsWith(item.to)
    },
    [location.pathname],
  )

  const isBackgroundVideo = isBrandingVideo(backgroundImage)
  const isDark = theme === 'dark'
  const overlayOpacity = Math.max(0, Math.min(100, backgroundOpacity)) / 100
  const overlayBlur = Math.max(0, Math.min(40, backgroundBlur))
  const glassOpacity = Math.max(0, Math.min(100, backgroundGlassOpacity)) / 100
  const glassBlur = Math.max(0, Math.min(40, backgroundGlassBlur))

  const backgroundMediaStyle: CSSProperties = {
    opacity: overlayOpacity,
    filter: overlayBlur > 0 ? `blur(${overlayBlur}px)` : undefined,
    transform: overlayBlur > 0 ? 'scale(1.05)' : undefined,
  }

  const glassStyle: CSSProperties | undefined = backgroundImage
    ? {
        backgroundColor: isDark
          ? `hsl(222 16% 14% / ${glassOpacity})`
          : `hsl(0 0% 100% / ${glassOpacity})`,
        backdropFilter: glassBlur > 0 ? `blur(${glassBlur}px)` : undefined,
        WebkitBackdropFilter: glassBlur > 0 ? `blur(${glassBlur}px)` : undefined,
      }
    : undefined

  const containerEase = 'duration-300 cubic-bezier(0.2, 0.8, 0.2, 1.0)'
  const textEase = 'duration-200 ease-out'
  const textRevealDelay = sidebarCollapsed ? 'delay-0' : 'delay-75'

  return (
    <div className="relative min-h-dvh">
      <GlobalLoadingBar />
      {backgroundImage ? (
        <div aria-hidden="true" className="pointer-events-none fixed inset-0 z-0 overflow-hidden">
          {isBackgroundVideo ? (
            <video
              src={backgroundImage}
              className="absolute inset-0 size-full object-cover transition-[opacity,filter,transform] duration-300"
              style={backgroundMediaStyle}
              autoPlay
              loop
              muted
              playsInline
            />
          ) : (
            <div
              className="absolute inset-0 size-full bg-cover bg-center bg-no-repeat transition-[opacity,filter,transform] duration-300"
              style={{
                ...backgroundMediaStyle,
                backgroundImage: `url(${JSON.stringify(backgroundImage)})`,
              }}
            />
          )}
        </div>
      ) : null}

      {/* Main Container */}
      <div className="relative z-10 flex min-h-dvh flex-col lg:flex-row">
        {/* Desktop Sidebar (lg+ 屏) */}
        <aside
          style={glassStyle}
          className={`sticky top-0 hidden h-dvh shrink-0 flex-col border-r border-border bg-[hsl(var(--sidebar-background))] p-3 transition-[width,padding,background-color] ${containerEase} lg:flex ${
            sidebarCollapsed ? 'w-16 px-2' : 'w-64 px-3'
          }`}
        >
          {/* Brand header */}
          <div className="flex items-center gap-3 px-1 py-1">
            <img
              src={logoSrc}
              alt="Logo"
              className="size-8 shrink-0 rounded-lg object-contain transition-transform duration-200 hover:scale-105"
            />
            <div
              className={`flex flex-col min-w-0 overflow-hidden transition-[max-width,opacity] ${textEase} ${textRevealDelay} ${
                sidebarCollapsed ? 'max-w-0 opacity-0' : 'max-w-[180px] opacity-100'
              }`}
            >
              <span className="truncate text-base font-bold tracking-tight text-foreground" title={siteName}>
                {siteName}
              </span>

              {/* Version Popover trigger */}
              <div className="relative" ref={buttonRef}>
                <button
                  type="button"
                  onClick={() => setShowVersionPopover((prev) => !prev)}
                  className="inline-flex items-center gap-1 rounded px-1 text-[11px] font-medium text-muted-foreground transition-colors hover:bg-muted/80 hover:text-foreground"
                >
                  <span>v{__APP_VERSION__}</span>
                  {hasUpdate && (
                    <span className="relative flex size-2 shrink-0">
                      <span className="absolute inline-flex size-full animate-ping rounded-full bg-destructive/60 opacity-75" />
                      <span className="relative inline-flex size-2 rounded-full bg-destructive" />
                    </span>
                  )}
                </button>

                {showVersionPopover &&
                  createPortal(
                    <div
                      ref={popoverRef}
                      style={{
                        position: 'fixed',
                        top: popoverPos.top,
                        left: popoverPos.left,
                        transform: 'translateY(-100%)',
                      }}
                      className="z-[9999] w-64 rounded-lg border border-border bg-popover p-3 shadow-xl backdrop-blur-md animate-in fade-in zoom-in-95 duration-150"
                    >
                      <div className="flex items-center justify-between">
                        <span className="text-xs font-bold text-foreground"> ZK-CodexProxy </span>
                        {hasUpdate ? (
                          <span className="rounded bg-destructive/10 px-1.5 py-0.5 text-[10px] font-medium text-destructive">
                            {t('common.updateAvailable')}
                          </span>
                        ) : (
                          <span className="rounded bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-medium text-emerald-500">
                            {t('common.latest')}
                          </span>
                        )}
                      </div>
                      <div className="mt-2 text-xs text-muted-foreground">
                        {t('common.currentVersion', { version: __APP_VERSION__ })}
                      </div>
                      {latestVersion && (
                        <div className="mt-1 text-[11px] text-muted-foreground">
                          {t('common.latestVersion', { version: latestVersion })}
                        </div>
                      )}
                      {releaseURL && (
                        <a
                          href={releaseURL}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="mt-3 inline-flex w-full items-center justify-center gap-1.5 rounded-md border border-primary/20 bg-primary/10 px-2.5 py-1.5 text-[12px] font-semibold text-primary transition-colors hover:bg-primary/15"
                          onClick={() => setShowVersionPopover(false)}
                        >
                          {t('common.viewReleaseNotes')}
                          <ExternalLink className="size-3.5" />
                        </a>
                      )}
                    </div>,
                    document.body,
                  )}
              </div>
            </div>
          </div>

          {/* Collapse toggle */}
          <button
            type="button"
            onClick={toggleSidebarCollapsed}
            title={sidebarCollapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
            aria-label={sidebarCollapsed ? t('common.expandSidebar') : t('common.collapseSidebar')}
            className={`mt-3 flex items-center min-h-9 rounded-lg text-[12px] font-semibold text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-[background-color,color,padding] ${containerEase} ${
              sidebarCollapsed ? 'justify-center px-2 py-2' : 'gap-2 px-3 py-2'
            }`}
          >
            <ChevronLeft
              className={`size-4 transition-transform ${containerEase} ${
                sidebarCollapsed ? 'rotate-180' : 'rotate-0'
              }`}
            />
            <span
              className={`overflow-hidden whitespace-nowrap transition-[max-width,opacity] ${textEase} ${textRevealDelay} ${
                sidebarCollapsed ? 'max-w-0 opacity-0' : 'max-w-[160px] opacity-100'
              }`}
            >
              {t('common.collapseSidebar')}
            </span>
          </button>

          {/* Nav Links: 允许整行拖拽排序 */}
          <nav className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto pt-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden" aria-label="Main navigation">
            <span
              className={`mb-1 overflow-hidden whitespace-nowrap px-2 text-[11px] font-bold uppercase text-muted-foreground transition-[max-height,opacity,margin] ${textEase} ${textRevealDelay} ${
                sidebarCollapsed ? 'mb-0 max-h-0 opacity-0' : 'max-h-5 opacity-100'
              }`}
              aria-hidden={sidebarCollapsed}
            >
              {t('nav.console')}
            </span>
            {navOrder.map((item) => {
              const active = isNavActive(item)
              const label = t(item.labelKey)
              const isOver = dragOverId === item.id
              const isDragging = draggingId === item.id

              return (
                <div
                  key={item.id}
                  draggable
                  onDragStart={(e) => handleDragStart(item.id, e)}
                  onDragOver={(e) => handleDragOver(item.id, e)}
                  onDrop={(e) => handleDrop(item.id, e)}
                  onDragEnd={handleDragEnd}
                  onClick={() => {
                    if (!hasDraggedRef.current) {
                      navigate(item.to)
                    }
                  }}
                  title={sidebarCollapsed ? label : undefined}
                  className={`group relative flex w-full items-center min-h-10 border rounded-lg text-[14px] font-semibold cursor-pointer select-none transition-all duration-150 ${containerEase} ${
                    sidebarCollapsed ? 'justify-center px-2 py-2' : 'gap-2.5 px-3 py-2'
                  } ${
                    isOver
                      ? 'border border-dashed border-primary bg-primary/10 shadow-sm'
                      : isDragging
                      ? 'border border-dashed border-primary/50 opacity-40 bg-muted/40 scale-[0.98]'
                      : active
                      ? 'bg-primary/10 border-primary/20 text-primary'
                      : 'border-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground'
                  }`}
                >
                  {item.icon}
                  <span
                    className={`overflow-hidden whitespace-nowrap transition-[max-width,opacity] ${textEase} ${textRevealDelay} ${
                      sidebarCollapsed ? 'max-w-0 opacity-0' : 'max-w-[160px] opacity-100'
                    }`}
                  >
                    {label}
                  </span>
                </div>
              )
            })}
          </nav>

          {/* Footer */}
          <div
            className={`mt-auto border-t border-border pt-3 transition-[gap] ${containerEase} ${
              sidebarCollapsed
                ? 'flex flex-col items-center gap-1'
                : 'flex items-center justify-between gap-2'
            }`}
          >
            <span
              aria-hidden={sidebarCollapsed}
              className={`inline-flex items-center gap-1.5 overflow-hidden rounded-md border border-emerald-500/16 bg-[hsl(var(--success-bg))] text-[11px] font-bold text-[hsl(var(--success))] shrink-0 whitespace-nowrap transition-[max-width,opacity,padding] ${textEase} ${textRevealDelay} ${
                sidebarCollapsed ? 'max-w-0 opacity-0 px-0 py-0' : 'max-w-[120px] opacity-100 px-2 py-1'
              }`}
            >
              <span className="size-1.5 rounded-full bg-[hsl(var(--success))]" />
              {t('common.online')}
            </span>

            <div className="flex items-center gap-1 shrink-0">
              <button
                type="button"
                onClick={() => {
                  const nextLang = i18n.language === 'zh' ? 'en' : 'zh'
                  void i18n.changeLanguage(nextLang)
                }}
                className="inline-flex size-8 items-center justify-center rounded-lg border border-transparent text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                title={t('common.switchLanguage')}
              >
                <Languages className="size-4" />
              </button>

              <a
                href={CODEX2API_GITHUB_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex size-8 items-center justify-center rounded-lg border border-transparent text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                title="GitHub"
              >
                <svg className="size-4 fill-current" viewBox="0 0 24 24">
                  <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z" />
                </svg>
              </a>

              <button
                type="button"
                onClick={() => {
                  setSpinning(true)
                  toggle()
                  setTimeout(() => setSpinning(false), 500)
                }}
                className="inline-flex size-8 items-center justify-center rounded-lg border border-transparent text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                title={t('common.toggleTheme')}
              >
                {theme === 'dark' ? (
                  <Sun className={`size-4 transition-transform duration-500 ${spinning ? 'rotate-180 scale-110' : ''}`} />
                ) : (
                  <Moon className={`size-4 transition-transform duration-500 ${spinning ? 'rotate-180 scale-110' : ''}`} />
                )}
              </button>

              <button
                type="button"
                onClick={resetAdminAuthState}
                className="inline-flex size-8 items-center justify-center rounded-lg border border-transparent text-muted-foreground transition-colors hover:bg-muted/60 hover:text-destructive"
                title={t('common.logout')}
              >
                <LogOut className="size-4" />
              </button>
            </div>
          </div>
        </aside>

        {/* Content Area */}
        <main className="flex min-h-dvh flex-1 flex-col overflow-x-hidden pb-16 lg:pb-0">
          <SecurityBanner />
          <div className="flex-1 p-4 lg:p-6">{children}</div>
        </main>
      </div>

      {/* Mobile Bottom Navigation (保持同步排序) */}
      <nav
        style={glassStyle}
        className="fixed bottom-0 inset-x-0 z-40 flex h-14 items-center justify-around border-t border-border bg-[hsl(var(--sidebar-background))] px-2 lg:hidden"
        aria-label="Mobile main navigation"
      >
        {navOrder.slice(0, 5).map((item) => {
          const active = isNavActive(item)
          const label = t(item.labelKey)
          return (
            <NavLink
              key={item.id}
              to={item.to}
              end={item.end}
              className={`flex flex-col items-center justify-center gap-0.5 rounded-lg px-3 py-1.5 text-[11px] font-medium transition-colors ${
                active ? 'text-primary font-bold' : 'text-muted-foreground hover:text-foreground'
              }`}
            >
              {item.icon}
              <span className="truncate max-w-[64px]">{label}</span>
            </NavLink>
          )
        })}
      </nav>
    </div>
  )
}
