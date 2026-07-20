import { authenticatedRequest } from './client'
import type { CreateSessionRequest, PaginatedData, Session } from './contracts'

export function sessionApi(token: string) {
  const call = authenticatedRequest(token)
  return {
    list: (page = 1, pageSize = 20) =>
      call<PaginatedData<Session>>(`/sessions?page=${page}&page_size=${pageSize}`),
    get: (id: string) => call<Session>(`/sessions/${encodeURIComponent(id)}`),
    create: (body: CreateSessionRequest, idempotencyKey: string) =>
      call<Session>('/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(body),
      }),
    remove: (id: string) =>
      call<Session>(`/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    transition: (id: string, action: 'hibernate' | 'resume' | 'retry') =>
      call<Session>(`/sessions/${encodeURIComponent(id)}/${action}`, { method: 'POST' }),
  }
}
