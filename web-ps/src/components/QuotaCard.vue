<template>
  <!-- 周期流量用量提醒（seeinpm 经控制通道同步到 B 端缓存；配置了配额才显示） -->
  <div v-if="quota.enabled" class="page-card quota-card" style="padding:14px 20px;margin-bottom:16px">
    <div class="quota-head">
      <span>周期流量用量（{{ periodLabel }}）</span>
      <span class="quota-time">重置时间：{{ resetLabel }}</span>
    </div>
    <el-progress :percentage="quotaPercent" :status="barStatus" :stroke-width="10" style="margin-top:8px" />
    <div class="quota-time">已用 {{ fmtBytes(quota.used) }} / 上限 {{ fmtBytes(quota.limit) }}（{{ quotaPercent }}%）</div>
    <el-alert v-if="quota.exceeded" type="error" :closable="false" show-icon style="margin-top:10px" :title="exceededTitle" />
    <el-alert v-else-if="quotaPercent >= 90" type="warning" :closable="false" show-icon style="margin-top:10px"
      title="本周期流量即将用尽，达上限后业务代理将自动停用（web-ui 管理页除外）。" />
  </div>
</template>
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { quotaApi } from '@/api'

const quota = ref<any>({ enabled: false })
let timer: number | undefined

const periodNames: Record<string, string> = { month: '按月', quarter: '按季度', year: '按年' }
const periodLabel = computed(() => periodNames[quota.value.period] || '周期')
const quotaPercent = computed(() => {
  const { used = 0, limit = 0 } = quota.value
  if (!limit) return 0
  return Math.min(100, Math.round((used / limit) * 1000) / 10)
})
const barStatus = computed(() => (quota.value.exceeded ? 'exception' : quotaPercent.value >= 90 ? 'warning' : ''))
const resetLabel = computed(() => {
  const t = quota.value.periodEnd
  if (!t) return '-'
  const d = new Date(t * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
})
const fmtBytes = (n?: number) => {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}
const exceededTitle = computed(() => `本周期流量已达上限，业务代理已停用（本管理页不受影响）。到重置时间 ${resetLabel.value} 自动恢复；如急需使用请联系管理员上调限额。`)

const loadQuota = async () => {
  try {
    const res: any = await quotaApi.get()
    if (res.code === 0) quota.value = res.data || { enabled: false }
  } catch (e) { console.error('Load quota error:', e) }
}

onMounted(() => {
  loadQuota()
  // B 端配额缓存随心跳响应（约 10s）刷新，同频轮询即可
  timer = window.setInterval(loadQuota, 10000)
})
onUnmounted(() => { if (timer) window.clearInterval(timer) })
</script>
<style scoped>
.quota-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  font-weight: 600;
}
.quota-time {
  font-size: 12px;
  color: var(--app-text-secondary);
  font-weight: 400;
}
</style>
