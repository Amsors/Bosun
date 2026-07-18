import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, request } from './client'

describe('request', () => {
  afterEach(() => vi.restoreAllMocks())

  it('unwraps a successful API envelope', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ code: 0, message: 'ok', data: { ready: true } }), {
        status: 200,
      }),
    )

    await expect(request<{ ready: boolean }>('/health')).resolves.toEqual({ ready: true })
  })

  it('raises the stable API error', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ code: 30001, message: 'session_not_found', data: null }), {
        status: 404,
      }),
    )

    await expect(request('/sessions/missing')).rejects.toEqual(
      new ApiError(404, 30001, 'session_not_found'),
    )
  })
})
