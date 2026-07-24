import { createRouter, createWebHistory } from 'vue-router'

import { useAuthStore } from '../stores/auth-store'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/sessions' },
    {
      path: '/login',
      name: 'login',
      component: () => import('../views/login-view.vue'),
      meta: { public: true },
    },
    {
      path: '/register',
      name: 'register',
      component: () => import('../views/register-view.vue'),
      meta: { public: true },
    },
    {
      path: '/admin',
      name: 'admin',
      component: () => import('../views/admin-view.vue'),
      meta: { public: true },
    },
    {
      path: '/sessions',
      name: 'sessions',
      component: () => import('../views/session-list-view.vue'),
    },
    {
      path: '/sessions/new',
      name: 'session-create',
      component: () => import('../views/session-create-view.vue'),
    },
    {
      path: '/sessions/:id',
      name: 'session-detail',
      component: () => import('../views/session-detail-view.vue'),
    },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  if (to.meta.public) return auth.authenticated && to.name === 'login' ? { name: 'sessions' } : true
  if (auth.authenticated || (await auth.refresh())) return true
  return { name: 'login', query: { redirect: to.fullPath } }
})
