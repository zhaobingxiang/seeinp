<template>
  <Layout>
    <!-- 周期流量用量提醒（配置了配额才显示） -->
    <QuotaCard />

    <!-- 代理列表 -->
    <div class="page-card" style="padding:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>代理列表</span>
        <div>
          <el-button @click="refreshNow" :loading="refreshing">刷新</el-button>
          <el-button type="primary" @click="openCreate">添加代理</el-button>
        </div>
      </div>

      <div v-if="proxies.length === 0" class="empty-state">
        <el-empty description="暂无代理，点击右上角「添加代理」创建第一个代理" />
      </div>

      <el-table v-else :data="proxies" style="width: 100%">
        <el-table-column prop="id" label="名称" min-width="120" />
        <el-table-column prop="type" label="类型" width="110">
          <template #default="{ row }">
            <el-tag :type="typeTag(row.type)" size="small">{{ typeLabel(row.type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="本地地址" width="180">
          <template #default="{ row }">
            <span v-if="row.type === 'tcp' || row.type === 'udp'" style="font-family:var(--font-mono,monospace)">{{ row.localAddr }}:{{ row.localPort }}</span>
            <span v-else>{{ row.proxyUsername }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="hasOps ? '转发端口 / 运维ID' : '转发端口'" width="190">
          <template #default="{ row }">
            <template v-if="row.forwardPort">
              <el-tag type="success" size="small">{{ row.forwardPort }}</el-tag>
              <el-button type="primary" link size="small" @click="copyText(String(row.forwardPort))">复制</el-button>
            </template>
            <el-tag v-else type="info" size="small">未分配</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="160">
          <template #default="{ row }">
            <el-button type="primary" link @click="openEdit(row)">编辑</el-button>
            <el-button type="danger" link @click="deleteProxy(row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 添加 / 编辑代理弹窗 -->
    <el-dialog v-model="dialogVisible" :title="editingProxy ? '编辑代理' : '添加代理'"
      width="560px" :close-on-click-modal="false" :before-close="guardClose" @closed="resetForm">
      <el-form :model="form" :rules="rules" ref="formRef" label-width="100px">
        <el-form-item label="代理名称" prop="id">
          <el-input v-model="form.id" :disabled="!!editingProxy" placeholder="如: my-ssh / ops-proxy" />
          <div class="form-tip">1-32 位，仅字母/数字/下划线/连字符，以字母或数字开头；创建后不可修改</div>
        </el-form-item>

        <el-form-item label="代理类型" prop="type">
          <el-select v-model="form.type" style="width: 100%" :disabled="!!editingProxy">
            <el-option label="TCP 映射" value="tcp" />
            <el-option label="UDP 映射" value="udp" />
            <el-option label="运维代理 (HTTP)" value="ops_http" />
          </el-select>
          <div v-if="editingProxy" class="form-tip">类型创建后不可修改，如需变更请删除后重建</div>
        </el-form-item>

        <template v-if="form.type === 'tcp' || form.type === 'udp'">
          <el-form-item label="本地地址" prop="localAddr">
            <el-input v-model="form.localAddr" placeholder="127.0.0.1" />
          </el-form-item>
          <el-form-item label="本地端口" prop="localPort">
            <el-input-number v-model="form.localPort" :min="1" :max="65535" style="width: 100%" />
          </el-form-item>
        </template>

        <template v-else>
          <el-form-item label="代理账号" prop="proxyUsername">
            <el-input v-model="form.proxyUsername" placeholder="运维代理认证账号（告知 seeinpc 使用者）" />
          </el-form-item>
          <el-form-item label="代理密码" prop="proxyPassword">
            <el-input v-model="form.proxyPassword" type="password" show-password
              :placeholder="editingProxy ? '留空则不修改密码' : '至少10位，含大小写/数字/特殊字符'" />
          </el-form-item>
          <el-form-item label="ACL 网段" prop="acl">
            <el-select v-model="form.acl" multiple filterable allow-create default-first-option
              style="width: 100%" placeholder="允许访问的目标网段（CIDR）">
              <el-option label="不限制 (0.0.0.0/0)" value="0.0.0.0/0" />
            </el-select>
            <div class="form-tip">如果需要限制运维能访问的地址和网段，请在这里填写，填写格式：IP网段/掩码位数，比如：192.168.1.0/24 按回车添加；0.0.0.0/0 表示不限制</div>
          </el-form-item>
        </template>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="handleSave">保存</el-button>
      </template>
    </el-dialog>
  </Layout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { proxyApi } from '@/api'
import Layout from '@/components/Layout.vue'
import QuotaCard from '@/components/QuotaCard.vue'

const formRef = ref<FormInstance>()
const saving = ref(false)
const refreshing = ref(false)
const dialogVisible = ref(false)
const editingProxy = ref<any>(null)
const proxies = ref<any[]>([])

const defaultACL = ['0.0.0.0/0']
const hasOps = computed(() => proxies.value.some((p: any) => p.type === 'ops_http'))

const typeLabel = (t: string) => (t === 'tcp' ? 'TCP' : t === 'udp' ? 'UDP' : '运维HTTP')
const typeTag = (t: string) => (t === 'ops_http' ? 'warning' : 'primary')

const emptyForm = () => ({
  id: '',
  type: 'tcp',
  localAddr: '127.0.0.1',
  localPort: 22,
  proxyUsername: '',
  proxyPassword: '',
  acl: [...defaultACL]
})

const form = reactive(emptyForm())

const rules: FormRules = {
  id: [
    {
      required: true,
      validator: (_rule: any, value: string, callback: any) => {
        if (!value) return callback(new Error('请输入代理名称'))
        if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/.test(value)) {
          return callback(new Error('仅支持字母、数字、下划线、连字符，以字母或数字开头'))
        }
        if (!editingProxy.value && proxies.value.some((p: any) => p.id === value)) {
          return callback(new Error('代理名称已存在'))
        }
        callback()
      },
      trigger: 'blur'
    }
  ],
  type: [{ required: true, message: '请选择代理类型', trigger: 'change' }],
  localAddr: [{ required: true, message: '请输入本地地址', trigger: 'blur' }],
  localPort: [{ required: true, message: '请输入本地端口', trigger: 'blur' }],
  proxyUsername: [{ required: true, message: '请输入代理账号', trigger: 'blur' }],
  proxyPassword: [
    {
      required: true,
      validator: (_rule: any, value: string, callback: any) => {
        if (editingProxy.value && !value) return callback()
        if (!value) return callback(new Error('请输入代理密码'))
        if (value.length < 10) return callback(new Error('密码至少 10 位'))
        if (!/[A-Z]/.test(value) || !/[a-z]/.test(value) || !/[0-9]/.test(value) || !/[^A-Za-z0-9]/.test(value)) {
          return callback(new Error('密码需包含大写、小写、数字和特殊字符'))
        }
        callback()
      },
      trigger: 'blur'
    }
  ],
  acl: [
    {
      required: true,
      validator: (_rule: any, value: string[], callback: any) => {
        if (!value || value.length === 0) return callback(new Error('请至少填写一个 ACL 网段'))
        for (const cidr of value) {
          if (!/^(\d{1,3}\.){3}\d{1,3}\/\d{1,2}$/.test(cidr)) {
            return callback(new Error(`网段格式错误：${cidr}，应为 IP网段/掩码位数，如 192.168.1.0/24`))
          }
        }
        callback()
      },
      trigger: 'change'
    }
  ]
}

const loadProxies = async () => {
  try {
    const res: any = await proxyApi.list()
    if (res.code === 0) {
      proxies.value = res.data || []
    }
  } catch (error) {
    console.error('Load proxies error:', error)
  }
}

const refreshNow = async () => {
  refreshing.value = true
  await loadProxies()
  refreshing.value = false
}

const openCreate = () => {
  editingProxy.value = null
  Object.assign(form, emptyForm())
  dialogVisible.value = true
  nextTick(() => formRef.value?.clearValidate())
}

const openEdit = (proxy: any) => {
  editingProxy.value = proxy
  Object.assign(form, {
    id: proxy.id,
    type: proxy.type,
    localAddr: proxy.localAddr || '127.0.0.1',
    localPort: proxy.localPort || 22,
    proxyUsername: proxy.proxyUsername || '',
    proxyPassword: '',
    acl: proxy.acl && proxy.acl.length ? [...proxy.acl] : [...defaultACL]
  })
  dialogVisible.value = true
  nextTick(() => formRef.value?.clearValidate())
}

const resetForm = () => {
  editingProxy.value = null
  Object.assign(form, emptyForm())
  formRef.value?.clearValidate()
}

// 保存中禁止误触关闭（ESC / 右上角 X）
const guardClose = (done: () => void) => {
  if (saving.value) {
    ElMessage.warning('正在保存，请稍候')
    return
  }
  done()
}

const handleSave = async () => {
  if (!formRef.value) return

  await formRef.value.validate(async (valid) => {
    if (!valid) return

    saving.value = true
    try {
      let res: any
      const payload: any = { ...form }
      if (editingProxy.value) {
        res = await proxyApi.update(editingProxy.value.id, payload)
      } else {
        res = await proxyApi.create(payload)
      }

      if (res.code === 0) {
        if (editingProxy.value) {
          ElMessage.success('保存成功')
        } else if (res.data?.forwardPort) {
          ElMessage.success(form.type === 'ops_http'
            ? `添加成功，运维ID ${res.data.forwardPort}`
            : `添加成功，转发端口 ${res.data.forwardPort}`)
        } else {
          ElMessage.success('添加成功，待连接 seeinpm 后自动分配端口')
        }
        dialogVisible.value = false
        loadProxies()
      } else {
        // 失败保持弹窗打开，便于用户修改后重试
        ElMessage.error(res.message || '操作失败')
      }
    } catch (error: any) {
      ElMessage.error(error.response?.data?.message || '操作失败')
    } finally {
      saving.value = false
    }
  })
}

const copyText = async (text: string) => {
  // navigator.clipboard 仅在安全上下文（HTTPS/localhost）可用，B 端 HTTP 访问时需 execCommand 兜底
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      ElMessage.success(`已复制: ${text}`)
      return
    } catch {
      // Clipboard API 失败时降级 execCommand
    }
  }
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
    if (ok) {
      ElMessage.success(`已复制: ${text}`)
      return
    }
    throw new Error('execCommand returned false')
  } catch {
    ElMessage.warning(`复制失败，请手动复制: ${text}`)
  }
}

const deleteProxy = async (id: string) => {
  try {
    await ElMessageBox.confirm('确定要删除该代理吗？删除后端口将被释放，正在进行的连接会被断开。', '确认删除', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })

    const res: any = await proxyApi.delete(id)
    if (res.code === 0) {
      ElMessage.success('删除成功')
      if (editingProxy.value?.id === id) {
        dialogVisible.value = false
      }
      loadProxies()
    } else {
      ElMessage.error(res.message || '删除失败')
      loadProxies()
    }
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error('删除失败')
    }
  }
}

let pollTimer: number | undefined

onMounted(() => {
  loadProxies()
  // 端口分配状态会异步变化（未分配 -> 已分配），定时刷新保持列表最新
  pollTimer = window.setInterval(loadProxies, 10000)
})

onUnmounted(() => {
  if (pollTimer) window.clearInterval(pollTimer)
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
  margin-top: 4px;
  width: 100%;
}
</style>
