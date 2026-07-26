import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ClusterResourceSnapshot } from '../api/contracts'
import { resourceRefreshIntervalStorageKey } from '../utils/resource-refresh'
import AdminView from './admin-view.vue'

const cluster = vi.hoisted(() => vi.fn())
const resizeAgent = vi.hoisted(() => vi.fn())
const restoreAuto = vi.hoisted(() => vi.fn())

vi.mock('../api/monitor', () => ({
  monitorApi: {
    cluster,
    resizeAgent,
    restoreAuto,
  },
}))

function snapshot(cpuLimit: number): ClusterResourceSnapshot {
  const mebibyte = 1024 * 1024
  return {
    observedAt: '2026-07-24T03:00:00Z',
    podMetricsAvailable: true,
    nodeMetricsAvailable: true,
    nodes: [],
    pods: [
      {
        namespace: 'bosun-user',
        name: 'agent-session',
        phase: 'Running',
        resize: null,
        nodeName: 'worker-1',
        ready: true,
        restarts: 0,
        createdAt: '2026-07-24T02:00:00Z',
        metricsObservedAt: '2026-07-24T03:00:00Z',
        metricsWindowSeconds: 1,
        metricsSource: 'kubelet-summary',
        usage: { cpuMillicores: 100, memoryBytes: 256 * mebibyte },
        requests: { cpuMillicores: 250, memoryBytes: 512 * mebibyte },
        limits: { cpuMillicores: cpuLimit + 50, memoryBytes: 1024 * mebibyte },
        containers: [
          {
            name: 'agent',
            usage: { cpuMillicores: 95, memoryBytes: 244 * mebibyte },
            requests: { cpuMillicores: 240, memoryBytes: 496 * mebibyte },
            limits: { cpuMillicores: cpuLimit, memoryBytes: 960 * mebibyte },
            actualLimits: { cpuMillicores: cpuLimit, memoryBytes: 960 * mebibyte },
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
        sessionName: '课程演示',
        username: 'student@example.com',
        resourceScaling: {
          mode: 'Auto',
          desiredResources: { cpuMillicores: cpuLimit, memoryBytes: 960 * mebibyte },
          actualResources: { cpuMillicores: cpuLimit, memoryBytes: 960 * mebibyte },
          actualResourcesAvailable: true,
          minCPUMillicores: 250,
          maxCPUMillicores: 1500,
          minMemoryBytes: 512 * mebibyte,
          maxMemoryBytes: 3 * 1024 * mebibyte,
        },
      },
    ],
  }
}

describe('AdminView', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
    globalThis.localStorage.clear()
  })

  it('preserves an edited resize draft while cluster polling continues', async () => {
    vi.useFakeTimers()
    cluster.mockResolvedValueOnce(snapshot(450)).mockResolvedValueOnce(snapshot(500))

    const wrapper = mount(AdminView, {
      global: {
        stubs: {
          AppShell: { template: '<main><slot /></main>' },
          StatusPanel: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()

    const cpuInput = wrapper.find<HTMLInputElement>('.resize-form input')
    await cpuInput.trigger('focus')
    await cpuInput.setValue('733')

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect(wrapper.find<HTMLInputElement>('.resize-form input').element.value).toBe('733')
    expect(cluster).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('applies and persists a faster resource refresh interval', async () => {
    vi.useFakeTimers()
    cluster.mockResolvedValue(snapshot(450))

    const wrapper = mount(AdminView, {
      global: {
        stubs: {
          AppShell: { template: '<main><slot /></main>' },
          StatusPanel: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()

    const intervalSelect = wrapper.get<HTMLSelectElement>('select[aria-label="资源数据刷新周期"]')
    expect(intervalSelect.element.value).toBe('5000')

    await intervalSelect.setValue('1000')
    expect(globalThis.localStorage.getItem(resourceRefreshIntervalStorageKey)).toBe('1000')

    await vi.advanceTimersByTimeAsync(999)
    expect(cluster).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(cluster).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })

  it('shows queued Manual intent and restores Auto mode', async () => {
    const current = snapshot(450)
    const scaling = current.pods[0]!.resourceScaling!
    current.pods[0]!.resourceScaling = {
      ...scaling,
      mode: 'Manual',
      manualLimits: { cpuMillicores: 700, memoryBytes: 1024 * 1024 * 1024 },
    }
    cluster.mockResolvedValue(current)
    restoreAuto.mockResolvedValue({
      observedAt: '2026-07-24T03:00:05Z',
      ...scaling,
      mode: 'Auto',
      resize: null,
    })

    const wrapper = mount(AdminView, {
      global: {
        stubs: {
          AppShell: { template: '<main><slot /></main>' },
          StatusPanel: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('手动调整已排队')
    const restore = wrapper.findAll('button').find((button) => button.text() === '恢复自动调度')
    expect(restore).toBeTruthy()
    await restore!.trigger('click')
    await flushPromises()

    expect(restoreAuto).toHaveBeenCalledWith('018f9c6e-1234-7000-8000-abcdef012501')
    expect(wrapper.text()).toContain('自动调度')
    wrapper.unmount()
  })

  it('shows the Auto CPU load class and recommendation without a memory recommendation', async () => {
    const current = snapshot(450)
    current.pods[0]!.resourceScaling = {
      ...current.pods[0]!.resourceScaling!,
      loadClass: 'CPUHigh',
      recommendedCPUMillicores: 700,
      lastAppliedAt: '2026-07-24T03:00:00Z',
    }
    cluster.mockResolvedValue(current)

    const wrapper = mount(AdminView, {
      global: {
        stubs: {
          AppShell: { template: '<main><slot /></main>' },
          StatusPanel: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('CPU 高负载')
    expect(wrapper.text()).toContain('CPU 推荐: 700m')
    expect(wrapper.text()).toContain('最近应用:')
    expect(wrapper.text()).not.toContain('内存推荐')
    wrapper.unmount()
  })
})
