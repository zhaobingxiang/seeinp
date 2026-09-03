<template>
  <div class="pagination-bar">
    <el-pagination
      background
      layout="total, sizes, prev, pager, next, jumper"
      :total="total"
      :current-page="page"
      :page-size="pageSize"
      :page-sizes="[20, 50, 100, 200]"
      @current-change="(p: number) => emit('update:page', p)"
      @size-change="(s: number) => { emit('update:pageSize', s); emit('update:page', 1) }"
    />
  </div>
</template>
<script setup lang="ts">
// 通用分页条：默认 20 条/页，可选 20/50/100/200
defineProps<{ total: number; page: number; pageSize: number }>()
const emit = defineEmits<{
  (e: 'update:page', v: number): void
  (e: 'update:pageSize', v: number): void
}>()
</script>
<style scoped>
.pagination-bar {
  display: flex;
  justify-content: flex-end;
  padding: 14px 4px 2px;
}
</style>