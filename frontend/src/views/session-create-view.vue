<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { sessionApi } from '../api/sessions'
import AppShell from '../components/app-shell.vue'
import { useAuthStore } from '../stores/auth-store'
import type { SessionTier } from '../api/contracts'

const auth = useAuthStore()
const router = useRouter()
const tier = ref<SessionTier>('small')
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
        ? '当前集群容量不足，请稍后重试。'
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
