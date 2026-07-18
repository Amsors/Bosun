import { createRouter, createWebHistory } from 'vue-router'

import HomeView from '../views/home-view.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [{ path: '/', name: 'home', component: HomeView }],
})
