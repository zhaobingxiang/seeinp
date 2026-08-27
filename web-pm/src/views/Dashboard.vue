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
        <el-header class="header"><span>seeinpm</span><el-button type="danger" @click="logout">退出登录</el-button></el-header>
        <el-main>
          <div class="stats">
            <el-card class="stat-card"><div class="stat-value">{{ health.clients || 0 }}</div><div class="stat-label">客户端</div></el-card>
            <el-card class="stat-card"><div class="stat-value">{{ health.ports?.used || 0 }} / {{ health.ports?.total || 0 }}</div><div class="stat-label">端口</div></el-card>
            <el-card class="stat-card"><div class="stat-value">{{ health.version }}</div><div class="stat-label">版本</div></el-card>
          </div>
          <el-card>
            <template #header><span>在线客户端</span></template>
            <el-table :data="clients" v-loading="loadingClients">
              <el-table-column prop="username" label="用户名" />
              <el-table-column prop="sessionId" label="会话"><template #default="{row}">{{ row.sessionId?.substring(0,8) }}...</template></el-table-column>
              <el-table-column label="连接时间"><template #default="{row}">{{ formatTime(row.connected) }}</template></el-table-column>
            </el-table>
            <el-empty v-if="clients.length === 0" description="暂无在线客户端" />
          </el-card>
        </el-main>
      </el-container>
    </el-container>
  </div>
</template>
<script setup lang="ts">
import { ref, computed, onMounted } from "vue"
import { useRoute, useRouter } from "vue-router"
import { healthApi, clientApi } from "@/api"
import { DataBoard, User, Connection } from "@element-plus/icons-vue"
const route = useRoute()
const router = useRouter()
const currentPath = computed(() => route.path)
const health = ref<any>({})
const clients = ref<any[]>([])
const loadingClients = ref(true)
const formatTime = (ts: number) => { if (!ts) return "-"; return new Date(ts * 1000).toLocaleString() }
const loadData = async () => { try { const [h, c]: any[] = await Promise.all([healthApi.get(), clientApi.list()]); if (h) health.value = h; if (c?.data) clients.value = c.data } catch (e) { console.error(e) } finally { loadingClients.value = false } }
const logout = () => { localStorage.removeItem("pm_token"); router.push("/pm/login") }
onMounted(loadData)
</script>
<style scoped>
.layout { height: 100vh; }
.sidebar { background: #001529; }
.logo { color: white; font-size: 20px; font-weight: bold; padding: 20px; text-align: center; }
.header { background: white; display: flex; justify-content: space-between; align-items: center; box-shadow: 0 1px 4px rgba(0,0,0,0.08); }
.stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; margin-bottom: 20px; }
.stat-card { text-align: center; }
.stat-value { font-size: 28px; font-weight: bold; color: #409EFF; }
.stat-label { color: #909399; margin-top: 8px; }
.el-menu { border-right: none; }
</style>
