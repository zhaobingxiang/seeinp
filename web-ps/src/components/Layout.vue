<template>
  <div class="app-layout">
    <aside class="sidebar">
      <div class="logo">
        <div class="logo-icon"><el-icon><Connection /></el-icon></div>
        seeinps
      </div>
      <el-menu :default-active="route.path" router class="side-menu">
        <el-menu-item index="/ps/dashboard"><el-icon><DataBoard /></el-icon><span>仪表盘</span></el-menu-item>
        <el-menu-item index="/ps/proxies"><el-icon><Share /></el-icon><span>代理管理</span></el-menu-item>
        <el-menu-item index="/ps/audit-logs"><el-icon><Tickets /></el-icon><span>审计日志</span></el-menu-item>
        <el-menu-item index="/ps/system-logs"><el-icon><Document /></el-icon><span>系统日志</span></el-menu-item>
      </el-menu>
      <div class="sidebar-foot">
        <div class="conn-state">
          <span class="conn-dot" :class="{ offline: status !== 'connected' }"></span>
          <span>{{ status === 'connected' ? '已连接 seeinpm' : '未连接' }}</span>
        </div>
        <div class="user-line">
          <span>{{ username || '未登录' }}</span>
          <el-button text size="small" @click="logout">退出</el-button>
        </div>
      </div>
    </aside>
    <div class="main-panel">
      <header class="topbar">
        <div class="page-title">{{ pageTitle }}</div>
        <div class="topbar-right">
          <el-button text @click="logout">
            <el-icon style="margin-right:4px"><SwitchButton /></el-icon>退出登录
          </el-button>
        </div>
      </header>
      <main class="content">
        <slot />
      </main>
    </div>
  </div>
</template>
<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue"
import { useRoute, useRouter } from "vue-router"
import { DataBoard, Share, Tickets, Document, SwitchButton, Connection } from "@element-plus/icons-vue"
import { statusApi } from "@/api"

const route = useRoute()
const router = useRouter()
const pageTitle = computed(() => (route.meta.title as string) || "seeinps")
const status = ref("disconnected")
const username = ref("")
let timer: any = null

const loadStatus = async () => {
  try {
    const res: any = await statusApi.getStatus()
    if (res.code === 0) {
      status.value = res.data?.status || "disconnected"
      username.value = res.data?.username || ""
    }
  } catch (e) { /* 忽略：接口不可达时保持未连接 */ }
}

const logout = () => { localStorage.removeItem("token"); router.push("/ps/login") }

onMounted(() => { loadStatus(); timer = setInterval(loadStatus, 10000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>
<style scoped>
.sidebar-foot {
  border-top: 1px solid var(--app-border-light);
  padding: 12px 16px;
  font-size: 12px;
  color: var(--app-text-tertiary);
}
.conn-state { display: flex; align-items: center; gap: 6px; margin-bottom: 4px; }
.conn-dot { width: 8px; height: 8px; border-radius: 50%; background: #10b981; }
.conn-dot.offline { background: #9ca3af; }
.user-line { display: flex; align-items: center; justify-content: space-between; }
.user-line span { color: var(--app-text-secondary); }
</style>
