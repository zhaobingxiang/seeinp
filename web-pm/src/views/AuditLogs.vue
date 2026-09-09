<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:14px 20px;border-bottom:1px solid var(--app-border-light)">
        <div class="filters">
          <el-select v-model="filterSource" placeholder="全部来源" clearable style="width:110px">
            <el-option label="PM 管理端" value="pm" />
            <el-option label="B 端 (seeinps)" value="ps" />
          </el-select>
          <el-select v-model="filterAction" placeholder="全部动作" clearable style="width:150px">
            <el-option v-for="a in actions" :key="a.value" :label="a.label" :value="a.value" />
          </el-select>
          <el-input v-model="filterUsername" placeholder="操作者" clearable style="width:120px" @keyup.enter="doSearch" />
          <el-input v-model="filterKeyword" placeholder="关键词搜索" clearable style="width:150px" @keyup.enter="doSearch">
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-date-picker v-model="timeRange" type="datetimerange" range-separator="至" start-placeholder="开始时间" end-placeholder="结束时间"
            value-format="X" style="width:330px" />
          <el-button type="primary" @click="doSearch">查询</el-button>
          <el-button @click="reset">重置</el-button>
        </div>
        <div class="tools">
          <el-button type="success" :disabled="list.length === 0" @click="download">下载</el-button>
          <el-button @click="load" :loading="loading">刷新</el-button>
        </div>
      </div>
      <div style="padding:12px 20px 20px">
        <el-table :data="list" v-loading="loading">
          <el-table-column label="时间" width="175"><template #default="{row}">{{ formatTime(row.createdAt) }}</template></el-table-column>
          <el-table-column label="来源" width="110"><template #default="{row}">
            <el-tag size="small" :type="row.source === 'ps' ? 'primary' : 'info'">{{ row.source === 'ps' ? 'B端 seeinps' : 'PM 管理端' }}</el-tag>
          </template></el-table-column>
          <el-table-column prop="username" label="操作者" width="120" />
          <el-table-column label="动作" width="130"><template #default="{row}"><el-tag size="small" :type="actionTag(row.action)">{{ actionLabel(row.action) }}</el-tag></template></el-table-column>
          <el-table-column prop="target" label="对象" width="170" />
          <el-table-column prop="detail" label="详情" min-width="220" show-overflow-tooltip />
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
import { ref, onMounted } from "vue"
import { Search } from "@element-plus/icons-vue"
import { auditApi } from "@/api"
import Layout from "@/components/Layout.vue"

const list = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const loading = ref(true)
const filterAction = ref("")
const filterUsername = ref("")
const filterKeyword = ref("")
const filterSource = ref("")
const timeRange = ref<any[]>([])
const actions = [
  { value: "login", label: "登录" },
  { value: "login_failed", label: "登录失败" },
  { value: "admin_init", label: "初始化管理账号" },
  { value: "user_create", label: "创建用户" },
  { value: "user_update", label: "修改用户" },
  { value: "user_delete", label: "删除用户" },
  { value: "user_disable", label: "禁用用户" },
  { value: "user_enable", label: "启用用户" },
  { value: "user_reset_code", label: "重置授权码" },
  { value: "user_batch_move_group", label: "批量修改分组" },
  { value: "user_group_create", label: "新建分组" },
  { value: "user_group_rename", label: "重命名分组" },
  { value: "user_group_delete", label: "删除分组" },
  { value: "proxy_disable", label: "禁用代理" },
  { value: "proxy_enable", label: "启用代理" },
  { value: "port_pool_update", label: "修改端口池" },
  { value: "auth_init", label: "初始化B端账号" },
  { value: "auth_rebind", label: "重新绑定授权码" },
  { value: "proxy_create", label: "创建代理" },
  { value: "proxy_update", label: "修改代理" },
  { value: "proxy_delete", label: "删除代理" },
  { value: "client_upgrade", label: "推送升级客户端" },
  { value: "upgrade_failed", label: "升级客户端失败" },
  { value: "version_upload", label: "上传升级包" },
  { value: "version_delete", label: "删除升级包" },
  { value: "self_upgrade", label: "B端自主升级" },
  { value: "pm_upgrade", label: "B端从PM拉取升级" },
  { value: "log_level_update", label: "修改日志级别" },
  { value: "user_expired", label: "账号过期踢线" }
]
const actionLabel = (a: string) => actions.find((x: any) => x.value === a)?.label || a
const formatTime = (ts?: number) => { if (!ts) return "-"; return new Date(ts * 1000).toLocaleString() }
const actionTag = (a: string) => {
  if (a.endsWith("_failed")) return "danger"
  if (a.endsWith("_disable") || a === "user_delete" || a === "proxy_delete") return "warning"
  if (a === "login" || a === "user_create" || a === "proxy_create") return "success"
  return "info"
}
const load = async () => {
  loading.value = true
  try {
    const params: any = {
      username: filterUsername.value || undefined,
      action: filterAction.value || undefined,
      keyword: filterKeyword.value || undefined,
      source: filterSource.value || undefined,
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
const reset = () => {
  filterAction.value = ""; filterUsername.value = ""; filterKeyword.value = ""; filterSource.value = ""; timeRange.value = []
  doSearch()
}
const onPage = (p: number) => { page.value = p; load() }
const download = async () => {
  const params: any = {
    username: filterUsername.value || undefined,
    action: filterAction.value || undefined,
    keyword: filterKeyword.value || undefined,
    source: filterSource.value || undefined,
    page_size: 10000
  }
  if (timeRange.value && timeRange.value.length === 2) {
    params.start_time = Number(timeRange.value[0])
    params.end_time = Number(timeRange.value[1])
  }
  try {
    const res: any = await auditApi.list(params)
    const rows: any[] = res.data?.list || []
    if (rows.length === 0) { return }
    const header = "时间,来源,操作者,动作,对象,详情"
    const lines = rows.map((r: any) => [
      formatTime(r.createdAt), r.source === 'ps' ? 'B端' : 'PM', r.username, actionLabel(r.action), r.target,
      (r.detail || '').replace(/,/g, '，').replace(/\n/g, ' ')
    ].join(","))
    const blob = new Blob(["\ufeff" + [header, ...lines].join("\n")], { type: "text/csv;charset=utf-8" })
    const a = document.createElement("a")
    a.href = URL.createObjectURL(blob)
    a.download = `audit-logs-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e) { console.error(e) }
}
onMounted(load)
</script>
<style scoped>
.filters { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.tools { display: flex; gap: 10px; }
.pager { margin-top: 16px; display: flex; justify-content: flex-end; }
</style>
