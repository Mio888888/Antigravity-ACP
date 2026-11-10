const COMPACT_UNITS = [
  { suffix: '', divisor: 1 },
  { suffix: 'K', divisor: 1_000 },
  { suffix: 'M', divisor: 1_000_000 },
  { suffix: 'B', divisor: 1_000_000_000 },
] as const

function compactDigits(unitIndex: number, scaled: number): number {
  if (unitIndex === 0) return 0
  if (unitIndex === 1) return scaled >= 100 ? 0 : 1
  return 2
}

export function formatCompact(num: number): string {
  if (!Number.isFinite(num) || num <= 0) return '0'
  let idx = num >= 1e9 ? 3 : num >= 1e6 ? 2 : num >= 1e3 ? 1 : 0
  for (;;) {
    const scaled = num / COMPACT_UNITS[idx].divisor
    let digits = compactDigits(idx, scaled)
    let rounded = Number(scaled.toFixed(digits))
    const recomputed = compactDigits(idx, rounded)
    if (recomputed !== digits) {
      digits = recomputed
      rounded = Number(scaled.toFixed(digits))
    }
    if (rounded >= 1000 && idx < COMPACT_UNITS.length - 1) {
      idx += 1
      continue
    }
    return `${rounded.toFixed(digits)}${COMPACT_UNITS[idx].suffix}`
  }
}

export function formatProtocolDisplay(proto?: string): string {
  if (!proto) return '—'
  const p = proto.toLowerCase()
  if (p.includes('responses')) return 'OpenAI 官方 (Responses)'
  if (p.includes('anthropic') || p.includes('claude')) return 'Claude 协议 (Messages)'
  if (p.includes('openai')) return 'OpenAI 兼容 (Chat)'
  if (p.includes('acp') || p.includes('ws')) return 'ACP Studio'
  return proto
}

export function formatDurationCn(ms?: number | null): string {
  if (ms === undefined || ms === null || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms >= 1000) {
    const sec = ms / 1000
    return `${sec >= 10 ? Math.round(sec) : Number(sec.toFixed(1))} 秒`
  }
  return `${Math.round(ms)} 毫秒`
}
