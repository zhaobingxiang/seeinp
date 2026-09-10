<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>审计日志</span>
        <div class="tools">
          <el-button type="success" size="small" :disabled="downloading" :loading="downloading" @click="download">导出 CSV</el-button>
          <el-button size="small" @click="load" :loading="loading">刷新</el-button>
        </div>
      </div>

      <div class="filters" style="padding:14px 20px;border-bottom:1px solid var(--app-border-light)">
        <el-select v-model="filterAction" placeholder="全部动作" clearable style="width:150px">
          <el-option v-for="a in actions" :key="a.value" :label="a.label" :value="a.value" />
        </el-select>
        <el-input v-model="filterKeyword" placeholder="关键词搜索" clearable style="width:180px" @keyup.enter="doSearch">
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-date-picker v-model="timeRange" type="datetimerange" range-separator="至" start-placeholder="开始时间" end-placeholder="结束时间"
          value-format="X" style="width:340px" />
        <el-button type="primary" @click="doSearch">查询</el-button>
        <el-button @click="reset">重置</el-button>
      </div>

      <div style="padding:12px 20px 20px">
        <el-table :data="list" v-loading="loading" size="small">
          <el-table-column label="时间" width="180"><template #default="{row}">{{ formatTime(row.createdAt) }}</template></el-table-column>
          <el-table-column prop="username" label="操作者" width="130" />
          <el-table-column label="动作" width="130"><template #default="{row}"><el-tag size="small" :type="actionTag(row.action)">{{ actionLabel(row.action) }}</el-tag></template></el-table-column>
          <el-table-column prop="target" label="对象" width="150" />
          <el-table-column prop="detail" label="详情" />
        </el-table>
        <div class="pager">
          <el-pagination background layout="prev, pager, next, total" :total="total" :page-size="pageSize" :current-page="page" @current-change="onPage" />
        </div>
        <el-empty v-if="list.length === 0 && !loading" description="暂无审计记录" />
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { auditApi } from '@/api'
import Layout from '@/components/Layout.vue'
import { ElMessage } from 'element-plus'

const list = ref<any[]>([])
const total = ref(0)
const downloading = ref(false)
const page = ref(1)
const pageSize = 20
const loading = ref(true)
const filterAction = ref('')
const filterKeyword = ref('')
const timeRange = ref<any[]>([])
const actions = [
  { value: 'login', label: '登录' },
  { value: 'login_failed', label: '登录失败' },
  { value: 'login_locked', label: '账号锁定拒绝登录' },
  { value: 'auth_init', label: '初始化本地账号' },
  { value: 'auth_rebind', label: '重新绑定授权码' },
  { value: 'proxy_create', label: '创建代理' },
  { value: 'proxy_update', label: '修改代理' },
  { value: 'proxy_delete', label: '删除代理' },
  { value: 'self_upgrade', label: '自主升级(上传包)' },
  { value: 'pm_upgrade', label: '从 seeinpm 拉取升级' },
  { value: 'log_level_update', label: '修改日志级别' },
  { value: 'log_download', label: '下载运行日志' },
  { value: 'audit_log_export', label: '导出审计日志' }
]
const actionLabel = (a: string) => actions.find((x: any) => x.value === a)?.label || a
const formatTime = (ts?: number) => { if (!ts) return '-'; return new Date(ts * 1000).toLocaleString() }
const actionTag = (a: string) => {
  if (a.endsWith('_failed') || a === 'login_locked') return 'danger'
  if (a === 'proxy_delete' || a.endsWith('_export') || a.endsWith('_download')) return 'warning'
  if (a === 'login' || a === 'proxy_create') return 'success'
  return 'info'
}
const load = async () => {
  loading.value = true
  try {
    const params: any = {
      action: filterAction.value || undefined,
      keyword: filterKeyword.value || undefined,
      page: page.value,
      page_size: pageSize
    }
    if (timeRange.value && timeRange.value.length === 2) {
      params.start_time = Number(timeRange.value[0])
      params.end_time = Number(timeRange.value[1])
    }
    const res: any = await auditApi.list(params)
    if (res.code === 0) { list.value = res.data.list || []; total.value = res.data.total || 0 }
  } catch (e) { console.error(e) } finally { loading.value = false }
}
const doSearch = () => { page.value = 1; load() }
const reset = () => { filterAction.value = ''; filterKeyword.value = ''; timeRange.value = []; doSearch() }
const onPage = (p: number) => { page.value = p; load() }
// 导出：走服务端导出接口。
// 分页接口的 page_size 上限是 200，之前传 10000 会被静默重置为 20，导出的 CSV 只有 20 行。
const download = async () => {
  const params: any = {
    action: filterAction.value || undefined,
    keyword: filterKeyword.value || undefined
  }
  if (timeRange.value && timeRange.value.length === 2) {
    params.start_time = Number(timeRange.value[0])
    params.end_time = Number(timeRange.value[1])
  }
  downloading.value = true
  try {
    const q = new URLSearchParams()
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') q.set(k, String(v))
    })
    const resp = await fetch('/api/v1/audit-logs/export?' + q.toString(), {
      headers: { Authorization: 'Bearer ' + (localStorage.getItem('token') || '') }
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    const blob = await resp.blob()
    const a = document.createElement("a")
    a.href = URL.createObjectURL(blob)
    a.download = `audit-logs-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(a.href)
    ElMessage.success('已导出（导出行为本身也会记入审计）')
  } catch (e: any) {
    ElMessage.error(e?.message || '导出失败')
  } finally { downloading.value = false }
}
onMounted(load)
</script>
<style scoped>
.filters { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.tools { display: flex; gap: 10px; }
.pager { margin-top: 16px; display: flex; justify-content: flex-end; }
</style>
