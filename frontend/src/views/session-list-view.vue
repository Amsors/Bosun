<script setup lang="ts">
import { onMounted, ref } from 'vue'

import AppShell from '../components/app-shell.vue'
import StatusPanel from '../components/status-panel.vue'
import { useAuthStore } from '../stores/auth-store'
import { useSessionStore } from '../stores/session-store'

const auth = useAuthStore()
const sessions = useSessionStore()
const error = ref('')

async function load(): Promise<void> {
  error.value = ''
  try {
    if (auth.accessToken) await sessions.load(auth.accessToken)
  } catch {
    error.value = '会话加载失败，请检查网络后重试。'
  }
}
onMounted(load)
</script>

<template>
  <AppShell>
    <div class="page-heading">
      <div>
        <p class="eyebrow">WORKSPACES</p>
        <h1>会话</h1>
        <p>创建并管理隔离的 coding agent 工作区。</p>
      </div>
      <RouterLink class="button primary" to="/sessions/new">创建会话</RouterLink>
    </div>
    <StatusPanel v-if="sessions.loading" kind="loading" message="正在加载会话…" />
    <StatusPanel v-else-if="error" kind="error" :message="error"
      ><button @click="load">重试</button></StatusPanel
    >
    <StatusPanel
      v-else-if="sessions.items.length === 0"
      kind="empty"
      message="还没有会话。创建第一个工作区开始使用 Bosun。"
    >
      <RouterLink class="button primary" to="/sessions/new">创建会话</RouterLink>
    </StatusPanel>
    <div v-else class="session-grid">
      <RouterLink
        v-for="session in sessions.items"
        :key="session.id"
        class="card session-card"
        :to="`/sessions/${session.id}`"
      >
        <span class="phase" :data-phase="session.phase">{{ session.phase }}</span>
        <h2>{{ session.runtime }}</h2>
        <p>{{ session.tier }} · {{ new Date(session.createdAt).toLocaleString('zh-CN') }}</p>
      </RouterLink>
    </div>
  </AppShell>
</template>
