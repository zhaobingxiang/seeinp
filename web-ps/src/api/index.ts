import axios from 'axios'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 30000
})

api.interceptors.request.use(
  (config) => {
    const token = localStorage.getItem('token')
    if (token) {
      config.headers.Authorization = 'Bearer ' + token
    }
    return config
  },
  (error) => Promise.reject(error)
)

api.interceptors.response.use(
  (response) => response.data,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token')
      window.location.href = '/ps/login'
    }
    return Promise.reject(error)
  }
)

export const authApi = {
  login: (data: any) => api.post('/auth/login', data),
  init: (data: any) => api.post('/auth/init', data),
  getStatus: () => api.get('/auth/status'),
  rebind: (data: any) => api.post('/auth/rebind', data)
}

export const proxyApi = {
  list: () => api.get('/proxies'),
  create: (data: any) => api.post('/proxies', data),
  update: (id: string, data: any) => api.patch('/proxies/' + id, data),
  delete: (id: string) => api.delete('/proxies/' + id)
}

// 版本管理（B 端自主升级）：大包上传不设超时，进度经 onProgress 反馈
export const versionApi = {
  status: () => api.get('/self-upgrade/status', { timeout: 10000 }),
  selfUpload: (formData: FormData, onProgress?: (percent: number) => void) => api.post('/self-upgrade', formData, {
    headers: { 'Content-Type': 'multipart/form-data' },
    timeout: 0,
    onUploadProgress: (e: any) => { if (e.total) onProgress?.(Math.round((e.loaded / e.total) * 100)) }
  }),
  pmVersions: () => api.get('/pm-versions', { timeout: 20000 }),
  pmUpgrade: (version: string) => api.post('/pm-upgrade', { version }, { timeout: 30000 })
}

export const statusApi = {
  getStatus: () => api.get('/status'),
  getStats: () => api.get('/stats')
}

export const auditApi = {
  list: (params: { username?: string; action?: string; keyword?: string; start_time?: number; end_time?: number; page?: number; page_size?: number }) => api.get('/audit-logs', { params })
}

export const logApi = {
  listFiles: () => api.get('/logs'),
  content: (file: string, lines: number, keyword?: string, download?: boolean) => api.get('/logs/content', { params: { file, lines, keyword, download } })
}

export default api
