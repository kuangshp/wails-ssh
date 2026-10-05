<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowUp, ChevronRight, File, Folder, House, RefreshCw, Search, X } from '@lucide/vue'
import AppDialog from './AppDialog.vue'
import { api, errorText, formatBytes, type LocalDirectory, type LocalEntry } from '../api'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; select: [entries: LocalEntry[]] }>()
const directory = ref<LocalDirectory | null>(null)
const pathInput = ref('')
const query = ref('')
const hidden = ref(false)
const loading = ref(false)
const error = ref('')
const selection = ref(new Map<string, LocalEntry>())
let request = 0

const visibleEntries = computed(() => (directory.value?.entries || []).filter(entry =>
  (hidden.value || !entry.name.startsWith('.')) && entry.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
))
const selectableEntries = computed(() => visibleEntries.value.filter(entry => !entry.isSymlink))
const selectedVisible = computed(() => selectableEntries.value.filter(entry => selection.value.has(entry.path)).length)
const selected = computed(() => [...selection.value.values()])
const selectedFolders = computed(() => selected.value.filter(entry => entry.isDir).length)

async function navigate(path: string) {
  const current = ++request
  loading.value = true
  error.value = ''
  try {
    const result = await api.ListLocalDirectory(path)
    if (current !== request || !props.modelValue) return
    directory.value = result
    pathInput.value = result.path
    query.value = ''
  } catch (cause) {
    if (current === request && props.modelValue) {
      error.value = `无法打开${path ? `“${path}”` : '主目录'}：${errorText(cause)}`
      if (directory.value) pathInput.value = directory.value.path
    }
  } finally {
    if (current === request) loading.value = false
  }
}
watch(() => props.modelValue, open => {
  if (open) {
    selection.value = new Map()
    query.value = ''
    void navigate(directory.value?.path || '')
  } else {
    request++
    loading.value = false
  }
}, { immediate: true })
function toggle(entry: LocalEntry, checked: boolean) {
  if (entry.isSymlink) return
  if (checked) selection.value.set(entry.path, entry)
  else selection.value.delete(entry.path)
}
function toggleVisible(checked: boolean) {
  for (const entry of selectableEntries.value) toggle(entry, checked)
}
function openDirectory(entry: LocalEntry) {
  if (entry.isDir && !entry.isSymlink) void navigate(entry.path)
}
function rowClass({ row }: { row: LocalEntry }) { return selection.value.has(row.path) ? 'picker-selected' : '' }
function confirm() {
  if (!selected.value.length) return
  emit('select', selected.value)
  emit('update:modelValue', false)
}
</script>

<template>
  <AppDialog :model-value="modelValue" title="选择上传文件与文件夹" :width="800" class="local-source-picker" @update:model-value="emit('update:modelValue', $event)">
    <p class="picker-description">文件和文件夹可以一起勾选，双击文件夹进入。切换目录后，已选项目会保留。</p>
    <form class="picker-path" @submit.prevent="navigate(pathInput)">
      <el-button aria-label="本地主目录" title="主目录" @click="navigate(directory?.homePath || '')"><House :size="16" /></el-button>
      <el-button aria-label="本地上一级目录" title="上一级" :disabled="!directory?.parentPath" @click="navigate(directory!.parentPath)"><ArrowUp :size="16" /></el-button>
      <el-input v-model="pathInput" aria-label="本地目录路径" placeholder="输入本地文件夹路径，按回车打开" spellcheck="false" />
      <el-button native-type="submit">前往</el-button>
      <el-button aria-label="刷新本地目录" title="刷新" @click="navigate(directory?.path || '')"><RefreshCw :size="15" /></el-button>
    </form>
    <div class="picker-filter">
      <el-input v-model="query" clearable aria-label="筛选本地文件" placeholder="筛选当前目录"><template #prefix><Search :size="14" /></template></el-input>
      <el-checkbox v-model="hidden">显示隐藏项目</el-checkbox>
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon />
    <el-table v-loading="loading" :data="visibleEntries" row-key="path" :height="286" :row-class-name="rowClass" empty-text="此目录没有匹配的文件或文件夹" @row-dblclick="openDirectory">
      <el-table-column width="46" align="center">
        <template #header><el-checkbox aria-label="全选当前列表" :model-value="selectableEntries.length > 0 && selectedVisible === selectableEntries.length" :indeterminate="selectedVisible > 0 && selectedVisible < selectableEntries.length" :disabled="!selectableEntries.length || loading" @change="toggleVisible(!!$event)" /></template>
        <template #default="{row}"><el-checkbox :aria-label="`选择 ${row.name}`" :model-value="selection.has(row.path)" :disabled="row.isSymlink || loading" @change="toggle(row, !!$event)" @dblclick.stop /></template>
      </el-table-column>
      <el-table-column label="名称" min-width="280">
        <template #default="{row}"><span class="picker-entry" :title="row.path"><Folder v-if="row.isDir" :size="17" /><File v-else :size="17" /><span>{{row.name}}</span></span></template>
      </el-table-column>
      <el-table-column label="类型" width="96"><template #default="{row}"><span :title="row.isSymlink ? '暂不支持上传符号链接' : ''">{{row.isSymlink ? '符号链接' : row.isDir ? '文件夹' : '文件'}}</span></template></el-table-column>
      <el-table-column label="大小" width="90" align="right"><template #default="{row}">{{row.isDir || row.isSymlink ? '—' : formatBytes(row.size)}}</template></el-table-column>
      <el-table-column width="45" align="center"><template #default="{row}"><el-button v-if="row.isDir && !row.isSymlink" text :aria-label="`进入 ${row.name}`" title="打开文件夹" @click="openDirectory(row)"><ChevronRight :size="15" /></el-button></template></el-table-column>
    </el-table>
    <section class="picker-selection" aria-label="本次已选项目">
      <div class="picker-selection-heading"><strong>已选 {{selected.length}} 项</strong><span v-if="selected.length">{{selectedFolders}} 个文件夹 · {{selected.length-selectedFolders}} 个文件</span><el-button text size="small" :disabled="!selected.length" @click="selection.clear()">清空选择</el-button></div>
      <div v-if="selected.length" class="picker-selection-list"><div v-for="entry in selected" :key="entry.path" class="picker-selected-entry" :title="entry.path"><Folder v-if="entry.isDir" :size="13" /><File v-else :size="13" /><span>{{entry.name}}</span><button :aria-label="`移除 ${entry.name}`" @click="selection.delete(entry.path)"><X :size="12" /></button></div></div>
      <p v-else>勾选列表中的项目，一次添加到上传列表。</p>
    </section>
    <template #footer><el-button @click="emit('update:modelValue', false)">取消</el-button><el-button type="primary" :disabled="!selected.length" @click="confirm">添加{{selected.length ? ` ${selected.length} 项` : '到上传列表'}}</el-button></template>
  </AppDialog>
</template>

<style scoped>
.picker-description{margin:0 0 14px;color:var(--ui-muted);font-size:12px;line-height:1.7}
.picker-path{display:flex;gap:7px;margin-bottom:12px}.picker-path .el-input{flex:1;min-width:0}.picker-path>.el-button{padding:0 10px}.picker-path :deep(input){font-family:Menlo,Consolas,monospace;font-size:12px}
.picker-filter{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:12px}.picker-filter>.el-input{max-width:310px}.picker-filter :deep(.el-checkbox){height:30px}
.picker-entry{display:flex;align-items:center;gap:9px;min-width:0}.picker-entry>svg{flex-shrink:0;color:var(--ui-muted)}.picker-entry>span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
:deep(.el-table){border:1px solid var(--ui-border);border-radius:8px;--el-table-bg-color:var(--ui-panel);--el-table-tr-bg-color:var(--ui-panel);--el-table-header-bg-color:var(--ui-bg);--el-table-row-hover-bg-color:var(--ui-hover);font-size:12px}
:deep(.el-table .cell){line-height:24px;padding:0 10px}:deep(.el-table .el-checkbox){height:25px}:deep(.el-table th.el-table__cell){color:var(--ui-muted);font-weight:500}:deep(.el-table .picker-selected){--el-table-tr-bg-color:#1b352b}:deep(.el-table .el-button){padding:0 5px;height:26px}
.picker-selection{margin-top:14px;border:1px solid var(--ui-border);border-radius:8px;padding:9px 12px;background:var(--ui-panel)}.picker-selection-heading{display:flex;align-items:center;gap:12px;font-size:12px}.picker-selection-heading>strong{font-weight:500}.picker-selection-heading>span{color:var(--ui-muted)}.picker-selection-heading>.el-button{margin-left:auto}.picker-selection>p{font-size:12px;color:var(--ui-muted);margin:4px 0}
.picker-selection-list{display:flex;gap:6px;flex-wrap:wrap;max-height:58px;overflow:auto;margin-top:6px}.picker-selected-entry{display:flex;align-items:center;gap:6px;max-width:230px;padding:3px 6px;border:1px solid var(--ui-border);border-radius:5px;background:var(--ui-raised);font-size:11px}.picker-selected-entry>span{overflow:hidden;white-space:nowrap;text-overflow:ellipsis}.picker-selected-entry>svg{flex-shrink:0;color:var(--ui-accent)}.picker-selected-entry>button{display:flex;align-items:center;border:0;border-radius:3px;padding:3px;background:transparent;color:var(--ui-muted);cursor:pointer}.picker-selected-entry>button:hover{color:var(--ui-text);background:var(--ui-hover)}
</style>
