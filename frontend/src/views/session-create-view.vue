<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { sessionApi } from '../api/sessions'
import AppShell from '../components/app-shell.vue'
import { useAuthStore } from '../stores/auth-store'
import type { SessionPriority } from '../api/contracts'

const auth = useAuthStore()
const router = useRouter()
const name = ref('')
const priority = ref<SessionPriority>('normal')
const memoryGiB = ref(2)
const busy = ref(false)
const error = ref('')
const idempotencyKey = globalThis.crypto.randomUUID()

async function create(): Promise<void> {
  if (!auth.accessToken) return
  busy.value = true
  error.value = ''
  try {
    const session = await sessionApi(auth.accessToken).create(
      {
        name: name.value.trim(),
        priority: priority.value,
        memoryRequest: `${memoryGiB.value}Gi`,
        runtime: 'claude-code',
        provider: { mode: 'platform' },
        storagePolicy: 'local',
      },
      idempotencyKey,
    )
    await router.push(`/sessions/${session.id}`)
  } catch (cause) {
    const code = cause instanceof ApiError ? cause.code : 0
    error.value =
      code === 30003
        ? '最多保留 20 个会话，请先删除不再需要的会话。'
        : code === 30006
          ? '用户环境尚未就绪，请稍后重试。'
          : '创建失败，请稍后重试。'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AppShell>
    <div class="narrow">
      <RouterLink to="/sessions">← 返回会话</RouterLink>
      <div class="page-heading">
        <div>
          <p class="eyebrow">NEW WORKSPACE</p>
          <h1>创建会话</h1>
        </div>
      </div>
      <form class="card create-form" @submit.prevent="create">
        <label class="field-label" for="session-name">
          <span>会话名称</span>
          <small>{{ name.length }}/80</small>
        </label>
        <input
          id="session-name"
          v-model="name"
          maxlength="80"
          required
          autofocus
          autocomplete="off"
          placeholder="例如：课程项目后端优化"
        />
        <p class="field-help">使用任务或项目名称，方便之后快速找到这个工作区。</p>
        <fieldset>
          <legend>调度优先级</legend>
          <label class="priority-option"
            ><input v-model="priority" type="radio" value="high" /><span
              ><strong>高优先级</strong
              ><small>资源紧张时最先启动，适合紧急或演示任务。</small></span
            ></label
          >
          <label class="priority-option"
            ><input v-model="priority" type="radio" value="normal" /><span
              ><strong>普通优先级</strong><small>默认选择，适合日常开发任务。</small></span
            ></label
          >
          <label class="priority-option"
            ><input v-model="priority" type="radio" value="low" /><span
              ><strong>低优先级</strong
              ><small>资源不足时继续排队，适合后台或非紧急任务。</small></span
            ></label
          >
          <p class="field-help">优先级只影响等待队列顺序，不会中断已经运行的其他会话。</p>
        </fieldset>
        <fieldset class="resource-settings">
          <legend>资源设置</legend>
          <div class="resource-setting-row">
            <div>
              <strong>CPU</strong>
              <small>初始 0.5 核，运行后由平台根据负载自动调度</small>
            </div>
            <span class="resource-setting-value">0.5 核起</span>
          </div>
          <div class="resource-setting-row">
            <div>
              <label for="memory-request"><strong>内存</strong></label>
              <small>用于 Kubernetes 调度，创建后保持固定</small>
            </div>
            <div class="memory-request-control">
              <input
                id="memory-request"
                v-model.number="memoryGiB"
                type="number"
                min="1"
                max="64"
                step="1"
                required
                aria-describedby="memory-request-help"
              />
              <span>GiB</span>
            </div>
          </div>
          <p id="memory-request-help" class="field-help">
            Agent 容器的 memory request 与 limit
            均设为该值；可利用不同节点的可用内存间接控制调度位置。
          </p>
        </fieldset>
        <p v-if="error" class="alert" role="alert">{{ error }}</p>
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '正在创建…' : '创建会话' }}
        </button>
      </form>
    </div>
  </AppShell>
</template>
