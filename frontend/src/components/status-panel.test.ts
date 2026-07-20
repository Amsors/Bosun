import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import StatusPanel from './status-panel.vue'

describe('status panel', () => {
  it('announces errors and exposes retry content', () => {
    const wrapper = mount(StatusPanel, {
      props: { kind: 'error', message: '加载失败' },
      slots: { default: '<button>重试</button>' },
    })

    expect(wrapper.attributes('aria-live')).toBe('assertive')
    expect(wrapper.text()).toContain('加载失败')
    expect(wrapper.get('button').text()).toBe('重试')
  })
})
