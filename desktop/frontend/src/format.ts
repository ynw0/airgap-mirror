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

const readableStatuses: Record<string, string> = {
  PLANNED: '等待下载', DOWNLOADING: '下载中', VERIFYING: '正在检查',
  READY: '可以导入', TRANSFERRING: '传输中', IMPORTING: '正在导入',
  IMPORTED: '已导入', PUBLISHABLE: '准备发布', PUBLISHED: '已发布',
  COMPLETE: '已完成', COMPLETED: '已完成', FAILED: '失败',
  CANCELLED: '已取消', QUEUED: '等待处理', RUNNING: '进行中', PAUSED: '已暂停（可继续）',
  BATCH_IMPORTED: '已导入',
}

export function statusLabel(status: string): string {
  return readableStatuses[status] ?? status
}
