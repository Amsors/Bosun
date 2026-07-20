import { request } from './client'
import type { AuthData, MeData } from './contracts'

export function login(email: string, password: string): Promise<AuthData> {
  return request('/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
}

export function register(email: string, password: string, idempotencyKey: string): Promise<MeData> {
  return request('/auth/register', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify({ email, password }),
  })
}

export function refresh(): Promise<AuthData> {
  return request('/auth/refresh', { method: 'POST' })
}

export function logout(): Promise<unknown> {
  return request('/auth/logout', { method: 'POST' })
}
