import { defineStore } from 'pinia'
import { ref } from 'vue'

import type { Session } from '../api/contracts'
import { sessionApi } from '../api/sessions'

export const useSessionStore = defineStore('sessions', () => {
  const items = ref<Session[]>([])
  const total = ref(0)
  const loading = ref(false)

  function replace(sessions: Session[], nextTotal: number): void {
    items.value = sessions
    total.value = nextTotal
  }

  async function load(token: string, page = 1, silent = false): Promise<void> {
    if (!silent) loading.value = true
    try {
      const data = await sessionApi(token).list(page)
      replace(data.items, data.total)
    } finally {
      if (!silent) loading.value = false
    }
  }

  return { items, total, loading, replace, load }
})
