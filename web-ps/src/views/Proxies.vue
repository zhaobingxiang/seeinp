<template>
  <Layout>
    <!-- 添加 / 编辑代理 -->
    <div class="page-card" style="padding:20px;margin-bottom:20px">
      <div class="card-header" style="margin-bottom:18px">
        <span>{{ editingProxy ? '编辑代理' : '添加代理' }}</span>
      </div>
      <el-form :model="form" :rules="rules" ref="formRef" label-width="100px" style="max-width: 560px">
        <el-form-item label="代理名称" prop="id">
          <el-input v-model="form.id" :disabled="!!editingProxy" placeholder="如: my-ssh / ops-proxy" />
        </el-form-item>

        <el-form-item label="代理类型" prop="type">
          <el-select v-model="form.type" style="width: 100%" :disabled="!!editingProxy">
            <el-option label="TCP 映射" value="tcp" />
            <el-option label="运维代理 (HTTP)" value="ops_http" />
          </el-select>
        </el-form-item>

        <template v-if="form.type === 'tcp'">
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
              <el-option v-for="c in defaultACL" :key="c" :label="c" :value="c" />
            </el-select>
            <div class="form-tip">默认仅允许内网网段；可输入自定义 CIDR 后回车添加，0.0.0.0/0 表示不限制</div>
          </el-form-item>
        </template>

        <el-form-item>
          <el-button type="primary" @click="handleSave" :loading="saving">
            {{ editingProxy ? '更新' : '添加' }}
          </el-button>
          <el-button v-if="editingProxy" @click="cancelEdit">取消</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 代理列表 -->
    <div class="page-card" style="padding:20px">
      <div class="card-header" style="margin-bottom:16px">
        <span>已有代理</span>
      </div>

      <div v-if="proxies.length === 0" class="empty-state">
        <el-empty description="暂无代理" />
      </div>

      <el-table v-else :data="proxies" style="width: 100%">
        <el-table-column prop="id" label="名称" min-width="120" />
        <el-table-column prop="type" label="类型" width="110">
          <template #default="{ row }">
            <el-tag :type="row.type === 'tcp' ? 'primary' : 'warning'" size="small">
              {{ row.type === 'tcp' ? 'TCP' : '运维HTTP' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="本地地址" width="180">
          <template #default="{ row }">
            <span v-if="row.type === 'tcp'" style="font-family:var(--font-mono,monospace)">{{ row.localAddr }}:{{ row.localPort }}</span>
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
            <el-button type="primary" link @click="editProxy(row)">编辑</el-button>
            <el-button type="danger" link @click="deleteProxy(row.id)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-alert v-if="hasOps" type="info" :closable="false" style="margin-top: 14px"
        title="运维代理使用方式：外网电脑使用 seeinpc，服务器地址填 see.timesee.cn，再填 运维ID + 代理账号 + 代理密码 即可连接" />
    </div>
  </Layout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { proxyApi } from '@/api'
import Layout from '@/components/Layout.vue'

const formRef = ref<FormInstance>()
const saving = ref(false)
const editingProxy = ref<any>(null)
const proxies = ref<any[]>([])

const defaultACL = ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16']
const hasOps = computed(() => proxies.value.some((p: any) => p.type === 'ops_http'))

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
  id: [{ required: true, message: '请输入代理名称', trigger: 'blur' }],
  type: [{ required: true, message: '请选择类型', trigger: 'change' }],
  localAddr: [{ required: true, message: '请输入本地地址', trigger: 'blur' }],
  localPort: [{ required: true, message: '请输入本地端口', trigger: 'blur' }],
  proxyUsername: [{ required: true, message: '请输入代理账号', trigger: 'blur' }],
  proxyPassword: [
    {
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
          ElMessage.success('更新成功')
        } else if (res.data?.forwardPort) {
          ElMessage.success(form.type === 'ops_http'
            ? `添加成功，运维ID ${res.data.forwardPort}`
            : `添加成功，转发端口 ${res.data.forwardPort}`)
        } else {
          ElMessage.success('添加成功，待连接 seeinpm 后自动分配端口')
        }
        cancelEdit()
        loadProxies()
      } else {
        ElMessage.error(res.message || '操作失败')
      }
    } catch (error: any) {
      ElMessage.error(error.response?.data?.message || '操作失败')
    } finally {
      saving.value = false
    }
  })
}

const editProxy = (proxy: any) => {
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
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

const cancelEdit = () => {
  editingProxy.value = null
  Object.assign(form, emptyForm())
  if (formRef.value) {
    formRef.value.resetFields()
  }
}

const copyText = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`已复制: ${text}`)
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
}

const deleteProxy = async (id: string) => {
  try {
    await ElMessageBox.confirm('确定要删除该代理吗？删除后端口将被释放。', '确认删除', {
      type: 'warning',
      confirmButtonText: '确定',
      cancelButtonText: '取消'
    })

    const res: any = await proxyApi.delete(id)
    if (res.code === 0) {
      ElMessage.success('删除成功')
      if (editingProxy.value?.id === id) {
        cancelEdit()
      }
      loadProxies()
    } else {
      ElMessage.error(res.message || '删除失败')
    }
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error('删除失败')
    }
  }
}

onMounted(() => {
  loadProxies()
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
}
</style>
