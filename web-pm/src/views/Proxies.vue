<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>代理列表</span>
        <div>
          <el-button size="small" type="danger" plain @click="cleanupOffline">清理离线代理</el-button>
          <el-button size="small" @click="loadProxies" :loading="loading">刷新</el-button>
        </div>
      </div>
      <div style="padding:12px 20px 20px">
        <el-alert type="info" :closable="false" show-icon style="margin-bottom:14px">
          显示所有连接过的代理；离线超 7 天的代理会自动清理（手动禁用的不会清理）；代理重新连接后自动恢复显示。
        </el-alert>
        <div class="filter-bar">
          <el-input v-model="filters.name" placeholder="代理名称" clearable style="width:170px" @input="onFilterChange" />
          <el-select v-model="filters.type" placeholder="类型" clearable style="width:110px" @change="onFilterChange">
            <el-option v-for="t in proxyTypes" :key="t" :label="t" :value="t" />
          </el-select>
          <el-select v-model="filters.status" placeholder="状态" clearable style="width:110px" @change="onFilterChange">
            <el-option label="在线" value="online" />
            <el-option label="离线" value="offline" />
            <el-option label="已禁用" value="disabled" />
          </el-select>
          <el-input v-model="filters.port" placeholder="端口" clearable style="width:120px" @input="onFilterChange" />
        </div>
        <el-table :data="pageProxies" v-loading="loading" row-key="name" :expand-row-keys="expandedKeys" @expand-change="onExpandChange" @sort-change="onSortChange">
          <el-table-column type="expand">
            <template #default="{row}">
              <div class="sessions-wrap">
                <div class="sessions-title">最近 10 次连接记录</div>
                <el-table :data="sessionsMap[row.name] || []" size="small" v-loading="sessionsLoading[row.name]">
                  <el-table-column label="连接时间" width="180"><template #default="{row: s}">{{ formatTime(s.onlineAt) }}</template></el-table-column>
                  <el-table-column label="离线时间" width="180"><template #default="{row: s}">{{ s.offlineAt ? formatTime(s.offlineAt) : '在线中' }}</template></el-table-column>
                  <el-table-column label="在线时长" width="120"><template #default="{row: s}">{{ formatDuration(s.onlineAt, s.offlineAt) }}</template></el-table-column>
                  <el-table-column label="来源地址"><template #default="{row: s}">{{ s.remoteAddr || '-' }}</template></el-table-column>
                </el-table>
                <el-empty v-if="!sessionsLoading[row.name] && (sessionsMap[row.name] || []).length === 0" description="暂无连接记录" :image-size="60" />
              </div>
            </template>
          </el-table-column>
          <el-table-column prop="name" label="代理" min-width="160" sortable="custom" />
          <el-table-column prop="type" label="类型" width="100" sortable="custom" />
          <el-table-column prop="status" label="状态" width="90" sortable="custom">
            <template #default="{row}">
              <el-tag v-if="row.status !== 1" type="danger" size="small">已禁用</el-tag>
              <el-tag v-else-if="row.online" type="success" size="small">在线</el-tag>
              <el-tag v-else type="info" size="small">离线</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="port" label="公网端口" width="100" sortable="custom"><template #default="{row}">{{ row.port || '-' }}</template></el-table-column>
          <el-table-column prop="bytesIn" label="入站流量" min-width="140" sortable="custom">
            <template #default="{row}">
              <div>{{ formatBytes(row.bytesIn) }}</div>
              <div v-if="row.online && row.status === 1" class="rate">↓ {{ formatBytes(row.rateIn) }}/s</div>
            </template>
          </el-table-column>
          <el-table-column prop="bytesOut" label="出站流量" min-width="140" sortable="custom">
            <template #default="{row}">
              <div>{{ formatBytes(row.bytesOut) }}</div>
              <div v-if="row.online && row.status === 1" class="rate">↑ {{ formatBytes(row.rateOut) }}/s</div>
            </template>
          </el-table-column>
          <el-table-column prop="lastOnlineAt" label="最近连接" width="170" sortable="custom"><template #default="{row}">{{ formatTime(row.lastOnlineAt) }}</template></el-table-column>
          <el-table-column prop="lastOfflineAt" label="最近离线" width="170" sortable="custom"><template #default="{row}">{{ formatTime(row.lastOfflineAt) }}</template></el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{row}">
              <el-button v-if="row.status === 1" type="warning" link @click="disableProxy(row)">禁用</el-button>
              <el-button v-else type="success" link @click="enableProxy(row)">启用</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="pageProxies.length === 0" :description="proxies.length && !filteredProxies.length ? '无匹配的代理' : '暂无代理'" />
        <PaginationBar v-if="sortedProxies.length > 0" v-model:page="page" v-model:pageSize="pageSize" :total="sortedProxies.length" />
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue"
import { ElMessage, ElMessageBox } from "element-plus"
import { proxyApi } from "@/api"
import Layout from "@/components/Layout.vue"
import PaginationBar from "@/components/PaginationBar.vue"

const proxies = ref<any[]>([])
const loading = ref(true)
const sessionsMap = ref<Record<string, any[]>>({})
const sessionsLoading = ref<Record<string, boolean>>({})
const expandedKeys = ref<string[]>([])
let timer: number | undefined

// ===== 分页 / 筛选 / 排序 =====
const filters = ref({ name: "", type: "", status: "", port: "" })
const sortState = ref<{ prop: string; order: string }>({ prop: "", order: "" })
const page = ref(1)
const pageSize = ref(20)

const onFilterChange = () => { page.value = 1 }
const onSortChange = ({ prop, order }: { prop: string; order: string }) => {
  sortState.value = { prop: prop || "", order: order || "" }
  page.value = 1
}

const proxyTypes = computed(() => {
  const set = new Set<string>()
  for (const p of proxies.value) if (p.type) set.add(p.type)
  return Array.from(set)
})

const filteredProxies = computed(() => {
  let arr = proxies.value
  const kw = filters.value.name.trim().toLowerCase()
  if (kw) arr = arr.filter((p: any) => (p.name || "").toLowerCase().includes(kw))
  if (filters.value.type) arr = arr.filter((p: any) => p.type === filters.value.type)
  if (filters.value.status === "online") arr = arr.filter((p: any) => p.online)
  else if (filters.value.status === "offline") arr = arr.filter((p: any) => !p.online && p.status === 1)
  else if (filters.value.status === "disabled") arr = arr.filter((p: any) => p.status !== 1)
  const port = parseInt(filters.value.port, 10)
  if (!isNaN(port)) arr = arr.filter((p: any) => p.port === port)
  return arr
})

const sortedProxies = computed(() => {
  const { prop, order } = sortState.value
  if (!prop || !order || order === "null") return filteredProxies.value
  const dir = order === "ascending" ? 1 : -1
  const get = (p: any): any => {
    switch (prop) {
      case "name": return p.name || ""
      case "type": return p.type || ""
      case "port": return p.port || 0
      case "bytesIn": return p.bytesIn || 0
      case "bytesOut": return p.bytesOut || 0
      case "lastOnlineAt": return p.lastOnlineAt || 0
      case "lastOfflineAt": return p.lastOfflineAt || 0
      case "status": return p.status === 1 ? (p.online ? 2 : 1) : 0
      default: return 0
    }
  }
  return [...filteredProxies.value].sort((a: any, b: any) => {
    const va = get(a), vb = get(b)
    if (va < vb) return -1 * dir
    if (va > vb) return 1 * dir
    return 0
  })
})

const pageProxies = computed(() => sortedProxies.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))

const formatTime = (ts?: number) => { if (!ts) return "-"; return new Date(ts * 1000).toLocaleString() }
const formatBytes = (n?: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + " " + units[i]
}
const formatDuration = (onlineAt: number, offlineAt?: number) => {
  const end = offlineAt || Math.floor(Date.now() / 1000)
  let s = Math.max(0, end - onlineAt)
  const d = Math.floor(s / 86400); s %= 86400
  const h = Math.floor(s / 3600); s %= 3600
  const m = Math.floor(s / 60); s %= 60
  if (d > 0) return `${d}天${h}时`
  if (h > 0) return `${h}时${m}分`
  if (m > 0) return `${m}分${s}秒`
  return `${s}秒`
}
const loadProxies = async () => {
  try {
    const res: any = await proxyApi.list()
    if (res.code === 0) proxies.value = res.data || []
  } catch (e) { console.error(e) } finally { loading.value = false }
}
const loadSessions = async (row: any) => {
  sessionsLoading.value = { ...sessionsLoading.value, [row.name]: true }
  try {
    const res: any = await proxyApi.sessions(row.username, row.proxyId)
    if (res.code === 0) sessionsMap.value = { ...sessionsMap.value, [row.name]: res.data || [] }
  } catch (e) { console.error(e) } finally {
    sessionsLoading.value = { ...sessionsLoading.value, [row.name]: false }
  }
}
const onExpandChange = (row: any, expanded: any[]) => {
  expandedKeys.value = expanded.map((r: any) => r.name)
  if (expanded.some((r: any) => r.name === row.name)) loadSessions(row)
}
const disableProxy = (row: any) => {
  const hint = row.online
    ? `确定禁用代理 ${row.name} 吗？将立即断开其公网连接并拒绝重连，同用户其他代理不受影响。`
    : `确定禁用代理 ${row.name} 吗？禁用后其连接请求会被拒绝。`
  ElMessageBox.confirm(hint, "禁用代理", { type: "warning" }).then(async () => {
    try {
      const res: any = await proxyApi.disable(row.username, row.proxyId)
      if (res.code === 0) { ElMessage.success("已禁用"); loadProxies() } else ElMessage.error(res.message || "操作失败")
    } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
  }).catch(() => {})
}
const enableProxy = async (row: any) => {
  try {
    const res: any = await proxyApi.enable(row.username, row.proxyId)
    if (res.code === 0) { ElMessage.success("已启用，代理将在 30 秒内自动恢复连接"); loadProxies() } else ElMessage.error(res.message || "操作失败")
  } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
}
const cleanupOffline = async () => {
  try {
    await ElMessageBox.confirm(
      "确定清理所有离线代理？\n\n离线代理的会话记录与预留端口将被一并删除，端口放回公共池。\n重新连接后代理会自动重建记录并重新分配端口。",
      "清理离线代理",
      { type: "warning", confirmButtonText: "确定清理", cancelButtonText: "取消" }
    )
    const res: any = await proxyApi.cleanupOffline()
    if (res.code === 0) {
      ElMessage.success(`已清理 ${res.data.removed} 个离线代理`)
      loadProxies()
    } else {
      ElMessage.error(res.message || "清理失败")
    }
  } catch (e: any) {
    if (e !== "cancel") ElMessage.error(e.response?.data?.message || "清理失败")
  }
}
onMounted(() => { loadProxies(); timer = window.setInterval(loadProxies, 5000) })
onUnmounted(() => { if (timer) window.clearInterval(timer) })
</script>
<style scoped>
.filter-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 14px;
}

.rate { font-size: 12px; color: var(--app-text-tertiary); }
.sessions-wrap { padding: 8px 24px 16px; }
.sessions-title { font-weight: 500; margin-bottom: 8px; color: var(--app-text-secondary); }
</style>
