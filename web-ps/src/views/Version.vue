<template>
  <Layout>
    <!-- 当前版本 -->
    <div class="page-card" style="padding:20px;margin-bottom:20px">
      <div class="card-header">
        <span>当前版本</span>
        <el-tag size="small" style="font-family:var(--font-mono,monospace)">{{ currentVersion || '-' }}</el-tag>
      </div>
      <div class="form-tip" style="margin-top:10px">
        支持两种升级方式：直接上传新版本包（版本号自动从包内识别），或从 seeinpm 版本库拉取。升级过程中全部代理会短暂断开，完成后自动恢复。
      </div>
      <template v-if="upStage === 'pulling' || upStage === 'receiving'">
        <el-progress :percentage="upPercent" style="width:100%;margin-top:14px" />
        <div class="form-tip" style="margin-top:6px">
          正在从 seeinpm 拉取升级包{{ upTargetVersion ? '（目标版本 ' + upTargetVersion + '）' : '' }}：{{ formatSize(upDoneBytes) }} / {{ formatSize(upTotalBytes) }}
        </div>
      </template>
      <el-progress v-if="upStage === 'applying'" :percentage="100" status="success" style="width:100%;margin-top:14px" />
      <el-alert v-if="upStage === 'applying'" type="success" :closable="false" style="margin-top:8px"
        title="升级包校验通过，节点正在重启恢复（约 1 分钟内自动恢复全部代理）" />
      <el-alert v-if="upDone" type="success" :closable="false" style="margin-top:12px"
        :title="`✅ 升级完成，节点已运行 ${currentVersion}，代理已自动恢复`" />
      <el-alert v-if="upStage === 'failed'" type="error" :closable="false" style="margin-top:12px"
        :title="'升级失败：' + upErrMsg" />
    </div>

    <!-- 从 seeinpm 拉取升级 -->
    <div class="page-card" style="padding:20px;margin-bottom:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>从 seeinpm 版本库升级</span>
        <el-button @click="loadPMVersions" :loading="pmLoading">刷新列表</el-button>
      </div>
      <div v-if="pmVersions.length === 0" class="empty-state">
        <el-empty description="seeinpm 版本库暂无本平台的版本包" />
      </div>
      <el-table v-else :data="pmVersions" style="width: 100%">
        <el-table-column prop="version" label="版本号" width="150">
          <template #default="{ row }">
            <span style="font-family:var(--font-mono,monospace)">{{ row.version }}</span>
            <el-tag v-if="row.version === currentVersion" type="info" size="small" style="margin-left:6px">当前</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="110">
          <template #default="{ row }">{{ formatSize(row.fileSize) }}</template>
        </el-table-column>
        <el-table-column prop="note" label="更新说明" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ row.note || '-' }}</template>
        </el-table-column>
        <el-table-column label="发布时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template #default="{ row }">
            <el-button type="primary" size="small" :disabled="busy" @click="upgradeFromPM(row.version)">
              {{ row.version === currentVersion ? '重新安装' : '升级到此版本' }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 自上传升级 -->
    <div class="page-card" style="padding:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>上传版本包升级</span>
      </div>
      <el-form label-width="90px" style="max-width:560px">
        <el-form-item label="安装包">
          <div class="form-tip" style="margin-bottom:6px">版本号将自动从包内识别；包平台必须与本节点（{{ nodePlatform }}）一致</div>
          <el-upload ref="uploadRef" :limit="1" :auto-upload="false" :on-change="onFileChange" :on-remove="() => uploadFile = null"
            :disabled="busy" drag style="width:100%">
            <div style="padding:12px 0">拖拽或点击选择 seeinps 二进制文件（≤200MB）</div>
          </el-upload>
        </el-form-item>
        <el-form-item v-if="uploadPercent > 0 && busy" label="">
          <el-progress :percentage="uploadPercent" style="width:100%" />
          <div class="form-tip">正在上传安装包，请勿关闭页面</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="busy" @click="handleSelfUpload">上传并升级</el-button>
        </el-form-item>
      </el-form>
    </div>
  </Layout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage } from 'element-plus'
import type { UploadFile } from 'element-plus'
import { versionApi, statusApi } from '@/api'
import Layout from '@/components/Layout.vue'

const currentVersion = ref('')
const nodePlatform = ref('')
const pmVersions = ref<any[]>([])
const pmLoading = ref(false)

const uploadFile = ref<any>(null)
const uploadPercent = ref(0)
const uploadRef = ref()

const upStage = ref('idle')
const upErrMsg = ref('')
const upDoneBytes = ref(0)
const upTotalBytes = ref(0)
const upTargetVersion = ref('')
const upDone = ref(false)

const busy = computed(() => upStage.value === 'pulling' || upStage.value === 'receiving' || upStage.value === 'applying' || uploadingNow.value)
const uploadingNow = ref(false)

const percent = computed(() => {
  if (upStage.value === 'applying') return 100
  if (upTotalBytes.value > 0 && (upStage.value === 'pulling' || upStage.value === 'receiving')) {
    return Math.min(100, Math.round((upDoneBytes.value / upTotalBytes.value) * 100))
  }
  return 0
})

const formatTime = (ts?: number) => { if (!ts) return '-'; return new Date(ts * 1000).toLocaleString() }
const formatSize = (n?: number) => {
  if (!n) return '-'
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n > 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' B'
}

const loadCurrent = async () => {
  try {
    const res: any = await statusApi.getStatus()
    if (res.code === 0) {
      currentVersion.value = res.data?.version || ''
      nodePlatform.value = res.data?.platform || ''
    }
  } catch (error) {
    console.error('Load status error:', error)
  }
}

const loadPMVersions = async () => {
  pmLoading.value = true
  try {
    const res: any = await versionApi.pmVersions()
    if (res.code === 0) pmVersions.value = res.data || []
    else ElMessage.error(res.message || '获取版本列表失败')
  } catch (error: any) {
    ElMessage.error(error.response?.data?.message || '获取版本列表失败')
  } finally {
    pmLoading.value = false
  }
}

const onFileChange = (file: UploadFile) => { uploadFile.value = file.raw }

const pollStatus = () => {
  if (pollTimer) return
  pollTimer = window.setInterval(async () => {
    try {
      const res: any = await versionApi.status()
      if (res.code !== 0) return
      const d = res.data
      upStage.value = d.stage
      upErrMsg.value = d.error || ''
      upDoneBytes.value = d.doneBytes || 0
      upTotalBytes.value = d.totalBytes || 0
      if (d.stage === 'failed') {
        ElMessage.error('升级失败：' + (d.error || '未知错误'))
        stopPoll()
      } else if (d.stage === 'idle') {
        // 状态归位：进程已重启，检查版本号判定完成
        stopPoll()
        await loadCurrent()
        if (upTargetVersion.value && currentVersion.value === upTargetVersion.value) {
          upDone.value = true
          ElMessage.success(`升级完成：节点已运行 ${currentVersion.value}，代理已自动恢复`)
        } else if (upTargetVersion.value) {
          ElMessage.warning('节点已重启，请确认当前版本（预期 ' + upTargetVersion.value + '）')
        }
        loadPMVersions()
      }
    } catch { /* 轮询错误忽略（重启瞬间的连接重置会走到这里） */ }
  }, 2000)
}

const stopPoll = () => {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = undefined }
}

const upgradeFromPM = async (version: string) => {
  try {
    const res: any = await versionApi.pmUpgrade(version)
    if (res.code === 0) {
      upTargetVersion.value = version
      upStage.value = 'pulling'
      pollStatus()
    } else {
      ElMessage.error(res.message || '发起升级失败')
    }
  } catch (error: any) {
    ElMessage.error(error.response?.data?.message || '发起升级失败')
  }
}

const handleSelfUpload = async () => {
  if (!uploadFile.value) {
    ElMessage.warning('请选择安装包文件')
    return
  }
  uploadingNow.value = true
  try {
    const fd = new FormData()
    fd.append('file', uploadFile.value)
    const res: any = await versionApi.selfUpload(fd, (p: number) => { uploadPercent.value = p })
    if (res.code === 0) {
      ElMessage.success('上传完成，节点正在应用升级（即将重启）')
      upStage.value = 'applying'
      pollStatus()
    } else {
      ElMessage.error(res.message || '上传失败')
    }
  } catch (error: any) {
    ElMessage.error(error.response?.data?.message || '上传失败')
  } finally {
    uploadingNow.value = false
  }
}

let pollTimer: number | undefined
let statusTimer: number | undefined

onMounted(() => {
  loadCurrent()
  loadPMVersions()
  statusTimer = window.setInterval(loadCurrent, 10000)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (statusTimer) clearInterval(statusTimer)
})
</script>

<style scoped>
.empty-state {
  padding: 30px 0;
}

.form-tip {
  font-size: 12px;
  color: var(--app-text-tertiary);
  line-height: 1.5;
}
</style>
