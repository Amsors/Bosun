import { afterEach, describe, expect, it, vi } from 'vitest'

import { monitorApi } from './monitor'

describe('monitor API', () => {
  afterEach(() => vi.restoreAllMocks())

  it('keeps cluster monitoring public and authenticates session resources', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      return new Response(JSON.stringify({ code: 0, message: 'ok', data: {} }), { status: 200 })
    })

    await monitorApi.cluster()
    await monitorApi.session('access-token', 'session/id')
    await monitorApi.resizeAgent('session/id', { cpuMillicores: 700, memoryBytes: 1073741824 })

    expect(fetch).toHaveBeenNthCalledWith(
      1,
      '/api/v1/admin/cluster',
      expect.objectContaining({
        headers: expect.not.objectContaining({ Authorization: expect.anything() }),
      }),
    )
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      '/api/v1/sessions/session%2Fid/resources',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer access-token' }),
      }),
    )
    expect(fetch).toHaveBeenNthCalledWith(
      3,
      '/api/v1/admin/sessions/session%2Fid/resources',
      expect.objectContaining({
        method: 'PUT',
        headers: expect.not.objectContaining({ Authorization: expect.anything() }),
        body: JSON.stringify({ cpuMillicores: 700, memoryBytes: 1073741824 }),
      }),
    )
  })
})
