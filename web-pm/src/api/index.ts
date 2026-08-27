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
  delete: (username: string) => api.delete('/users?username=' + username),
  disable: (username: string, disconnectNow: boolean) => api.post(`/users/${username}/disable`, { disconnectNow }),
  enable: (username: string) => api.post(`/users/${username}/enable`),
  resetCode: (username: string) => api.post(`/users/${username}/reset-code`)
}

export const portApi = {
  getPool: () => api.get('/port-pool')
}

export const healthApi = {
  get: () => axios.get('/health').then(r => r.data)
}

export const clientApi = {
  list: () => api.get('/clients')
}

export default api
