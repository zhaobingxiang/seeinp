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
      localStorage.removeItem('pm_token')
      window.location.href = '/pm/login'
    }
    return Promise.reject(error)
  }
)

export const authApi = {
  init: (data: any) => api.post('/auth/init', data),
  login: (data: any) => api.post('/auth/login', data)
}

export const userApi = {
  list: () => api.get('/users'),
  create: (data: any) => api.post('/users', data),
  update: (username: string, data: any) => api.put('/users/' + encodeURIComponent(username), data),
  delete: (username: string) => api.delete('/users?username=' + username),
  disable: (username: string, disconnectNow: boolean) => api.post(`/users/${username}/disable`, { disconnectNow }),
  enable: (username: string) => api.post(`/users/${username}/enable`),
  resetCode: (username: string) => api.post(`/users/${username}/reset-code`)
}

export const portApi = {
  getPool: () => api.get('/port-pool'),
  updatePool: (ranges: { start: number; end: number }[], action: string) => api.put('/port-pool', { ranges, action })
}

export const proxyApi = {
  list: () => api.get('/proxies'),
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
export const versionApi = {
  list: (endpoint: string) => api.get('/versions', { params: { endpoint } }),
  upload: (formData: FormData, onProgress?: (percent: number) => void) => api.post('/versions', formData, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 0,
    onUploadProgress: (e: any) => { if (e.total) onProgress?.(Math.round((e.loaded / e.total) * 100)) }
  }),
  remove: (id: number) => api.delete('/versions/' + id)
}

export default api
