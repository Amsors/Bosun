<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import type { Session } from '../api/contracts'
import { sessionApi } from '../api/sessions'
import AppShell from '../components/app-shell.vue'
import ResourceUsagePanel from '../components/resource-usage-panel.vue'
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
const priorityLabels = { low: '低优先级', normal: '普通优先级', high: '高优先级' } as const
const userStatus = computed(() => {
  switch (session.value?.phaseReason) {
    case 'AgentWorking':
      return { label: 'AI 工作中', message: 'Claude 正在执行任务，暂时无需操作。', kind: 'working' }
    case 'AwaitingApproval':
      return {
        label: '等待批准',
        message: 'Claude 正在等待你批准一项操作，请查看下方终端。',
        kind: 'attention',
      }
    case 'AwaitingChoice':
      return {
        label: '等待选择',
        message: 'Claude 正在等待你选择下一步方案，请查看下方终端。',
        kind: 'attention',
      }
    case 'AwaitingInput':
      return {
        label: '等待指令',
        message: 'Claude 已完成当前回复，正在等待你的下一步指令。',
        kind: 'attention',
      }
    case 'Unschedulable':
      return {
        label: '等待资源',
        message: `${priorityLabels[session.value?.priority || 'normal']}会话正在调度队列中；资源释放后 Kubernetes 会自动启动它。`,
        kind: 'queued',
      }
    default:
      return { label: session.value?.phase || '', message: '', kind: 'normal' }
  }
})

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
          <h1>{{ session.name }}</h1>
          <p>{{ session.runtime }} · {{ session.tier === 'medium' ? 'Medium' : 'Small' }}</p>
          <p class="mono">{{ session.id }}</p>
        </div>
        <span class="phase" :data-phase="session.phase" :data-user-state="userStatus.kind">{{
          userStatus.label
        }}</span>
      </div>
      <p v-if="userStatus.message" class="agent-status-note" :data-kind="userStatus.kind">
        {{ userStatus.message }}
      </p>
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
      <section class="session-facts" aria-label="会话信息">
        <div>
          <span>当前状态</span>
          <strong>{{ userStatus.label }}</strong>
        </div>
        <div>
          <span>调度优先级</span>
          <strong>{{ priorityLabels[session.priority] }}</strong>
        </div>
        <div>
          <span>创建时间</span>
          <strong>{{ new Date(session.createdAt).toLocaleString('zh-CN') }}</strong>
        </div>
        <div>
          <span>最近活动</span>
          <strong>{{
            session.lastActiveAt
              ? new Date(session.lastActiveAt).toLocaleString('zh-CN')
              : '尚无活动'
          }}</strong>
        </div>
      </section>
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
      <ResourceUsagePanel
        v-if="session.phase === 'Running'"
        :session-id="session.id"
        :get-access-token="() => auth.accessToken"
        :refresh-access-token="auth.refresh"
      />
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
