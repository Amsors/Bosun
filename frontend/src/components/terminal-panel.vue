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
    case 'ended':
      return '终端运行时已结束，请恢复或重建会话后重试'
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
    fontFamily:
      '"Noto Sans Mono CJK SC", "Noto Sans Mono", "Cascadia Mono", "Microsoft YaHei Mono", "Microsoft YaHei", monospace',
    scrollback: 10000,
    scrollOnEraseInDisplay: true,
    scrollOnUserInput: true,
    scrollSensitivity: 1,
    theme: {
      background: '#101418',
      foreground: '#e7edf3',
      scrollbarSliderBackground: '#71808a99',
      scrollbarSliderHoverBackground: '#91a0aa',
      scrollbarSliderActiveBackground: '#b0bec7',
    },
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
  position: relative;
  height: 32rem;
  overflow: hidden;
  border: 1px solid #34404b;
  border-radius: 0.5rem;
  background: #101418;
}

.terminal-notice {
  position: absolute;
  z-index: 1;
  top: 0;
  right: 0;
  left: 0;
  margin: 0;
  padding: 0.55rem 0.75rem;
  color: #1b252e;
  background: #f3c96b;
}

.terminal-host {
  height: 100%;
  overflow: hidden;
  padding: 0.5rem;
}

.terminal-host :deep(.xterm) {
  height: 100%;
}

.terminal-host :deep(.xterm-viewport) {
  overscroll-behavior: contain;
  scrollbar-color: #71808a #101418;
  scrollbar-width: thin;
}

.terminal-host :deep(.xterm-viewport::-webkit-scrollbar) {
  width: 0.7rem;
}

.terminal-host :deep(.xterm-viewport::-webkit-scrollbar-track) {
  background: #101418;
}

.terminal-host :deep(.xterm-viewport::-webkit-scrollbar-thumb) {
  border: 2px solid #101418;
  border-radius: 999px;
  background: #71808a;
}
</style>
