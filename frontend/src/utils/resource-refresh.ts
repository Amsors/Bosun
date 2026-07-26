export const resourceRefreshIntervals = [1000, 2000, 5000, 10000, 30000, 60000] as const

export const resourceRefreshIntervalStorageKey = 'bosun.resource-refresh-interval'

export const defaultResourceRefreshInterval = 5000

export function formatResourceRefreshInterval(intervalMs: number): string {
  if (intervalMs < 60000) return `${intervalMs / 1000} 秒`
  return `${intervalMs / 60000} 分钟`
}

export function loadResourceRefreshInterval(): number {
  try {
    const saved = Number(globalThis.localStorage.getItem(resourceRefreshIntervalStorageKey))
    if (resourceRefreshIntervals.some((interval) => interval === saved)) return saved
  } catch {
    // localStorage may be unavailable in privacy-restricted browser contexts.
  }
  return defaultResourceRefreshInterval
}

export function saveResourceRefreshInterval(intervalMs: number): void {
  try {
    globalThis.localStorage.setItem(resourceRefreshIntervalStorageKey, String(intervalMs))
  } catch {
    // The current page can still use the selected interval without persistence.
  }
}
