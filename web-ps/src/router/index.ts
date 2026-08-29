import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/ps/login',
    name: 'Login',
    component: () => import('@/views/Login.vue'),
    meta: { title: '登录' }
  },
  {
    path: '/ps/dashboard',
    name: 'Dashboard',
    component: () => import('@/views/Dashboard.vue'),
    meta: { requiresAuth: true, title: '仪表盘' }
  },
  {
    path: '/ps/proxies',
    name: 'Proxies',
    component: () => import('@/views/Proxies.vue'),
    meta: { requiresAuth: true, title: '代理管理' }
  },
  {
    path: '/ps/audit-logs',
    name: 'AuditLogs',
    component: () => import('@/views/AuditLogs.vue'),
    meta: { requiresAuth: true, title: '审计日志' }
  },
  {
    path: '/ps/system-logs',
    name: 'SystemLogs',
    component: () => import('@/views/SystemLogs.vue'),
    meta: { requiresAuth: true, title: '系统日志' }
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

router.afterEach((to) => {
  const title = (to.meta.title as string) || 'seeinps'
  document.title = `${title} - seeinps`
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
