<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { sessionApi } from '../api/sessions'
import AppShell from '../components/app-shell.vue'
import { useAuthStore } from '../stores/auth-store'
import type { SessionPriority, SessionTier } from '../api/contracts'

const auth = useAuthStore()
const router = useRouter()
const name = ref('')
const tier = ref<SessionTier>('small')
const priority = ref<SessionPriority>('normal')
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
        tier: tier.value,
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
          <label class="tier priority-option"
            ><input v-model="priority" type="radio" value="high" /><span
              ><strong>高优先级</strong
              ><small>资源紧张时最先启动，适合紧急或演示任务。</small></span
            ></label
          >
          <label class="tier priority-option"
            ><input v-model="priority" type="radio" value="normal" /><span
              ><strong>普通优先级</strong><small>默认选择，适合日常开发任务。</small></span
            ></label
          >
          <label class="tier priority-option"
            ><input v-model="priority" type="radio" value="low" /><span
              ><strong>低优先级</strong
              ><small>资源不足时继续排队，适合后台或非紧急任务。</small></span
            ></label
          >
          <p class="field-help">优先级只影响等待队列顺序，不会中断已经运行的其他会话。</p>
        </fieldset>
        <fieldset>
          <legend>资源档位</legend>
          <label class="tier"
            ><input v-model="tier" type="radio" value="small" /><span
              ><strong>Small</strong><small>轻量脚本与日常项目 · 250m CPU / 512Mi</small></span
            ></label
          >
          <label class="tier"
            ><input v-model="tier" type="radio" value="medium" /><span
              ><strong>Medium</strong><small>编译型项目 · 500m CPU / 1Gi</small></span
            ></label
          >
        </fieldset>
        <p v-if="error" class="alert" role="alert">{{ error }}</p>
        <button class="primary" type="submit" :disabled="busy">
          {{ busy ? '正在创建…' : '创建会话' }}
        </button>
      </form>
    </div>
  </AppShell>
</template>
