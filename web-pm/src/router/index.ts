import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/pm/login',
    name: 'Login',
    component: () => import('@/views/Login.vue')
  },
  {
    path: '/pm/dashboard',
    name: 'Dashboard',
    component: () => import('@/views/Dashboard.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/pm/users',
    name: 'Users',
    component: () => import('@/views/Users.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/pm/ports',
    name: 'Ports',
    component: () => import('@/views/Ports.vue'),
    meta: { requiresAuth: true }
  },
  {
    path: '/',
    redirect: '/pm/login'
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to, from, next) => {
  const token = localStorage.getItem('pm_token')
  if (to.meta.requiresAuth && !token) {
    next('/pm/login')
  } else if (to.path === '/pm/login' && token) {
    next('/pm/dashboard')
  } else {
    next()
  }
})

export default router