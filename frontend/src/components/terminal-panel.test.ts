import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import TerminalPanel from './terminal-panel.vue'

const connect = vi.fn()
const disconnect = vi.fn()
const resize = vi.fn()
const terminalOptions = vi.hoisted(() => ({ value: null as Record<string, unknown> | null }))

vi.mock('../composables/use-terminal', () => ({
  useTerminal: () => ({
    status: { value: 'idle' },
    reconnectAttempt: { value: 0 },
    connect,
    disconnect,
    sendInput: vi.fn(),
    resize,
  }),
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24

    constructor(options: Record<string, unknown>) {
      terminalOptions.value = options
    }

    loadAddon = vi.fn()
    open = vi.fn()
    onData = vi.fn()
    onResize = vi.fn()
    dispose = vi.fn()
  },
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit = vi.fn()
  },
}))

class ResizeObserverStub {
  observe(): void {}
  disconnect(): void {}
}

describe('TerminalPanel', () => {
  beforeEach(() => {
    terminalOptions.value = null
    vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  })

  afterEach(() => {
    vi.clearAllMocks()
    vi.unstubAllGlobals()
  })

  it('connects the terminal after xterm initialization', () => {
    const wrapper = mount(TerminalPanel, {
      props: {
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
        getSessionPhase: async () => 'Running',
      },
    })

    expect(resize).toHaveBeenCalledWith(80, 24)
    expect(connect).toHaveBeenCalledOnce()
    expect(terminalOptions.value).toMatchObject({
      scrollback: 10000,
      scrollOnEraseInDisplay: true,
      scrollOnUserInput: true,
      scrollSensitivity: 1,
    })
    wrapper.unmount()
    expect(disconnect).toHaveBeenCalledOnce()
  })
})
