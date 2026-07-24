export function formatCPU(millicores: number): string {
  if (millicores >= 1000) return `${(millicores / 1000).toFixed(2)} cores`
  return `${Math.round(millicores)}m`
}

export function formatMemory(bytes: number): string {
  const mebibytes = bytes / 1024 / 1024
  if (mebibytes >= 1024) return `${(mebibytes / 1024).toFixed(2)} GiB`
  return `${Math.round(mebibytes)} MiB`
}

export function percent(value: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (value / total) * 100))
}
