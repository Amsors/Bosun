<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'

import type { Session, SessionPhase } from '../api/contracts'
import AppShell from '../components/app-shell.vue'
import StatusPanel from '../components/status-panel.vue'
import { useAuthStore } from '../stores/auth-store'
import { useSessionStore } from '../stores/session-store'

const auth = useAuthStore()
const sessions = useSessionStore()
const error = ref('')
const query = ref('')
const phaseFilter = ref<'all' | 'working' | 'sleeping' | 'attention'>('all')
let poller: ReturnType<typeof globalThis.setInterval> | null = null

const sleepingPhases: SessionPhase[] = ['Hibernating', 'Hibernated', 'Archived']
const attentionReasons = ['AwaitingApproval', 'AwaitingChoice', 'AwaitingInput']

const phaseLabels: Record<SessionPhase, string> = {
  Pending: '排队中',
  Provisioning: '正在准备',
  Running: '运行中',
  Idle: '空闲',
  Hibernating: '正在休眠',
  Hibernated: '已休眠',
  Archiving: '正在归档',
  Archived: '已归档',
  Restoring: '正在恢复',
  Deleting: '正在删除',
  Failed: '需要处理',
}

const phaseDescriptions: Record<SessionPhase, string> = {
  Pending: '等待集群分配资源',
  Provisioning: '正在创建 Agent 工作区',
  Running: '终端与 Agent 已就绪',
  Idle: '暂时没有终端活动',
  Hibernating: '正在安全释放计算资源',
  Hibernated: '工作区已保存，可随时恢复',
  Archiving: '正在保存工作区',
  Archived: '工作区已归档',
  Restoring: '正在重新挂载工作区',
  Deleting: '正在清理工作区',
  Failed: '打开详情查看失败原因',
}

const counts = computed(() => ({
  total: sessions.items.length,
  working: sessions.items.filter(isWorking).length,
  sleeping: sessions.items.filter((item) => sleepingPhases.includes(item.phase)).length,
  attention: sessions.items.filter(needsAttention).length,
}))

function isWorking(session: Session): boolean {
  return session.phaseReason === 'AgentWorking'
}

function needsAttention(session: Session): boolean {
  return session.phase === 'Failed' || attentionReasons.includes(session.phaseReason || '')
}

function userState(session: Session): 'working' | 'attention' | 'normal' {
  if (needsAttention(session)) return 'attention'
  if (isWorking(session)) return 'working'
  return 'normal'
}

function sessionLabel(session: Session): string {
  switch (session.phaseReason) {
    case 'AgentWorking':
      return 'AI 工作中'
    case 'AwaitingApproval':
      return '等待批准'
    case 'AwaitingChoice':
      return '等待选择'
    case 'AwaitingInput':
      return '等待指令'
    default:
      return phaseLabels[session.phase]
  }
}

function sessionDescription(session: Session): string {
  switch (session.phaseReason) {
    case 'AgentWorking':
      return 'Claude 正在执行任务，暂时无需操作'
    case 'AwaitingApproval':
      return 'Claude 正在等待你批准一项操作'
    case 'AwaitingChoice':
      return 'Claude 正在等待你选择下一步方案'
    case 'AwaitingInput':
      return 'Claude 已完成当前回复，等待你的下一步指令'
    default:
      return phaseDescriptions[session.phase]
  }
}

const visibleSessions = computed(() => {
  const keyword = query.value.trim().toLocaleLowerCase('zh-CN')
  return sessions.items.filter((session) => {
    const matchesQuery =
      !keyword ||
      session.name.toLocaleLowerCase('zh-CN').includes(keyword) ||
      session.id.toLowerCase().includes(keyword)
    const matchesPhase =
      phaseFilter.value === 'all' ||
      (phaseFilter.value === 'working' && isWorking(session)) ||
      (phaseFilter.value === 'sleeping' && sleepingPhases.includes(session.phase)) ||
      (phaseFilter.value === 'attention' && needsAttention(session))
    return matchesQuery && matchesPhase
  })
})

function relativeTime(raw?: string): string {
  if (!raw) return '尚无活动'
  const elapsed = Date.now() - new Date(raw).getTime()
  const minutes = Math.max(0, Math.floor(elapsed / 60000))
  if (minutes < 1) return '刚刚活跃'
  if (minutes < 60) return `${minutes} 分钟前活跃`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前活跃`
  return `${Math.floor(hours / 24)} 天前活跃`
}

async function load(silent = false): Promise<void> {
  try {
    if (auth.accessToken) await sessions.load(auth.accessToken, 1, silent)
    error.value = ''
  } catch {
    error.value = '会话加载失败，请检查网络后重试。'
  }
}
onMounted(async () => {
  await load()
  poller = globalThis.setInterval(() => void load(true), 5000)
})
onUnmounted(() => poller && globalThis.clearInterval(poller))
</script>

<template>
  <AppShell>
    <div class="page-heading">
      <div>
        <p class="eyebrow">WORKSPACES</p>
        <h1>会话中心</h1>
        <p>集中查看 Agent 状态，在任务之间快速切换。</p>
      </div>
      <RouterLink class="button primary" to="/sessions/new">创建会话</RouterLink>
    </div>
    <StatusPanel v-if="sessions.loading" kind="loading" message="正在加载会话…" />
    <StatusPanel v-else-if="error" kind="error" :message="error"
      ><button @click="load()">重试</button></StatusPanel
    >
    <StatusPanel
      v-else-if="sessions.items.length === 0"
      kind="empty"
      message="还没有会话。创建第一个工作区开始使用 Bosun。"
    >
      <RouterLink class="button primary" to="/sessions/new">创建会话</RouterLink>
    </StatusPanel>
    <template v-else>
      <section class="session-summary" aria-label="会话概览">
        <button :class="{ selected: phaseFilter === 'all' }" @click="phaseFilter = 'all'">
          <span>全部会话</span><strong>{{ counts.total }}</strong>
        </button>
        <button :class="{ selected: phaseFilter === 'working' }" @click="phaseFilter = 'working'">
          <span>暂不关注</span><strong>{{ counts.working }}</strong>
        </button>
        <button
          :class="{ selected: phaseFilter === 'attention' }"
          @click="phaseFilter = 'attention'"
        >
          <span>需处理</span><strong>{{ counts.attention }}</strong>
        </button>
        <button :class="{ selected: phaseFilter === 'sleeping' }" @click="phaseFilter = 'sleeping'">
          <span>休眠/归档</span><strong>{{ counts.sleeping }}</strong>
        </button>
      </section>
      <div class="session-toolbar">
        <label class="session-search">
          <span aria-hidden="true">⌕</span>
          <input v-model="query" type="search" placeholder="搜索会话名称或 ID" />
        </label>
        <span>每 5 秒自动更新状态</span>
      </div>
      <StatusPanel
        v-if="visibleSessions.length === 0"
        kind="empty"
        message="没有符合当前筛选条件的会话。"
      />
      <div v-else class="session-list">
        <RouterLink
          v-for="session in visibleSessions"
          :key="session.id"
          class="card session-card"
          :to="`/sessions/${session.id}`"
        >
          <div class="session-card-main">
            <div class="session-card-title">
              <span
                class="phase-dot"
                :data-phase="session.phase"
                :data-user-state="userState(session)"
                aria-hidden="true"
              ></span>
              <div>
                <h2>{{ session.name }}</h2>
                <p>{{ sessionDescription(session) }}</p>
              </div>
            </div>
            <span class="phase" :data-phase="session.phase" :data-user-state="userState(session)">{{
              sessionLabel(session)
            }}</span>
          </div>
          <div class="session-card-meta">
            <span>{{
              session.tier === 'medium' ? 'Medium · 500m / 1Gi' : 'Small · 250m / 512Mi'
            }}</span>
            <span>{{ relativeTime(session.lastActiveAt || session.createdAt) }}</span>
            <span class="mono">#{{ session.id.slice(0, 8) }}</span>
            <strong aria-hidden="true">打开 →</strong>
          </div>
        </RouterLink>
      </div>
    </template>
  </AppShell>
</template>
