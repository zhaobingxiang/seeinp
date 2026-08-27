<template>
  <div class="login-container">
    <div class="login-card">
      <h1>seeinpm</h1>
      <p class="subtitle">管理中心</p>
      <el-form :model="form" :rules="rules" ref="formRef">
        <el-form-item prop="username">
          <el-input v-model="form.username" placeholder="用户名" prefix-icon="User" size="large" />
        </el-form-item>
        <el-form-item prop="password">
          <el-input v-model="form.password" type="password" placeholder="密码" prefix-icon="Lock" size="large" show-password @keyup.enter="handleLogin" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="handleLogin">登录</el-button>
        </el-form-item>
      </el-form>
      <div class="footer">
        <el-button type="primary" link @click="showInit = true">初始化管理员</el-button>
      </div>
    </div>
    <el-dialog v-model="showInit" title="初始化管理员" width="400px">
      <el-form :model="initForm" :rules="initRules" ref="initFormRef">
        <el-form-item label="用户名" prop="username"><el-input v-model="initForm.username" /></el-form-item>
        <el-form-item label="密码" prop="password"><el-input v-model="initForm.password" type="password" show-password /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showInit = false">取消</el-button>
        <el-button type="primary" @click="handleInit" :loading="initLoading">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>
<script setup lang="ts">
import { ref, reactive } from "vue"
import { useRouter } from "vue-router"
import { ElMessage } from "element-plus"
import type { FormInstance, FormRules } from "element-plus"
import { authApi } from "@/api"
const router = useRouter()
const loading = ref(false)
const initLoading = ref(false)
const showInit = ref(false)
const formRef = ref<FormInstance>()
const initFormRef = ref<FormInstance>()
const form = reactive({ username: "", password: "" })
const initForm = reactive({ username: "", password: "" })
const rules: FormRules = { username: [{ required: true, message: "必填项", trigger: "blur" }], password: [{ required: true, message: "必填项", trigger: "blur" }] }
const initRules: FormRules = { username: [{ required: true, message: "必填项", trigger: "blur" }], password: [{ required: true, message: "必填项", trigger: "blur" }, { min: 8, message: "最少8个字符", trigger: "blur" }] }
const handleLogin = async () => { if (!formRef.value) return; await formRef.value.validate(async (valid) => { if (!valid) return; loading.value = true; try { const res: any = await authApi.login(form); if (res.code === 0) { localStorage.setItem("pm_token", res.data.access_token); ElMessage.success("登录成功"); router.push("/pm/dashboard") } else ElMessage.error(res.message || "登录失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "登录失败") } finally { loading.value = false } }) }
const handleInit = async () => { if (!initFormRef.value) return; await initFormRef.value.validate(async (valid) => { if (!valid) return; initLoading.value = true; try { const res: any = await authApi.init(initForm); if (res.code === 0) { ElMessage.success("初始化成功"); showInit.value = false } else ElMessage.error(res.message || "初始化失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "初始化失败") } finally { initLoading.value = false } }) }
</script>
<style scoped>
.login-container { display: flex; justify-content: center; align-items: center; min-height: 100vh; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }
.login-card { width: 400px; padding: 40px; background: white; border-radius: 12px; box-shadow: 0 8px 24px rgba(0,0,0,0.15); }
.login-card h1 { text-align: center; margin: 0 0 8px 0; color: #303133; font-size: 28px; }
.subtitle { text-align: center; color: #909399; margin-bottom: 30px; }
.footer { text-align: center; margin-top: 20px; }
</style>
