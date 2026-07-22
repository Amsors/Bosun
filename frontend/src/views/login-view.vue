<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { ApiError } from '../api/client'
import { useAuthStore } from '../stores/auth-store'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const identifier = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

async function submit(): Promise<void> {
  busy.value = true
  error.value = ''
  try {
    await auth.login(identifier.value, password.value)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/sessions'
    await router.push(redirect)
  } catch (cause) {
    error.value =
      cause instanceof ApiError && cause.code === 20003
        ? '登录尝试过多，请稍后再试。'
        : '用户名或密码不正确。'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="auth-layout">
    <section class="auth-intro">
      <p class="eyebrow">BOSUN CLOUD WORKSPACE</p>
      <h1>让 coding agent<br />随时待命。</h1>
      <p>隔离运行、跨区调度、浏览器终端。工作区在你离开后依然安全保留。</p>
    </section>
    <form class="card auth-card" @submit.prevent="submit">
      <h2>登录 Bosun</h2>
      <label
        >用户名或邮箱<input
          v-model="identifier"
          type="text"
          pattern="(?:[aA][dD][mM][iI][nN]|[^@\s]+@[^@\s]+)"
          title="请输入 admin 或有效邮箱"
          autocomplete="username"
          required
      /></label>
      <label
        >密码<input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          minlength="8"
      /></label>
      <p v-if="error" class="alert" role="alert">{{ error }}</p>
      <button class="primary" type="submit" :disabled="busy">
        {{ busy ? '登录中…' : '登录' }}
      </button>
      <p>还没有账号？<RouterLink to="/register">创建账号</RouterLink></p>
    </form>
  </main>
</template>
