import { computed, onScopeDispose, ref, type Ref } from 'vue'

import {
  terminalReconnectDelaysMs,
  terminalSubprotocol,
  type SessionPhase,
  type TerminalFrame,
} from '../api/contracts'

const socketOpen = 1
const replacedCloseCode = 4001
const runtimeEndedCloseCode = 4004
const authenticationCloseCode = 4401
const defaultBufferedAmountLimit = 256 * 1024
const defaultInputChunkSize = 48 * 1024

export type TerminalConnectionStatus =
  | 'idle'
  | 'connecting'
  | 'open'
  | 'reconnecting'
  | 'slow'
  | 'hibernated'
  | 'replaced'
  | 'ended'
  | 'closed'
  | 'error'

export interface TerminalWebSocket {
  readonly readyState: number
  readonly bufferedAmount: number
  readonly protocol: string
  onopen: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent<unknown>) => void) | null
  onclose: ((event: CloseEvent) => void) | null
  onerror: ((event: Event) => void) | null
  send(data: string): void
  close(code?: number, reason?: string): void
}

export interface UseTerminalOptions {
  sessionId: string
  getAccessToken: () => string | null
  refreshAccessToken: () => Promise<string | null>
  getSessionPhase: () => Promise<SessionPhase>
  onOutput: (data: Uint8Array) => void
  webSocketFactory?: (url: string, protocols: string[]) => TerminalWebSocket
  bufferedAmountLimit?: number
}

export interface TerminalController {
  status: Ref<TerminalConnectionStatus>
  slowConnection: Readonly<Ref<boolean>>
  reconnectAttempt: Readonly<Ref<number>>
  connect: () => void
  disconnect: () => void
  sendInput: (data: string | Uint8Array) => boolean
  resize: (cols: number, rows: number) => boolean
}

export function useTerminal(options: UseTerminalOptions): TerminalController {
  const status = ref<TerminalConnectionStatus>('idle')
  const slowConnection = ref(false)
  const reconnectAttempt = ref(0)
  const socketFactory =
    options.webSocketFactory ??
    ((url, protocols) => new WebSocket(url, protocols) as TerminalWebSocket)
  const bufferedAmountLimit = options.bufferedAmountLimit ?? defaultBufferedAmountLimit

  let socket: TerminalWebSocket | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let stopped = false
  let opened = false
  let refreshAttempted = false
  let retryIndex = 0
  let generation = 0
  let lastResize: { cols: number; rows: number } | null = null

  function connect(): void {
    if (stopped || socket?.readyState === socketOpen) {
      return
    }
    clearReconnectTimer()
    openSocket()
  }

  function openSocket(): void {
    const token = options.getAccessToken()
    if (!token) {
      void refreshAndReconnect()
      return
    }

    const currentGeneration = ++generation
    opened = false
    status.value = retryIndex === 0 ? 'connecting' : 'reconnecting'
    const next = socketFactory(terminalURL(options.sessionId), [
      terminalSubprotocol,
      `bearer.${token}`,
    ])
    socket = next

    next.onopen = () => {
      if (currentGeneration !== generation || stopped) {
        next.close(1000, 'stale_connection')
        return
      }
      if (next.protocol !== terminalSubprotocol) {
        status.value = 'error'
        next.close(1002, 'invalid_subprotocol')
        return
      }
      opened = true
      retryIndex = 0
      reconnectAttempt.value = 0
      refreshAttempted = false
      slowConnection.value = false
      status.value = 'open'
      if (lastResize !== null) {
        sendFrame({ t: 'resize', d: JSON.stringify(lastResize) })
      }
    }
    next.onmessage = (event) => {
      if (currentGeneration !== generation) {
        return
      }
      const frame = parseTerminalFrame(event.data)
      if (frame?.t === 'stdout') {
        const output = decodeBase64(frame.d)
        if (output !== null) {
          options.onOutput(output)
        }
      }
    }
    next.onerror = () => {
      if (currentGeneration === generation && status.value !== 'hibernated') {
        status.value = 'error'
      }
    }
    next.onclose = (event) => {
      if (currentGeneration !== generation || stopped) {
        return
      }
      socket = null
      if (event.code === replacedCloseCode) {
        stopped = true
        status.value = 'replaced'
        return
      }
      if (event.code === runtimeEndedCloseCode) {
        status.value = 'ended'
        void scheduleReconnect()
        return
      }
      if ((!opened || event.code === authenticationCloseCode) && !refreshAttempted) {
        void refreshAndReconnect()
        return
      }
      void scheduleReconnect()
    }
  }

  async function refreshAndReconnect(): Promise<void> {
    if (stopped || refreshAttempted) {
      await scheduleReconnect()
      return
    }
    refreshAttempted = true
    status.value = 'reconnecting'
    try {
      const token = await options.refreshAccessToken()
      if (stopped) {
        return
      }
      if (token) {
        openSocket()
        return
      }
    } catch {
      status.value = 'error'
    }
    await scheduleReconnect()
  }

  async function scheduleReconnect(): Promise<void> {
    if (stopped || reconnectTimer !== null) {
      return
    }
    try {
      const phase = await options.getSessionPhase()
      if (stopped) {
        return
      }
      if (phase === 'Hibernated' || phase === 'Hibernating' || phase === 'Archived') {
        stopped = true
        status.value = 'hibernated'
        return
      }
    } catch {
      status.value = 'error'
    }

    const delay =
      terminalReconnectDelaysMs[Math.min(retryIndex, terminalReconnectDelaysMs.length - 1)]
    retryIndex += 1
    reconnectAttempt.value = retryIndex
    status.value = 'reconnecting'
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      openSocket()
    }, delay)
  }

  function disconnect(): void {
    stopped = true
    generation += 1
    clearReconnectTimer()
    socket?.close(1000, 'terminal_closed')
    socket = null
    status.value = 'closed'
  }

  function sendInput(data: string | Uint8Array): boolean {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data
    for (let offset = 0; offset < bytes.length; offset += defaultInputChunkSize) {
      if (
        !sendFrame({
          t: 'stdin',
          d: encodeBase64(bytes.subarray(offset, offset + defaultInputChunkSize)),
        })
      ) {
        return false
      }
    }
    return true
  }

  function resize(cols: number, rows: number): boolean {
    if (!Number.isInteger(cols) || !Number.isInteger(rows) || cols <= 0 || rows <= 0) {
      return false
    }
    lastResize = { cols, rows }
    return sendFrame({ t: 'resize', d: JSON.stringify(lastResize) })
  }

  function sendFrame(frame: TerminalFrame): boolean {
    if (socket === null || socket.readyState !== socketOpen) {
      return false
    }
    if (socket.bufferedAmount >= bufferedAmountLimit) {
      slowConnection.value = true
      status.value = 'slow'
      return false
    }
    socket.send(JSON.stringify(frame))
    if (slowConnection.value) {
      slowConnection.value = false
      status.value = 'open'
    }
    return true
  }

  function clearReconnectTimer(): void {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  onScopeDispose(disconnect)

  return {
    status,
    slowConnection: computed(() => slowConnection.value),
    reconnectAttempt: computed(() => reconnectAttempt.value),
    connect,
    disconnect,
    sendInput,
    resize,
  }
}

function terminalURL(sessionId: string): string {
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${window.location.host}/api/v1/sessions/${encodeURIComponent(sessionId)}/terminal`
}

function parseTerminalFrame(value: unknown): TerminalFrame | null {
  if (typeof value !== 'string') {
    return null
  }
  try {
    const parsed: unknown = JSON.parse(value)
    if (
      typeof parsed === 'object' &&
      parsed !== null &&
      't' in parsed &&
      'd' in parsed &&
      (parsed.t === 'stdin' || parsed.t === 'stdout' || parsed.t === 'resize') &&
      typeof parsed.d === 'string'
    ) {
      return { t: parsed.t, d: parsed.d }
    }
  } catch {
    return null
  }
  return null
}

function encodeBase64(data: Uint8Array): string {
  let binary = ''
  const chunkSize = 0x8000
  for (let offset = 0; offset < data.length; offset += chunkSize) {
    binary += String.fromCharCode(...data.subarray(offset, offset + chunkSize))
  }
  return btoa(binary)
}

function decodeBase64(value: string): Uint8Array | null {
  try {
    const binary = atob(value)
    return Uint8Array.from(binary, (character) => character.charCodeAt(0))
  } catch {
    return null
  }
}
