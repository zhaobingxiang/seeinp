<template>
  <Layout>
    <div class="page-card">
      <div class="card-header" style="padding:16px 20px;border-bottom:1px solid var(--app-border-light)">
        <span>用户分组</span>
        <el-button type="primary" @click="openCreate(selected ?? rootNode)">新建分组</el-button>
      </div>
      <div style="padding:12px 20px 20px">
        <el-alert type="info" :closable="false" style="margin-bottom:14px">
          分组为树状结构，最多 {{ MAX_GROUP_DEPTH }} 层（根分组计第 1 层）；根分组可改名但不可删除，新建分组默认挂在选中分组之下。
          含子分组或用户的分组需先移走内容才能删除。
        </el-alert>
        <el-tree
          ref="treeRef"
          :data="groupTree"
          node-key="id"
          :props="{ label: 'name', children: 'children' }"
          default-expand-all
          highlight-current
          :expand-on-click-node="false"
          v-loading="loading"
          @node-click="(_n: any, info: any) => selected = info.node.data"
        >
          <template #default="{ data }">
            <span class="group-node">
              <el-icon class="group-node-icon"><Folder /></el-icon>
              <span class="group-node-name">{{ data.name }}</span>
              <el-tag v-if="data.isRoot" size="small" type="warning" effect="plain">根分组</el-tag>
              <span class="group-node-count">{{ data.userCount }} 用户</span>
              <span class="group-node-actions">
                <el-button v-if="data.level < MAX_GROUP_DEPTH" type="primary" link size="small" @click.stop="openCreate(data)">新建下级</el-button>
                <el-button type="primary" link size="small" @click.stop="openRename(data)">重命名</el-button>
                <el-button v-if="!data.isRoot" type="danger" link size="small" @click.stop="handleDelete(data)">删除</el-button>
              </span>
            </span>
          </template>
        </el-tree>
        <el-empty v-if="!loading && groupTree.length === 0" description="暂无分组" />
      </div>
    </div>

    <el-dialog v-model="showDialog" :title="dialogTitle" width="420px" :close-on-click-modal="false">
      <el-form @submit.prevent>
        <el-form-item v-if="dialogMode === 'create'" label="上级分组">
          <span>{{ parentGroup?.name }}</span>
          <span v-if="parentGroup && parentGroup.level >= MAX_GROUP_DEPTH" style="color:var(--el-color-danger);margin-left:8px">已达最大层数</span>
        </el-form-item>
        <el-form-item label="分组名称">
          <el-input v-model="dialogName" maxlength="30" placeholder="不超过 30 个字符" show-word-limit @keyup.enter="handleDialogOk" />
          <div class="form-tip">同级分组内名称不可重复</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="showDialog = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="handleDialogOk">{{ dialogMode === 'create' ? '创建' : '保存' }}</el-button>
      </template>
    </el-dialog>
  </Layout>
</template>
<script setup lang="ts">
import { ref, computed, onMounted } from "vue"
import { ElMessage, ElMessageBox } from "element-plus"
import { Folder } from "@element-plus/icons-vue"
import { userGroupApi } from "@/api"
import Layout from "@/components/Layout.vue"
import { buildGroupTree, MAX_GROUP_DEPTH, type UserGroupNode } from "@/utils/userGroups"

const loading = ref(true)
const groupTree = ref<UserGroupNode[]>([])
const selected = ref<UserGroupNode | null>(null)

// 「新建分组」按钮的默认上级：选中分组，未选中时归根分组
const rootNode = computed<UserGroupNode | undefined>(() => groupTree.value.find((n) => n.isRoot))
const parentGroup = ref<UserGroupNode | null>(null)

const showDialog = ref(false)
const dialogMode = ref<'create' | 'rename'>('create')
const dialogName = ref("")
const saving = ref(false)
const renamingId = ref(0)
const dialogTitle = computed(() => (dialogMode.value === 'create' ? '新建分组' : '重命名分组'))

const loadGroups = async () => {
  loading.value = true
  try {
    const res: any = await userGroupApi.list()
    if (res.code === 0) groupTree.value = buildGroupTree(res.data || [])
  } catch (e) {
    console.error(e)
  } finally {
    loading.value = false
  }
}

const openCreate = (parent?: UserGroupNode) => {
  const p = parent || rootNode.value
  if (!p) { ElMessage.error("根分组不存在，无法创建"); return }
  if (p.level >= MAX_GROUP_DEPTH) { ElMessage.warning(`分组最多 ${MAX_GROUP_DEPTH} 层，「${p.name}」已在最后一层`); return }
  dialogMode.value = 'create'
  parentGroup.value = p
  dialogName.value = ""
  showDialog.value = true
}

const openRename = (node: UserGroupNode) => {
  dialogMode.value = 'rename'
  renamingId.value = node.id
  dialogName.value = node.name
  showDialog.value = true
}

const handleDialogOk = async () => {
  const name = dialogName.value.trim()
  if (!name) { ElMessage.warning("请输入分组名称"); return }
  saving.value = true
  try {
    if (dialogMode.value === 'create') {
      const res: any = await userGroupApi.create({ parentId: parentGroup.value!.id, name })
      if (res.code === 0) { ElMessage.success("已创建"); showDialog.value = false; loadGroups() }
      else ElMessage.error(res.message || "创建失败")
    } else {
      const res: any = await userGroupApi.rename(renamingId.value, name)
      if (res.code === 0) { ElMessage.success("已重命名"); showDialog.value = false; loadGroups() }
      else ElMessage.error(res.message || "重命名失败")
    }
  } catch (e: any) {
    ElMessage.error(e.response?.data?.message || "操作失败")
  } finally {
    saving.value = false
  }
}

const handleDelete = async (node: UserGroupNode) => {
  if (node.isRoot) { ElMessage.warning("根分组不可删除"); return }
  if (node.childCount > 0) { ElMessage.warning(`该分组下还有 ${node.childCount} 个子分组，请先处理子分组`); return }
  if (node.userCount > 0) { ElMessage.warning(`该分组下还有 ${node.userCount} 个用户，请先移动用户`); return }
  try {
    await ElMessageBox.confirm(`确定删除分组「${node.name}」吗？`, "确认", { type: "warning" })
  } catch { return }
  try {
    const res: any = await userGroupApi.remove(node.id)
    if (res.code === 0) { ElMessage.success("已删除"); loadGroups() }
    else ElMessage.error(res.message || "删除失败")
  } catch (e: any) {
    ElMessage.error(e.response?.data?.message || "删除失败")
  }
}

onMounted(loadGroups)
</script>
<style scoped>
.group-node {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding-right: 8px;
}

.group-node-icon {
  color: var(--el-color-warning);
}

.group-node-name {
  font-weight: 500;
}

.group-node-count {
  font-size: 12px;
  color: var(--app-text-tertiary);
}

.group-node-actions {
  margin-left: auto;
  display: none;
  gap: 4px;
}

.el-tree-node__content:hover .group-node-actions {
  display: inline-flex;
}

.form-tip {
  font-size: 12px;
  color: var(--app-text-tertiary);
  line-height: 1.5;
  margin-top: 4px;
  width: 100%;
}
</style>
