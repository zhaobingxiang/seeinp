<template>
  <Layout>
    <!-- 统计卡片 -->
    <div class="stat-grid">
      <div class="stat-card">
        <div class="stat-value">{{ health.clients || 0 }}</div>
        <div class="stat-label">在线客户端</div>
        <div class="stat-sub">控制通道连接数</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{{ onlineCount }}<span class="stat-total"> / {{ proxies.length }}</span></div>
        <div class="stat-label">在线代理</div>
        <div class="stat-sub">{{ enabledCount }} 个启用</div>
      </div>
      <div class="stat-card">
        <div class="stat-value traffic-value">{{ formatBytes(totalTraffic) }}</div>
        <div class="stat-label">累计流量</div>
        <div class="stat-sub">下行 {{ formatBytes(totalIn) }} · 上行 {{ formatBytes(totalOut) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">{{ health.ports?.used || 0 }}<span class="stat-total"> / {{ health.ports?.total || 0 }}</span></div>
        <div class="stat-label">端口使用</div>
        <div class="stat-sub">端口池 {{ poolText }}</div>
      </div>
    </div>

    <div class="dash-grid">
      <!-- 实时流量 Top -->
      <div class="page-card">
        <div class="card-header" style="padding:14px 16px;border-bottom:1px solid var(--app-border-light)">
          <span>实时流量 Top</span>
          <div class="header-actions">
            <span class="refresh-label">自动刷新</span>
            <el-select v-model="refreshInterval" size="small" style="width:86px" @change="restartTimer">
              <el-option v-for="iv in refreshOptions" :key="iv" :label="iv + ' 秒'" :value="iv" />
            </el-select>
          </div>
        </div>
        <el-table :data="topProxies" v-loading="loadingProxies" style="width:100%">
          <el-table-column prop="name" label="代理" min-width="150">
            <template #default="{ row }"><span style="font-weight:500">{{ row.name }}</span></template>
          </el-table-column>
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag size="small" :type="row.online && row.status === 1 ? 'success' : 'info'">
                {{ row.online && row.status === 1 ? '在线' : '离线' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="转发端口" width="100">
            <template #default="{ row }">
              <span style="font-family:var(--font-mono,monospace);color:var(--app-text-secondary)">{{ row.port || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="实时速率" width="180">
            <template #default="{ row }">
              <div class="rate-cell">
                <span class="rate-down">↓ {{ formatBytes(row.rateIn) }}/s</span>
                <span class="rate-up">↑ {{ formatBytes(row.rateOut) }}/s</span>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="总流量" min-width="110">
            <template #default="{ row }">{{ formatBytes((row.bytesIn || 0) + (row.bytesOut || 0)) }}</template>
          </el-table-column>
        </el-table>
        <div v-if="topProxies.length === 0" class="empty-tip">暂无在线代理</div>
      </div>

      <!-- 最近动态 -->
      <div class="page-card">
        <div class="card-header" style="padding:14px 16px;border-bottom:1px solid var(--app-border-light)">
          <span>最近动态</span>
          <span class="link-all" @click="router.push('/pm/audit-logs')">全部 →</span>
        </div>
        <div class="feed-list">
          <div v-for="it in feeds" :key="it.id" class="feed-item">
            <span class="feed-time">{{ formatHM(it.createdAt) }}</span>
            <span class="feed-text">{{ it.username }} {{ actionLabel(it.action) }}{{ it.target ? ' ' + it.target : '' }}</span>
          </div>
          <el-empty v-if="feeds.length === 0" description="暂无动态" :image-size="60" />
        </div>
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue"
import { useRouter } from "vue-router"
import { healthApi, proxyApi, auditApi, portApi } from "@/api"
import Layout from "@/components/Layout.vue"

const router = useRouter()
const refreshOptions = [5, 10, 30, 60, 120]
const refreshInterval = ref(10)

const health = ref<any>({})
const proxies = ref<any[]>([])
const feeds = ref<any[]>([])
const poolText = ref("-")
const loadingProxies = ref(false)
let timer: any = null

const onlineCount = computed(() => proxies.value.filter((p: any) => p.online && p.status === 1).length)
const enabledCount = computed(() => proxies.value.filter((p: any) => p.status === 1).length)
const totalIn = computed(() => proxies.value.reduce((s, p: any) => s + (p.bytesIn || 0), 0))
const totalOut = computed(() => proxies.value.reduce((s, p: any) => s + (p.bytesOut || 0), 0))
const totalTraffic = computed(() => totalIn.value + totalOut.value)
// 仅显示在线代理中总流量（入站+出站）前 5 名
const topProxies = computed(() =>
  proxies.value
    .filter((p: any) => p.online && p.status === 1)
    .sort((a: any, b: any) => ((b.bytesIn || 0) + (b.bytesOut || 0)) - ((a.bytesIn || 0) + (a.bytesOut || 0)))
    .slice(0, 5)
)

const formatBytes = (n?: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + " " + units[i]
}
const formatHM = (ts?: number) => {
  if (!ts) return "-"
  const d = new Date(ts * 1000)
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
}

const actions = [
  { value: "login", label: "登录" },
  { value: "login_failed", label: "登录失败" },
  { value: "admin_init", label: "初始化管理账号" },
  { value: "user_create", label: "创建用户" },
  { value: "user_delete", label: "删除用户" },
  { value: "user_disable", label: "禁用用户" },
  { value: "user_enable", label: "启用用户" },
  { value: "user_reset_code", label: "重置授权码" },
  { value: "proxy_disable", label: "禁用代理" },
  { value: "proxy_enable", label: "启用代理" },
  { value: "port_pool_update", label: "修改端口池" },
  { value: "auth_init", label: "初始化B端账号" },
  { value: "auth_rebind", label: "重新绑定授权码" },
  { value: "proxy_create", label: "创建代理" },
  { value: "proxy_update", label: "修改代理" },
  { value: "proxy_delete", label: "删除代理" },
  { value: "proxy_cleanup_offline", label: "清理离线代理" }
]
const actionLabel = (a: string) => actions.find((x: any) => x.value === a)?.label || a

const loadData = async () => {
  loadingProxies.value = true
  try {
    const [h, pr, au, pp]: any[] = await Promise.all([
      healthApi.get(),
      proxyApi.list(),
      auditApi.list({ page_size: 6 }),
      portApi.getPool()
    ])
    if (h) health.value = h
    if (pr?.data) proxies.value = pr.data
    if (au?.data?.list) feeds.value = au.data.list
    if (pp?.data?.ranges?.length) poolText.value = pp.data.ranges.map((r: any) => `${r.start}-${r.end}`).join("，")
  } catch (e) { console.error(e) } finally { loadingProxies.value = false }
}

const restartTimer = () => { if (timer) clearInterval(timer); timer = setInterval(loadData, refreshInterval.value * 1000) }

onMounted(() => { loadData(); restartTimer() })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>
<style scoped>
.stat-total { font-size: 15px; color: var(--app-text-tertiary); font-weight: 400; }
.traffic-value { font-size: 22px; }
.stat-sub { margin-top: 2px; font-size: 12px; color: var(--app-text-tertiary); }

.dash-grid { display: grid; grid-template-columns: 1.5fr 1fr; gap: 16px; align-items: stretch; }
@media (max-width: 1100px) { .dash-grid { grid-template-columns: 1fr; } }

.header-actions { display: flex; align-items: center; gap: 6px; font-weight: 400; }
.refresh-label { font-size: 12px; color: var(--app-text-tertiary); }
.rate-cell { display: flex; flex-direction: column; gap: 2px; font-family: var(--font-mono, monospace); font-size: 12px; }
.rate-down { color: #2563eb; }
.rate-up { color: #6b7280; }
.empty-tip { padding: 28px 0; text-align: center; font-size: 13px; color: var(--app-text-tertiary); }

.feed-list { padding: 6px 16px 12px; }
.feed-item { display: flex; gap: 10px; align-items: baseline; padding: 9px 0; border-bottom: 0.5px solid var(--app-border-light); font-size: 12px; }
.feed-item:last-child { border-bottom: none; }
.feed-time { color: #2563eb; flex-shrink: 0; font-family: var(--font-mono, monospace); }
.feed-text { color: var(--app-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.link-all { font-size: 12px; color: #2563eb; cursor: pointer; font-weight: 400; }
.link-all:hover { color: var(--app-primary-hover); }
</style>
