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
      <span>{{ auth.user?.email }}</span>
      <button class="link-button" type="button" @click="signOut">退出</button>
    </nav>
  </header>
  <main class="page"><slot /></main>
</template>
