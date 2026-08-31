// 部署工具后端 JSON API 与 SSE 客户端封装
// 后端基址：与前端同源（go:embed + SPAHandler 同一 http.Server）

export interface ApiResp<T> {
  code: number
  message: string
  data: T
}

export interface ServerConfig {
  serverAddr: string
  port: number
  controlPort: number
}

export interface TargetInfo {
  kind: 'windows' | 'linux'
  host: string
  sshPort: number
  username: string
  rootPass: string
  password: string
}

export interface ValidateReq {
  serverAddr: string
  username: string
  authCode: string
}

async function req<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined
  })
  const json = (await resp.json().catch(() => ({}))) as ApiResp<T>
  if (json.code !== 0) {
    throw new Error(json.message || `请求失败 (HTTP ${resp.status})`)
  }
  return json.data
}

export const api = {
  // 读取/保存 seeinpm 服务器参数（"服务器参数配置"折叠区）
  getServerConfig: () => req<ServerConfig>('/api/v1/config/server', 'GET'),
  saveServerConfig: (cfg: { serverAddr: string; port: number }) =>
    req<ServerConfig>('/api/v1/config/server', 'POST', cfg),

  // 校验 seeinpm 用户名+授权码
  validateAuth: (body: ValidateReq) =>
    req<{ serverAddr: string; validated: boolean }>('/api/v1/deploy/validate-auth', 'POST', body),

  // 某平台可用版本列表
  versionList: (body: { serverAddr: string; username: string; authCode: string; goos: string; goarch: string }) =>
    req<Array<{ version: string; note: string; fileSize: number; sha256: string; createdAt: number }>>(
      '/api/v1/deploy/versions', 'POST', body
    ),

  // 检测目标是否已安装
  status: (body: { target: TargetInfo }) =>
    req<{ installed: boolean; detail: string }>('/api/v1/deploy/status', 'POST', body),

  // 卸载
  uninstall: (body: { target: TargetInfo }) =>
    req<{ message: string }>('/api/v1/deploy/uninstall', 'POST', body),

  // 测试目标 Linux 服务器 SSH 连通性
  testSSH: (body: { target: TargetInfo }) =>
    req<{ ok: boolean; arch?: string; elapsedMs?: number; error?: string }>(
      '/api/v1/deploy/test-ssh', 'POST', body
    ),

  // seeinps 网页地址
  links: () => req<{ url: string }>('/api/v1/deploy/links', 'GET'),

  // 当前进程管理员权限状态
  privilege: () =>
    req<{ admin: boolean; kind: string; needElevate: boolean }>('/api/v1/deploy/privilege', 'GET'),

  // 触发 UAC 提权重启部署工具
  elevate: () => req<{ elevated: boolean }>('/api/v1/deploy/elevate', 'POST')
}

// ---- SSE 部署事件流 ----
export interface SSEHandlers {
  onLog: (level: 'info' | 'warn' | 'error' | 'success', msg: string) => void
  onProgress: (received: number, total: number) => void
  onConfirmUninstall: (msg: string) => void
  onSuccess: (msg: string, url: string) => void
  onError: (msg: string) => void
  onClose: () => void
}

export function startDeploySSE(body: object, handlers: SSEHandlers) {
  const controller = new AbortController()
  fetch('/api/v1/deploy/install', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal: controller.signal
  })
    .then(async (resp) => {
      if (!resp.ok || !resp.body) {
        throw new Error(`部署请求失败 (HTTP ${resp.status})`)
      }
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      // 按 \n\n 切分 SSE 块
      const dispatch = (block: string) => {
        const evMatch = block.match(/^event:\s*(\S+)/m)
        const dataMatch = block.match(/^data:\s*(.+)$/m)
        if (!dataMatch) return
        let data: any = {}
        try {
          data = JSON.parse(dataMatch[1])
        } catch {
          data = { message: dataMatch[1] }
        }
        const event = evMatch ? evMatch[1] : 'log'
        switch (event) {
          case 'log':
            handlers.onLog(data.level || 'info', data.message || '')
            break
          case 'progress':
            handlers.onProgress(data.received || 0, data.total || 0)
            break
          case 'confirm':
            handlers.onConfirmUninstall(data.message || '')
            break
          case 'success':
            handlers.onSuccess(data.message || '', data.url || '')
            break
          case 'error':
            handlers.onError(data.message || '部署失败')
            break
        }
      }
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        let idx: number
        while ((idx = buffer.indexOf('\n\n')) >= 0) {
          const block = buffer.slice(0, idx)
          buffer = buffer.slice(idx + 2)
          if (block.trim()) dispatch(block)
        }
      }
    })
    .catch((e) => {
      if (e instanceof DOMException && e.name === 'AbortError') return
      handlers.onError(e.message)
    })
    .finally(() => handlers.onClose())

  return () => controller.abort()
}