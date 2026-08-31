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
    </div>

    <!-- 升级进度弹窗 -->
    <el-dialog v-model="showUpDialog" :close-on-click-modal="false" :close-on-press-escape="false" :show-close="upStage === 'failed'"
      width="440px" align-center>
      <template #header>
        <span>{{ dialogTitle }}</span>
      </template>

      <!-- 拉取中 -->
      <template v-if="upStage === 'pulling'">
        <div class="up-dialog-body">
          <el-progress :percentage="percent" :indeterminate="true" :duration="2"
            :stroke-width="10" :show-text="false" style="width:100%" />
          <div class="up-dialog-tip">
            正在从 seeinpm 拉取升级包{{ upTargetVersion ? '（目标版本 ' + upTargetVersion + '）' : '' }}，请勿关闭本页面…
          </div>
        </div>
      </template>

      <!-- 接收升级包 -->
      <template v-else-if="upStage === 'receiving'">
        <div class="up-dialog-body">
          <el-progress :percentage="percent" :stroke-width="12" style="width:100%" />
          <div class="up-dialog-tip">
            正在接收升级包：{{ formatSize(upDoneBytes) }} / {{ formatSize(upTotalBytes) }}，接收完成后自动校验并重启。
          </div>
        </div>
      </template>

      <!-- 应用升级 -->
      <template v-else-if="upStage === 'applying'">
        <div class="up-dialog-body">
          <el-progress :percentage="100" status="success" :stroke-width="12" style="width:100%" />
          <div class="up-dialog-tip">升级包校验通过，节点正在重启恢复（约 1 分钟内自动恢复全部代理），请稍候…</div>
        </div>
      </template>

      <!-- 升级成功 -->
      <template v-else-if="upDone">
        <div class="up-dialog-body">
          <el-result icon="success" title="升级成功" :sub-title="`节点已成功升级到 ${currentVersion}，正在重启，页面即将自动刷新…`" />
        </div>
      </template>

      <!-- 升级失败 -->
      <template v-else-if="upStage === 'failed'">
        <div class="up-dialog-body">
          <el-result icon="error" title="升级失败" :sub-title="upErrMsg" />
        </div>
      </template>
    </el-dialog>

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
        <el-table-column prop="version" label="版本号" width="160">
          <template #default="{ row }">
            <span style="font-family:var(--font-mono,monospace)">{{ row.version }}</span>
            <el-tag v-if="badgeFor(row.version)" :type="badgeFor(row.version).type" size="small" style="margin-left:6px">{{ badgeFor(row.version).text }}</el-tag>
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
              {{ actionLabelFor(row.version) }}
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
          <div class="form-tip" style="margin-bottom:6px">版本号将自动从包内识别；包平台必须与本节点（{{ nodePlatform }}）一致<template v-if="detectedVersion">，已识别：<b style="font-family:var(--font-mono,monospace)">{{ detectedVersion }}</b></template></div>
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
import { ElMessage, ElMessageBox } from 'element-plus'
import type { UploadFile } from 'element-plus'
import { versionApi, statusApi } from '@/api'
import { extractVersionFromFile } from '@/utils/version'
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
const showUpDialog = ref(false)

const busy = computed(() => upStage.value === 'pulling' || upStage.value === 'receiving' || upStage.value === 'applying' || uploadingNow.value)
const uploadingNow = ref(false)

const dialogTitle = computed(() => {
  if (upStage.value === 'failed') return '升级失败'
  if (upDone.value) return '升级成功'
  if (upStage.value === 'applying') return '正在应用升级'
  if (upStage.value === 'receiving') return '正在接收升级包'
  return '正在拉取升级包'
})

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

// 五段版本号逐段数值比较：>0 表示 a 更新
const compareVersion = (a: string, b: string) => {
  const pa = (a || '').split('.').map((x: string) => parseInt(x, 10))
  const pb = (b || '').split('.').map((x: string) => parseInt(x, 10))
  if (pa.length !== 5 || pa.some((n: number) => isNaN(n))) return -1
  if (pb.length !== 5 || pb.some((n: number) => isNaN(n))) return 1
  for (let i = 0; i < 5; i++) {
    if (pa[i] !== pb[i]) return pa[i] > pb[i] ? 1 : -1
  }
  return 0
}

// 相对当前版本的操作标签：重新安装 / 升级到此版本 / 回滚至该版本
const actionLabelFor = (version: string) => {
  if (version === currentVersion.value) return '重新安装'
  if (compareVersion(version, currentVersion.value) > 0) return '升级到此版本'
  return '回滚至该版本'
}
// 版本状态标签：最新 / 当前 / 更低版本（回滚目标）
const badgeFor = (version: string) => {
  if (version === currentVersion.value) return { text: '当前', type: 'info' as const }
  if (compareVersion(version, currentVersion.value) > 0) return { text: '最新', type: 'success' as const }
  return { text: '更低版本', type: 'warning' as const }
}

const preSessionId = ref('')

const loadCurrent = async () => {
  try {
    const res: any = await statusApi.getStatus()
    if (res.code === 0) {
      currentVersion.value = res.data?.version || ''
      nodePlatform.value = res.data?.platform || ''
      // 升级等待期记录升级前的会话 ID，作为"节点确实重启过"的判定依据
      if (!preSessionId.value) preSessionId.value = res.data?.sessionId || ''
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

const detectedVersion = ref('')

const onFileChange = async (file: UploadFile) => {
  uploadFile.value = file.raw
  detectedVersion.value = ''
  if (file.raw) {
    detectedVersion.value = (await extractVersionFromFile(file.raw)) || ''
    if (detectedVersion.value) ElMessage.success(`已识别包内版本号：${detectedVersion.value}`)
    else ElMessage.warning('未能从包内识别版本号，请确认包由 scripts/build.sh 构建')
  }
}

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
        upErrMsg.value = d.error || '未知错误'
        showUpDialog.value = true
        ElMessage.error('升级失败：' + (d.error || '未知错误'))
        stopPoll()
      } else if (d.stage === 'idle') {
        // 状态归位：进程已重启，检查版本号判定完成
        stopPoll()
        await loadCurrent()
        // 会话 ID 变化 = 节点确实重启过（重装同版本时版本号恒等，只能靠会话区分）
        const restarted = !!(preSessionId.value && res.data?.sessionId && res.data.sessionId !== preSessionId.value)
        if (upTargetVersion.value && restarted && currentVersion.value === upTargetVersion.value) {
          upDone.value = true
          showUpDialog.value = true
          ElMessage.success(`升级完成：节点已运行 ${currentVersion.value}，代理已自动恢复`)
          // 确认升级成功后，等待 3 秒刷新页面
          window.setTimeout(() => { window.location.reload() }, 3000)
        } else if (upTargetVersion.value) {
          showUpDialog.value = true
          upStage.value = 'failed'
          upErrMsg.value = '节点已重启，请确认当前版本（预期 ' + upTargetVersion.value + '）'
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
  // 风险确认：告知升级会中断本节点代理
  try {
    await ElMessageBox.confirm(
      `${actionLabelFor(version)} ${version} 期间，本节点的全部代理将中断，客户端无法使用映射端口，操作完成后节点自动重连并恢复端口映射。\n\n是否确认开始？`,
      '升级风险提示',
      { type: 'warning', confirmButtonText: '我已了解，开始操作', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return // 用户取消，不执行升级
  }
  try {
    // 记录当前会话 ID：节点重启重连后会变化，用于判定升级完成
    const st: any = await statusApi.getStatus()
    if (st.code === 0) preSessionId.value = st.data?.sessionId || preSessionId.value
    const res: any = await versionApi.pmUpgrade(version)
    if (res.code === 0) {
      upTargetVersion.value = version
      upDone.value = false
      upStage.value = 'pulling'
      showUpDialog.value = true
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
  // 风险确认：告知升级会中断本节点代理
  try {
    await ElMessageBox.confirm(
      `上传并应用升级包期间，本节点的全部代理将中断，客户端无法使用映射端口，升级完成后节点自动重连并恢复端口映射。\n\n是否确认开始升级？`,
      '升级风险提示',
      { type: 'warning', confirmButtonText: '我已了解，开始升级', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return // 用户取消，不执行升级
  }
  uploadingNow.value = true
  try {
    const fd = new FormData()
    fd.append('file', uploadFile.value)
    const res: any = await versionApi.selfUpload(fd, (p: number) => { uploadPercent.value = p })
    if (res.code === 0) {
      // 后端自动识别包内版本，作为完成判定依据
      upTargetVersion.value = res.data?.version || ''
      upDone.value = false
      ElMessage.success('上传完成，节点正在应用升级（即将重启）')
      upStage.value = 'applying'
      showUpDialog.value = true
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

<style>
/* 升级弹窗（el-dialog 经 teleport 渲染到 body，须用非 scoped 样式） */
.up-dialog-body {
  padding: 6px 4px 4px;
}
.up-dialog-tip {
  margin-top: 14px;
  font-size: 13px;
  color: var(--app-text-secondary, #606266);
  line-height: 1.6;
  text-align: center;
}
</style>
