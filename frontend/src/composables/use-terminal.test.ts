import { effectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { terminalSubprotocol, type SessionPhase } from '../api/contracts'
import { useTerminal, type TerminalWebSocket } from './use-terminal'

class FakeWebSocket implements TerminalWebSocket {
  readyState = 0
  bufferedAmount = 0
  protocol = terminalSubprotocol
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  readonly sent: string[] = []
  readonly url: string
  readonly protocols: string[]

  constructor(url: string, protocols: string[]) {
    this.url = url
    this.protocols = protocols
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(code = 1000, reason = ''): void {
    this.readyState = 3
    this.onclose?.({ code, reason } as CloseEvent)
  }

  open(): void {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }

  receive(data: string): void {
    this.onmessage?.({ data } as MessageEvent<string>)
  }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('useTerminal', () => {
  it('uses the frozen 1, 2, 4, 8, 15 second reconnect sequence', async () => {
    vi.useFakeTimers()
    const sockets: FakeWebSocket[] = []
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => null,
        getSessionPhase: async () => 'Running',
        onOutput: () => undefined,
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    sockets[0]?.close(1006)
    await flushPromises()

    for (const [index, delay] of [1000, 2000, 4000, 8000, 15000].entries()) {
      expect(controller.reconnectAttempt.value).toBe(index + 1)
      await vi.advanceTimersByTimeAsync(delay)
      expect(sockets).toHaveLength(index + 2)
      if (index < 4) {
        sockets[index + 1]?.close(1006)
        await flushPromises()
      }
    }
    scope.stop()
  })

  it('refreshes the access token once after an authentication handshake failure', async () => {
    const sockets: FakeWebSocket[] = []
    let token = 'expired-token'
    const refresh = vi.fn(async () => {
      token = 'fresh-token'
      return token
    })
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => token,
        refreshAccessToken: refresh,
        getSessionPhase: async () => 'Running',
        onOutput: () => undefined,
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    sockets[0]?.close(1006)
    await flushPromises()

    expect(refresh).toHaveBeenCalledTimes(1)
    expect(sockets).toHaveLength(2)
    expect(sockets[1]?.protocols).toContain('bearer.fresh-token')
    scope.stop()
  })

  it('stops reconnecting when the session is hibernating', async () => {
    vi.useFakeTimers()
    const sockets: FakeWebSocket[] = []
    let phase: SessionPhase = 'Running'
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
        getSessionPhase: async () => phase,
        onOutput: () => undefined,
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    sockets[0]?.open()
    phase = 'Hibernating'
    sockets[0]?.close(1006)
    await flushPromises()
    await vi.runAllTimersAsync()

    expect(controller.status.value).toBe('hibernated')
    expect(sockets).toHaveLength(1)
    scope.stop()
  })

  it('stops reconnecting when the terminal runtime has ended', async () => {
    vi.useFakeTimers()
    const sockets: FakeWebSocket[] = []
    const getSessionPhase = vi.fn(async (): Promise<SessionPhase> => 'Running')
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
        getSessionPhase,
        onOutput: () => undefined,
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    sockets[0]?.open()
    sockets[0]?.close(4004, 'terminal_runtime_ended')
    await vi.runAllTimersAsync()

    expect(controller.status.value).toBe('ended')
    expect(sockets).toHaveLength(1)
    expect(getSessionPhase).not.toHaveBeenCalled()
    scope.stop()
  })

  it('encodes frames and exposes bounded browser send backpressure', () => {
    const sockets: FakeWebSocket[] = []
    const output: Uint8Array[] = []
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
        getSessionPhase: async () => 'Running',
        onOutput: (data) => output.push(data),
        bufferedAmountLimit: 16,
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    const socket = sockets[0]
    expect(controller.resize(120, 32)).toBe(false)
    socket?.open()
    expect(socket?.sent[0]).toContain('"t":"resize"')
    expect(controller.sendInput('ls\n')).toBe(true)
    expect(socket?.sent[1]).toContain('"t":"stdin"')

    socket?.receive(JSON.stringify({ t: 'stdout', d: btoa('ready') }))
    expect(new TextDecoder().decode(output[0])).toBe('ready')

    if (socket) {
      socket.bufferedAmount = 16
    }
    expect(controller.sendInput('blocked')).toBe(false)
    expect(controller.slowConnection.value).toBe(true)
    expect(controller.status.value).toBe('slow')
    scope.stop()
  })

  it('preserves UTF-8 input and output used by Chinese text and Claude Code', () => {
    const sockets: FakeWebSocket[] = []
    const output: Uint8Array[] = []
    const scope = effectScope()
    const controller = scope.run(() =>
      useTerminal({
        sessionId: 'session-id',
        getAccessToken: () => 'access-token',
        refreshAccessToken: async () => 'access-token',
        getSessionPhase: async () => 'Running',
        onOutput: (data) => output.push(data),
        webSocketFactory: (url, protocols) => {
          const socket = new FakeWebSocket(url, protocols)
          sockets.push(socket)
          return socket
        },
      }),
    )
    if (controller === undefined) {
      throw new Error('controller was not created')
    }

    controller.connect()
    const socket = sockets[0]
    socket?.open()

    const text = '中文输入 ─╭╯ ✓ ⏺ ⎿ 🟢'
    expect(controller.sendInput(text)).toBe(true)
    const inputFrame = JSON.parse(socket?.sent[0] ?? '{}') as {
      t?: string
      d?: string
    }
    expect(inputFrame.t).toBe('stdin')
    expect(new TextDecoder().decode(decodeTestBase64(inputFrame.d ?? ''))).toBe(text)

    const encodedOutput = encodeTestBase64(new TextEncoder().encode(text))
    socket?.receive(JSON.stringify({ t: 'stdout', d: encodedOutput }))
    expect(new TextDecoder().decode(output[0])).toBe(text)
    scope.stop()
  })
})

async function flushPromises(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

function encodeTestBase64(data: Uint8Array): string {
  return btoa(String.fromCharCode(...data))
}

function decodeTestBase64(value: string): Uint8Array {
  return Uint8Array.from(atob(value), (character) => character.charCodeAt(0))
}
