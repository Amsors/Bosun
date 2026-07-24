import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SessionResourceSnapshot } from '../api/contracts'
import ResourceUsagePanel from './resource-usage-panel.vue'

const getSessionResources = vi.hoisted(() => vi.fn())

vi.mock('../api/monitor', () => ({
  monitorApi: {
    session: getSessionResources,
  },
}))

function snapshot(agentCPU: number, agentMemoryMiB: number): SessionResourceSnapshot {
  const mebibyte = 1024 * 1024
  return {
    observedAt: '2026-07-24T03:00:00Z',
    metricsAvailable: true,
    pod: {
      namespace: 'bosun-user',
      name: 'agent-session',
      phase: 'Running',
      resize: null,
      nodeName: 'worker-1',
      ready: true,
      restarts: 0,
      createdAt: '2026-07-24T02:00:00Z',
      usage: { cpuMillicores: 100, memoryBytes: 256 * mebibyte },
      requests: { cpuMillicores: 250, memoryBytes: 512 * mebibyte },
      limits: {
        cpuMillicores: agentCPU + 50,
        memoryBytes: (agentMemoryMiB + 64) * mebibyte,
      },
      containers: [
        {
          name: 'agent',
          usage: { cpuMillicores: 95, memoryBytes: 244 * mebibyte },
          requests: { cpuMillicores: 240, memoryBytes: 496 * mebibyte },
          limits: {
            cpuMillicores: agentCPU,
            memoryBytes: agentMemoryMiB * mebibyte,
          },
        },
        {
          name: 'auth-proxy',
          usage: { cpuMillicores: 5, memoryBytes: 12 * mebibyte },
          requests: { cpuMillicores: 10, memoryBytes: 16 * mebibyte },
          limits: { cpuMillicores: 50, memoryBytes: 64 * mebibyte },
        },
      ],
      isAgent: true,
      sessionID: '018f9c6e-1234-7000-8000-abcdef012501',
    },
  }
}

describe('ResourceUsagePanel', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('updates the displayed agent and Pod limits on the next polling cycle', async () => {
    vi.useFakeTimers()
    getSessionResources
      .mockResolvedValueOnce(snapshot(450, 960))
      .mockResolvedValueOnce(snapshot(700, 1536))

    const wrapper = mount(ResourceUsagePanel, {
      props: {
        sessionId: '018f9c6e-1234-7000-8000-abcdef012501',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
      },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('450m')
    expect(wrapper.text()).toContain('960 MiB')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.text()).toContain('700m')
    expect(wrapper.text()).toContain('1.50 GiB')
    expect(getSessionResources).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })
})
