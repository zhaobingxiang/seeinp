import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/pm/login',
    name: 'Login',
    component: () => import('@/views/Login.vue'),
    meta: { title: '登录' }
  },
  {
    path: '/pm/dashboard',
    name: 'Dashboard',
    component: () => import('@/views/Dashboard.vue'),
    meta: { requiresAuth: true, title: '仪表盘' }
  },
  {
    path: '/pm/users',
    name: 'Users',
    component: () => import('@/views/Users.vue'),
    meta: { requiresAuth: true, title: '用户管理' }
  },
  {
    path: '/pm/proxies',
    name: 'Proxies',
    component: () => import('@/views/Proxies.vue'),
    meta: { requiresAuth: true, title: '代理管理' }
  },
  {
    path: '/pm/audit-logs',
    name: 'AuditLogs',
    component: () => import('@/views/AuditLogs.vue'),
    meta: { requiresAuth: true, title: '审计日志' }
  },
  {
    path: '/pm/system-logs',
    name: 'SystemLogs',
    component: () => import('@/views/SystemLogs.vue'),
    meta: { requiresAuth: true, title: '系统日志' }
  },
  {
    path: '/pm/ports',
    name: 'Ports',
    component: () => import('@/views/Ports.vue'),
    meta: { requiresAuth: true, title: '端口池' }
  },
  {
    path: '/pm/versions',
    name: 'Versions',
    component: () => import('@/views/Versions.vue'),
    meta: { requiresAuth: true, title: '版本管理' }
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

router.afterEach((to) => {
  const title = (to.meta.title as string) || 'seeinpm'
  document.title = `${title} - seeinpm`
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
