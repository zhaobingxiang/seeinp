<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>系统日志</span>
        <span class="subtitle">运行日志（logs/ 目录）</span>
      </div>

      <div class="toolbar" style="padding:14px 20px;border-bottom:1px solid var(--app-border-light)">
        <div class="filters">
          <span class="hint">选择左侧文件查看运行日志</span>
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
        </div>
        <el-button type="success" :disabled="!currentFile" @click="download">下载日志</el-button>
      </div>

      <div class="log-layout" style="padding:16px 20px">
        <div class="file-list">
          <div v-for="f in files" :key="f.name" class="file-item" :class="{ active: f.name === currentFile }" @click="selectFile(f.name)">
            <div class="file-name">{{ f.name }}</div>
            <div class="file-meta">{{ formatSize(f.size) }} · {{ formatTime(f.modified) }}</div>
          </div>
          <el-empty v-if="files.length === 0 && !loadingFiles" description="无日志文件" :image-size="60" />
        </div>
        <div class="log-view">
          <pre class="log-content">{{ content || '选择左侧文件查看日志' }}</pre>
        </div>
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, onMounted } from "vue"
import { Search } from "@element-plus/icons-vue"
import { logApi } from "@/api"
import Layout from "@/components/Layout.vue"

const files = ref<any[]>([])
const loadingFiles = ref(true)
const currentFile = ref("")
const content = ref("")
const loadingContent = ref(false)
const lines = ref(500)
const keyword = ref("")
const formatTime = (ts?: number) => { if (!ts) return "-"; return new Date(ts * 1000).toLocaleString() }
const formatSize = (n?: number) => {
  if (!n || n <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB"]
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(i === 0 ? 0 : 1) + " " + units[i]
}
const loadFiles = async () => {
  loadingFiles.value = true
  try {
    const res: any = await logApi.listFiles()
    if (res.code === 0) {
      files.value = (res.data || []).sort((a: any, b: any) => (a.name > b.name ? 1 : -1))
      if (!currentFile.value && files.value.length > 0) selectFile(files.value[0].name)
    }
  } catch (e) { console.error(e) } finally { loadingFiles.value = false }
}
const selectFile = (name: string) => { currentFile.value = name; loadContent() }
const loadContent = async () => {
  if (!currentFile.value) return
  loadingContent.value = true
  try {
    const res: any = await logApi.content(currentFile.value, lines.value, keyword.value || undefined)
    if (res.code === 0) {
      content.value = res.data.content || "(空)"
      setTimeout(() => { const el = document.querySelector(".log-content"); if (el) el.scrollTop = el.scrollHeight }, 50)
    }
  } catch (e) { console.error(e) } finally { loadingContent.value = false }
}
const download = async () => {
  if (!currentFile.value) return
  try {
    const resp = await fetch(`/api/v1/logs/content?file=${encodeURIComponent(currentFile.value)}&download=1`, {
      headers: { Authorization: 'Bearer ' + (localStorage.getItem('token') || '') }
    })
    if (!resp.ok) return
    const blob = await resp.blob()
    const a = document.createElement("a")
    a.href = URL.createObjectURL(blob)
    a.download = currentFile.value
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e) { console.error(e) }
}
onMounted(loadFiles)
</script>
<style scoped>
.subtitle { color: var(--app-text-tertiary); font-size: 12px; font-weight: 400; }
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; }
.filters { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.hint { color: var(--app-text-tertiary); font-size: 13px; }
.log-layout { display: flex; gap: 16px; }
.file-list { width: 240px; flex-shrink: 0; border-right: 1px solid var(--app-border-light); padding-right: 16px; }
.file-item { padding: 8px 10px; border-radius: 6px; cursor: pointer; }
.file-item:hover { background: var(--app-bg); }
.file-item.active { background: var(--app-primary-light); }
.file-name { font-weight: 500; color: var(--app-text); }
.file-meta { font-size: 12px; color: var(--app-text-tertiary); margin-top: 2px; }
.log-view { flex: 1; min-width: 0; }
.log-content { background: #1e1e1e; color: #d4d4d4; padding: 12px; border-radius: 8px; height: 62vh; overflow: auto; font-size: 12px; line-height: 1.6; margin: 0; white-space: pre-wrap; word-break: break-all; }
</style>
