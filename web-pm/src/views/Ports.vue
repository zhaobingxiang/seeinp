<template>
  <Layout>
    <div class="stat-grid">
      <div class="stat-card"><div class="stat-value">{{ pool.total || 0 }}</div><div class="stat-label">端口总数</div></div>
      <div class="stat-card"><div class="stat-value" style="color:var(--app-warning,#E6A23C)">{{ pool.used || 0 }}</div><div class="stat-label">已分配</div></div>
      <div class="stat-card"><div class="stat-value" style="color:var(--app-success,#67C23A)">{{ (pool.total || 0) - (pool.used || 0) }}</div><div class="stat-label">可用</div></div>
    </div>

    <div class="page-card" style="margin-bottom:20px">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>端口池范围配置（修改即时生效，无需重启）</span>
        <div>
          <el-button size="small" @click="addRange">添加范围</el-button>
          <el-button size="small" type="primary" :loading="saving" :disabled="!dirty" @click="save">保存</el-button>
        </div>
      </div>
      <div style="padding:16px 20px">
        <el-alert v-if="mergeHint" type="warning" :closable="false" show-icon style="margin-bottom:12px" :title="mergeHint" />
        <div v-for="(rg, i) in ranges" :key="i" class="range-row">
          <span class="range-label">范围 {{ i + 1 }}</span>
          <el-input-number v-model="rg.start" :min="1" :max="65535" controls-position="right" style="width:150px" @change="onEdit" />
          <span class="range-sep">—</span>
          <el-input-number v-model="rg.end" :min="1" :max="65535" controls-position="right" style="width:150px" @change="onEdit" />
          <span class="range-count">共 {{ rg.end >= rg.start ? rg.end - rg.start + 1 : 0 }} 个端口</span>
          <el-button type="danger" link :disabled="ranges.length <= 1" @click="removeRange(i)">删除</el-button>
        </div>
        <el-alert v-if="rangeError" type="error" :closable="false" show-icon style="margin-top:12px" :title="rangeError" />
      </div>
    </div>

    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>已分配端口（{{ pool.allocations?.length || 0 }}）</span>
      </div>
      <div style="padding:12px 20px 20px">
        <el-table :data="pageAllocations" size="small">
          <el-table-column prop="port" label="端口" width="100" />
          <el-table-column label="代理" min-width="180"><template #default="{ row }">{{ row.username }}.{{ row.proxyId }}</template></el-table-column>
          <el-table-column prop="type" label="类型" width="100" />
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag size="small" :type="row.inPool ? 'success' : 'danger'">{{ row.inPool ? '池内' : '池外' }}</el-tag>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="!(pool.allocations?.length)" description="暂无已分配端口" :image-size="60" />
        <PaginationBar v-if="(pool.allocations?.length || 0) > 0" v-model:page="page" v-model:pageSize="pageSize" :total="pool.allocations?.length || 0" />
      </div>
    </div>
  </Layout>
</template>
<script setup lang="ts">
import { ref, computed, onMounted } from "vue"
import { ElMessage, ElMessageBox } from "element-plus"
import { portApi } from "@/api"
import Layout from "@/components/Layout.vue"
import PaginationBar from "@/components/PaginationBar.vue"

const pool = ref<any>({})
const ranges = ref<any[]>([])
const saving = ref(false)
const dirty = ref(false)
// 已分配端口分页（默认 20 条/页）
const page = ref(1)
const pageSize = ref(20)
const pageAllocations = computed(() => {
  const allocs = pool.value.allocations || []
  return allocs.slice((page.value - 1) * pageSize.value, page.value * pageSize.value)
})

const rangeError = computed(() => {
  for (const rg of ranges.value) {
    if (!rg.start || !rg.end) return "起始和结束端口都必须填写"
    if (rg.start < 1 || rg.end > 65535) return "端口必须在 1-65535 之间"
    if (rg.start > rg.end) return `范围 ${rg.start}-${rg.end} 起始大于结束`
  }
  if (ranges.value.length === 0) return "至少需要一段端口范围"
  return ""
})
const mergeHint = computed(() => {
  const list = ranges.value.filter((r: any) => r.start && r.end && r.start <= r.end)
  for (let i = 0; i < list.length; i++) {
    for (let j = i + 1; j < list.length; j++) {
      const a = list[i], b = list[j]
      if (a.start <= b.end && b.start <= a.end) return `范围 ${a.start}-${a.end} 与 ${b.start}-${b.end} 重叠，保存时将自动合并`
      if (a.end + 1 === b.start || b.end + 1 === a.start) return `范围 ${a.start}-${a.end} 与 ${b.start}-${b.end} 相邻，保存时将自动合并`
    }
  }
  return ""
})

const loadPool = async () => {
  try {
    const res: any = await portApi.getPool()
    if (res.code === 0) {
      pool.value = res.data
      ranges.value = (res.data.ranges || []).map((r: any) => ({ start: r.start, end: r.end }))
      if (ranges.value.length === 0) ranges.value = [{ start: 20000, end: 30000 }]
      dirty.value = false
    }
  } catch (e) { console.error(e) }
}
const addRange = () => { ranges.value.push({ start: 20001, end: 20010 }); onEdit() }
const removeRange = (i: number) => { ranges.value.splice(i, 1); onEdit() }
const onEdit = () => { dirty.value = true }

const save = async () => {
  if (rangeError.value) { ElMessage.warning(rangeError.value); return }
  const outside = (pool.value.allocations || []).filter((a: any) => !a.inPool)
  let action = "recycle"
  if (outside.length > 0) {
    const desc = outside.map((a: any) => `${a.username}.${a.proxyId}（端口 ${a.port}）`).join("<br/>")
    try {
      await ElMessageBox.confirm(
        `以下 ${outside.length} 个代理的端口不在新池内：<br/>${desc}<br/><br/>选择处理方式：`,
        "端口池改小提示",
        {
          confirmButtonText: "回收（断开并重连到池内）",
          cancelButtonText: "保留（继续使用，新分配只用池内）",
          distinguishCancelAndClose: true,
          confirmButtonClass: "el-button--danger",
          type: "warning",
          dangerouslyUseHTMLString: true
        }
      )
      action = "recycle"
    } catch (e: any) {
      if (e === "cancel") { action = "keep" } else { return }
    }
  }
  saving.value = true
  try {
    const res: any = await portApi.updatePool(ranges.value.map((r: any) => ({ start: r.start, end: r.end })), action)
    if (res.code === 0) {
      const d = res.data
      ElMessage.success(`端口池已更新（共 ${d.total} 个端口${d.recycled?.length ? `，回收 ${d.recycled.length} 个代理重连` : ""}）`)
      loadPool()
    } else { ElMessage.error(res.message || "保存失败") }
  } catch (e: any) { ElMessage.error(e?.response?.data?.message || "保存失败") } finally { saving.value = false }
}
onMounted(loadPool)
</script>
<style scoped>
.range-row { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.range-label { width: 60px; color: var(--app-text-secondary); }
.range-sep { color: var(--app-text-tertiary); }
.range-count { color: var(--app-text-tertiary); font-size: 13px; }
</style>
