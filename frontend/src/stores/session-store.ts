import { defineStore } from 'pinia'
import { ref } from 'vue'

import type { Session } from '../api/contracts'

export const useSessionStore = defineStore('sessions', () => {
  const items = ref<Session[]>([])
  const total = ref(0)

  function replace(sessions: Session[], nextTotal: number): void {
    items.value = sessions
    total.value = nextTotal
  }

  return { items, total, replace }
})
