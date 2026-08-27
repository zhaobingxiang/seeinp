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
        <el-header class="header"><span>端口池管理</span><el-button type="danger" @click="logout">退出登录</el-button></el-header>
        <el-main>
          <el-row :gutter="20">
            <el-col :span="8"><el-card class="stat-card"><div class="stat-value">{{ pool.total || 0 }}</div><div class="stat-label">总数</div></el-card></el-col>
            <el-col :span="8"><el-card class="stat-card"><div class="stat-value" style="color:#E6A23C">{{ pool.used || 0 }}</div><div class="stat-label">已用</div></el-card></el-col>
            <el-col :span="8"><el-card class="stat-card"><div class="stat-value" style="color:#67C23A">{{ (pool.total || 0) - (pool.used || 0) }}</div><div class="stat-label">可用</div></el-card></el-col>
          </el-row>
          <el-card style="margin-top:20px">
            <template #header><span>端口池配置</span></template>
            <el-descriptions :column="2" border>
              <el-descriptions-item label="范围">20000 - 30000</el-descriptions-item>
              <el-descriptions-item label="总数">{{ pool.total }}</el-descriptions-item>
              <el-descriptions-item label="已分配">{{ pool.used }}</el-descriptions-item>
              <el-descriptions-item label="使用率"><el-progress :percentage="pool.total ? Math.round(pool.used / pool.total * 100) : 0" /></el-descriptions-item>
            </el-descriptions>
          </el-card>
        </el-main>
      </el-container>
    </el-container>
  </div>
</template>
<script setup lang="ts">
import { ref, onMounted } from "vue"
import { useRouter } from "vue-router"
import { portApi } from "@/api"
import { DataBoard, User, Connection } from "@element-plus/icons-vue"
const router = useRouter()
const pool = ref<any>({})
const loadPool = async () => { try { const res: any = await portApi.getPool(); if (res.code === 0) pool.value = res.data } catch (e) { console.error(e) } }
const logout = () => { localStorage.removeItem("pm_token"); router.push("/pm/login") }
onMounted(loadPool)
</script>
<style scoped>
.layout { height: 100vh; }
.sidebar { background: #001529; }
.logo { color: white; font-size: 20px; font-weight: bold; padding: 20px; text-align: center; }
.header { background: white; display: flex; justify-content: space-between; align-items: center; box-shadow: 0 1px 4px rgba(0,0,0,0.08); }
.el-menu { border-right: none; }
.stat-card { text-align: center; }
.stat-value { font-size: 32px; font-weight: bold; color: #409EFF; }
.stat-label { color: #909399; margin-top: 8px; }
</style>
