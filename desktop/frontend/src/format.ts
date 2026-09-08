export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '—'
  if (value === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let n = value
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(2)} ${units[i]}`
}

export function formatCount(value: number): string {
  return Number.isFinite(value) ? new Intl.NumberFormat('zh-CN').format(value) : '—'
}

export function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN')
}

export function shortID(value?: string): string {
  if (!value) return '—'
  return value.length <= 14 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`
}

export function errorText(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  try { return JSON.stringify(error) } catch { return String(error) }
}
