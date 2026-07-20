import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import * as authApi from '../api/auth'
import type { User } from '../api/contracts'

export const useAuthStore = defineStore('auth', () => {
  const accessToken = ref<string | null>(null)
  const user = ref<User | null>(null)
  const authenticated = computed(() => accessToken.value !== null)

  async function login(email: string, password: string): Promise<void> {
    const data = await authApi.login(email, password)
    accessToken.value = data.accessToken
    user.value = data.user
  }

  async function refresh(): Promise<string | null> {
    try {
      const data = await authApi.refresh()
      accessToken.value = data.accessToken
      user.value = data.user
      return data.accessToken
    } catch {
      clear()
      return null
    }
  }

  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } finally {
      clear()
    }
  }

  function clear(): void {
    accessToken.value = null
    user.value = null
  }

  return { accessToken, user, authenticated, login, refresh, logout, clear }
})
