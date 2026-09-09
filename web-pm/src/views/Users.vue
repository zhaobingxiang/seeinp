<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>seeinps 用户列表</span>
        <span class="header-actions">
          <el-button v-if="selectedUsers.length" type="primary" plain @click="openBatchMove">批量修改分组（{{ selectedUsers.length }}）</el-button>
          <el-button type="primary" @click="openCreate">创建用户</el-button>
        </span>
      </div>
      <div class="users-layout">
        <aside class="group-side">
          <div class="group-side-head">
            <span class="group-side-title">分组筛选</span>
            <el-checkbox v-model="includeSub" size="small" @change="onFilterChange">包含下级</el-checkbox>
          </div>
          <el-tree
            :data="sideTreeData"
            node-key="id"
            :props="{ label: 'name', children: 'children' }"
            default-expand-all
            highlight-current
            :expand-on-click-node="false"
            @node-click="onGroupNodeClick"
          >
            <template #default="{ data }">
              <span class="side-node">
                <span>{{ data.name }}</span>
                <span v-if="data.id !== 0" class="side-node-count">{{ data.userCount }}</span>
              </span>
            </template>
          </el-tree>
        </aside>
        <div class="user-main" style="padding:12px 20px 20px">
        <div class="filter-bar">
          <el-input v-model="filters.username" placeholder="用户名" clearable style="width:150px" @input="onFilterChange" />
          <el-select v-model="filters.status" placeholder="账号状态" clearable style="width:110px" @change="onFilterChange">
            <el-option label="启用" value="1" />
            <el-option label="禁用" value="0" />
          </el-select>
          <el-select v-model="filters.online" placeholder="在线情况" clearable style="width:110px" @change="onFilterChange">
            <el-option label="在线" value="online" />
            <el-option label="离线" value="offline" />
          </el-select>
          <el-select v-model="filters.expire" placeholder="有效期" clearable style="width:110px" @change="onFilterChange">
            <el-option label="永久" value="permanent" />
            <el-option label="未过期" value="valid" />
            <el-option label="已过期" value="expired" />
          </el-select>
          <el-input v-model="filters.port" placeholder="端口(匹配用户端口池)" clearable style="width:180px" @input="onFilterChange" />
        </div>
        <el-table ref="tableRef" :data="pageUsers" v-loading="loading" @sort-change="onSortChange" @selection-change="(rows: any[]) => selectedUsers = rows">
          <el-table-column type="selection" width="42" />
          <el-table-column prop="id" label="ID" width="60" />
          <el-table-column prop="username" label="用户名" min-width="110" sortable="custom" />
          <el-table-column prop="remark" label="备注" min-width="110" />
          <el-table-column label="所属分组" min-width="100">
            <template #default="{row}">
              <el-tag size="small" type="info" effect="plain">{{ groupNameMap[row.groupId] || '-' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="status" label="账号" width="80" sortable="custom"><template #default="{row}"><el-tag :type="row.status === 1 ? 'success' : 'danger'" size="small">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag></template></el-table-column>
          <el-table-column prop="online" label="在线" width="80" sortable="custom"><template #default="{row}"><el-tag :type="row.online ? 'success' : 'info'" size="small">{{ row.online ? '在线' : '离线' }}</el-tag></template></el-table-column>
          <el-table-column prop="expireDate" label="有效期" width="150" sortable="custom">
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
          <el-table-column label="带宽" width="95">
            <template #default="{row}">
              <span v-if="row.maxMbps > 0">{{ row.maxMbps }} Mbps</span>
              <span v-else style="color:var(--app-text-tertiary)">不限</span>
            </template>
          </el-table-column>
          <el-table-column label="流量周期" min-width="160">
            <template #default="{row}">
              <template v-if="row.quota && row.quota.enabled">
                <span>{{ fmtGB(row.quota.used) }} / {{ fmtGB(row.quota.limit) }}</span>
                <el-tag v-if="row.quota.exceeded" type="danger" size="small" style="margin-left:4px">超额停用</el-tag>
                <div class="cell-sub">{{ periodShort(row.quota.period) }} {{ fmtDate(row.quota.resetAt) }} 重置</div>
              </template>
              <span v-else style="color:var(--app-text-tertiary)">不限</span>
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
        <el-empty v-if="pageUsers.length === 0" :description="users.length && !filteredUsers.length ? '无匹配的用户' : '暂无用户'" />
        <PaginationBar v-if="sortedUsers.length > 0" v-model:page="page" v-model:pageSize="pageSize" :total="sortedUsers.length" />
        </div>
      </div>
    </div>

    <el-dialog v-model="showEdit" :title="editing ? '编辑用户' : '创建用户'" width="560px" :close-on-click-modal="false" @closed="resetForm">
      <el-form :model="form" :rules="rules" ref="formRef" label-width="90px">
        <el-form-item label="用户名" prop="username"><el-input v-model="form.username" :disabled="!!editing" placeholder="至少 3 个字符" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
        <el-form-item label="所属分组">
          <el-tree-select v-model="form.groupId" :data="groupSelectData" check-strictly :render-after-expand="false" default-expand-all placeholder="选择分组" style="width:100%" />
          <div class="form-tip">用户归属的分组（可在“分组管理”维护）；新建默认归入根分组</div>
        </el-form-item>
        <el-form-item label="有效期">
          <el-date-picker v-model="form.expireDate" type="date" value-format="YYYY-MM-DD" placeholder="选择日期（留空为永久有效）" style="width:100%" clearable />
          <div class="form-tip">到期后该用户的 seeinps 连接与全部代理将被断开；改回今天或以后，seeinps 自动重连恢复</div>
        </el-form-item>
        <el-form-item label="端口数量">
          <el-input-number v-model="form.maxPorts" :min="0" :max="65535" controls-position="right" style="width:100%" />
          <div class="form-tip">该用户最多可同时占用的转发端口数，0 表示不限制；超配额后新代理无法分配端口</div>
        </el-form-item>
        <el-form-item label="带宽限制">
          <el-input-number v-model="form.maxMbps" :min="0" :max="100000" controls-position="right" style="width:100%" />
          <div class="form-tip">Mbps，该用户全部代理共享的瞬时带宽上限（入+出合计），0 表示不限制；web-ui 管理代理不受限速</div>
        </el-form-item>
        <el-form-item label="总流量限制">
          <div style="width:100%">
            <el-input-number v-model="form.quotaGB" :min="0" :max="102400" :precision="1" controls-position="right" style="width:100%" @change="onQuotaGBChange" />
            <div v-if="form.quotaGB > 0" class="quota-row">
              <el-select v-model="form.quotaPeriod" style="width:110px">
                <el-option label="按月" value="month" />
                <el-option label="按季度" value="quarter" />
                <el-option label="按年" value="year" />
              </el-select>
              <el-date-picker v-model="form.quotaStart" type="date" value-format="YYYY-MM-DD" placeholder="起始日期（默认本月1日，0点重置）" style="flex:1" />
            </div>
            <div class="form-tip">周期内总流量（入+出）上限，单位 GB，0 表示不限制；达上限自动停用除 web-ui 外的代理，周期滚动后恢复。起始日为第一个周期起点，此后按同日期 0 点滚动重置</div>
            <div v-if="editing && editing.quota && editing.quota.enabled" class="form-tip">
              本周期已用 {{ fmtGB(editing.quota.used) }} / {{ fmtGB(editing.quota.limit) }}，{{ periodShort(editing.quota.period) }} {{ fmtDate(editing.quota.resetAt) }} 重置<span v-if="editing.quota.exceeded" style="color:var(--el-color-danger)">（已超额停用）</span>
            </div>
          </div>
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
    <el-dialog v-model="showBatchMove" title="批量修改分组" width="440px" :close-on-click-modal="false">
      <div style="margin-bottom:12px">已将 {{ selectedUsers.length }} 个用户移动到：</div>
      <el-tree-select v-model="batchGroupId" :data="groupSelectData" check-strictly :render-after-expand="false" default-expand-all placeholder="选择目标分组" style="width:100%" />
      <template #footer>
        <el-button :disabled="batchSaving" @click="showBatchMove = false">取消</el-button>
        <el-button type="primary" :loading="batchSaving" @click="handleBatchMove">确定</el-button>
      </template>
    </el-dialog>
  </Layout>
</template>
<script setup lang="ts">
import { ref, reactive, computed, onMounted } from "vue"
import { ElMessage, ElMessageBox } from "element-plus"
import type { FormInstance, FormRules } from "element-plus"
import { userApi, portApi, userGroupApi } from "@/api"
import Layout from "@/components/Layout.vue"
import PaginationBar from "@/components/PaginationBar.vue"
import { buildGroupTree, collectDescendantIds, toTreeSelectData, type UserGroupNode } from "@/utils/userGroups"

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

// ===== 用户分组 =====
const groupTree = ref<UserGroupNode[]>([])
const includeSub = ref(false)
const groupFilter = ref(0) // 0=全部用户（侧树顶级节点）
const tableRef = ref()
const selectedUsers = ref<any[]>([])
const showBatchMove = ref(false)
const batchGroupId = ref(0)
const batchSaving = ref(false)

// 侧栏树：顶级“全部用户”节点 + 分组树
const sideTreeData = computed(() => [{ id: 0, name: "全部用户", userCount: users.value.length, children: groupTree.value } as any])
// el-tree-select 数据源（分组树，可任意层级选择）
const groupSelectData = computed(() => toTreeSelectData(groupTree.value))
// id → 分组名映射（表格“所属分组”列展示）
const groupNameMap = computed(() => {
  const m: Record<number, string> = {}
  const walk = (nodes: UserGroupNode[]) => nodes.forEach((n) => { m[n.id] = n.name; walk(n.children) })
  walk(groupTree.value)
  return m
})
const rootGroupId = computed(() => groupTree.value.find((n) => n.isRoot)?.id || 0)

const loadGroups = async () => {
  try {
    const res: any = await userGroupApi.list()
    if (res.code === 0) groupTree.value = buildGroupTree(res.data || [])
  } catch (e) { console.error(e) }
}
const onGroupNodeClick = (data: any) => {
  groupFilter.value = data.id ?? 0
  page.value = 1
}
const openBatchMove = () => {
  if (!selectedUsers.value.length) { ElMessage.warning("请先勾选用户"); return }
  batchGroupId.value = 0
  showBatchMove.value = true
}
const handleBatchMove = async () => {
  if (!batchGroupId.value) { ElMessage.warning("请选择目标分组"); return }
  batchSaving.value = true
  try {
    const names = selectedUsers.value.map((u: any) => u.username)
    const res: any = await userApi.batchMoveGroup(names, batchGroupId.value)
    if (res.code === 0) {
      ElMessage.success(`已移动 ${res.data?.moved ?? names.length} 个用户`)
      showBatchMove.value = false
      tableRef.value?.clearSelection()
      loadUsers(); loadGroups()
    } else ElMessage.error(res.message || "操作失败")
  } catch (e: any) {
    ElMessage.error(e.response?.data?.message || "操作失败")
  } finally {
    batchSaving.value = false
  }
}

// ===== 分页 / 筛选 / 排序 =====
const filters = reactive({ username: "", status: "", online: "", expire: "", port: "" })
const sortState = ref<{ prop: string; order: string }>({ prop: "", order: "" })
const page = ref(1)
const pageSize = ref(20)

const onFilterChange = () => { page.value = 1 }
const onSortChange = ({ prop, order }: { prop: string; order: string }) => {
  sortState.value = { prop: prop || "", order: order || "" }
  page.value = 1
}

const filteredUsers = computed(() => {
  let arr = users.value
  const kw = filters.username.trim().toLowerCase()
  if (kw) arr = arr.filter((u: any) => (u.username || "").toLowerCase().includes(kw))
  if (filters.status === "1") arr = arr.filter((u: any) => u.status === 1)
  else if (filters.status === "0") arr = arr.filter((u: any) => u.status !== 1)
  if (filters.online === "online") arr = arr.filter((u: any) => u.online)
  else if (filters.online === "offline") arr = arr.filter((u: any) => !u.online)
  if (filters.expire === "permanent") arr = arr.filter((u: any) => !u.expireDate)
  else if (filters.expire === "valid") arr = arr.filter((u: any) => u.expireDate && !u.expired)
  else if (filters.expire === "expired") arr = arr.filter((u: any) => u.expired)
  const port = parseInt(filters.port, 10)
  if (!isNaN(port)) {
    // 端口筛选：仅匹配有固定用户端口池且包含该端口的用户（不含「不限」）
    arr = arr.filter((u: any) => (u.portRanges || []).some((r: any) => r.start <= port && port <= r.end))
  }
  if (groupFilter.value > 0) {
    const allowed = includeSub.value
      ? collectDescendantIds(groupTree.value, groupFilter.value)
      : new Set<number>([groupFilter.value])
    arr = arr.filter((u: any) => allowed.has(u.groupId))
  }
  return arr
})

const sortedUsers = computed(() => {
  const { prop, order } = sortState.value
  if (!prop || !order || order === "null") return filteredUsers.value
  const dir = order === "ascending" ? 1 : -1
  const cmp = (a: any, b: any): number => {
    let va: any, vb: any
    switch (prop) {
      case "username": va = a.username; vb = b.username; break
      case "status": va = a.status; vb = b.status; break
      case "online": va = a.online ? 1 : 0; vb = b.online ? 1 : 0; break
      case "expireDate":
        va = a.expireDate ? new Date(a.expireDate).getTime() : Number.MAX_SAFE_INTEGER
        vb = b.expireDate ? new Date(b.expireDate).getTime() : Number.MAX_SAFE_INTEGER
        break
      default: return 0
    }
    if (va < vb) return -1 * dir
    if (va > vb) return 1 * dir
    return 0
  }
  return [...filteredUsers.value].sort(cmp)
})

const pageUsers = computed(() => sortedUsers.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))

// ===== 带宽 / 周期流量展示辅助 =====
const GB = 1024 * 1024 * 1024
const fmtGB = (bytes: number) => {
  if (!bytes || bytes <= 0) return '0 GB'
  const gb = bytes / GB
  return gb >= 100 ? `${Math.round(gb)} GB` : `${Math.round(gb * 10) / 10} GB`
}
const fmtDate = (unixSec: number) => {
  const d = new Date(unixSec * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
const periodShort = (p: string) => ({ month: "按月", quarter: "按季", year: "按年" } as Record<string, string>)[p] || "周期"
// 表单开启流量上限时给默认统计周期（按月）
const onQuotaGBChange = (v: number) => {
  if (v > 0 && !form.quotaPeriod) form.quotaPeriod = 'month'
  if (v <= 0) { form.quotaPeriod = ''; form.quotaStart = '' }
}

const form = reactive({
  username: "", remark: "", expireDate: "", maxPorts: 0, groupId: 0,
  maxMbps: 0, quotaGB: 0, quotaPeriod: "", quotaStart: "",
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
  Object.assign(form, { username: "", remark: "", expireDate: "", maxPorts: 0, groupId: rootGroupId.value, maxMbps: 0, quotaGB: 0, quotaPeriod: "", quotaStart: "", portRanges: [] })
  showEdit.value = true
}
const openEdit = (row: any) => {
  editing.value = row
  const q = row.quota && row.quota.enabled ? row.quota : null
  Object.assign(form, {
    username: row.username, remark: row.remark || "",
    expireDate: row.expireDate || "", maxPorts: row.maxPorts || 0,
    groupId: row.groupId || rootGroupId.value,
    maxMbps: row.maxMbps || 0,
    quotaGB: q ? Math.round((q.limit / GB) * 10) / 10 : 0,
    quotaPeriod: q ? q.period : "",
    quotaStart: q && q.startAt ? fmtDate(q.startAt) : "",
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
    const limitPayload = {
      expireDate: form.expireDate || "", maxPorts: form.maxPorts, portRanges: form.portRanges,
      maxMbps: form.maxMbps || 0,
      quotaBytes: form.quotaGB > 0 ? Math.round(form.quotaGB * 1024 * 1024 * 1024) : 0,
      quotaPeriod: form.quotaGB > 0 ? form.quotaPeriod : "",
      quotaStart: form.quotaGB > 0 ? form.quotaStart : ""
    }
    try {
      if (editing.value) {
        const res: any = await userApi.update(editing.value.username, {
          ...limitPayload, groupId: form.groupId
        })
        if (res.code === 0) {
          ElMessage.success(res.data?.kicked ? "已保存，配置收紧已断开该用户连接，将按新配置自动恢复" : "保存成功")
          showEdit.value = false
          loadUsers(); loadGroups()
        } else ElMessage.error(res.message || "操作失败")
      } else {
        const res: any = await userApi.create({
          username: form.username, remark: form.remark,
          ...limitPayload, groupId: form.groupId
        })
        if (res.code === 0) {
          createdAuthCode.value = res.data.authCode; authCodeIsNew.value = false; showEdit.value = false; showAuthCode.value = true; loadUsers(); loadGroups(); ElMessage.success("创建成功")
        } else ElMessage.error(res.message || "操作失败")
      }
    } catch (e: any) { ElMessage.error(e.response?.data?.message || "操作失败") } finally { saving.value = false }
  })
}

const deleteUser = async (username: string) => { try { await ElMessageBox.confirm("确定删除用户 " + username + " 吗？", "确认", { type: "warning" }); const res: any = await userApi.delete(username); if (res.code === 0) { ElMessage.success("已删除"); loadUsers(); loadGroups() } else ElMessage.error(res.message || "操作失败") } catch (e) { if (e !== "cancel") ElMessage.error("操作失败") } }
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
onMounted(() => { loadUsers(); loadGlobalRanges(); loadGroups() })
</script>
<style scoped>
.header-actions {
  display: inline-flex;
  gap: 8px;
}

.users-layout {
  display: flex;
  align-items: stretch;
}

.group-side {
  flex: 0 0 230px;
  border-right: 1px solid var(--app-border-light);
  padding: 12px 8px 20px 16px;
  overflow: auto;
  max-height: calc(100vh - 200px);
}

.group-side-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  padding-right: 8px;
}

.group-side-title {
  font-weight: 600;
  font-size: 13px;
}

.side-node {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding-right: 4px;
}

.side-node-count {
  margin-left: auto;
  font-size: 12px;
  color: var(--app-text-tertiary);
}

.user-main {
  flex: 1;
  min-width: 0;
}

.filter-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 14px;
}

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

.quota-row {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  width: 100%;
}

.cell-sub {
  font-size: 12px;
  color: var(--app-text-tertiary);
  line-height: 1.4;
}
</style>
