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

export const statusApi = {
  getStatus: () => api.get('/status'),
  getStats: () => api.get('/stats')
}

export default api
