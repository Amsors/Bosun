import type { ApiEnvelope } from './contracts'

export class ApiError extends Error {
  readonly status: number
  readonly code: number

  constructor(status: number, code: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    headers: { Accept: 'application/json', ...init.headers },
  })
  const envelope = (await response.json()) as ApiEnvelope<T>
  if (!response.ok || envelope.code !== 0) {
    throw new ApiError(response.status, envelope.code, envelope.message)
  }
  return envelope.data
}
