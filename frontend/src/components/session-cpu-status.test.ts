import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SessionCPUStatus from './session-cpu-status.vue'

describe('SessionCPUStatus', () => {
  it('shows live usage, limit, utilization and scale-up state', () => {
    const wrapper = mount(SessionCPUStatus, {
      props: {
        usageMillicores: 900,
        limitMillicores: 1500,
        direction: 'up',
      },
    })

    expect(wrapper.text()).toContain('0.90 核 / 1.50 核')
    expect(wrapper.text()).toContain('使用率 60%')
    expect(wrapper.text()).toContain('↑ 刚刚扩容')
    expect(wrapper.get('.session-cpu-track span').attributes('style')).toContain('width: 60%')
  })

  it('labels low utilization when no limit change is active', () => {
    const wrapper = mount(SessionCPUStatus, {
      props: {
        usageMillicores: 100,
        limitMillicores: 1000,
        direction: null,
      },
    })

    expect(wrapper.text()).toContain('低负载')
    expect(wrapper.text()).toContain('Limit 1.00 核')
  })
})
