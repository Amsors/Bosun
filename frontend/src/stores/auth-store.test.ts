import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import * as authApi from '../api/auth'
import { useAuthStore } from './auth-store'

vi.mock('../api/auth')

describe('auth store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('keeps the access token in store memory after login', async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      accessToken: 'access',
      tokenType: 'Bearer',
      accessExpiresAt: '2026-07-20T03:00:00Z',
      user: { id: 'user-1', email: 'user@example.com', createdAt: '2026-07-20T02:00:00Z' },
    })
    const store = useAuthStore()

    await store.login('user@example.com', 'correcthorse')

    expect(store.accessToken).toBe('access')
    expect(store.authenticated).toBe(true)
  })

  it('clears authentication when refresh fails', async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      accessToken: 'old',
      tokenType: 'Bearer',
      accessExpiresAt: '2026-07-20T03:00:00Z',
      user: { id: 'user-1', email: 'user@example.com', createdAt: '2026-07-20T02:00:00Z' },
    })
    vi.mocked(authApi.refresh).mockRejectedValue(new Error('expired'))
    const store = useAuthStore()
    await store.login('user@example.com', 'correcthorse')

    await expect(store.refresh()).resolves.toBeNull()
    expect(store.accessToken).toBeNull()
  })
})
