import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  {
    path: '/ps/login',
    name: 'Login',
    component: () => import('@/views/Login.vue'),
    meta: { requiresAuth: false }
  },
  {
    path: '/ps/dashboard',
    name: 'Dashboard',
    component: () => import('@/views/Dashboard.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/ps/proxies',
    name: 'Proxies',
    component: () => import('@/views/Proxies.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/',
    redirect: '/ps/login'
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to, from, next) => {
  const token = localStorage.getItem('token')
  if (to.meta.requiresAuth && !token) {
    next('/ps/login')
  } else if (to.path === '/ps/login' && token) {
    next('/ps/dashboard')
  } else {
    next()
  }
})

export default router
