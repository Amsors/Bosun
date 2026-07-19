<script setup lang="ts">
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

import type { SessionPhase } from '../api/contracts'
import { useTerminal } from '../composables/use-terminal'

const props = defineProps<{
  sessionId: string
  getAccessToken: () => string | null
  refreshAccessToken: () => Promise<string | null>
  getSessionPhase: () => Promise<SessionPhase>
}>()

const host = ref<InstanceType<typeof globalThis.HTMLElement> | null>(null)
let xterm: Terminal | null = null
let fitAddon: FitAddon | null = null
let resizeObserver: InstanceType<typeof globalThis.ResizeObserver> | null = null

const terminal = useTerminal({
  sessionId: props.sessionId,
  getAccessToken: props.getAccessToken,
  refreshAccessToken: props.refreshAccessToken,
  getSessionPhase: props.getSessionPhase,
  onOutput: (data) => xterm?.write(data),
})

const notice = computed(() => {
  switch (terminal.status.value) {
    case 'reconnecting':
      return `连接已断开，正在进行第 ${terminal.reconnectAttempt.value} 次重连`
    case 'slow':
      return '网络发送缓慢，输入已暂停；连接恢复后可继续操作'
    case 'hibernated':
      return '会话已休眠，请先恢复会话再打开终端'
    case 'replaced':
      return '终端已在另一个窗口打开，本窗口连接已停止'
    case 'error':
      return '终端连接异常，正在检查会话状态'
    default:
      return ''
  }
})

onMounted(() => {
  if (host.value === null) {
    return
  }
  xterm = new Terminal({
    cursorBlink: true,
    convertEol: false,
    scrollback: 5000,
    theme: { background: '#101418', foreground: '#e7edf3' },
  })
  fitAddon = new FitAddon()
  xterm.loadAddon(fitAddon)
  xterm.open(host.value)
  xterm.onData((data) => terminal.sendInput(data))
  xterm.onResize(({ cols, rows }) => terminal.resize(cols, rows))

  resizeObserver = new globalThis.ResizeObserver(() => fitAddon?.fit())
  resizeObserver.observe(host.value)
  fitAddon.fit()
  terminal.resize(xterm.cols, xterm.rows)
  terminal.connect()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  terminal.disconnect()
  xterm?.dispose()
})
</script>

<template>
  <section class="terminal-shell" aria-label="会话终端">
    <p v-if="notice" class="terminal-notice" role="status">{{ notice }}</p>
    <div ref="host" class="terminal-host" />
  </section>
</template>

<style scoped>
.terminal-shell {
  min-height: 24rem;
  overflow: hidden;
  border: 1px solid #34404b;
  border-radius: 0.5rem;
  background: #101418;
}

.terminal-notice {
  margin: 0;
  padding: 0.55rem 0.75rem;
  color: #1b252e;
  background: #f3c96b;
}

.terminal-host {
  height: 32rem;
  padding: 0.5rem;
}
</style>
