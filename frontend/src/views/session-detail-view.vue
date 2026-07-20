<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import type { Session } from '../api/contracts'
import { sessionApi } from '../api/sessions'
import AppShell from '../components/app-shell.vue'
import StatusPanel from '../components/status-panel.vue'
import TerminalPanel from '../components/terminal-panel.vue'
import { useAuthStore } from '../stores/auth-store'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const session = ref<Session | null>(null)
const loading = ref(true)
const error = ref('')
const actionBusy = ref(false)
let poller: ReturnType<typeof globalThis.setInterval> | null = null
const id = computed(() => String(route.params.id))

async function load(): Promise<void> {
  if (!auth.accessToken) return
  try {
    session.value = await sessionApi(auth.accessToken).get(id.value)
    error.value = ''
  } catch {
    error.value = '无法加载会话详情。'
  } finally {
    loading.value = false
  }
}

async function transition(action: 'resume' | 'retry' | 'hibernate'): Promise<void> {
  if (!auth.accessToken) return
  actionBusy.value = true
  try {
    session.value = await sessionApi(auth.accessToken).transition(id.value, action)
  } finally {
    actionBusy.value = false
  }
}

async function remove(): Promise<void> {
  if (
    !auth.accessToken ||
    !globalThis.confirm('永久删除该会话？Pod 与工作区中的全部文件都会被删除，且无法恢复。')
  )
    return
  actionBusy.value = true
  session.value = { ...session.value!, phase: 'Deleting' }
  try {
    await sessionApi(auth.accessToken).remove(id.value)
    await router.push('/sessions')
  } catch {
    error.value = '删除失败，请稍后重试。'
    actionBusy.value = false
  }
}

onMounted(async () => {
  await load()
  poller = globalThis.setInterval(load, 5000)
})
onUnmounted(() => poller && globalThis.clearInterval(poller))
</script>

<template>
  <AppShell>
    <RouterLink to="/sessions">← 返回会话</RouterLink>
    <StatusPanel v-if="loading" kind="loading" message="正在加载会话…" />
    <StatusPanel v-else-if="error && !session" kind="error" :message="error"
      ><button @click="load">重试</button></StatusPanel
    >
    <template v-else-if="session">
      <div class="page-heading detail-heading">
        <div>
          <p class="eyebrow">SESSION</p>
          <h1>{{ session.runtime }}</h1>
          <p class="mono">{{ session.id }}</p>
        </div>
        <span class="phase" :data-phase="session.phase">{{ session.phase }}</span>
      </div>
      <p v-if="error" class="alert" role="alert">{{ error }}</p>
      <div class="action-row">
        <button
          v-if="session.phase === 'Hibernated'"
          class="primary"
          :disabled="actionBusy"
          @click="transition('resume')"
        >
          恢复会话
        </button>
        <button
          v-if="session.phase === 'Failed'"
          class="primary"
          :disabled="actionBusy"
          @click="transition('retry')"
        >
          重试
        </button>
        <button
          v-if="session.phase === 'Running'"
          :disabled="actionBusy"
          @click="transition('hibernate')"
        >
          休眠
        </button>
        <button
          class="danger"
          :disabled="actionBusy || session.phase === 'Deleting'"
          @click="remove"
        >
          永久删除
        </button>
      </div>
      <section v-if="session.conditions.length" class="card conditions">
        <h2>状态详情</h2>
        <article
          v-for="condition in session.conditions"
          :key="`${condition.type}-${condition.lastTransitionTime}`"
        >
          <strong>{{ condition.reason || condition.type }}</strong>
          <p>{{ condition.message }}</p>
        </article>
      </section>
      <TerminalPanel
        v-if="session.phase === 'Running'"
        :session-id="session.id"
        :get-access-token="() => auth.accessToken"
        :refresh-access-token="auth.refresh"
        :get-session-phase="async () => (await sessionApi(auth.accessToken!).get(id)).phase"
      />
      <StatusPanel
        v-else
        kind="empty"
        :message="
          session.phase === 'Hibernated'
            ? '工作区已安全保留。恢复会话后可继续使用终端。'
            : 'Agent 正在准备中，页面会自动更新。'
        "
      />
    </template>
  </AppShell>
</template>
