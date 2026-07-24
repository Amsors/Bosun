<script setup lang="ts">
import { useRouter } from 'vue-router'

import { useAuthStore } from '../stores/auth-store'

const auth = useAuthStore()
const router = useRouter()

async function signOut(): Promise<void> {
  await auth.logout()
  await router.push({ name: 'login' })
}
</script>

<template>
  <header class="topbar">
    <RouterLink class="brand" to="/sessions">Bosun</RouterLink>
    <nav aria-label="主导航">
      <RouterLink to="/admin">全局监控</RouterLink>
      <span v-if="auth.user">{{ auth.user.email }}</span>
      <button v-if="auth.authenticated" class="link-button" type="button" @click="signOut">
        退出
      </button>
      <RouterLink v-else to="/login">登录</RouterLink>
    </nav>
  </header>
  <main class="page"><slot /></main>
</template>
