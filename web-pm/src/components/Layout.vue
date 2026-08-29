<template>
  <div class="app-layout">
    <aside class="sidebar">
      <div class="logo">
        <div class="logo-icon"><el-icon><Connection /></el-icon></div>
        seeinpm
      </div>
      <el-menu :default-active="route.path" router class="side-menu">
        <el-menu-item index="/pm/dashboard"><el-icon><DataBoard /></el-icon><span>仪表盘</span></el-menu-item>
        <el-menu-item index="/pm/users"><el-icon><User /></el-icon><span>用户管理</span></el-menu-item>
        <el-menu-item index="/pm/proxies"><el-icon><Share /></el-icon><span>代理管理</span></el-menu-item>
        <el-menu-item index="/pm/ports"><el-icon><Connection /></el-icon><span>端口池</span></el-menu-item>
        <el-menu-item index="/pm/audit-logs"><el-icon><Tickets /></el-icon><span>审计日志</span></el-menu-item>
        <el-menu-item index="/pm/system-logs"><el-icon><Document /></el-icon><span>系统日志</span></el-menu-item>
      </el-menu>
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
import { computed } from "vue"
import { useRoute, useRouter } from "vue-router"
import { DataBoard, User, Share, Connection, Tickets, Document, SwitchButton } from "@element-plus/icons-vue"
const route = useRoute()
const router = useRouter()
const pageTitle = computed(() => (route.meta.title as string) || "seeinpm")
const logout = () => { localStorage.removeItem("pm_token"); router.push("/pm/login") }
</script>
