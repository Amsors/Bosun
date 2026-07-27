import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SessionResourceSnapshot } from '../api/contracts'
import { resourceRefreshIntervalStorageKey } from '../utils/resource-refresh'
import ResourceUsagePanel from './resource-usage-panel.vue'

const getSessionResources = vi.hoisted(() => vi.fn())

vi.mock('../api/monitor', () => ({
  monitorApi: {
    session: getSessionResources,
  },
}))

function snapshot(
  agentCPU: number,
  agentMemoryMiB: number,
  metricsObservedAt = '2026-07-24T03:00:00Z',
): SessionResourceSnapshot {
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
      metricsObservedAt,
      metricsWindowSeconds: 1,
      metricsSource: 'kubelet-summary',
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
          actualLimits: {
            cpuMillicores: agentCPU,
            memoryBytes: agentMemoryMiB * mebibyte,
          },
          actualResourcesAvailable: true,
        },
        {
          name: 'auth-proxy',
          usage: { cpuMillicores: 5, memoryBytes: 12 * mebibyte },
          requests: { cpuMillicores: 10, memoryBytes: 16 * mebibyte },
          limits: { cpuMillicores: 50, memoryBytes: 64 * mebibyte },
          actualLimits: { cpuMillicores: 50, memoryBytes: 64 * mebibyte },
          actualResourcesAvailable: true,
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
    globalThis.localStorage.clear()
  })

  it('displays agent requests and limits without the auth-proxy resources', async () => {
    getSessionResources.mockResolvedValue(snapshot(450, 960))

    const wrapper = mount(ResourceUsagePanel, {
      props: {
        sessionId: '018f9c6e-1234-7000-8000-abcdef012501',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
      },
    })
    await flushPromises()

    const [cpuChart, memoryChart] = wrapper.findAll('.resource-chart')
    expect(cpuChart.text()).toContain('Request240m')
    expect(cpuChart.text()).toContain('Limit450m')
    expect(cpuChart.text()).not.toContain('Request250m')
    expect(cpuChart.text()).not.toContain('Limit500m')
    expect(memoryChart.text()).toContain('Request496 MiB')
    expect(memoryChart.text()).toContain('Limit960 MiB')
    expect(memoryChart.text()).not.toContain('Request512 MiB')
    expect(memoryChart.text()).not.toContain('Limit1.00 GiB')

    wrapper.unmount()
  })

  it('updates the displayed agent limits on the next polling cycle', async () => {
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
    expect(wrapper.text()).toContain('CPU 调度更新')
    expect(wrapper.text()).toContain('CPU 分配已从 450m 调整为 700m')
    expect(getSessionResources).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })

  it('uses the saved refresh interval and updates the chart description', async () => {
    vi.useFakeTimers()
    globalThis.localStorage.setItem(resourceRefreshIntervalStorageKey, '2000')
    getSessionResources.mockResolvedValue(snapshot(450, 960))

    const wrapper = mount(ResourceUsagePanel, {
      props: {
        sessionId: '018f9c6e-1234-7000-8000-abcdef012501',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
      },
    })
    await flushPromises()

    expect(
      wrapper.get<HTMLSelectElement>('select[aria-label="资源数据刷新周期"]').element.value,
    ).toBe('2000')
    expect(wrapper.text()).toContain('每 2 秒刷新')

    await vi.advanceTimersByTimeAsync(1999)
    expect(getSessionResources).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(getSessionResources).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })

  it('records chart points only when the real metric timestamp changes', async () => {
    vi.useFakeTimers()
    getSessionResources
      .mockResolvedValueOnce(snapshot(450, 960, '2026-07-24T03:00:00Z'))
      .mockResolvedValueOnce(snapshot(450, 960, '2026-07-24T03:00:00Z'))
      .mockResolvedValueOnce(snapshot(700, 960, '2026-07-24T03:00:01Z'))

    const wrapper = mount(ResourceUsagePanel, {
      props: {
        sessionId: '018f9c6e-1234-7000-8000-abcdef012501',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
      },
    })
    await flushPromises()
    expect(wrapper.text()).toContain('最近 1 个采样点')
    expect(wrapper.text()).toContain('Kubelet 秒级采样')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.text()).toContain('最近 1 个采样点')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()
    expect(wrapper.text()).toContain('最近 2 个采样点')

    wrapper.unmount()
  })
})
