<template>
  <Layout>
    <!-- 版本包列表 -->
    <div class="page-card" style="padding:20px;margin-bottom:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>版本包（seeinps）</span>
        <el-button type="primary" @click="openUpload">上传版本</el-button>
      </div>

      <div v-if="versions.length === 0" class="empty-state">
        <el-empty description="暂无版本包，点击右上角「上传版本」发布第一个版本" />
      </div>

      <el-table v-else :data="versions" style="width: 100%">
        <el-table-column prop="version" label="版本号" width="150">
          <template #default="{ row }">
            <span style="font-family:var(--font-mono,monospace)">{{ row.version }}</span>
            <el-tag v-if="row.version === latestFor(row.goos, row.goarch)" type="success" size="small" style="margin-left:6px">最新</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="平台" width="130">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ row.goos }} / {{ row.goarch }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="fileName" label="文件名" min-width="160" show-overflow-tooltip />
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatSize(row.fileSize) }}</template>
        </el-table-column>
        <el-table-column label="SHA256" width="150">
          <template #default="{ row }">
            <span style="font-family:var(--font-mono,monospace);font-size:12px">{{ row.sha256.slice(0, 12) }}…</span>
            <el-button type="primary" link size="small" @click="copyText(row.sha256)">复制</el-button>
          </template>
        </el-table-column>
        <el-table-column prop="note" label="更新说明" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">{{ row.note || '-' }}</template>
        </el-table-column>
        <el-table-column prop="releasedBy" label="发布人" width="100" />
        <el-table-column label="上传时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button type="danger" link @click="deleteVersion(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 在线节点 -->
    <div class="page-card" style="padding:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>在线节点</span>
        <el-button @click="refreshNow" :loading="refreshing">刷新</el-button>
      </div>

      <div v-if="clients.length === 0" class="empty-state">
        <el-empty description="暂无在线的 seeinps 节点" />
      </div>

      <el-table v-else :data="clients" style="width: 100%">
        <el-table-column prop="username" label="用户名" min-width="120" />
        <el-table-column label="连接状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.upgrading ? 'warning' : 'success'" size="small">{{ row.upgrading ? '升级中' : '在线' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="当前版本" width="170">
          <template #default="{ row }">
            <span style="font-family:var(--font-mono,monospace)">{{ row.version || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="平台" width="130">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ row.goos || '?' }} / {{ row.goarch || '?' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="升级状态" width="120">
          <template #default="{ row }">
            <el-tag v-if="row.upgradable" type="warning" size="small">可升级</el-tag>
            <el-tag v-else type="success" size="small">已是最新</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最后心跳" width="170">
          <template #default="{ row }">{{ formatTime(row.lastPing) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-button type="primary" link :disabled="row.upgrading || versions.length === 0" @click="openUpgrade(row)">升级</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="form-tip" style="margin-top:12px">
        升级过程中该节点的所有代理会短暂断开，节点升级完成后自动重连并恢复端口映射；升级结果以节点重连后上报的版本号为准。
      </div>
    </div>

    <!-- 上传版本弹窗 -->
    <el-dialog v-model="showUpload" title="上传版本" width="560px" :close-on-click-modal="false" @closed="resetUploadForm">
      <el-form :model="uploadForm" :rules="uploadRules" ref="uploadFormRef" label-width="90px">
        <el-form-item label="适用端">
          <el-select v-model="uploadForm.endpoint" style="width: 100%">
            <el-option label="seeinps（服务端代理）" value="seeinps" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标平台" prop="platform">
          <el-select v-model="uploadForm.platform" style="width: 100%">
            <el-option v-for="p in platforms" :key="p.value" :value="p.value" :label="p.label" />
          </el-select>
          <div class="form-tip">必须与目标 seeinps 节点的操作系统和架构一致，否则升级时会被拒绝</div>
        </el-form-item>
        <el-form-item label="版本号" prop="version">
          <el-input v-model="uploadForm.version" placeholder="留空自动识别包内版本" />
          <div class="form-tip">五段数字（大版本.大版本.年.月日.当日序号），留空将自动从包内识别；填写了则校验与包内一致；同一版本号可分别上传多个平台的包</div>
        </el-form-item>
        <el-form-item label="更新说明" prop="note">
          <el-input v-model="uploadForm.note" type="textarea" :rows="3" placeholder="本次版本更新的内容摘要（可选）" />
        </el-form-item>
        <el-form-item label="安装包" prop="file">
          <el-upload ref="uploadRef" :limit="1" :auto-upload="false" :on-change="onFileChange" :on-remove="() => uploadForm.file = null"
            :disabled="uploading" drag style="width:100%">
            <div style="padding:12px 0">拖拽或点击选择 seeinps 二进制文件（≤200MB）</div>
          </el-upload>
        </el-form-item>
        <el-form-item v-if="uploading" label="">
          <el-progress :percentage="uploadPercent" style="width:100%" />
          <div class="form-tip">正在上传安装包（{{ formatSize(uploadForm.file?.size) }}），慢链路可能需要几分钟，请勿关闭页面</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="uploading" @click="showUpload = false">取消</el-button>
        <el-button type="primary" :loading="uploading" @click="handleUpload">上传</el-button>
      </template>
    </el-dialog>

    <!-- 升级确认 / 进度弹窗 -->
    <el-dialog v-model="showUpgrade" title="节点升级" width="520px" :close-on-click-modal="false" :before-close="guardUpgradeClose">
      <template v-if="upgradePhase === 'confirm'">
        <div style="margin-bottom:14px">
          节点 <b>{{ upgradeForm.username }}</b>（当前版本
          <span style="font-family:var(--font-mono,monospace)">{{ upgradeForm.currentVersion || '-' }}</span>）
        </div>
        <el-form label-width="90px">
          <el-form-item label="目标版本">
            <el-select v-model="upgradeForm.versionId" style="width: 100%">
              <el-option v-for="v in versions.filter((x: any) => x.goos === upgradeForm.goos && x.goarch === upgradeForm.goarch)" :key="v.id" :value="v.id"
                :label="v.version + (compareVersion(v.version, upgradeForm.currentVersion) > 0 ? '（升级）' : '（回滚）')" />
            </el-select>
            <div class="form-tip" v-if="!versions.some((x: any) => x.goos === upgradeForm.goos && x.goarch === upgradeForm.goarch)">
              该节点的平台（{{ upgradeForm.goos }}/{{ upgradeForm.goarch }}）暂无可用版本包，请先上传对应平台的包
            </div>
          </el-form-item>
        </el-form>
        <el-alert type="warning" :closable="false" title="升级期间该节点全部代理会短暂断开，升级完成后自动恢复。若升级失败，可在节点上用 seeinps.old 手动恢复。"
          style="margin-bottom:6px" />
      </template>
      <template v-else>
        <div style="margin-bottom:14px">
          节点 <b>{{ upgradeForm.username }}</b> → 目标版本
          <span style="font-family:var(--font-mono,monospace)">{{ upgradeTargetVersion }}</span>
        </div>
        <el-progress :percentage="upgradePercent"
          :status="upgradeFailed ? 'exception' : (upgradeDone ? 'success' : undefined)" style="width:100%" />
        <div class="form-tip" style="margin-top:10px">
          {{ upgradeStageText }}
          <span v-if="upgradeStage === 'transferring'">（{{ formatSize(upgradeProgressSent) }} / {{ formatSize(upgradeProgressTotal) }}）</span>
        </div>
        <el-alert v-if="upgradeDone" type="success" :closable="false" style="margin-top:10px"
          :title="`✅ 升级完成，节点已运行 ${upgradeTargetVersion}，代理已自动恢复`" />
        <el-alert v-if="upgradeFailed" type="error" :closable="false" :title="'升级失败：' + upgradeErrMsg" style="margin-top:10px" />
      </template>
      <template #footer>
        <template v-if="upgradePhase === 'confirm'">
          <el-button @click="showUpgrade = false">取消</el-button>
          <el-button type="primary" :loading="upgrading" @click="handleUpgrade">确认升级</el-button>
        </template>
        <el-button v-else type="primary" @click="closeUpgradeProgress">关闭</el-button>
      </template>
    </el-dialog>
  </Layout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules, UploadFile } from 'element-plus'
import { versionApi, clientApi } from '@/api'
import Layout from '@/components/Layout.vue'

const versions = ref<any[]>([])
const clients = ref<any[]>([])
const refreshing = ref(false)

const showUpload = ref(false)
const uploading = ref(false)
const uploadPercent = ref(0)
const uploadFormRef = ref<FormInstance>()
const uploadRef = ref()
const uploadForm = reactive({ endpoint: 'seeinps', platform: 'linux/amd64', version: '', note: '', file: null as any })

const showUpgrade = ref(false)
const upgrading = ref(false)
const upgradeForm = reactive({ username: '', currentVersion: '', versionId: 0 as number, goos: '', goarch: '' })

const platforms = [
  { value: 'linux/amd64', label: 'Linux x86_64 (amd64)' },
  { value: 'linux/arm64', label: 'Linux ARM64 (arm64)' },
  { value: 'windows/amd64', label: 'Windows x86_64 (amd64)' },
  { value: 'darwin/amd64', label: 'macOS Intel (amd64)' },
  { value: 'darwin/arm64', label: 'macOS Apple Silicon (arm64)' }
]

// 同平台最新版本号（「最新」标签与升级选项默认值用）
const latestFor = (goos: string, goarch: string) => {
  for (const v of versions.value) {
    if (v.goos === goos && v.goarch === goarch) return v.version
  }
  return ''
}

const uploadRules: FormRules = {
  version: [
    {
      validator: (_r: any, v: string, cb: any) => {
        const [upGoos, upGoarch] = uploadForm.platform.split('/')
        if (v && !/^\d{1,4}(\.\d{1,4}){4}$/.test(v)) return cb(new Error('版本号须为五段数字，如 1.0.26.0829.01'))
        if (versions.value.some((x: any) => x.version === v && x.goos === upGoos && x.goarch === upGoarch)) return cb(new Error('该版本号的此平台包已存在'))
        cb()
      },
      trigger: 'blur'
    }
  ],
  file: [
    {
      validator: (_r: any, _v: any, cb: any) => {
        if (!uploadForm.file) return cb(new Error('请选择安装包文件'))
        cb()
      },
      trigger: 'change'
    }
  ]
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

const formatTime = (ts?: number) => { if (!ts) return '-'; return new Date(ts * 1000).toLocaleString() }
const formatSize = (n?: number) => {
  if (!n) return '-'
  if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n > 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' B'
}

const copyText = async (text: string) => {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      ElMessage.success('已复制')
      return
    }
  } catch { /* 降级 */ }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.top = '-9999px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) { ElMessage.success('已复制'); return }
    throw new Error('copy failed')
  } catch {
    ElMessage.warning('复制失败，请手动复制')
  }
}

const loadVersions = async () => {
  try {
    const res: any = await versionApi.list('seeinps')
    if (res.code === 0) versions.value = res.data || []
  } catch (error) {
    console.error('Load versions error:', error)
  }
}

const loadClients = async () => {
  try {
    const res: any = await clientApi.list()
    if (res.code === 0) clients.value = res.data || []
  } catch (error) {
    console.error('Load clients error:', error)
  }
}

const refreshNow = async () => {
  refreshing.value = true
  await Promise.all([loadVersions(), loadClients()])
  refreshing.value = false
}

const openUpload = () => {
  uploadForm.endpoint = 'seeinps'
  uploadForm.platform = 'linux/amd64'
  uploadForm.version = ''
  uploadForm.note = ''
  uploadForm.file = null
  showUpload.value = true
}

const onFileChange = (file: UploadFile) => {
  uploadForm.file = file.raw
  uploadFormRef.value?.validateField('file')
}

const resetUploadForm = () => {
  uploadFormRef.value?.clearValidate()
  uploadRef.value?.clearFiles()
}

const handleUpload = async () => {
  if (!uploadFormRef.value) return
  await uploadFormRef.value.validate(async (valid) => {
    if (!valid) return
    uploading.value = true
    try {
      const fd = new FormData()
      fd.append('endpoint', uploadForm.endpoint)
      const [goos, goarch] = uploadForm.platform.split('/')
      fd.append('goos', goos)
      fd.append('goarch', goarch)
      fd.append('version', uploadForm.version)
      fd.append('note', uploadForm.note)
      fd.append('file', uploadForm.file)
      uploadPercent.value = 0
      const res: any = await versionApi.upload(fd, (p: number) => { uploadPercent.value = p })
      if (res.code === 0) {
        ElMessage.success(`版本 ${res.data.version} 上传成功`)
        showUpload.value = false
        loadVersions()
      } else {
        ElMessage.error(res.message || '上传失败')
      }
    } catch (error: any) {
      ElMessage.error(error.response?.data?.message || '上传失败')
    } finally {
      uploading.value = false
    }
  })
}

const deleteVersion = async (row: any) => {
  try {
    await ElMessageBox.confirm(`确定删除版本 ${row.version} 吗？已上传的安装包文件将一并删除。`, '确认删除', {
      type: 'warning', confirmButtonText: '确定', cancelButtonText: '取消'
    })
    const res: any = await versionApi.remove(row.id)
    if (res.code === 0) {
      ElMessage.success('删除成功')
      loadVersions()
    } else {
      ElMessage.error(res.message || '删除失败')
    }
  } catch (error: any) {
    if (error !== 'cancel') ElMessage.error(error.response?.data?.message || '删除失败')
  }
}

const openUpgrade = (row: any) => {
  upgradeForm.username = row.username
  upgradeForm.currentVersion = row.version || ''
  upgradeForm.goos = row.goos || ''
  upgradeForm.goarch = row.goarch || ''
  upgradeForm.versionId = versions.value.length ? versions.value[0].id : 0
  // 默认选中该节点平台的最新包
  for (const v of versions.value) {
    if (v.goos === upgradeForm.goos && v.goarch === upgradeForm.goarch) {
      upgradeForm.versionId = v.id
      break
    }
  }
  upgradePhase.value = 'confirm'
  upgradeDone.value = false
  showUpgrade.value = true
}

const handleUpgrade = async () => {
  if (!upgradeForm.versionId) {
    ElMessage.warning('请选择目标版本')
    return
  }
  upgrading.value = true
  try {
    const res: any = await clientApi.upgrade(upgradeForm.username, upgradeForm.versionId)
    if (res.code === 0) {
      const target = versions.value.find((v: any) => v.id === upgradeForm.versionId)
      upgradeTargetVersion.value = target ? target.version : ''
      upgradePhase.value = 'progress'
      upgradeStage.value = 'notifying'
      upgradeErrMsg.value = ''
      upgradeProgressSent.value = 0
      upgradeProgressTotal.value = res.data?.totalBytes || 0
      startUpgradePoll()
      // 传输完成到"校验通过"回报可能丢失（节点重启瞬间），盯节点版本号兜底判定完成
      watchNodeVersion()
    } else {
      ElMessage.error(res.message || '升级失败')
    }
  } catch (error: any) {
    ElMessage.error(error.response?.data?.message || '升级失败')
  } finally {
    upgrading.value = false
  }
}

// ===== 升级进度轮询 =====
const upgradePhase = ref<'confirm' | 'progress'>('confirm')
const upgradeStage = ref('none')
const upgradeErrMsg = ref('')
const upgradeProgressSent = ref(0)
const upgradeProgressTotal = ref(0)
const upgradeTargetVersion = ref('')
let upgradePollTimer: number | undefined
let upgradeWatchTimer: number | undefined

const upgradeFailed = computed(() => upgradeStage.value === 'failed')

const upgradeDone = ref(false)

const upgradeStageText = computed(() => {
  if (upgradeDone.value) return '✅ 升级完成'
  return ({
    none: '等待开始…',
    notifying: '正在通知节点…',
    transferring: '正在传输升级包',
    sent: '传输完成，等待节点校验…',
    verified: '校验通过，节点正在重启恢复…',
    failed: '升级失败'
  }[upgradeStage.value] || upgradeStage.value)
})

const upgradePercent = computed(() => {
  if (upgradeStage.value === 'verified') return 100
  if (upgradeProgressTotal.value > 0 && (upgradeStage.value === 'transferring' || upgradeStage.value === 'sent')) {
    return Math.min(100, Math.round((upgradeProgressSent.value / upgradeProgressTotal.value) * 100))
  }
  return 0
})

const stopUpgradePoll = () => {
  if (upgradePollTimer) { clearInterval(upgradePollTimer); upgradePollTimer = undefined }
}

const stopUpgradeWatch = () => {
  if (upgradeWatchTimer) { clearInterval(upgradeWatchTimer); upgradeWatchTimer = undefined }
}

// 传输中拦截误关弹窗（关闭不影响服务端继续传输，但进度会看不到）
const guardUpgradeClose = (done: () => void) => {
  if (upgradeStage.value === 'transferring' || upgradeStage.value === 'notifying') {
    ElMessage.warning('升级正在传输中，请等待传输完成后再关闭（关闭不会中断升级）')
    return
  }
  stopUpgradePoll()
  stopUpgradeWatch()
  done()
}

const closeUpgradeProgress = () => {
  stopUpgradePoll()
  stopUpgradeWatch()
  showUpgrade.value = false
}

const startUpgradePoll = () => {
  stopUpgradePoll()
  upgradePollTimer = window.setInterval(async () => {
    try {
      const res: any = await clientApi.upgradeStatus(upgradeForm.username)
      if (res.code !== 0) return
      const d = res.data
      upgradeStage.value = d.stage
      upgradeProgressSent.value = d.sentBytes || 0
      upgradeProgressTotal.value = d.totalBytes || 0
      if (d.stage === 'verified') {
        stopUpgradePoll()
        ElMessage.success('升级包校验通过，节点正在重启恢复（约 1 分钟内自动恢复全部代理）')
        watchNodeVersion()
      } else if (d.stage === 'failed') {
        stopUpgradePoll()
        upgradeErrMsg.value = d.error || '未知错误'
        ElMessage.error('升级失败：' + upgradeErrMsg.value)
        loadClients()
      } else if (d.stage === 'none') {
        stopUpgradePoll()
        upgradeErrMsg.value = '升级状态丢失（PM 可能已重启），请确认节点当前版本后重试'
        ElMessage.warning(upgradeErrMsg.value)
      }
    } catch { /* 轮询错误忽略，下个周期重试 */ }
  }, 2000)
}

// 校验通过后盯节点版本：重连上报新版本即报成功，3 分钟未恢复给提示
const watchNodeVersion = () => {
  if (upgradeWatchTimer) return // 已在盯守，避免重复实例导致重复提示
  stopUpgradeWatch()
  let waited = 0
  upgradeWatchTimer = window.setInterval(async () => {
    waited += 3
    await loadClients()
    const row = clients.value.find((c: any) => c.username === upgradeForm.username)
    if (row && row.version === upgradeTargetVersion.value) {
      stopUpgradeWatch()
      stopUpgradePoll()
      upgradeDone.value = true
      ElMessage.success(`升级完成：节点已运行 ${upgradeTargetVersion.value}，代理已自动恢复`)
    } else if (waited > 180) {
      stopUpgradeWatch()
      ElMessage.warning('节点尚未恢复在线，请稍后在列表确认版本；如长时间未恢复请登录节点检查日志')
    }
  }, 3000)
}

let pollTimer: number | undefined

onMounted(() => {
  loadVersions()
  loadClients()
  pollTimer = window.setInterval(loadClients, 10000)
})

onUnmounted(() => {
  if (pollTimer) window.clearInterval(pollTimer)
  stopUpgradePoll()
  stopUpgradeWatch()
})
</script>

<style scoped>
.empty-state {
  padding: 40px 0;
}

.form-tip {
  font-size: 12px;
  color: var(--app-text-tertiary);
  line-height: 1.5;
}
</style>
