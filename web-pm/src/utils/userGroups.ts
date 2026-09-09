// 用户分组树公共工具：Users.vue 与 UserGroups.vue 共用
// 后端返回平铺列表（id/parentId/name/isRoot/userCount/childCount），前端按 parentId 组树

export interface UserGroupNode {
  id: number
  parentId: number
  name: string
  isRoot: boolean
  userCount: number
  childCount: number
  level: number
  children: UserGroupNode[]
}

// 分组树最大层数（根分组计第 1 层），与后端 store.MaxUserGroupDepth 保持一致
export const MAX_GROUP_DEPTH = 5

/** 平铺列表 → 嵌套树（根分组排最前，同级按 id 升序；level 从 1 开始） */
export function buildGroupTree(list: any[]): UserGroupNode[] {
  const map = new Map<number, UserGroupNode>()
  list.forEach((g: any) => map.set(g.id, { ...g, level: 0, children: [] as UserGroupNode[] }))
  const roots: UserGroupNode[] = []
  map.forEach((n) => {
    const parent = n.parentId ? map.get(n.parentId) : undefined
    if (parent) parent.children.push(n)
    else roots.push(n)
  })
  const sortRec = (nodes: UserGroupNode[], level: number) => {
    nodes.sort((a, b) => (Number(b.isRoot) - Number(a.isRoot)) || (a.id - b.id))
    nodes.forEach((n) => { n.level = level; sortRec(n.children, level + 1) })
  }
  sortRec(roots, 1)
  return roots
}

function findNode(nodes: UserGroupNode[], id: number): UserGroupNode | undefined {
  for (const n of nodes) {
    if (n.id === id) return n
    const hit = findNode(n.children, id)
    if (hit) return hit
  }
  return undefined
}

/** 收集分组自身及全部后代的 id 集合（用于「包含下级」筛选） */
export function collectDescendantIds(tree: UserGroupNode[], id: number): Set<number> {
  const set = new Set<number>()
  const walk = (n: UserGroupNode) => {
    set.add(n.id)
    n.children.forEach(walk)
  }
  const node = findNode(tree, id)
  if (node) walk(node)
  return set
}

export interface GroupTreeSelectNode {
  value: number
  label: string
  disabled?: boolean
  children?: GroupTreeSelectNode[]
}

/** 分组树 → el-tree-select 数据源 */
export function toTreeSelectData(nodes: UserGroupNode[]): GroupTreeSelectNode[] {
  return nodes.map((n) => ({
    value: n.id,
    label: n.name,
    children: n.children.length ? toTreeSelectData(n.children) : undefined
  }))
}
