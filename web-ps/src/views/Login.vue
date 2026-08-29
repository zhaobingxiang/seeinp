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
        <el-form-item>
          <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="handleLogin">登 录</el-button>
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
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { authApi } from '@/api'

const router = useRouter()
const loading = ref(false)
const showInit = ref(false)
const loginFormRef = ref<FormInstance>()
const initFormRef = ref<FormInstance>()

const loginForm = reactive({ username: '', password: '' })
const initForm = reactive({ username: '', authCode: '', password: '', confirmPassword: '' })

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
    if (!valid) return
    loading.value = true
    try {
      const res: any = await authApi.login(loginForm)
      if (res.code === 0) {
        localStorage.setItem('token', res.data.token)
        ElMessage.success('登录成功')
        router.push('/ps/dashboard')
      } else {
        ElMessage.error(res.message || '登录失败')
      }
    } catch (error: any) {
      ElMessage.error(error.response?.data?.message || '登录失败')
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
</script>

<style scoped>
.login-footer { text-align: center; margin-top: 20px; }
</style>
