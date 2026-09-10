<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:14px 20px;border-bottom:1px solid var(--app-border-light)">
        <div class="filters">
          <span class="filter-label">来源：</span>
          <el-select v-model="source" style="width:230px" @change="onSourceChange">
            <el-option label="seeinpm（中心端）" value="__local__" />
            <el-option v-for="c in clients" :key="c.username" :label="`${c.username} · seeinps（服务端）`" :value="c.username" />
          </el-select>
          <el-input v-model="keyword" placeholder="关键词搜索日志内容" clearable style="width:200px" @keyup.enter="loadContent">
            <template #prefix><el-icon><Search /></el-icon></template>
          </el-input>
          <el-select v-model="lines" style="width:110px" @change="loadContent">
            <el-option :value="200" label="200 行" />
            <el-option :value="500" label="500 行" />
            <el-option :value="1000" label="1000 行" />
            <el-option :value="2000" label="2000 行" />
          </el-select>
          <el-button type="primary" @click="loadContent" :loading="loadingContent">刷新</el-button>
          <template v-if="source === '__local__' || selectedClientOnline">
            <span class="filter-label">日志级别：</span>
            <el-select v-model="logLevel" style="width:110px">
              <el-option v-for="lv in ['debug','info','warn','error']" :key="lv" :label="lv.toUpperCase()" :value="lv" />
            </el-select>
            <el-button :type="source === '__local__' ? 'warning' : 'primary'" plain :loading="changingLevel" @click="onLevelChange">保存级别</el-button>
          </template>
          <el-tooltip :content="levelHint" placement="top">
            <span class="level-hint">{{ levelHint }}</span>
          </el-tooltip>
        </div>
        <div class="tools">
          <el-button type="success" :disabled="!currentFile || downloading" :loading="downloading" @click="download">下载日志</el-button>
        </div>
      </div>
      <el-alert v-if="source !== '__local__' && !loadingFiles && !hasFiles" type="warning" :closable="false" show-icon
        title="该 seeinps 当前在线，但未获取到日志文件列表" style="margin:14px 20px 0" />
      <div class="log-layout">
        <div class="file-list">
          <div v-for="f in files" :key="f.name" class="file-item" :class="{ active: f.name === currentFile }" @click="selectFile(f.name)">
            <div class="file-name">{{ f.name }}</div>
            <div class="file-meta">{{ formatSize(f.size) }} · {{ formatTime(f.modified) }}</div>
          </div>
          <el-empty v-if="files.length === 0 && !loadingFiles" description="无日志文件" :image-size="60" />
        </div>
        <div class="log-view">
          <pre class="log-content" v-html="renderContent()"></pre>
        </div>
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, computed, onMounted } from "vue"
import { Search } from "@element-plus/icons-vue"
import { logApi, psLogApi, clientApi, loggingApi } from "@/api"
import Layout from "@/components/Layout.vue"
import { ElMessage } from "element-plus"

const files = ref<any[]>([])
const loadingFiles = ref(true)
const currentFile = ref("")
const content = ref("")
const loadingContent = ref(false)
const lines = ref(500)
const keyword = ref("")
const source = ref<string>("__local__")
const clients = ref<any[]>([])
const hasFiles = computed(() => files.value.length > 0)
const logLevel = ref("info")
const changingLevel = ref(false)
const downloading = ref(false)
const selectedClientOnline = computed(() =>
  source.value !== "__local__" && clients.value.some((c: any) => c.username === source.value))
const levelHint = computed(() => {
  if (source.value === "__local__") return "中心端运行日志级别"
  if (!selectedClientOnline.value) return "该 seeinps 不在线，无法调整级别"
  return "级别将经控制通道下发到该 seeinps（并记入审计）"
})
const formatTime = (ts?: number) => { if (!ts) return "-"; return new Date(ts * 1000).toLocaleString() }
const formatSize = (n?: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(i === 0 ? 0 : 1) + " " + units[i]
}
const loadClients = async () => {
  try {
    const res: any = await clientApi.list()
    if (res.code === 0) {
      clients.value = (res.data || []).filter((c: any) => c.connected)
    }
  } catch (e) { console.error(e) }
}
const loadFiles = async () => {
  loadingFiles.value = true
  currentFile.value = ""
  content.value = ""
  try {
    const res: any = source.value === "__local__"
      ? await logApi.listFiles()
      : await psLogApi.listFiles(source.value)
    if (res.code === 0) {
      files.value = (res.data || []).sort((a: any, b: any) => (a.name > b.name ? 1 : -1))
      if (files.value.length > 0) selectFile(files.value[0].name)
    }
  } catch (e) { console.error(e) } finally { loadingFiles.value = false }
}
const onSourceChange = () => { loadFiles(); loadLogLevel() }
const loadLogLevel = async () => {
  try {
    if (source.value === "__local__") {
      const res: any = await loggingApi.get()
      if (res.code === 0 && res.data?.level) logLevel.value = res.data.level
      return
    }
    if (!selectedClientOnline.value) return
    const res: any = await psLogApi.getLevel(source.value)
    if (res.code === 0 && res.data?.level) logLevel.value = res.data.level
  } catch (e) { console.error(e) }
}
const onLevelChange = async () => {
  const lv = logLevel.value
  if (!["debug", "info", "warn", "error"].includes(lv)) return
  changingLevel.value = true
  try {
    const res: any = source.value === "__local__"
      ? await loggingApi.set(lv)
      : await psLogApi.setLevel(source.value, lv)
    if (res.code === 0) {
      ElMessage.success(source.value === "__local__"
        ? `日志级别已修改为 ${lv.toUpperCase()}`
        : `已下发到 seeinps（${source.value}）：${lv.toUpperCase()}`)
    } else {
      ElMessage.error(res.message || "修改失败")
    }
  } catch (e: any) {
    ElMessage.error(e?.response?.data?.message || "修改日志级别失败")
  } finally { changingLevel.value = false }
}
const selectFile = (name: string) => { currentFile.value = name; loadContent() }
const loadContent = async () => {
  if (!currentFile.value) return
  loadingContent.value = true
  try {
    let res: any
    if (source.value === "__local__") {
      res = await logApi.content(currentFile.value, lines.value, keyword.value || undefined)
    } else {
      // 关键词也交给服务端过滤（此前只在已拉取的窗口内过滤，搜不全）
      res = await psLogApi.content(source.value, currentFile.value, lines.value, keyword.value || undefined)
    }
    if (res.code === 0) {
      content.value = res.data.content || "(空)"
      setTimeout(() => { const el = document.querySelector(".log-content"); if (el) el.scrollTop = el.scrollHeight }, 50)
    } else {
      ElMessage.error(res.message || "读取日志失败")
    }
  } catch (e: any) {
    ElMessage.error(e?.response?.data?.message || "读取日志失败")
  } finally { loadingContent.value = false }
}
// 带鉴权的二进制下载：直接 window.open 不会带 Authorization 头，必须先取 blob
const downloadBlob = async (url: string, filename: string) => {
  const resp = await fetch(url, { headers: { Authorization: 'Bearer ' + (localStorage.getItem('pm_token') || '') } })
  if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
  const blob = await resp.blob()
  const a = document.createElement("a")
  a.href = URL.createObjectURL(blob)
  a.download = filename
  a.click()
  URL.revokeObjectURL(a.href)
}
const download = async () => {
  if (!currentFile.value) return
  downloading.value = true
  try {
    if (source.value === "__local__") {
      await downloadBlob(
        `/api/v1/logs/content?file=${encodeURIComponent(currentFile.value)}&download=1`,
        currentFile.value)
    } else {
      // seeinps 运行日志此前必须登录 PS 管理台才能下载；PM 已具备经控制通道取整文件的能力
      await downloadBlob(psLogApi.downloadUrl(source.value, currentFile.value), currentFile.value)
    }
    ElMessage.success("已开始下载")
  } catch (e: any) {
    ElMessage.error(e?.message || "下载失败")
  } finally { downloading.value = false }
}
const renderContent = () => {
  if (!content.value) return content.value || "选择左侧文件查看日志"
  const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
  const color = (lv: string) => ({
    debug: "#9aa6b2", info: "#d4d4d4", warn: "#e6a700", error: "#f56c6c", fatal: "#f56c6c"
  }[lv] || "#d4d4d4")
  // 级别标记必须锚定在行首（时间戳之后），否则正文里出现 [ERROR] 字样的行会被整行误染
  const levelRe = /^\d{4}-\d{2}-\d{2} \S+ \[(DEBUG|INFO|WARN|ERROR|FATAL)\]/
  return content.value.split(/\r?\n/).map((ln: string) => {
    const m = ln.match(levelRe)
    if (!m) return esc(ln)
    return `<span style="color:${color(m[1].toLowerCase())}">${esc(ln)}</span>`
  }).join("\n")
}
onMounted(() => { loadClients(); loadLogLevel(); loadFiles() })
</script>
<style scoped>
.filters { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.filter-label { color: var(--app-text-secondary); }
.tools { display: flex; gap: 10px; }
.log-layout { display: flex; gap: 0; }
.file-list { width: 230px; flex-shrink: 0; border-right: 1px solid var(--app-border-light); padding: 10px; }
.file-item { padding: 8px 10px; border-radius: var(--app-radius-sm); cursor: pointer; }
.file-item:hover { background: var(--app-bg); }
.file-item.active { background: var(--app-primary-light); }
.file-name { font-weight: 500; color: var(--app-text); }
.file-meta { font-size: 12px; color: var(--app-text-tertiary); margin-top: 2px; }
.log-view { flex: 1; min-width: 0; padding: 14px; }
.log-content { background: #1e2329; color: #d4d4d4; padding: 14px; border-radius: var(--app-radius-sm); height: 64vh; overflow: auto; font-size: 12px; line-height: 1.65; margin: 0; white-space: pre-wrap; word-break: break-all; }
</style>
