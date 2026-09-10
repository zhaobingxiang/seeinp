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

      <el-table v-else :data="pageVersions" style="width: 100%">
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
      <PaginationBar v-if="versions.length > 0" v-model:page="versionsPage" v-model:pageSize="versionsPageSize" :total="versions.length" />
    </div>

    <!-- 在线节点 -->
    <div class="page-card" style="padding:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>在线节点</span>
        <div>
          <el-button v-if="batchRunning" type="warning" plain @click="reopenBatch">批量升级任务进行中</el-button>
          <el-button type="primary" :disabled="clients.length === 0 || versions.length === 0" @click="openBatch">批量升级</el-button>
          <el-button @click="refreshNow" :loading="refreshing">刷新</el-button>
        </div>
      </div>

      <div v-if="clients.length === 0" class="empty-state">
        <el-empty description="暂无在线的 seeinps 节点" />
      </div>

      <el-table v-else :data="pageClients" style="width: 100%" row-key="username" ref="clientsTableRef"
        @selection-change="(rows: any[]) => (selectedClients = rows)">
        <el-table-column type="selection" width="42" reserve-selection :selectable="() => !batchRunning" />
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
      <PaginationBar v-if="clients.length > 0" v-model:page="clientsPage" v-model:pageSize="clientsPageSize" :total="clients.length" />
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

    <!-- 批量升级弹窗 -->
    <el-dialog v-model="showBatch" title="批量升级" width="820px" :close-on-click-modal="false"
      :before-close="(done: any) => (batchPhase === 'progress' ? (showBatch = false, done()) : done())">
      <template v-if="batchPhase === 'config'">
        <el-alert type="warning" :closable="false" style="margin-bottom:14px"
          title="批量升级将按并发数逐个重启所选节点，期间这些节点的全部代理会短暂断开；建议安排在业务低峰期执行。" />
        <div class="form-tip" style="margin-bottom:10px">已选择 <b>{{ selectedClients.length }}</b> 个节点（未勾选任何节点时默认为全部在线节点），按平台匹配升级包：</div>
        <el-table :data="batchPlatformGroups" size="small" style="margin-bottom:14px">
          <el-table-column label="平台" width="150">
            <template #default="{ row }">
              <el-tag size="small" type="info">{{ row.goos }} / {{ row.goarch }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="count" label="节点数" width="80" />
          <el-table-column label="目标版本包">
            <template #default="{ row }">
              <el-select v-if="row.packages.length" v-model="batchPkgs[row.key]" style="width:100%">
                <el-option v-for="p in row.packages" :key="p.id" :value="p.id" :label="p.version + (row.sampleVersion && compareVersion(p.version, row.sampleVersion) > 0 ? '（升级）' : '（回滚）')" />
              </el-select>
              <span v-else style="color:var(--el-color-danger);font-size:12px">无该平台版本包，这些节点将被跳过</span>
            </template>
          </el-table-column>
        </el-table>
        <el-form label-width="110px">
          <el-form-item label="金丝雀先行">
            <el-switch v-model="batchCanary" />
            <div class="form-tip">先升级 1 台并等待验证通过，再放开其余节点；金丝雀失败将中止整个批次</div>
          </el-form-item>
          <el-form-item label="并发数">
            <el-input-number v-model="batchConcurrency" :min="1" :max="8" />
            <div class="form-tip">同时传输的节点数。升级包经 seeinpm 上行带宽分发，并发越高每台越慢，一般 2~3 即可</div>
          </el-form-item>
        </el-form>
      </template>
      <template v-else>
        <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:10px">
          <span>任务 <span style="font-family:var(--font-mono,monospace)">{{ batchTask?.id }}</span></span>
          <el-tag size="small" :type="batchPhaseDoneUI ? 'success' : 'warning'">{{ batchPhaseDoneUI ? '已结束' : '进行中' }}</el-tag>
          <el-tag size="small" type="info">等待 {{ batchCount('pending') }}</el-tag>
          <el-tag size="small" type="warning">进行中 {{ batchCount('running') }}</el-tag>
          <el-tag size="small" type="success">成功 {{ batchCount('verified') }}</el-tag>
          <el-tag size="small" type="danger">失败 {{ batchCount('failed') }}</el-tag>
          <el-tag size="small">跳过 {{ batchCount('skipped') }}</el-tag>
        </div>
        <el-alert v-if="batchCanaryNote" :closable="false" :type="batchCanaryNote.type" :title="batchCanaryNote.text" style="margin-bottom:10px" />
        <el-table :data="batchTask?.nodes || []" size="small" max-height="380">
          <el-table-column prop="username" label="节点" min-width="110" show-overflow-tooltip />
          <el-table-column label="角色" width="72">
            <template #default="{ row }">
              <el-tag v-if="row.role === 'canary'" size="small" type="warning">金丝雀</el-tag>
              <span v-else>-</span>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="190">
            <template #default="{ row }">
              <span style="font-family:var(--font-mono,monospace);font-size:12px">{{ row.currentVersion || '?' }} → {{ row.version || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="88">
            <template #default="{ row }">
              <el-tag size="small" :type="(batchStageTag[row.stage] || 'info') as any">{{ batchStageText[row.stage] || row.stage }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="传输进度" width="130">
            <template #default="{ row }">
              <el-progress v-if="row.totalBytes > 0" :percentage="Math.min(100, Math.round(row.sentBytes / row.totalBytes * 100))" :stroke-width="6"
                :status="row.stage === 'failed' ? 'exception' : (row.stage === 'verified' ? 'success' : undefined)" />
              <span v-else>-</span>
            </template>
          </el-table-column>
          <el-table-column prop="reason" label="说明" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">{{ row.reason || '-' }}</template>
          </el-table-column>
        </el-table>
      </template>
      <template #footer>
        <template v-if="batchPhase === 'config'">
          <el-button @click="showBatch = false">取消</el-button>
          <el-button type="danger" :loading="batchStarting" @click="startBatch">开始批量升级</el-button>
        </template>
        <template v-else>
          <el-button v-if="!batchPhaseDoneUI" type="warning" @click="stopBatch">停止（仅未开始的节点）</el-button>
          <el-button type="primary" @click="showBatch = false">{{ batchPhaseDoneUI ? '关闭' : '后台运行' }}</el-button>
        </template>
      </template>
    </el-dialog>
  </Layout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watchEffect, onMounted, onUnmounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules, UploadFile } from 'element-plus'
import { versionApi, clientApi } from '@/api'
import { extractVersionFromFile } from '@/utils/version'
import Layout from '@/components/Layout.vue'
import PaginationBar from '@/components/PaginationBar.vue'

const versions = ref<any[]>([])
const clients = ref<any[]>([])
const refreshing = ref(false)

// 版本包 / 在线节点分页（默认 20 条/页，各自独立）
const versionsPage = ref(1)
const versionsPageSize = ref(20)
const clientsPage = ref(1)
const clientsPageSize = ref(20)
const pageVersions = computed(() => versions.value.slice((versionsPage.value - 1) * versionsPageSize.value, versionsPage.value * versionsPageSize.value))
const pageClients = computed(() => clients.value.slice((clientsPage.value - 1) * clientsPageSize.value, clientsPage.value * clientsPageSize.value))
watchEffect(() => {
  const maxV = Math.max(1, Math.ceil(versions.value.length / versionsPageSize.value))
  if (versionsPage.value > maxV) versionsPage.value = maxV
  const maxC = Math.max(1, Math.ceil(clients.value.length / clientsPageSize.value))
  if (clientsPage.value > maxC) clientsPage.value = maxC
})

const showUpload = ref(false)
const uploading = ref(false)
const uploadPercent = ref(0)
const uploadFormRef = ref<FormInstance>()
const uploadRef = ref()
const uploadForm = reactive({ endpoint: 'seeinps', platform: 'linux/amd64', version: '', note: '', file: null as any })

const showUpgrade = ref(false)
const upgrading = ref(false)
const upgradeForm = reactive({ username: '', currentVersion: '', versionId: 0 as number, goos: '', goarch: '', preSessionId: '' })

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

const onFileChange = async (file: UploadFile) => {
  uploadForm.file = file.raw
  uploadFormRef.value?.validateField('file')
  // 本地识别包内版本号并自动填入
  if (file.raw) {
    const detected = await extractVersionFromFile(file.raw)
    if (detected) {
      uploadForm.version = detected
      ElMessage.success(`已识别包内版本号：${detected}`)
    } else {
      ElMessage.warning('未能从包内识别版本号，请手动填写')
    }
  }
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
  upgradeForm.preSessionId = row.sessionId || ''
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
  // 风险确认：告知升级会导致该节点代理断开
  try {
    await ElMessageBox.confirm(
      `节点「${upgradeForm.username}」升级期间，该节点的全部代理将中断，客户端无法使用映射端口，升级完成后节点自动重连并恢复端口映射。\n\n是否已确认并在业务低峰期执行本次升级？`,
      '升级风险提示',
      { type: 'warning', confirmButtonText: '我已了解，开始升级', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return // 用户取消，不执行升级
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
        upgradeErrMsg.value = '升级状态丢失（seeinpm 可能已重启），请确认节点当前版本后重试'
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
    // 会话 ID 变化 = 节点确实重启过：重装（目标版本==当前版本）时版本号恒等，只能靠会话区分
    const restarted = !!(upgradeForm.preSessionId && row && row.sessionId && row.sessionId !== upgradeForm.preSessionId)
    if (row && restarted && row.version === upgradeTargetVersion.value) {
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

// ===== 批量升级 =====
const selectedClients = ref<any[]>([])
const clientsTableRef = ref()
const showBatch = ref(false)
const batchPhase = ref<'config' | 'progress'>('config')
const batchPkgs = reactive<Record<string, number>>({})
const batchCanary = ref(true)
const batchConcurrency = ref(2)
const batchStarting = ref(false)
const batchTask = ref<any>(null)
let batchPollTimer: number | undefined

const batchStageText: Record<string, string> = {
  pending: '等待中', running: '进行中', verified: '成功', failed: '失败', skipped: '跳过'
}
const batchStageTag: Record<string, string> = {
  pending: 'info', running: 'warning', verified: 'success', failed: 'danger', skipped: 'info'
}

const batchCount = (stage: string) => (batchTask.value?.counts || {})[stage] || 0
const batchPhaseDoneUI = computed(() => {
  const t = batchTask.value
  if (!t) return false
  if (t.phase === 'done') return true
  // phase 兜底：PM 重启丢任务时按节点终态判断，避免前端永远"进行中"
  const nodes: any[] = t.nodes || []
  return nodes.length > 0 && nodes.every((n: any) => ['verified', 'failed', 'skipped'].includes(n.stage))
})
const batchRunning = computed(() => !!batchTask.value && !batchPhaseDoneUI.value)

const batchCanaryNote = computed(() => {
  const t = batchTask.value
  if (!t || !t.useCanary) return null
  const canary = (t.nodes || []).find((n: any) => n.role === 'canary')
  if (!canary) return null
  if (canary.stage === 'running' || canary.stage === 'pending') return { type: 'info', text: '金丝雀节点升级中，通过后再放开其余节点' }
  if (canary.stage === 'verified') return { type: 'success', text: '金丝雀已通过，其余节点按计划推进' }
  return { type: 'error', text: '金丝雀失败：' + (canary.reason || '未知原因') + '，批量已中止' }
})

// 目标节点集合（未勾选=全部在线）按平台分组 + 各平台可用包
const batchPlatformGroups = computed(() => {
  const targets = selectedClients.value.length ? selectedClients.value : clients.value
  const map = new Map<string, any>()
  for (const c of targets) {
    const key = (c.goos || '?') + '/' + (c.goarch || '?')
    if (!map.has(key)) map.set(key, { key, goos: c.goos, goarch: c.goarch, count: 0, sampleVersion: c.version, packages: [] as any[] })
    map.get(key).count++
  }
  for (const g of map.values()) {
    g.packages = versions.value.filter((v: any) => v.goos === g.goos && v.goarch === g.goarch)
  }
  return [...map.values()]
})

const openBatch = () => {
  batchPhase.value = 'config'
  for (const k of Object.keys(batchPkgs)) delete batchPkgs[k]
  for (const g of batchPlatformGroups.value) {
    if (g.packages.length) batchPkgs[g.key] = g.packages[0].id // 版本列表按新→旧排，默认最新
  }
  showBatch.value = true
}

const reopenBatch = () => {
  if (batchTask.value) { batchPhase.value = 'progress'; showBatch.value = true }
}

const startBatch = async () => {
  const targets = selectedClients.value.length ? selectedClients.value : clients.value
  const packages = batchPlatformGroups.value
    .filter((g: any) => g.packages.length && batchPkgs[g.key])
    .map((g: any) => ({ versionId: batchPkgs[g.key], goos: g.goos, goarch: g.goarch }))
  if (!packages.length) { ElMessage.warning('没有任何平台选到了版本包'); return }
  try {
    await ElMessageBox.confirm(
      `将对 ${targets.length} 个节点执行批量升级（并发 ${batchConcurrency.value}${batchCanary.value ? '，金丝雀先行' : ''}）。升级期间这些节点的全部代理将中断并自动恢复，请确认已在业务低峰期。\n\n是否开始？`,
      '批量升级风险提示',
      { type: 'warning', confirmButtonText: '我已了解，开始升级', cancelButtonText: '取消', confirmButtonClass: 'el-button--danger' }
    )
  } catch { return }
  batchStarting.value = true
  try {
    const res: any = await clientApi.upgradeBatch({
      packages,
      usernames: selectedClients.value.length ? targets.map((c: any) => c.username) : undefined,
      concurrency: batchConcurrency.value,
      useCanary: batchCanary.value
    })
    if (res.code === 0) {
      batchTask.value = res.data
      batchPhase.value = 'progress'
      startBatchPoll()
    } else {
      ElMessage.error(res.message || '批量任务创建失败')
    }
  } catch (error: any) {
    ElMessage.error(error.response?.data?.message || '批量任务创建失败')
  } finally {
    batchStarting.value = false
  }
}

const stopBatch = async () => {
  try {
    await ElMessageBox.confirm('停止后仅跳过尚未开始的节点，传输/重启中的节点不会被打断。', '确认停止', { type: 'warning' })
    const res: any = await clientApi.batchStop(batchTask.value.id)
    if (res.code === 0) batchTask.value = res.data
    else ElMessage.error(res.message || '停止失败')
  } catch { /* 取消或失败提示 */ }
}

const stopBatchPoll = () => { if (batchPollTimer) { clearInterval(batchPollTimer); batchPollTimer = undefined } }

const startBatchPoll = () => {
  stopBatchPoll()
  batchPollTimer = window.setInterval(async () => {
    const t = batchTask.value
    if (!t) { stopBatchPoll(); return }
    try {
      const res: any = await clientApi.batchStatus(t.id)
      if (res.code !== 0) return
      batchTask.value = res.data
      if (batchPhaseDoneUI.value) {
        stopBatchPoll()
        const c = res.data.counts || {}
        ElMessage({ type: (c.failed ? 'warning' : 'success'), message: `批量升级结束：成功 ${c.verified || 0} / 失败 ${c.failed || 0} / 跳过 ${c.skipped || 0}`, duration: 8000 })
        loadClients()
      }
    } catch { /* 忽略瞬时轮询错误 */ }
  }, 2500)
}

let pollTimer: number | undefined

onMounted(() => {
  loadVersions()
  loadClients()
  pollTimer = window.setInterval(loadClients, 10000)
  // 刷新页面后恢复进行中的批量任务视图（任务本体在 PM 内存中持续执行）
  clientApi.batches().then((res: any) => {
    if (res.code !== 0) return
    const active = (res.data.items || []).find((t: any) =>
      t.phase !== 'done' && (t.nodes || []).some((n: any) => ['pending', 'running'].includes(n.stage)))
    if (active) { batchTask.value = active; startBatchPoll() }
  }).catch(() => {})
})

onUnmounted(() => {
  if (pollTimer) window.clearInterval(pollTimer)
  stopUpgradePoll()
  stopUpgradeWatch()
  stopBatchPoll()
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
