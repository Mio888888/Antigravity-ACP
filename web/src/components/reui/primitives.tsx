import React from 'react'

export function Frame({
  children,
  className = '',
  stacked = false,
  spacing = 'default',
}: {
  children: React.ReactNode
  className?: string
  stacked?: boolean
  spacing?: 'xs' | 'sm' | 'default' | 'lg'
}) {
  const gapMap = {
    xs: 'p-1 space-y-1',
    sm: 'p-1.5 space-y-1.5',
    default: 'p-1.5 space-y-1.5',
    lg: 'p-2 space-y-2',
  }
  return (
    <div
      className={`rounded-xl border border-border bg-muted/35 ${
        stacked ? 'p-1.5 space-y-0 [&>[data-slot=frame-panel]+*[data-slot=frame-panel]]:border-t-0' : gapMap[spacing]
      } ${className}`}
    >
      {children}
    </div>
  )
}

export function FrameHeader({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={`flex flex-wrap items-center justify-between gap-2 px-3 py-2 ${className}`}>
      {children}
    </div>
  )
}

export function FrameTitle({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return <h2 className={`text-sm font-semibold text-foreground ${className}`}>{children}</h2>
}

export function FrameDescription({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return <p className={`text-xs text-muted-foreground ${className}`}>{children}</p>
}

export function FramePanel({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="frame-panel"
      className={`rounded-lg border border-border bg-card p-4 text-card-foreground shadow-2xs ${className}`}
    >
      {children}
    </div>
  )
}

export function FrameFooter({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={`flex flex-wrap items-center justify-between gap-2 px-3 py-1.5 text-xs text-muted-foreground ${className}`}>
      {children}
    </div>
  )
}

export function Skeleton({ className = '' }: { className?: string }) {
  return (
    <div
      aria-hidden="true"
      className={`animate-pulse motion-reduce:animate-none rounded-md bg-muted/60 ${className}`}
    />
  )
}

export function ProgressBar({
  value,
  max = 100,
  tone = 'info',
  size = 'default',
  ariaLabel,
}: {
  value: number
  max?: number
  tone?: 'info' | 'success' | 'warning' | 'destructive' | 'primary'
  size?: 'default' | 'md'
  ariaLabel: string
}) {
  const safeMax = max > 0 ? max : 100
  const pct = Math.min(100, Math.max(0, Math.round((value / safeMax) * 1000) / 10))
  const toneMap = {
    primary: 'bg-primary',
    info: 'bg-info',
    success: 'bg-success',
    warning: 'bg-warning',
    destructive: 'bg-destructive',
  }
  const hClass = size === 'md' ? 'h-2' : 'h-1.5'
  return (
    <div
      role="progressbar"
      aria-label={ariaLabel}
      aria-valuemin={0}
      aria-valuemax={safeMax}
      aria-valuenow={value}
      className={`${hClass} w-full overflow-hidden rounded-full bg-muted`}
    >
      <div
        className={`h-full rounded-full ${toneMap[tone]}`}
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

export function Switch({
  checked,
  onCheckedChange,
  ariaLabel,
  disabled = false,
}: {
  checked: boolean
  onCheckedChange: (next: boolean) => void
  ariaLabel: string
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onCheckedChange(!checked)}
      className={`relative inline-flex h-6 shrink-0 w-11 cursor-pointer items-center rounded-full border border-transparent transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 ${
        checked ? 'bg-success' : 'bg-muted'
      }`}
    >
      <span
        className={`pointer-events-none block size-4.5 rounded-full bg-background shadow-2xs transition-transform duration-150 ${
          checked ? 'translate-x-5.5' : 'translate-x-0.5'
        }`}
      />
    </button>
  )
}

export type BadgeVariant =
  | 'default'
  | 'outline'
  | 'secondary'
  | 'primary-light'
  | 'success-light'
  | 'warning-light'
  | 'info-light'
  | 'destructive-light'

export function Badge({
  children,
  variant = 'outline',
  className = '',
}: {
  children: React.ReactNode
  variant?: BadgeVariant
  className?: string
}) {
  const variantClasses: Record<BadgeVariant, string> = {
    default: 'bg-primary text-primary-foreground border-transparent',
    secondary: 'bg-secondary text-secondary-foreground border-transparent',
    outline: 'bg-transparent text-muted-foreground border-border',
    'primary-light': 'bg-primary/10 text-foreground border-border',
    'success-light': 'bg-success/12 text-success-foreground border-success/25',
    'warning-light': 'bg-warning/12 text-warning-foreground border-warning/25',
    'info-light': 'bg-info/12 text-info-foreground border-info/25',
    'destructive-light': 'bg-destructive/12 text-destructive-foreground border-destructive/25',
  }
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium ${variantClasses[variant]} ${className}`}
    >
      {children}
    </span>
  )
}

export type AlertVariant = 'default' | 'info' | 'success' | 'warning' | 'destructive'

export function Alert({
  children,
  variant = 'default',
  className = '',
}: {
  children: React.ReactNode
  variant?: AlertVariant
  className?: string
}) {
  const styles: Record<AlertVariant, string> = {
    default: 'border-border bg-card text-card-foreground',
    info: 'border-info/30 bg-info/8 text-info-foreground',
    success: 'border-success/30 bg-success/8 text-success-foreground',
    warning: 'border-warning/30 bg-warning/8 text-warning-foreground',
    destructive: 'border-destructive/30 bg-destructive/8 text-destructive-foreground',
  }
  return (
    <div
      role={variant === 'destructive' ? 'alert' : 'status'}
      className={`flex items-start gap-3 rounded-lg border px-3.5 py-2.5 text-xs ${styles[variant]} ${className}`}>
      {children}
    </div>
  )
}

export function AlertTitle({ children }: { children: React.ReactNode }) {
  return <div className="font-medium text-foreground">{children}</div>
}

export function AlertDescription({ children }: { children: React.ReactNode }) {
  return <div className="text-muted-foreground mt-0.5 leading-relaxed">{children}</div>
}

export function IconTile({
  children,
  variant = 'outline',
  className = '',
}: {
  children: React.ReactNode
  variant?: 'outline' | 'elevated' | 'soft'
  className?: string
}) {
  const vClasses = {
    outline: 'border-border bg-muted/50 text-muted-foreground',
    elevated: 'border-border bg-card shadow-2xs text-foreground',
    soft: 'border-transparent bg-primary/8 text-foreground',
  }
  return (
    <span
      aria-hidden="true"
      className={`inline-flex size-8 shrink-0 items-center justify-center rounded-lg border [&_svg]:size-4 ${vClasses[variant]} ${className}`}
    >
      {children}
    </span>
  )
}

export function Button({
  children,
  onClick,
  variant = 'default',
  size = 'sm',
  type = 'button',
  disabled = false,
  className = '',
  ariaLabel,
}: {
  children: React.ReactNode
  onClick?: () => void
  variant?: 'default' | 'outline' | 'secondary' | 'ghost' | 'destructive'
  size?: 'xs' | 'sm' | 'default'
  type?: 'button' | 'submit'
  disabled?: boolean
  className?: string
  ariaLabel?: string
}) {
  const vMap = {
    default: 'bg-primary text-primary-foreground hover:opacity-90 border-transparent',
    secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80 border-border',
    outline: 'bg-transparent text-foreground hover:bg-accent border-border',
    ghost: 'bg-transparent text-muted-foreground hover:text-foreground hover:bg-accent border-transparent',
    destructive: 'bg-destructive/15 text-destructive-foreground hover:bg-destructive/25 border-destructive/30',
  }
  const sMap = {
    xs: 'h-7 px-2 text-xs [&_svg]:size-3.5',
    sm: 'h-8 px-2.5 text-xs [&_svg]:size-3.5',
    default: 'h-9 px-3.5 text-sm [&_svg]:size-4',
  }
  return (
    <button
      type={type}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={onClick}
      className={`inline-flex shrink-0 cursor-pointer whitespace-nowrap items-center justify-center gap-1.5 rounded-md border font-medium transition-colors duration-150 focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 ${vMap[variant]} ${sMap[size]} ${className}`}
    >
      {children}
    </button>
  )
}

export function Input({
  value,
  onChange,
  placeholder,
  type = 'text',
  className = '',
  id,
  autoFocus = false,
  autoComplete,
  disabled = false,
  readOnly = false,
  inputMode,
  invalid = false,
  onKeyDown,
}: {
  value: string | number
  onChange: (val: string) => void
  placeholder?: string
  type?: string
  className?: string
  id?: string
  autoFocus?: boolean
  autoComplete?: string
  disabled?: boolean
  readOnly?: boolean
  inputMode?: React.HTMLAttributes<HTMLInputElement>['inputMode']
  invalid?: boolean
  onKeyDown?: React.KeyboardEventHandler<HTMLInputElement>
}) {
  return (
    <input
      id={id}
      type={type}
      value={value}
      placeholder={placeholder}
      autoFocus={autoFocus}
      autoComplete={autoComplete}
      disabled={disabled}
      readOnly={readOnly}
      inputMode={inputMode}
      aria-invalid={invalid || undefined}
      onKeyDown={onKeyDown}
      onChange={(e) => onChange(e.target.value)}
      className={`h-8 w-full rounded-md border bg-background px-2.5 text-xs text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 read-only:bg-muted/40 read-only:text-muted-foreground ${
        invalid ? 'border-destructive/60' : 'border-input'
      } ${className}`}
    />
  )
}

export function Textarea({
  value,
  onChange,
  placeholder,
  rows = 4,
  className = '',
  id,
}: {
  value: string
  onChange: (val: string) => void
  placeholder?: string
  rows?: number
  className?: string
  id?: string
}) {
  return (
    <textarea
      id={id}
      rows={rows}
      value={value}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      className={`w-full rounded-md border border-input bg-background p-2.5 font-mono text-xs text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${className}`}
    />
  )
}

export interface SelectOption {
  value: string
  label: string
}

export function SelectField({
  value,
  onValueChange,
  options,
  id,
  className = '',
}: {
  value: string
  onValueChange: (val: string) => void
  options: SelectOption[]
  id?: string
  className?: string
}) {
  return (
    <div id={id} role="radiogroup" className={`inline-flex flex-wrap gap-1 rounded-md border border-input bg-muted/40 p-0.5 ${className}`}>
      {options.map((opt) => {
        const active = opt.value === value
        return (
          <button
            key={opt.value}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onValueChange(opt.value)}
            className={`rounded px-2 py-1 text-xs font-medium transition-colors duration-150 ${
              active ? 'bg-card text-foreground shadow-2xs' : 'text-muted-foreground hover:text-foreground'
            }`}
          >
            {opt.label}
          </button>
        )
      })}
    </div>
  )
}

export interface DataGridColumn<T> {
  id: string
  header: string
  align?: 'start' | 'end'
  cell: (row: T) => React.ReactNode
}

export function DataGridView<T>({
  columns,
  rows,
  getRowId,
  emptyTitle,
  emptyDescription,
  emptyAction,
}: {
  columns: DataGridColumn<T>[]
  rows: T[]
  getRowId: (row: T) => string
  emptyTitle: string
  emptyDescription: string
  emptyAction?: React.ReactNode
}) {
  if (rows.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 py-10 text-center">
        <div className="flex flex-col gap-1">
          <p className="text-sm font-medium text-foreground">{emptyTitle}</p>
          <p className="text-xs text-muted-foreground">{emptyDescription}</p>
        </div>
        {emptyAction}
      </div>
    )
  }
  return (
    <div className="overflow-x-auto">
      <div role="grid" className="min-w-[680px] divide-y divide-border text-xs">
        <div
          role="row"
          className="grid bg-muted/40 px-3 py-2 font-medium text-muted-foreground"
          style={{ gridTemplateColumns: `repeat(${columns.length}, minmax(0, 1fr))` }}
        >
          {columns.map((col) => (
            <div
              key={col.id}
              role="columnheader"
              className={col.align === 'end' ? 'text-end' : 'text-start'}
            >
              {col.header}
            </div>
          ))}
        </div>
        {rows.map((row) => (
          <div
            key={getRowId(row)}
            role="row"
            className="grid items-center px-3 py-2.5 hover:bg-muted/25 transition-colors duration-150"
            style={{ gridTemplateColumns: `repeat(${columns.length}, minmax(0, 1fr))` }}
          >
            {columns.map((col) => (
              <div
                key={col.id}
                role="gridcell"
                className={col.align === 'end' ? 'text-end tabular-nums' : 'text-start truncate pe-2'}
              >
                {col.cell(row)}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

function fallbackCopyTextToClipboard(text: string, container?: HTMLElement | null): boolean {
  if (typeof document === 'undefined' || !text) return false
  try {
    const textarea = document.createElement('textarea')
    textarea.value = text
    textarea.setAttribute('readonly', '')
    textarea.style.position = 'fixed'
    textarea.style.top = '0'
    textarea.style.left = '0'
    textarea.style.width = '1px'
    textarea.style.height = '1px'
    textarea.style.padding = '0'
    textarea.style.border = 'none'
    textarea.style.outline = 'none'
    textarea.style.boxShadow = 'none'
    textarea.style.background = 'transparent'
    textarea.style.opacity = '0'
    textarea.style.pointerEvents = 'none'

    const mountTarget = container || document.body
    mountTarget.appendChild(textarea)

    const activeEl = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const selection = document.getSelection()
    const savedRange =
      selection && selection.rangeCount > 0 ? selection.getRangeAt(0).cloneRange() : null

    textarea.focus({ preventScroll: true })
    textarea.select()
    textarea.setSelectionRange(0, text.length)
    const copied = document.execCommand('copy')

    mountTarget.removeChild(textarea)
    if (savedRange && selection) {
      selection.removeAllRanges()
      selection.addRange(savedRange)
    }
    if (activeEl) {
      activeEl.focus({ preventScroll: true })
    }
    return copied
  } catch {
    return false
  }
}

export async function copyTextToClipboard(
  text: string,
  container?: HTMLElement | null
): Promise<boolean> {
  if (!text) return false
  const syncCopied = fallbackCopyTextToClipboard(text, container)
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      return syncCopied
    }
  }
  return syncCopied
}

export function CodeBlock({
  title,
  language,
  code,
  collapsible = false,
  defaultOpen = true,
  translatable = false,
  translateTitle = true,
}: {
  title: string
  language: string
  code: string
  collapsible?: boolean
  defaultOpen?: boolean
  translatable?: boolean
  translateTitle?: boolean
}) {
  const [copyState, setCopyState] = React.useState<'idle' | 'ok' | 'fail'>('idle')
  const [open, setOpen] = React.useState(collapsible ? defaultOpen : true)
  const containerRef = React.useRef<HTMLDivElement>(null)
  const codeRef = React.useRef<HTMLElement>(null)
  const timerRef = React.useRef<number | null>(null)

  React.useEffect(() => {
    return () => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
      }
    }
  }, [])

  const copy = () => {
    void copyTextToClipboard(code, containerRef.current).then((ok) => {
      if (!ok && codeRef.current && typeof window !== 'undefined') {
        const sel = window.getSelection()
        if (sel) {
          sel.selectAllChildren(codeRef.current)
        }
      }
      setCopyState(ok ? 'ok' : 'fail')
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
      }
      timerRef.current = window.setTimeout(() => setCopyState('idle'), ok ? 1500 : 2500)
    })
  }

  return (
    <div ref={containerRef} className="rounded-lg border border-border bg-background overflow-hidden">
      <div
        role={collapsible ? 'button' : undefined}
        tabIndex={collapsible ? 0 : undefined}
        aria-expanded={collapsible ? open : undefined}
        onClick={collapsible ? () => setOpen((prev) => !prev) : undefined}
        onKeyDown={
          collapsible
            ? (e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  setOpen((prev) => !prev)
                }
              }
            : undefined
        }
        className={`flex items-center justify-between gap-3 bg-muted/40 px-3.5 py-2.5 text-xs ${
          open ? 'border-b border-border' : ''
        } ${collapsible ? 'cursor-pointer select-none transition-colors hover:bg-muted/65' : ''}`}
      >
        <div className="flex items-center gap-2 min-w-0">
          {collapsible && (
            <svg
              aria-hidden="true"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              className={`size-3.5 shrink-0 text-muted-foreground transition-transform duration-200 ease-[cubic-bezier(0.22,1,0.36,1)] ${
                open ? 'rotate-180 text-foreground' : ''
              }`}
            >
              <path d="m6 9 6 6 6-6" />
            </svg>
          )}
          <span
            translate={translateTitle ? undefined : 'no'}
            className="font-mono font-medium text-foreground truncate"
          >
            {title}
          </span>
        </div>
        <div
          className="flex items-center gap-2 shrink-0"
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
        >
          <Badge variant="outline">{language.toUpperCase()}</Badge>
          {collapsible && (
            <span className="text-muted-foreground hidden sm:inline">
              {open ? '收起' : '展开'}
            </span>
          )}
          <Button size="xs" variant="outline" onClick={copy}>
            {copyState === 'ok' ? '已复制' : copyState === 'fail' ? '复制失败，已选中文本' : '复制'}
          </Button>
        </div>
      </div>
      {open && (
        <pre
          translate={translatable ? undefined : 'no'}
          className="overflow-x-auto p-3.5 font-mono text-xs leading-relaxed text-foreground"
        >
          <code ref={codeRef}>{code}</code>
        </pre>
      )}
    </div>
  )
}

export function InlineError({
  title,
  message,
  onRetry,
  retryLabel = '重试',
  retrying = false,
  className = '',
}: {
  title: string
  message: string
  onRetry?: () => void
  retryLabel?: string
  retrying?: boolean
  className?: string
}) {
  return (
    <Alert variant="destructive" className={className}>
      <div className="flex w-full items-center justify-between gap-3">
        <div className="flex-1 min-w-0">
          <AlertTitle>{title}</AlertTitle>
          <AlertDescription>{message}</AlertDescription>
        </div>
        {onRetry && (
          <Button size="xs" variant="outline" disabled={retrying} onClick={onRetry}>
            {retrying ? '重试中...' : retryLabel}
          </Button>
        )}
      </div>
    </Alert>
  )
}

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

const openDialogStack: number[] = []
let dialogSeq = 0
let scrollLockCount = 0
let savedBodyOverflow = ''

function lockBodyScroll() {
  if (scrollLockCount === 0) {
    savedBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
  scrollLockCount += 1
}

function unlockBodyScroll() {
  scrollLockCount = Math.max(0, scrollLockCount - 1)
  if (scrollLockCount === 0) {
    document.body.style.overflow = savedBodyOverflow
  }
}

export function DialogModal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  maxWidthClass = 'max-w-xl',
  translateTitle = true,
}: {
  open: boolean
  onClose: () => void
  title: React.ReactNode
  description?: string
  children: React.ReactNode
  footer?: React.ReactNode
  maxWidthClass?: string
  translateTitle?: boolean
}) {
  const titleId = React.useId()
  const descriptionId = React.useId()
  const panelRef = React.useRef<HTMLDivElement>(null)
  const onCloseRef = React.useRef(onClose)
  const [revealed, setRevealed] = React.useState(false)

  React.useLayoutEffect(() => {
    onCloseRef.current = onClose
  })

  React.useEffect(() => {
    if (!open) {
      setRevealed(false)
      return
    }
    dialogSeq += 1
    const dialogId = dialogSeq
    openDialogStack.push(dialogId)
    const previouslyFocused =
      document.activeElement instanceof HTMLElement ? document.activeElement : null
    lockBodyScroll()

    const frame = window.requestAnimationFrame(() => {
      setRevealed(true)
      const panel = panelRef.current
      if (panel && !panel.contains(document.activeElement)) {
        panel.focus({ preventScroll: true })
      }
    })

    const onKeyDown = (e: KeyboardEvent) => {
      if (openDialogStack[openDialogStack.length - 1] !== dialogId) return
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onCloseRef.current()
        return
      }
      if (e.key !== 'Tab') return
      const panel = panelRef.current
      if (!panel) return
      const focusables = Array.from(
        panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)
      ).filter((el) => el.getClientRects().length > 0)
      if (focusables.length === 0) {
        e.preventDefault()
        panel.focus({ preventScroll: true })
        return
      }
      const first = focusables[0]
      const last = focusables[focusables.length - 1]
      const active = document.activeElement
      const outside = !active || !panel.contains(active)
      if (e.shiftKey && (active === first || active === panel || outside)) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && (active === last || outside)) {
        e.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)

    return () => {
      window.cancelAnimationFrame(frame)
      document.removeEventListener('keydown', onKeyDown)
      const idx = openDialogStack.lastIndexOf(dialogId)
      if (idx >= 0) openDialogStack.splice(idx, 1)
      unlockBodyScroll()
      if (previouslyFocused && previouslyFocused.isConnected) {
        previouslyFocused.focus({ preventScroll: true })
      }
    }
  }, [open])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-background/75 backdrop-blur-xs p-4"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        className={`w-full ${maxWidthClass} outline-none transition-[opacity,transform] duration-200 ease-[cubic-bezier(0.22,1,0.36,1)] motion-reduce:transition-none ${
          revealed ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-1'
        }`}
      >
        <Frame className="shadow-lg max-h-[88vh] flex flex-col">
          <FrameHeader>
            <div className="min-w-0">
              <div id={titleId} translate={translateTitle ? undefined : 'no'}>
                <FrameTitle>{title}</FrameTitle>
              </div>
              {description && (
                <div id={descriptionId}>
                  <FrameDescription>{description}</FrameDescription>
                </div>
              )}
            </div>
            <Button size="xs" variant="ghost" onClick={onClose}>
              关闭
            </Button>
          </FrameHeader>
          <FramePanel className="space-y-3.5 overflow-y-auto max-h-[72vh]">{children}</FramePanel>
          {footer && <FrameFooter>{footer}</FrameFooter>}
        </Frame>
      </div>
    </div>
  )
}

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  busy = false,
  error = '',
  onConfirm,
  onCancel,
  children,
}: {
  open: boolean
  title: string
  description?: string
  confirmLabel: string
  busy?: boolean
  error?: string
  onConfirm: () => void
  onCancel: () => void
  children?: React.ReactNode
}) {
  return (
    <DialogModal
      open={open}
      onClose={() => {
        if (!busy) onCancel()
      }}
      title={title}
      description={description}
      maxWidthClass="max-w-md"
      footer={
        <div className="ms-auto flex items-center gap-2">
          <Button size="sm" variant="outline" disabled={busy} onClick={onCancel}>
            取消
          </Button>
          <Button size="sm" variant="destructive" disabled={busy} onClick={onConfirm}>
            {busy ? '正在处理...' : confirmLabel}
          </Button>
        </div>
      }
    >
      <div className="flex flex-col gap-3">
        {children}
        {error && <InlineError title="操作失败" message={error} />}
      </div>
    </DialogModal>
  )
}
