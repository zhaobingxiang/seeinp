<template>
  <div class="layout">
    <el-container>
      <el-aside width="200px" class="sidebar">
        <div class="logo">seeinpm</div>
        <el-menu :default-active="currentPath" router>
          <el-menu-item index="/pm/dashboard"><el-icon><DataBoard /></el-icon><span>仪表盘</span></el-menu-item>
          <el-menu-item index="/pm/users"><el-icon><User /></el-icon><span>用户管理</span></el-menu-item>
          <el-menu-item index="/pm/ports"><el-icon><Connection /></el-icon><span>端口池</span></el-menu-item>
        </el-menu>
      </el-aside>
      <el-container>
        <el-header class="header"><span>用户管理</span><el-button type="danger" @click="logout">退出登录</el-button></el-header>
        <el-main>
          <el-card>
            <template #header>
              <div style="display:flex;justify-content:space-between;align-items:center">
                <span>seeinps 用户列表</span>
                <el-button type="primary" @click="showCreate = true">创建用户</el-button>
              </div>
            </template>
            <el-table :data="users" v-loading="loading">
              <el-table-column prop="id" label="ID" width="60" />
              <el-table-column prop="username" label="用户名" />
              <el-table-column prop="remark" label="备注" />
              <el-table-column label="账号" width="80"><template #default="{row}"><el-tag :type="row.status === 1 ? 'success' : 'danger'" size="small">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag></template></el-table-column>
              <el-table-column label="在线" width="80"><template #default="{row}"><el-tag :type="row.online ? 'success' : 'info'" size="small">{{ row.online ? '在线' : '离线' }}</el-tag></template></el-table-column>
              <el-table-column label="操作" width="210">
                <template #default="{row}">
                  <el-button v-if="row.status === 1" type="warning" link @click="disableUser(row)">禁用</el-button>
                  <el-button v-else type="success" link @click="enableUser(row)">启用</el-button>
                  <el-button type="primary" link @click="resetAuthCode(row)">重置授权码</el-button>
                  <el-button type="danger" link @click="deleteUser(row.username)">删除</el-button>
                </template>
              </el-table-column>
            </el-table>
            <el-empty v-if="users.length === 0" description="暂无用户" />
          </el-card>
          <el-dialog v-model="showCreate" title="创建用户" width="450px" @closed="resetCreate">
            <el-form :model="createForm" :rules="createRules" ref="createFormRef" label-width="80px">
              <el-form-item label="用户名" prop="username"><el-input v-model="createForm.username" /></el-form-item>
              <el-form-item label="备注"><el-input v-model="createForm.remark" /></el-form-item>
            </el-form>
            <template #footer>
              <el-button @click="showCreate = false">取消</el-button>
              <el-button type="primary" @click="handleCreate" :loading="creating">创建</el-button>
            </template>
          </el-dialog>
          <el-dialog v-model="showAuthCode" title="授权码" width="500px">
            <el-alert :type="authCodeIsNew ? 'warning' : 'success'" :closable="false" style="margin-bottom:16px">{{ authCodeIsNew ? '授权码已重置，请复制新授权码。' : '用户创建成功！请复制认证码。' }}</el-alert>
            <el-input v-model="createdAuthCode" readonly ref="authCodeInputRef"><template #append><el-button @click="copyAuthCode">复制</el-button></template></el-input>
            <el-alert v-if="authCodeIsNew" type="info" :closable="false" style="margin-top:16px">旧授权码已立即失效，在线的 seeinps 已被断开，需使用新授权码重新部署。</el-alert>
            <el-alert v-else type="warning" :closable="false" style="margin-top:16px">请妥善保存此认证码，关闭后将不再显示！</el-alert>
            <template #footer><el-button type="primary" @click="showAuthCode = false">确定</el-button></template>
          </el-dialog>
        </el-main>
      </el-container>
    </el-container>
  </div>
</template>
<script setup lang="ts">
import { ref, reactive, onMounted } from "vue"
import { useRouter } from "vue-router"
import { ElMessage, ElMessageBox } from "element-plus"
import type { FormInstance, FormRules } from "element-plus"
import { userApi } from "@/api"
import { DataBoard, User, Connection } from "@element-plus/icons-vue"
const router = useRouter()
const users = ref<any[]>([])
const loading = ref(true)
const showCreate = ref(false)
const showAuthCode = ref(false)
const authCodeIsNew = ref(false)
const creating = ref(false)
const createdAuthCode = ref("")
const authCodeInputRef = ref()
const createFormRef = ref<FormInstance>()
const createForm = reactive({ username: "", remark: "" })
const createRules: FormRules = { username: [{ required: true, message: "必填项", trigger: "blur" }, { min: 3, message: "至少3个字符", trigger: "blur" }] }
const loadUsers = async () => { loading.value = true; try { const res: any = await userApi.list(); if (res.code === 0) users.value = res.data || [] } catch (e) { console.error(e) } finally { loading.value = false } }
const handleCreate = async () => { if (!createFormRef.value) return; await createFormRef.value.validate(async (valid) => { if (!valid) return; creating.value = true; try { const res: any = await userApi.create(createForm); if (res.code === 0) { createdAuthCode.value = res.data.authCode; authCodeIsNew.value = false; showCreate.value = false; showAuthCode.value = true; loadUsers(); ElMessage.success("创建成功") } else ElMessage.error(res.message || "操作失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") } finally { creating.value = false } }) }
const deleteUser = async (username: string) => { try { await ElMessageBox.confirm("确定删除用户 " + username + " 吗？", "确认", { type: "warning" }); const res: any = await userApi.delete(username); if (res.code === 0) { ElMessage.success("已删除"); loadUsers() } else ElMessage.error(res.message || "操作失败") } catch (e) { if (e !== "cancel") ElMessage.error("操作失败") } }
// F-U4：禁用用户；在线时询问是否断开其已建立的全部连接
const disableUser = (row: any) => {
  if (row.online) {
    ElMessageBox.confirm(`禁用用户 ${row.username}，是否同时断开其已建立的全部连接？`, "禁用用户", {
      confirmButtonText: "禁用并断开",
      cancelButtonText: "仅禁用",
      distinguishCancelAndClose: true,
      type: "warning"
    }).then(() => doDisable(row.username, true)).catch((action: string) => { if (action === "cancel") doDisable(row.username, false) })
  } else {
    ElMessageBox.confirm(`确定禁用用户 ${row.username} 吗？禁用后其 seeinps 将无法注册。`, "禁用用户", { type: "warning" })
      .then(() => doDisable(row.username, false)).catch(() => {})
  }
}
const doDisable = async (username: string, disconnectNow: boolean) => {
  try {
    const res: any = await userApi.disable(username, disconnectNow)
    if (res.code === 0) { ElMessage.success(disconnectNow ? "已禁用并断开连接" : "已禁用"); loadUsers() } else ElMessage.error(res.message || "操作失败")
  } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
}
const enableUser = async (row: any) => {
  try {
    const res: any = await userApi.enable(row.username)
    if (res.code === 0) { ElMessage.success("已启用，seeinps 重连后自动恢复"); loadUsers() } else ElMessage.error(res.message || "操作失败")
  } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
}
const resetAuthCode = (row: any) => {
  ElMessageBox.confirm(`重置用户 ${row.username} 的授权码？旧授权码将立即失效，在线的 seeinps 会被断开，需使用新授权码重新部署。`, "重置授权码", { type: "warning" })
    .then(async () => {
      try {
        const res: any = await userApi.resetCode(row.username)
        if (res.code === 0) { createdAuthCode.value = res.data.authCode; authCodeIsNew.value = true; showAuthCode.value = true; loadUsers() } else ElMessage.error(res.message || "操作失败")
      } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
    }).catch(() => {})
}
const copyAuthCode = async () => {
  const code = createdAuthCode.value
  if (!code) { ElMessage.warning("授权码为空，无法复制"); return }
  // 方式一：Clipboard API 仅在安全上下文（https / localhost）可用，
  // 通过 http://IP 访问 seeinpm 时 navigator.clipboard 不存在，需降级
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(code)
      ElMessage.success("复制成功")
      return
    } catch (e) {
      console.error("Clipboard API 复制失败:", e)
    }
  }
  // 方式二：隐藏 textarea + execCommand 兜底（兼容 http://IP 部署场景）
  try {
    const ta = document.createElement("textarea")
    ta.value = code
    ta.style.position = "fixed"
    ta.style.top = "-9999px"
    ta.style.opacity = "0"
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand("copy")
    document.body.removeChild(ta)
    if (ok) { ElMessage.success("复制成功"); return }
    throw new Error("execCommand 返回 false")
  } catch (e) {
    console.error("execCommand 复制失败:", e)
    // 最终兜底：自动选中弹窗内的授权码输入框内容，提示手动复制
    const el: HTMLInputElement | undefined = authCodeInputRef.value?.$el?.querySelector?.("input")
    if (el) { el.focus(); el.select() }
    ElMessage.warning("自动复制未成功，授权码已全选，请按 Ctrl+C 手动复制")
  }
}
const resetCreate = () => { createForm.username = ""; createForm.remark = "" }
const logout = () => { localStorage.removeItem("pm_token"); router.push("/pm/login") }
onMounted(loadUsers)
</script>
<style scoped>
.layout { height: 100vh; }
.sidebar { background: #001529; }
.logo { color: white; font-size: 20px; font-weight: bold; padding: 20px; text-align: center; }
.header { background: white; display: flex; justify-content: space-between; align-items: center; box-shadow: 0 1px 4px rgba(0,0,0,0.08); }
.el-menu { border-right: none; }
</style>
