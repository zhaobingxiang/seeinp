<template>
  <div class="deploy-page">
    <!-- 侧栏 -->
    <aside class="deploy-sider">
      <div class="sider-logo">
        <div class="logo-icon"><el-icon><Promotion /></el-icon></div>
        <div class="brand-name">seeinps <span>部署工具</span></div>
      </div>
      <div class="sider-sub">
        本工具将引导你完成 seeinps 的部署与初始化：选择部署目标（本机 Windows 或远程 Linux），
        校验 seeinpm 授权后，自动从 seeinpm 下载匹配当前系统与架构的最新安装包，完成安装、
        服务注册与网页账号初始化。
      </div>

      <div class="srv-config" style="margin-top: auto">
        <el-collapse v-model="activeConfig">
          <el-collapse-item name="server">
            <template #title>
              <span style="font-size:13px;color:var(--app-text-secondary)">
                <el-icon style="vertical-align:-2px"><Setting /></el-icon> 服务器参数配置
              </span>
            </template>
            <el-form label-position="top" size="small" @submit.prevent>
              <el-form-item label="seeinpm 服务器地址">
                <el-input v-model="serverAddr" placeholder="see.timemsee.cn" @change="saveServerConfig" />
              </el-form-item>
              <el-form-item label="API 端口">
                <el-input-number v-model="serverPort" :min="1" :max="65535" controls-position="right"
                  style="width:100%" placeholder="90" @change="saveServerConfig" />
              </el-form-item>
              <div class="sider-sub" style="font-size:12px;margin:0">
                默认 address see.timemsee.cn、端口 90。通常在获得 seeinpm 管理端后无需修改。
              </div>
            </el-form>
          </el-collapse-item>
        </el-collapse>
      </div>
    </aside>

    <!-- 主区 -->
    <main class="deploy-main">
      <div class="page-title">seeinps 一键部署</div>
      <div class="page-desc">部署前请确认已通过 seeinpm 获取用户名与授权码。</div>

      <!-- 步骤 1 / 2：目标 + 授权 -->
      <div class="deploy-card" v-show="stepText === 'form'">
        <div class="card-heading"><el-icon><Monitor /></el-icon> 1. 选择部署目标</div>
        <div class="target-tabs">
          <div class="target-tab" :class="{ 'is-active': target.kind === 'windows' }" @click="chooseTarget('windows')">
            <div class="tab-icon"><el-icon size="20"><Monitor /></el-icon></div>
            <div class="tab-name">本机部署（Windows）</div>
            <div class="tab-desc">安装并托管为 Windows 服务</div>
          </div>
          <div class="target-tab" :class="{ 'is-active': target.kind === 'linux' }" @click="chooseTarget('linux')">
            <div class="tab-icon"><el-icon size="20"><Platform /></el-icon></div>
            <div class="tab-name">远程部署（Linux）</div>
            <div class="tab-desc">SSH / systemd 托管</div>
          </div>
        </div>

        <!-- 管理员权限提示（本机 Windows 目标且非管理员运行时） -->
        <el-alert v-if="needElevate" type="warning" :closable="false" show-icon style="margin-top:16px">
          <template #title>
            Windows 服务注册需要管理员权限。请以管理员身份运行本工具，或点击下方按钮提权重启。
            <el-button link type="primary" :disabled="elevating" @click="doElevate" style="margin-left:8px">
              {{ elevating ? '正在提权…' : '提权重启' }}
            </el-button>
          </template>
        </el-alert>

        <!-- Linux 目标表单 -->
        <el-form v-if="target.kind === 'linux'" label-position="top" ref="linuxFormRef" :model="target"
          :rules="linuxRules" style="margin-top:16px" @submit.prevent>
          <el-row :gutter="16">
            <el-col :span="14">
              <el-form-item label="服务器地址" prop="host">
                <el-input v-model.trim="target.host" placeholder="如 192.168.1.10 或 server.example.com" />
              </el-form-item>
            </el-col>
            <el-col :span="10">
              <el-form-item label="SSH 端口" prop="sshPort">
                <el-input-number v-model="target.sshPort" :min="1" :max="65535" controls-position="right"
                  style="width:100%" :placeholder="22" />
              </el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="SSH 用户名" prop="username">
            <el-input v-model.trim="target.username" placeholder="root 或普通用户" />
          </el-form-item>
          <el-form-item label="SSH 登录密码" prop="password">
            <el-input v-model="target.password" type="password" show-password placeholder="SSH 登录密码" />
          </el-form-item>
          <el-form-item v-if="target.username !== 'root'" label="root 用户密码（用于提权安装服务）" prop="rootPass">
            <el-input v-model="target.rootPass" type="password" show-password placeholder="非 root 用户需填写，用于 sudo 提权" />
          </el-form-item>
        </el-form>

        <div v-if="target.kind === 'linux'" style="margin-top:4px">
          <el-button type="warning" plain :disabled="sshTesting" @click="testSSHConn">
            {{ sshTesting ? '正在测试连接…' : (sshTested ? '已连通，重新测试' : '测试连接') }}
          </el-button>
          <span v-if="sshTesting" style="margin-left:8px;font-size:12px;color:var(--app-text-secondary)">
            正在建立 SSH 会话（最长 15 秒）…
          </span>
          <el-alert v-if="sshTestResult" :type="sshTestResult.ok ? 'success' : 'error'" :closable="false"
            show-icon style="margin-top:10px" :title="sshTestResult.message" />
        </div>

        <div style="display:flex;justify-content:flex-end;margin-top:16px">
          <el-button type="primary" :disabled="target.kind === 'linux' && !sshTested" @click="goToAuth">
            下一步：seeinpm 授权
            <el-icon style="margin-left:4px"><ArrowRight /></el-icon>
          </el-button>
        </div>
      </div>

      <!-- 步骤 3：seeinpm 授权 + 两次密码 -->
      <div class="deploy-card" v-show="stepText === 'auth'">
        <div class="card-heading">
          <el-icon><Key /></el-icon> 2. seeinpm 授权与账号初始化
          <el-button link type="primary" style="margin-left:auto" @click="stepText = 'form'">
            <el-icon style="margin-right:2px"><ArrowLeft /></el-icon> 返回选择目标
          </el-button>
        </div>

        <el-form label-position="top" ref="authFormRef" :model="auth" :rules="authRules" @submit.prevent>
          <el-row :gutter="16">
            <el-col :span="12">
              <el-form-item label="用户名" prop="username">
                <el-input v-model.trim="auth.username" placeholder="seeinpm 发放的用户名" />
              </el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="授权码" prop="authCode">
                <el-input v-model.trim="auth.authCode" placeholder="Authorization Code" show-password />
              </el-form-item>
            </el-col>
          </el-row>

          <el-alert v-if="validated" type="success" :closable="false" show-icon style="margin-bottom:16px">
            seeinpm 授权校验通过
          </el-alert>

          <el-form-item label="初始化网页登录密码" prop="password">
            <el-input v-model="auth.password" type="password" show-password placeholder="至少 6 位，用于登录 seeinps 网页" />
          </el-form-item>
          <el-form-item label="确认密码" prop="confirmPassword">
            <el-input v-model="auth.confirmPassword" type="password" show-password placeholder="再次输入密码" />
          </el-form-item>
        </el-form>

        <div class="map-option">
          <el-checkbox v-model="createWebMapping">为 seeinps 的管理页面添加映射</el-checkbox>
          <div class="map-warn">警告：这会占用一个端口，如果您端口紧张，不建议勾选</div>
        </div>

        <div style="display:flex;gap:12px;margin-top:4px">
          <el-button type="primary" :disabled="validating || validated" @click="doValidate">
            {{ validated ? '已验证' : '校验授权' }}
          </el-button>
          <el-button v-if="!validated" @click="resetAuth">重置</el-button>
          <el-button v-if="validated" type="success" :disabled="deploying || confirmPending" @click="startDeploy">
            {{ confirmPending ? '等待确认卸载…' : '开始部署' }}
          </el-button>
        </div>
      </div>

      <!-- 步骤 4：部署进度 -->
      <div class="deploy-card" v-show="stepText === 'deploy' || stepText === 'done'">
        <div class="card-heading">
          <el-icon v-if="deploying || confirmPending"><Loading /></el-icon>
          <el-icon v-else><CircleCheck /></el-icon>
          {{ stepText === 'done' ? '部署成功' : '正在部署' }}
        </div>

        <div v-if="stepText === 'done'" class="success-box">
          <el-icon :size="44" color="#16a34a" style="margin-bottom:12px"><CircleCheckFilled /></el-icon>
          <div style="font-size:16px;font-weight:600;color:var(--app-text);margin-bottom:8px">{{ successMsg }}</div>
          <div style="font-size:13px;color:var(--app-text-secondary);margin-bottom:16px">
            服务已注册并启动成功，你可以通过以下地址访问 seeinps 管理端：
          </div>
          <el-button type="primary" size="large" @click="openSeeinps" style="min-width:260px">
            <el-icon style="margin-right:6px"><Link /></el-icon>
            访问 seeinps：{{ successUrl }}
          </el-button>
          <div style="margin-top:16px;font-size:12px;color:var(--app-text-tertiary)">
            提示：请记住刚才设置的登录密码；部署工具将在约 15 秒后自动退出（不影响 seeinps 服务与上方按钮）；如需重新部署，重新打开本工具即可。
          </div>
        </div>

        <template v-else>
          <div class="step-tip">
            部署进行中，期间请勿关闭部署工具窗口。若检测到已安装的 seeinps，将先提示卸载。
          </div>
          <div v-if="dlProgress" class="dl-progress">
            <el-progress :percentage="dlProgress.percent" :stroke-width="10" />
            <div class="dl-progress-text">已下载 {{ fmtBytes(dlProgress.received) }} / {{ fmtBytes(dlProgress.total) }}</div>
          </div>
          <div class="log-console" ref="logBoxRef">
            <span v-for="(l, i) in logs" :key="i" :class="'log-' + l.level" class="log-line">{{ l.message }}</span>
          </div>
          <div v-if="deployFailed && !deploying" style="margin-top:16px;display:flex;gap:12px">
            <el-button type="primary" @click="retryDeploy">重试部署</el-button>
            <el-button @click="backToAuth">返回修改参数</el-button>
          </div>
        </template>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, nextTick, watch } from 'vue'
import { ElMessage, ElMessageBox, ElInput, ElInputNumber } from 'element-plus'
import { api, startDeploySSE, type TargetInfo } from './api'

// ---------- 服务器参数配置 ----------
const activeConfig = ref<string[]>([])
const serverAddr = ref('see.timemsee.cn')
const serverPort = ref(90)

async function loadServerConfig() {
  try {
    const cfg = await api.getServerConfig()
    serverAddr.value = cfg.serverAddr
    serverPort.value = cfg.port
  } catch (e: any) {
    // 默认值即可
  }
}
async function saveServerConfig() {
  try {
    await api.saveServerConfig({ serverAddr: serverAddr.value, port: serverPort.value })
  } catch (e: any) {
    ElMessage.error('保存服务器参数失败：' + e.message)
  }
}
loadServerConfig()

// ---------- 部署目标 ----------
const target = reactive<TargetInfo>({
  kind: 'windows',
  host: '',
  sshPort: 22,
  username: 'root',
  rootPass: '',
  password: ''
})

const linuxRules = {
  host: [{ required: true, message: '请输入服务器地址', trigger: 'blur' }],
  username: [{ required: true, message: '请输入 SSH 用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入 SSH 登录密码', trigger: 'blur' }],
  rootPass: [
    {
      validator: (_: any, v: string, cb: (e?: Error) => void) => {
        if (target.username !== 'root' && !v) cb(new Error('非 root 用户需填写 root 密码'))
        else cb()
      },
      trigger: 'blur'
    }
  ]
}

const linuxFormRef = ref()
function chooseTarget(kind: 'windows' | 'linux') {
  if (deploying.value || confirmPending.value) return
  target.kind = kind
}

// 进入第 2 步：Linux 目标需先通过表单校验
async function goToAuth() {
  if (target.kind === 'linux') {
    await linuxFormRef.value.validate().catch(() => Promise.reject())
    if (!sshTested.value) {
      ElMessage.warning('请先点击“测试连接”，通过后再进入下一步')
      return
    }
  }
  stepText.value = 'auth'
}

// ---------- 管理员权限检测（Windows 服务注册需要） ----------
const needElevate = ref(false)
const elevating = ref(false)
async function loadPrivilege() {
  try {
    const p = await api.privilege()
    needElevate.value = p.needElevate
  } catch (e: any) {
    needElevate.value = false
  }
}
async function doElevate() {
  elevating.value = true
  try {
    const r = await api.elevate()
    if (!r.elevated) {
      ElMessage.info(
        '已触发管理员权限请求。请在系统弹出的 UAC 对话框中点击“是”，随后部署工具会以管理员身份重新打开，请关闭当前窗口后继续操作。'
      )
    }
  } catch (e: any) {
    ElMessage.error('提权失败：' + e.message)
  } finally {
    elevating.value = false
  }
}
loadPrivilege()

// ---------- seeinpm 授权 ----------
const auth = reactive({
  username: '',
  authCode: '',
  password: '',
  confirmPassword: ''
})
const authFormRef = ref()
const authRules = {
  username: [{ required: true, message: '请输入 seeinpm 用户名', trigger: 'blur' }],
  authCode: [{ required: true, message: '请输入授权码', trigger: 'blur' }],
  password: [
    { required: true, message: '请输入初始化密码', trigger: 'blur' },
    { min: 6, message: '密码至少 6 位', trigger: 'blur' }
  ],
  confirmPassword: [
    {
      validator: (_: any, v: string, cb: (e?: Error) => void) => {
        if (!v) cb(new Error('请再次输入密码'))
        else if (v !== auth.password) cb(new Error('两次输入的密码不一致'))
        else cb()
      },
      trigger: 'blur'
    }
  ]
}

const validated = ref(false)
const validating = ref(false)
async function doValidate() {
  await authFormRef.value.validate().catch(() => Promise.reject())
  validating.value = true
  try {
    await api.validateAuth({
      serverAddr: `${serverAddr.value}:${serverPort.value}`,
      username: auth.username,
      authCode: auth.authCode
    })
    validated.value = true
    ElMessage.success('seeinpm 授权校验通过')
  } catch (e: any) {
    ElMessage.error('授权校验失败：' + e.message)
  } finally {
    validating.value = false
  }
}

function resetAuth() {
  auth.username = ''
  auth.authCode = ''
  auth.password = ''
  auth.confirmPassword = ''
  validated.value = false
}

// ---------- 部署流程 ----------
const stepText = ref<'form' | 'auth' | 'deploy' | 'done'>('form')
const deploying = ref(false)
const confirmPending = ref(false)
const logs = ref<Array<{ level: string; message: string }>>([])
const logBoxRef = ref()
const successMsg = ref('')
const successUrl = ref('')
const dlProgress = ref<{ received: number; total: number; percent: number } | null>(null)
const deployFailed = ref(false)
const createWebMapping = ref(true)
let stopSSE: (() => void) | null = null

function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}

function pushLog(level: 'info' | 'warn' | 'error' | 'success', message: string) {
  logs.value.push({ level, message })
  nextTick(() => {
    if (logBoxRef.value) logBoxRef.value.scrollTop = logBoxRef.value.scrollHeight
  })
}

function currentTargetInfo(): TargetInfo {
  return {
    kind: target.kind,
    host: target.host,
    sshPort: target.sshPort,
    username: target.username,
    rootPass: target.rootPass,
    password: target.password
  }
}

// ---------- Linux SSH 连接测试 ----------
const sshTesting = ref(false)
const sshTested = ref(false)
const sshTestResult = ref<{ ok: boolean; message: string } | null>(null)

// 目标信息变更后，之前的测试结果作废，需重新测试
watch(
  () => [target.host, target.sshPort, target.username, target.password, target.rootPass],
  () => {
    sshTested.value = false
    sshTestResult.value = null
  }
)

async function testSSHConn() {
  await linuxFormRef.value.validate().catch(() => Promise.reject())
  sshTesting.value = true
  sshTestResult.value = null
  try {
    const r = await api.testSSH({ target: currentTargetInfo() })
    if (r.ok) {
      sshTested.value = true
      sshTestResult.value = {
        ok: true,
        message: `SSH 连接成功${r.arch ? '，系统架构 ' + r.arch : ''}（耗时 ${r.elapsedMs} ms），可以继续部署`
      }
    } else {
      sshTested.value = false
      sshTestResult.value = { ok: false, message: r.error || '连接失败' }
    }
  } catch (e: any) {
    sshTested.value = false
    sshTestResult.value = { ok: false, message: '测试请求失败：' + e.message }
  } finally {
    sshTesting.value = false
  }
}

function beginInstall(afterUninstall = false) {
  logs.value = []
  dlProgress.value = null
  deployFailed.value = false
  stepText.value = 'deploy'
  deploying.value = true
  stopSSE = startDeploySSE(
    {
      serverAddr: `${serverAddr.value}:${serverPort.value}`,
      username: auth.username,
      authCode: auth.authCode,
      password: auth.password,
      installDir: '',
      createWebMapping: createWebMapping.value,
      afterUninstall,
      target: currentTargetInfo()
    },
    {
      onLog: (level, msg) => pushLog(level, msg),
      onProgress: (received, total) => {
        if (!total) return
        const percent = Math.min(100, Math.floor((received / total) * 100))
        dlProgress.value = percent >= 100 ? null : { received, total, percent }
      },
      onConfirmUninstall: (msg) => {
        confirmPending.value = true
        pushLog('warn', '系统要求：' + msg)
        handleConfirmUninstall(msg)
      },
      onSuccess: (msg, url) => {
        pushLog('success', msg)
        successMsg.value = msg
        successUrl.value = url
        stepText.value = 'done'
        deploying.value = false
        confirmPending.value = false
      },
      onError: (msg) => {
        pushLog('error', '部署失败：' + msg)
        deploying.value = false
        confirmPending.value = false
        deployFailed.value = true
        ElMessage.error(msg)
      },
      onClose: () => {
        // 流结束：仅当不在等待卸载确认时复位部署态，避免弹窗期间被误复位
        if (deploying.value && !confirmPending.value) {
          deploying.value = false
        }
      }
    }
  )
}

async function handleConfirmUninstall(msg: string) {
  try {
    const choice = await ElMessageBox.confirm(
      '检测到目标主机已安装 seeinps。请先卸载后重新部署。是否立即卸载并继续部署？',
      '需要卸载已有 seeinps',
      {
        confirmButtonText: '卸载并继续',
        cancelButtonText: '退出安装',
        type: 'warning',
        distinguishCancelAndClose: true
      }
    )
    // 确认卸载 → 调卸载接口 → 重新执? install
    pushLog('info', '正在卸载已安装的 seeinps…')
    try {
      await api.uninstall({ target: currentTargetInfo() })
      pushLog('info', '卸载完成，重新发起部署…')
      beginInstall(true)
    } catch (e: any) {
      pushLog('error', '卸载失败：' + e.message)
      confirmPending.value = false
      ElMessage.error('卸载失败：' + e.message)
    }
  } catch (action) {
    // 取消 或 关闭 → 退出安装
    pushLog('warn', '已选择退出安装，部署中止。')
    if (stopSSE) stopSSE()
    confirmPending.value = false
    deploying.value = false
    stepText.value = 'auth'
    ElMessage.warning('已退出安装')
  }
}

async function startDeploy() {
  // 校验目标表单 + 授权表单
  if (target.kind === 'linux') {
    await linuxFormRef.value.validate().catch(() => Promise.reject())
  }
  await authFormRef.value.validate().catch(() => Promise.reject())
  if (!validated.value) {
    ElMessage.warning('请先完成 seeinpm 授权校验')
    return
  }
  beginInstall()
}

function retryDeploy() {
  deployFailed.value = false
  beginInstall()
}

function backToAuth() {
  deployFailed.value = false
  stepText.value = 'auth'
}

function openSeeinps() {
  window.open(successUrl.value, '_blank')
}
</script>

<style scoped>
.dl-progress {
  margin: 4px 0 12px;
}
.dl-progress-text {
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-top: 4px;
}
.map-option {
  margin: 12px 0 4px;
  padding: 10px 12px;
  border: 1px solid var(--app-border, #e4e7ed);
  border-radius: 6px;
}
.map-warn {
  font-size: 12px;
  color: #b45309;
  margin: 2px 0 0 24px;
}
</style>