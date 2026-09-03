<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <div class="brand-name"><span>seeinps</span></div>
      </div>
      <p class="login-sub">{{ showInit ? '首次使用，请完成初始化' : '请使用 seeinpm 分配的账号登录' }}</p>

      <!-- Login Form -->
      <el-form v-if="!showInit" :model="loginForm" :rules="loginRules" ref="loginFormRef" label-width="0">
        <el-form-item prop="username">
          <el-input v-model="loginForm.username" placeholder="用户名" prefix-icon="User" size="large" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input v-model="loginForm.password" type="password" placeholder="密码" prefix-icon="Lock" size="large" show-password @keyup.enter="handleLogin" />
        </el-form-item>
        <el-form-item v-if="needCaptcha" prop="captchaText">
          <div class="captcha-row">
            <el-input v-model="captchaText" placeholder="验证码" prefix-icon="Key" size="large" maxlength="4" @keyup.enter="handleLogin" />
            <img :src="captchaImage" class="captcha-img" alt="验证码" title="看不清？点击刷新" @click="loadCaptcha" />
          </div>
        </el-form-item>
        <el-alert v-if="lockRemaining > 0" type="error" show-icon :closable="false" class="lock-alert"
          :title="`账号已锁定，请 ${formatLock(lockRemaining)} 后再试`" />
        <el-form-item>
          <el-button type="primary" size="large" style="width: 100%" :loading="loading" :disabled="lockRemaining > 0" @click="handleLogin">登 录</el-button>
        </el-form-item>
      </el-form>

      <!-- Init Form -->
      <el-form v-else :model="initForm" :rules="initRules" ref="initFormRef" label-width="0">
        <el-form-item prop="username">
          <el-input v-model="initForm.username" placeholder="用户名 (seeinpm 创建的用户名)" prefix-icon="User" size="large" />
        </el-form-item>
        <el-form-item prop="authCode">
          <el-input v-model="initForm.authCode" placeholder="授权码 (seeinpm 生成的授权码)" prefix-icon="Key" size="large" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input v-model="initForm.password" type="password" placeholder="设置密码 (本地管理密码)" prefix-icon="Lock" size="large" show-password />
        </el-form-item>
        <el-form-item prop="confirmPassword">
          <el-input v-model="initForm.confirmPassword" type="password" placeholder="确认密码" prefix-icon="Lock" size="large" show-password @keyup.enter="handleInit" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="handleInit">初 始 化</el-button>
        </el-form-item>
      </el-form>

      <div class="login-footer" v-if="!showInit">
        <el-button type="primary" link @click="showInit = true">首次使用？点击初始化</el-button>
      </div>
      <div class="login-footer" v-else>
        <el-button type="primary" link @click="showInit = false">已有账号？返回登录</el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { authApi } from '@/api'

const router = useRouter()
const loading = ref(false)
const showInit = ref(false)
const loginFormRef = ref<FormInstance>()
const initFormRef = ref<FormInstance>()

// 登录防护：验证码 + 账号锁定
const needCaptcha = ref(false)
const captchaId = ref('')
const captchaImage = ref('')
const captchaText = ref('')
const lockRemaining = ref(0)
let lockTimer: number | undefined

const loginForm = reactive({ username: '', password: '' })
const initForm = reactive({ username: '', authCode: '', password: '', confirmPassword: '' })

const formatLock = (s: number) => {
  const m = Math.floor(s / 60), sec = s % 60
  return `${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`
}

const loadCaptcha = async () => {
  try {
    const res: any = await authApi.captcha()
    if (res.code === 0 && res.data) {
      captchaId.value = res.data.captcha_id
      captchaImage.value = res.data.image_base64
      captchaText.value = ''
    }
  } catch (e) { /* 静默：验证码获取失败不阻塞，用户可重试点击 */ }
}

const startLockCountdown = () => {
  clearInterval(lockTimer)
  lockTimer = window.setInterval(() => {
    lockRemaining.value--
    if (lockRemaining.value <= 0) {
      clearInterval(lockTimer)
      lockRemaining.value = 0
      // 锁定结束：失败计数仍在阈值之上，解锁后仍需验证码
      needCaptcha.value = true
      loadCaptcha()
    }
  }, 1000)
}

const handleLoginFailure = (code: number, data: any) => {
  if (code === 1103) { // 账号锁定
    lockRemaining.value = data?.lock_remaining || 1800
    startLockCountdown()
    ElMessage.error('密码错误次数过多，账号已锁定')
    return
  }
  if (code === 1104) { // 验证码错误或需重新获取：必须显示验证码框
    needCaptcha.value = true
    loadCaptcha()
    ElMessage.error('验证码错误或已失效，请重新输入')
    return
  }
  // 1001：用户名或密码错误
  const failed = data?.failed_count as number | undefined
  const hint = typeof failed === 'number' ? `，已连续失败 ${failed} 次` : ''
  if (data?.need_captcha) {
    needCaptcha.value = true
    loadCaptcha()
    ElMessage.warning(`用户名或密码错误${hint}，请同时输入验证码`)
  } else {
    ElMessage.error(`用户名或密码错误${hint}`)
  }
}

const loginRules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }]
}

const validateConfirmPassword = (rule: any, value: string, callback: any) => {
  if (value !== initForm.password) {
    callback(new Error('两次输入的密码不一致'))
  } else {
    callback()
  }
}

const initRules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  authCode: [{ required: true, message: '请输入授权码', trigger: 'blur' }],
  password: [
    { required: true, message: '请设置密码', trigger: 'blur' },
    { min: 6, message: '密码至少6位', trigger: 'blur' }
  ],
  confirmPassword: [
    { required: true, message: '请确认密码', trigger: 'blur' },
    { validator: validateConfirmPassword, trigger: 'blur' }
  ]
}

onMounted(async () => {
  try {
    const res: any = await authApi.getStatus()
    if (res.code === 0 && res.data && res.data.initialized) {
      showInit.value = false
    } else {
      showInit.value = true
    }
  } catch (e) {
    showInit.value = true
  }
})

const handleLogin = async () => {
  if (!loginFormRef.value) return
  await loginFormRef.value.validate(async (valid) => {
    if (!valid || loading.value) return
    if (needCaptcha.value && !captchaText.value) {
      ElMessage.warning('请输入验证码')
      return
    }
    loading.value = true
    try {
      const payload: any = {
        ...loginForm,
        captcha_id: needCaptcha.value ? captchaId.value : '',
        captcha_text: needCaptcha.value ? captchaText.value : ''
      }
      const res: any = await authApi.login(payload)
      if (res.code === 0) {
        localStorage.setItem('token', res.data.token)
        ElMessage.success('登录成功')
        router.push('/ps/dashboard')
      } else {
        handleLoginFailure(res.code, res.data)
      }
    } catch (error: any) {
      const d = error.response?.data
      if (d && typeof d.code === 'number') {
        handleLoginFailure(d.code, d.data)
      } else {
        ElMessage.error(d?.message || '登录失败')
      }
    } finally {
      loading.value = false
    }
  })
}

const handleInit = async () => {
  if (!initFormRef.value) return
  await initFormRef.value.validate(async (valid) => {
    if (!valid) return
    loading.value = true
    try {
      const res: any = await authApi.init(initForm)
      if (res.code === 0) {
        localStorage.setItem('token', res.data.token)
        ElMessage.success('初始化成功')
        router.push('/ps/dashboard')
      } else {
        ElMessage.error(res.message || '初始化失败')
      }
    } catch (error: any) {
      ElMessage.error(error.response?.data?.message || '初始化失败')
    } finally {
      loading.value = false
    }
  })
}

onBeforeUnmount(() => clearInterval(lockTimer))
</script>

<style scoped>
.login-footer { text-align: center; margin-top: 20px; }
.captcha-row { display: flex; width: 100%; gap: 8px; align-items: stretch; }
.captcha-row .el-input { flex: 1; }
.captcha-img { width: 120px; height: 40px; border-radius: 4px; cursor: pointer; border: 1px solid var(--el-border-color); }
.lock-alert { margin-bottom: 16px; }
</style>
