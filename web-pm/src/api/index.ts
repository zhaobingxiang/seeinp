import axios from 'axios'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 30000
})

api.interceptors.request.use((config) => {
  const token = localStorage.getItem('pm_token')
  if (token) {
    config.headers.Authorization = 'Bearer ' + token
  }
  return config
})

api.interceptors.response.use(
  (response) => response.data,
  (error) => {
    if (error.response?.status === 401) {
      // 登录/初始化接口的 401 是"账号或密码错误"等业务失败：不跳转页面，
      // 由调用方用 ElMessage 弹出具体错误提示；已在登录页时同样不跳转
      const url: string = error.config?.url || ''
      const isAuthEndpoint = url.includes('/auth/login') || url.includes('/auth/init')
      const onLoginPage = window.location.pathname.startsWith('/pm/login')
      if (!isAuthEndpoint && !onLoginPage) {
        localStorage.removeItem('pm_token')
        window.location.href = '/pm/login'
      }
    }
    return Promise.reject(error)
  }
)

export const authApi = {
  init: (data: any) => api.post('/auth/init', data),
  login: (data: any) => api.post('/auth/login', data),
  captcha: () => api.get('/auth/captcha')
}

export const userApi = {
  list: () => api.get('/users'),
  create: (data: any) => api.post('/users', data),
  update: (username: string, data: any) => api.put('/users/' + encodeURIComponent(username), data),
  delete: (username: string) => api.delete('/users?username=' + username),
  disable: (username: string, disconnectNow: boolean) => api.post(`/users/${username}/disable`, { disconnectNow }),
  enable: (username: string) => api.post(`/users/${username}/enable`),
  resetCode: (username: string) => api.post(`/users/${username}/reset-code`),
  batchMoveGroup: (usernames: string[], groupId: number) => api.post('/users/batch-move-group', { usernames, groupId })
}

// 用户分组：树状，根分组不可删可改名，最多 5 层
export const userGroupApi = {
  list: () => api.get('/user-groups'),
  create: (data: { parentId: number; name: string }) => api.post('/user-groups', data),
  rename: (id: number, name: string) => api.put('/user-groups/' + id, { name }),
  remove: (id: number) => api.delete('/user-groups/' + id)
}

export const portApi = {
  getPool: () => api.get('/port-pool'),
  updatePool: (ranges: { start: number; end: number }[], action: string) => api.put('/port-pool', { ranges, action })
}

export const proxyApi = {
  list: () => api.get('/proxies'),
  cleanupOffline: () => api.post('/proxies/cleanup-offline'),
  sessions: (username: string, proxyId: string) => api.get(`/proxies/${encodeURIComponent(username)}/${encodeURIComponent(proxyId)}/sessions`),
  disable: (username: string, proxyId: string) => api.post(`/proxies/${encodeURIComponent(username)}/${encodeURIComponent(proxyId)}/disable`),
  enable: (username: string, proxyId: string) => api.post(`/proxies/${encodeURIComponent(username)}/${encodeURIComponent(proxyId)}/enable`)
}

export const auditApi = {
  list: (params: { username?: string; action?: string; keyword?: string; source?: string; start_time?: number; end_time?: number; page?: number; page_size?: number }) => api.get('/audit-logs', { params })
}

export const logApi = {
  listFiles: () => api.get('/logs'),
  content: (file: string, lines: number, keyword?: string, download?: boolean) => api.get('/logs/content', { params: { file, lines, keyword, download } })
}

// 系统日志：运行时查询/修改日志级别
export const loggingApi = {
  get: () => api.get('/logging'),
  set: (level: string) => api.put('/logging', { level })
}

// 混合架构：经控制通道按需拉取在线 seeinps 的运行日志（不落 PM 存储）
export const psLogApi = {
  listFiles: (username: string) => api.get('/ps-logs', { params: { username } }),
  content: (username: string, file: string, lines: number) => api.get('/ps-logs/content', { params: { username, file, lines } })
}

export const healthApi = {
  get: () => axios.get('/health').then(r => r.data)
}

export const clientApi = {
  list: () => api.get('/clients'),
  // 升级请求只等到"通知节点成功"即返回，传输进度经 upgrade-status 轮询
  upgrade: (username: string, versionId: number) => api.post('/clients/' + encodeURIComponent(username) + '/upgrade', { versionId }, { timeout: 30000 }),
  upgradeStatus: (username: string) => api.get('/clients/' + encodeURIComponent(username) + '/upgrade-status', { timeout: 10000 })
}

// 版本管理：大包在慢链路上传可能持续数十分钟，不设客户端超时，进度经 onProgress 回调反馈
// 不要显式设置 Content-Type：axios 在 FormData + 浏览器 XHR 下会自动添加带 boundary 的 multipart header；
// 显式写 "multipart/form-data"（无 boundary）会让浏览器原样发送，导致服务端 r.ParseMultipartForm 失败
export const versionApi = {
  list: (endpoint: string) => api.get('/versions', { params: { endpoint } }),
  upload: (formData: FormData, onProgress?: (percent: number) => void) => api.post('/versions', formData, {
    timeout: 0,
    onUploadProgress: (e: any) => { if (e.total) onProgress?.(Math.round((e.loaded / e.total) * 100)) }
  }),
  remove: (id: number) => api.delete('/versions/' + id)
}

export default api
