<template>
  <div class="dashboard-container">
    <!-- 授权码被重置：提示重新绑定 -->
    <el-alert v-if="status.authCodeReset" type="error" :closable="false" class="rebind-alert"
      title="seeinpm 已重置授权码，当前连接已断开"
      description="代理数据已保留，输入新授权码绑定后即可自动恢复全部代理。"
      show-icon />

    <!-- Status Bar -->
    <div class="status-bar">
      <div class="status-indicator">
        <div class="status-dot" :class="{ offline: status.status !== 'connected' }"></div>
        <span>{{ status.status === 'connected' ? '已连接' : '未连接' }}</span>
      </div>
      <div class="user-info">
        <span>{{ status.username || '未登录' }}</span>
        <el-button type="text" @click="handleLogout">退出</el-button>
      </div>
    </div>

    <!-- 重新绑定授权码 -->
    <div v-if="status.authCodeReset" class="rebind-panel">
      <el-input v-model="rebindCode" type="password" show-password
        placeholder="请输入 seeinpm 下发的新授权码" @keyup.enter="handleRebind" />
      <el-button type="primary" :loading="rebinding" @click="handleRebind">绑定并重连</el-button>
    </div>

    <!-- Stats Cards -->
    <div class="stat-cards">
      <div class="stat-card">
        <h3>代理数量</h3>
        <div class="value">{{ proxies.length }}</div>
      </div>
      <div class="stat-card">
        <h3>连接状态</h3>
        <div class="value" :style="{ color: status.status === 'connected' ? '#67c23a' : '#f56c6c' }">
          {{ status.status === 'connected' ? '在线' : '离线' }}
        </div>
      </div>
    </div>

    <!-- Proxy List -->
    <div class="section-header">
      <h3>代理列表</h3>
      <el-button type="primary" @click="router.push('/ps/proxies')">
        <el-icon><Plus /></el-icon>
        添加/管理代理
      </el-button>
    </div>

    <div v-if="proxies.length === 0" class="empty-state">
      <el-empty description="暂无代理">
        <el-button type="primary" @click="router.push('/ps/proxies')">添加代理</el-button>
      </el-empty>
    </div>

    <div v-else class="proxy-list">
      <div v-for="proxy in proxies" :key="proxy.id" class="proxy-card">
        <div class="proxy-info">
          <h4>{{ proxy.id }}</h4>
          <p v-if="proxy.type === 'tcp'">TCP → {{ proxy.localAddr }}:{{ proxy.localPort }}</p>
          <p v-else>运维代理(HTTP) · 账号 {{ proxy.proxyUsername }}</p>
          <p v-if="proxy.forwardPort" class="forward-port">
            <el-tag type="success" size="small">
              {{ proxy.type === 'ops_http' ? '运维ID' : '转发端口' }}: {{ proxy.forwardPort }}
            </el-tag>
          </p>
        </div>
        <div class="proxy-actions">
          <el-button type="primary" size="small" @click="router.push('/ps/proxies')">管理</el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { proxyApi, statusApi, authApi } from '@/api'

const router = useRouter()
const proxies = ref<any[]>([])
const rebindCode = ref('')
const rebinding = ref(false)
let pollTimer: any = null
let loadingHandler: any = null  // “正在绑定”持续提示，得到最终结果后关闭
let bindTimeout: any = null

// 绑定等待上限：超时仍未恢复则按“授权码未通过验证”处理
const REBIND_TIMEOUT_MS = 15000

const status = reactive({
  status: 'disconnected',
  username: '',
  sessionId: '',
  authCodeReset: false
})

const loadData = async () => {
  try {
    const [statusRes, proxiesRes]: any[] = await Promise.all([
      statusApi.getStatus(),
      proxyApi.list()
    ])
    if (statusRes.code === 0) Object.assign(status, statusRes.data)
    if (proxiesRes.code === 0) proxies.value = proxiesRes.data || []
  } catch (error) {
    console.error('Load data error:', error)
  }
}

// 清理绑定等待期的全部状态（轮询/超时/持续提示）
const stopBindingWatch = () => {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  if (bindTimeout) { clearTimeout(bindTimeout); bindTimeout = null }
  if (loadingHandler) { loadingHandler.close(); loadingHandler = null }
}

// 绑定等待期轮询：seeinps 注册成功（connected）才代表新授权码通过 seeinpm 验证
const checkReconnect = async () => {
  try {
    const res: any = await statusApi.getStatus()
    if (res.code === 0) {
      Object.assign(status, res.data)
      if (status.status === 'connected' && !status.authCodeReset) {
        // 重连成功：刷新代理列表（端口复用恢复转发端口）
        const proxiesRes: any = await proxyApi.list()
        if (proxiesRes.code === 0) proxies.value = proxiesRes.data || []
        const wasBinding = !!rebinding.value
        stopBindingWatch()
        if (wasBinding) {
          rebinding.value = false
          ElMessage.success('重连成功')
        }
      }
    }
  } catch (error) {
    console.error('Refresh status error:', error)
  }
}

const handleRebind = async () => {
  if (!rebindCode.value.trim()) {
    ElMessage.warning('请输入新授权码')
    return
  }
  if (rebinding.value) return
  rebinding.value = true
  // “更新中”提示：点击后一直显示，拿到最终结果（成功/失败/超时）才关闭
  loadingHandler = ElMessage({
    message: '正在绑定并重连，请稍候...',
    type: 'info',
    duration: 0
  })
  try {
    const res: any = await authApi.rebind({ authCode: rebindCode.value.trim() })
    if (res.code === 0) {
      rebindCode.value = ''
      // rebind 仅保存新授权码并触发后台重连，验证结果需轮询确认
      pollTimer = setInterval(checkReconnect, 1500)
      bindTimeout = setTimeout(() => {
        stopBindingWatch()
        rebinding.value = false
        ElMessage.error('绑定失败，授权码未通过 seeinpm 验证，请确认授权码是否正确')
      }, REBIND_TIMEOUT_MS)
    } else {
      stopBindingWatch()
      rebinding.value = false
      ElMessage.error(res.message || '绑定失败，请确认授权码是否正确')
    }
  } catch (error: any) {
    stopBindingWatch()
    rebinding.value = false
    ElMessage.error(error?.response?.data?.message || '绑定失败，请确认授权码是否正确')
  }
}

const handleLogout = () => {
  localStorage.removeItem('token')
  router.push('/ps/login')
}

onMounted(() => loadData())
onUnmounted(() => stopBindingWatch())
</script>

<style scoped>
.rebind-alert { margin-bottom: 15px; }
.rebind-panel { display: flex; gap: 10px; margin-bottom: 15px; }
.section-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 15px; }
.section-header h3 { color: #303133; font-size: 16px; }
.user-info { display: flex; align-items: center; gap: 15px; }
.empty-state { background: white; border-radius: 8px; padding: 40px; }
.forward-port { margin-top: 8px; }
</style>
