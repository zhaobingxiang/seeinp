<template>
  <Layout>
    <!-- 授权码被重置：提示重新绑定 -->
    <el-alert v-if="status.authCodeReset" type="error" :closable="false" class="rebind-alert"
      title="seeinpm 已重置授权码，当前连接已断开"
      description="代理数据已保留，输入新授权码绑定后即可自动恢复全部代理。"
      show-icon />

    <QuotaCard />

    <!-- 连接状态横幅 -->
    <div class="conn-banner">
      <div class="conn-left">
        <span class="conn-dot" :class="{ offline: status.status !== 'connected' }"></span>
        <div>
          <div class="conn-title">{{ status.status === 'connected' ? '连接正常' : '未连接' }}</div>
          <div class="conn-desc">授权码绑定 {{ status.username || '-' }} · 版本 {{ status.version || '-' }}</div>
        </div>
      </div>
      <el-button type="primary" size="small" @click="router.push('/ps/proxies')">查看代理</el-button>
    </div>

    <!-- 重新绑定授权码 -->
    <div v-if="status.authCodeReset" class="rebind-panel">
      <el-input v-model="rebindCode" type="password" show-password
        placeholder="请输入 seeinpm 下发的新授权码" @keyup.enter="handleRebind" style="max-width:340px" />
      <el-button type="primary" :loading="rebinding" @click="handleRebind">绑定并重连</el-button>
    </div>

    <!-- 统计卡片 -->
    <div class="stat-grid">
      <div class="stat-card">
        <div class="stat-value">{{ proxies.length }}</div>
        <div class="stat-label">代理总数</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{{ allocatedPorts }}<span class="stat-total"> / {{ proxies.length }}</span></div>
        <div class="stat-label">已分配端口</div>
      </div>
      <div class="stat-card">
        <div class="stat-value" style="font-size:22px">{{ lastPingText }}</div>
        <div class="stat-label">最后心跳</div>
      </div>
    </div>

    <!-- 代理列表 -->
    <div class="page-card">
      <div class="card-header" style="padding:14px 16px;border-bottom:1px solid var(--app-border-light)">
        <span>代理列表</span>
        <span class="link-all" @click="router.push('/ps/proxies')">管理 →</span>
      </div>
      <div style="padding:8px 16px 14px">
        <div v-if="proxies.length === 0" class="empty-state">
          <el-empty description="暂无代理">
            <el-button type="primary" @click="router.push('/ps/proxies')">添加代理</el-button>
          </el-empty>
        </div>
        <el-table v-else :data="proxies" style="width:100%">
          <el-table-column prop="id" label="名称" min-width="130">
            <template #default="{ row }"><span style="font-weight:500">{{ row.id }}</span></template>
          </el-table-column>
          <el-table-column label="类型" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="row.type === 'ops_http' ? 'warning' : 'primary'">
                {{ row.type === 'tcp' ? 'TCP' : row.type === 'udp' ? 'UDP' : '运维HTTP' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="本地" min-width="170">
            <template #default="{ row }">
              <span v-if="row.type === 'tcp' || row.type === 'udp'" style="font-family:var(--font-mono,monospace);font-size:13px">{{ row.localAddr }}:{{ row.localPort }}</span>
              <span v-else>账号 {{ row.proxyUsername }}</span>
            </template>
          </el-table-column>
          <el-table-column label="转发端口" min-width="110">
            <template #default="{ row }">
              <span style="font-family:var(--font-mono,monospace);color:#2563eb">{{ row.forwardPort || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default>
              <el-tag size="small" type="success">在线</el-tag>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>
  </Layout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { proxyApi, statusApi, authApi } from '@/api'
import Layout from '@/components/Layout.vue'
import QuotaCard from '@/components/QuotaCard.vue'

const router = useRouter()
const proxies = ref<any[]>([])
const rebindCode = ref('')
const rebinding = ref(false)
let pollTimer: any = null
let loadingHandler: any = null  // “正在绑定”持续提示，得到最终结果后关闭
let bindTimeout: any = null
let refreshTimer: any = null

// 绑定等待上限：超时仍未恢复则按“授权码未通过验证”处理
const REBIND_TIMEOUT_MS = 15000

const status = reactive({
  status: 'disconnected',
  username: '',
  sessionId: '',
  serverAddr: '',
  lastPing: 0,
  authCodeReset: false,
  version: ''
})

const allocatedPorts = computed(() => proxies.value.filter((p: any) => p.forwardPort).length)
const lastPingText = computed(() => {
  if (status.status !== 'connected' || !status.lastPing) return '-'
  const diff = Math.floor(Date.now() / 1000) - status.lastPing
  if (diff < 10) return '刚刚'
  if (diff < 60) return `${diff} 秒前`
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  return new Date(status.lastPing * 1000).toLocaleTimeString()
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
  loadingHandler = ElMessage({
    message: '正在绑定并重连，请稍候...',
    type: 'info',
    duration: 0
  })
  try {
    const res: any = await authApi.rebind({ authCode: rebindCode.value.trim() })
    if (res.code === 0) {
      rebindCode.value = ''
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

onMounted(() => {
  loadData()
  refreshTimer = setInterval(loadData, 10000)
})
onUnmounted(() => {
  stopBindingWatch()
  if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null }
})
</script>

<style scoped>
.rebind-alert { margin-bottom: 16px; }
.rebind-panel { display: flex; gap: 10px; margin-bottom: 16px; }
.empty-state { padding: 24px 0; }

.conn-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--app-card);
  border: 1px solid var(--app-border-light);
  border-radius: var(--app-radius);
  padding: 16px 20px;
  margin-bottom: 16px;
  box-shadow: var(--app-shadow);
}
.conn-left { display: flex; align-items: center; gap: 14px; }
.conn-dot { width: 12px; height: 12px; border-radius: 50%; background: #10b981; box-shadow: 0 0 0 4px #E1F5EE; flex-shrink: 0; }
.conn-dot.offline { background: #9ca3af; box-shadow: 0 0 0 4px #f0f1f3; }
.conn-title { font-size: 15px; font-weight: 500; color: var(--app-text); }
.conn-desc { font-size: 12px; color: var(--app-text-secondary); margin-top: 2px; }

.stat-total { font-size: 15px; color: var(--app-text-tertiary); font-weight: 400; }
.link-all { font-size: 12px; color: #2563eb; cursor: pointer; font-weight: 400; }
.link-all:hover { color: var(--app-primary-hover); }
</style>
