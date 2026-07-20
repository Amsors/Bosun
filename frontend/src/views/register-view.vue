<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'

import { register } from '../api/auth'
import { ApiError } from '../api/client'

const router = useRouter()
const email = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')
const idempotencyKey = globalThis.crypto.randomUUID()

async function submit(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    await register(email.value, password.value, idempotencyKey)
    await router.push({ name: 'login', query: { registered: '1' } })
  } catch (cause) {
    error.value =
      cause instanceof ApiError && cause.code === 10001
        ? '请检查邮箱格式，密码至少需要 8 个字符。'
        : '账号创建失败，请稍后重试。'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="auth-layout compact">
    <form class="card auth-card" @submit.prevent="submit">
      <p class="eyebrow">GET STARTED</p>
      <h1>创建账号</h1>
      <label>邮箱<input v-model="email" type="email" autocomplete="email" required /></label>
      <label
        >密码<input
          v-model="password"
          type="password"
          autocomplete="new-password"
          minlength="8"
          required
      /></label>
      <p v-if="error" class="alert" role="alert">{{ error }}</p>
      <button class="primary" type="submit" :disabled="busy">
        {{ busy ? '创建中…' : '创建账号' }}
      </button>
      <RouterLink to="/login">返回登录</RouterLink>
    </form>
  </main>
</template>
