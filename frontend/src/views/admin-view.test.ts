import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ClusterResourceSnapshot } from '../api/contracts'
import AdminView from './admin-view.vue'

const cluster = vi.hoisted(() => vi.fn())
const resizeAgent = vi.hoisted(() => vi.fn())

vi.mock('../api/monitor', () => ({
  monitorApi: {
    cluster,
    resizeAgent,
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
        usage: { cpuMillicores: 100, memoryBytes: 256 * mebibyte },
        requests: { cpuMillicores: 250, memoryBytes: 512 * mebibyte },
        limits: { cpuMillicores: cpuLimit + 50, memoryBytes: 1024 * mebibyte },
        containers: [
          {
            name: 'agent',
            usage: { cpuMillicores: 95, memoryBytes: 244 * mebibyte },
            requests: { cpuMillicores: 240, memoryBytes: 496 * mebibyte },
            limits: { cpuMillicores: cpuLimit, memoryBytes: 960 * mebibyte },
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
        sessionName: '课程演示',
        username: 'student@example.com',
      },
    ],
  }
}

describe('AdminView', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
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
})
