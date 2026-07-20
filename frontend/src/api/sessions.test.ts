import { afterEach, describe, expect, it, vi } from 'vitest'

import { sessionApi } from './sessions'

describe('session API', () => {
  afterEach(() => vi.restoreAllMocks())

  it('sends bearer auth and reuses the supplied idempotency key', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          code: 0,
          message: 'ok',
          data: { id: 'session-1' },
        }),
        { status: 202 },
      ),
    )

    await sessionApi('access').create(
      {
        tier: 'small',
        runtime: 'claude-code',
        provider: { mode: 'platform' },
        storagePolicy: 'local',
      },
      'create-key',
    )

    expect(fetch).toHaveBeenCalledWith(
      '/api/v1/sessions',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({
          Authorization: 'Bearer access',
          'Idempotency-Key': 'create-key',
        }),
      }),
    )
  })
})
