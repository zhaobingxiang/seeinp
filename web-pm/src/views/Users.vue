<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>seeinps 用户列表</span>
        <el-button type="primary" @click="openCreate">创建用户</el-button>
      </div>
      <div style="padding:12px 20px 20px">
        <el-table :data="users" v-loading="loading">
          <el-table-column prop="id" label="ID" width="60" />
          <el-table-column prop="username" label="用户名" min-width="110" />
          <el-table-column prop="remark" label="备注" min-width="110" />
          <el-table-column label="账号" width="80"><template #default="{row}"><el-tag :type="row.status === 1 ? 'success' : 'danger'" size="small">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag></template></el-table-column>
          <el-table-column label="在线" width="80"><template #default="{row}"><el-tag :type="row.online ? 'success' : 'info'" size="small">{{ row.online ? '在线' : '离线' }}</el-tag></template></el-table-column>
          <el-table-column label="有效期" width="150">
            <template #default="{row}">
              <template v-if="row.expireDate">
                <span>{{ row.expireDate }}</span>
                <el-tag v-if="row.expired" type="danger" size="small" style="margin-left:6px">已过期</el-tag>
              </template>
              <span v-else style="color:var(--app-text-tertiary)">永久</span>
            </template>
          </el-table-column>
          <el-table-column label="端口(用/配)" width="110">
            <template #default="{row}">
              <span :style="row.maxPorts > 0 && row.usedPorts >= row.maxPorts ? 'color:var(--el-color-danger)' : ''">
                {{ row.usedPorts }}/{{ row.maxPorts > 0 ? row.maxPorts : '不限' }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="用户端口池" min-width="150">
            <template #default="{row}">
              <span v-if="row.portRanges && row.portRanges.length" style="font-family:var(--font-mono,monospace)">
                {{ row.portRanges.map((r: any) => r.start + '-' + r.end).join(', ') }}
              </span>
              <span v-else style="color:var(--app-text-tertiary)">不限</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="280">
            <template #default="{row}">
              <el-button type="primary" link @click="openEdit(row)">编辑</el-button>
              <el-button v-if="row.status === 1" type="warning" link @click="disableUser(row)">禁用</el-button>
              <el-button v-else type="success" link @click="enableUser(row)">启用</el-button>
              <el-button type="primary" link @click="resetAuthCode(row)">重置授权码</el-button>
              <el-button type="danger" link @click="deleteUser(row.username)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="users.length === 0" description="暂无用户" />
      </div>
    </div>

    <el-dialog v-model="showEdit" :title="editing ? '编辑用户' : '创建用户'" width="560px" :close-on-click-modal="false" @closed="resetForm">
      <el-form :model="form" :rules="rules" ref="formRef" label-width="90px">
        <el-form-item label="用户名" prop="username"><el-input v-model="form.username" :disabled="!!editing" placeholder="至少 3 个字符" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
        <el-form-item label="有效期">
          <el-date-picker v-model="form.expireDate" type="date" value-format="YYYY-MM-DD" placeholder="选择日期（留空为永久有效）" style="width:100%" clearable />
          <div class="form-tip">到期后该用户的 seeinps 连接与全部代理将被断开；改回今天或以后，seeinps 自动重连恢复</div>
        </el-form-item>
        <el-form-item label="端口数量">
          <el-input-number v-model="form.maxPorts" :min="0" :max="65535" controls-position="right" style="width:100%" />
          <div class="form-tip">该用户最多可同时占用的转发端口数，0 表示不限制；超配额后新代理无法分配端口</div>
        </el-form-item>
        <el-form-item label="用户端口池" prop="portRanges">
          <div style="width:100%">
            <div v-for="(rg, i) in form.portRanges" :key="i" class="range-row">
              <el-input-number v-model="rg.start" :min="1" :max="65535" controls-position="right" style="width:150px" />
              <span class="range-sep">-</span>
              <el-input-number v-model="rg.end" :min="1" :max="65535" controls-position="right" style="width:150px" />
              <el-button type="danger" link @click="form.portRanges.splice(i, 1)">删除</el-button>
            </div>
            <el-button size="small" @click="form.portRanges.push({ start: 20000, end: 20010 })">添加范围</el-button>
            <div class="form-tip">留空表示不限制；填写后该用户端口只能从这些范围内分配，必须在总端口池内且总跨度不超过端口数量</div>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="showEdit = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="handleSave">{{ editing ? '保存' : '创建' }}</el-button>
      </template>
    </el-dialog>
    <el-dialog v-model="showAuthCode" title="授权码" width="520px">
      <el-alert :type="authCodeIsNew ? 'warning' : 'success'" :closable="false" style="margin-bottom:16px">{{ authCodeIsNew ? '授权码已重置，请复制新授权码。' : '用户创建成功！请复制认证码。' }}</el-alert>
      <el-input v-model="createdAuthCode" readonly ref="authCodeInputRef"><template #append><el-button @click="copyAuthCode">复制</el-button></template></el-input>
      <el-alert v-if="authCodeIsNew" type="info" :closable="false" style="margin-top:16px">旧授权码已立即失效，在线的 seeinps 已被断开，需使用新授权码重新部署。</el-alert>
      <el-alert v-else type="warning" :closable="false" style="margin-top:16px">请妥善保存此认证码，关闭后将不再显示！</el-alert>
      <template #footer><el-button type="primary" @click="showAuthCode = false">确定</el-button></template>
    </el-dialog>
  </Layout>
</template>
<script setup lang="ts">
import { ref, reactive, onMounted } from "vue"
import { ElMessage, ElMessageBox } from "element-plus"
import type { FormInstance, FormRules } from "element-plus"
import { userApi, portApi } from "@/api"
import Layout from "@/components/Layout.vue"

const users = ref<any[]>([])
const loading = ref(true)
const showEdit = ref(false)
const showAuthCode = ref(false)
const authCodeIsNew = ref(false)
const saving = ref(false)
const editing = ref<any>(null)
const createdAuthCode = ref("")
const authCodeInputRef = ref()
const formRef = ref<FormInstance>()
const globalRanges = ref<any[]>([])

const form = reactive({
  username: "", remark: "", expireDate: "", maxPorts: 0,
  portRanges: [] as { start: number; end: number }[]
})

const validateRanges = (): string => {
  for (const rg of form.portRanges) {
    if (!rg.start || !rg.end || rg.start > rg.end) return `范围 ${rg.start}-${rg.end} 非法（需 start ≤ end）`
  }
  for (const rg of form.portRanges) {
    const ok = globalRanges.value.some((g: any) => rg.start >= g.start && rg.end <= g.end)
    if (!ok) return `范围 ${rg.start}-${rg.end} 不在总端口池内`
  }
  if (form.maxPorts > 0) {
    const span = form.portRanges.reduce((s: number, rg: any) => s + (rg.end - rg.start + 1), 0)
    if (span > form.maxPorts) return `用户端口池共 ${span} 个端口，超过端口数量 ${form.maxPorts}`
  }
  return ""
}

const rules: FormRules = {
  username: [{ required: true, message: "必填项", trigger: "blur" }, { min: 3, message: "至少3个字符", trigger: "blur" }],
  portRanges: [{ validator: (_r: any, _v: any, cb: any) => { const err = validateRanges(); if (err) return cb(new Error(err)); cb() }, trigger: "change" }]
}

const loadUsers = async () => { loading.value = true; try { const res: any = await userApi.list(); if (res.code === 0) users.value = res.data || [] } catch (e) { console.error(e) } finally { loading.value = false } }
const loadGlobalRanges = async () => { try { const res: any = await portApi.getPool(); if (res.code === 0) globalRanges.value = res.data.ranges || [] } catch (e) { console.error(e) } }

const openCreate = () => {
  editing.value = null
  Object.assign(form, { username: "", remark: "", expireDate: "", maxPorts: 0, portRanges: [] })
  showEdit.value = true
}
const openEdit = (row: any) => {
  editing.value = row
  Object.assign(form, {
    username: row.username, remark: row.remark || "",
    expireDate: row.expireDate || "", maxPorts: row.maxPorts || 0,
    portRanges: (row.portRanges || []).map((r: any) => ({ start: r.start, end: r.end }))
  })
  showEdit.value = true
}
const resetForm = () => { editing.value = null; formRef.value?.clearValidate() }

const handleSave = async () => {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    saving.value = true
    try {
      if (editing.value) {
        const res: any = await userApi.update(editing.value.username, {
          expireDate: form.expireDate || "", maxPorts: form.maxPorts, portRanges: form.portRanges
        })
        if (res.code === 0) {
          ElMessage.success(res.data?.kicked ? "已保存，配置收紧已断开该用户连接，将按新配置自动恢复" : "保存成功")
          showEdit.value = false
          loadUsers()
        } else ElMessage.error(res.message || "操作失败")
      } else {
        const res: any = await userApi.create({
          username: form.username, remark: form.remark,
          expireDate: form.expireDate || "", maxPorts: form.maxPorts, portRanges: form.portRanges
        })
        if (res.code === 0) {
          createdAuthCode.value = res.data.authCode; authCodeIsNew.value = false; showEdit.value = false; showAuthCode.value = true; loadUsers(); ElMessage.success("创建成功")
        } else ElMessage.error(res.message || "操作失败")
      }
    } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") } finally { saving.value = false }
  })
}

const deleteUser = async (username: string) => { try { await ElMessageBox.confirm("确定删除用户 " + username + " 吗？", "确认", { type: "warning" }); const res: any = await userApi.delete(username); if (res.code === 0) { ElMessage.success("已删除"); loadUsers() } else ElMessage.error(res.message || "操作失败") } catch (e) { if (e !== "cancel") ElMessage.error("操作失败") } }
const disableUser = (row: any) => {
  if (row.online) {
    ElMessageBox.confirm(`禁用用户 ${row.username}，是否同时断开其已建立的全部连接？`, "禁用用户", {
      confirmButtonText: "禁用并断开", cancelButtonText: "仅禁用", distinguishCancelAndClose: true, type: "warning"
    }).then(() => doDisable(row.username, true)).catch((action: string) => { if (action === "cancel") doDisable(row.username, false) })
  } else {
    ElMessageBox.confirm(`确定禁用用户 ${row.username} 吗？禁用后其 seeinps 将无法注册。`, "禁用用户", { type: "warning" })
      .then(() => doDisable(row.username, false)).catch(() => {})
  }
}
const doDisable = async (username: string, disconnectNow: boolean) => {
  try { const res: any = await userApi.disable(username, disconnectNow); if (res.code === 0) { ElMessage.success(disconnectNow ? "已禁用并断开连接" : "已禁用"); loadUsers() } else ElMessage.error(res.message || "操作失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
}
const enableUser = async (row: any) => {
  try { const res: any = await userApi.enable(row.username); if (res.code === 0) { ElMessage.success("已启用，seeinps 重连后自动恢复"); loadUsers() } else ElMessage.error(res.message || "操作失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
}
const resetAuthCode = (row: any) => {
  ElMessageBox.confirm(`重置用户 ${row.username} 的授权码？旧授权码将立即失效，在线的 seeinps 会被断开，需使用新授权码重新部署。`, "重置授权码", { type: "warning" })
    .then(async () => {
      try { const res: any = await userApi.resetCode(row.username); if (res.code === 0) { createdAuthCode.value = res.data.authCode; authCodeIsNew.value = true; showAuthCode.value = true; loadUsers() } else ElMessage.error(res.message || "操作失败") } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") }
    }).catch(() => {})
}
const copyAuthCode = async () => {
  const code = createdAuthCode.value
  if (!code) { ElMessage.warning("授权码为空，无法复制"); return }
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(code); ElMessage.success("复制成功"); return } catch (e) { console.error("Clipboard API 复制失败:", e) }
  }
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
    const el: HTMLInputElement | undefined = authCodeInputRef.value?.$el?.querySelector?.("input")
    if (el) { el.focus(); el.select() }
    ElMessage.warning("自动复制未成功，授权码已全选，请按 Ctrl+C 手动复制")
  }
}
onMounted(() => { loadUsers(); loadGlobalRanges() })
</script>
<style scoped>
.form-tip {
  font-size: 12px;
  color: var(--app-text-tertiary);
  line-height: 1.5;
  margin-top: 4px;
  width: 100%;
}

.range-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.range-sep {
  color: var(--app-text-tertiary);
}
</style>
