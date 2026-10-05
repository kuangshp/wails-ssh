<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Archive, ArchiveRestore, ArrowDownToLine, ArrowUpFromLine, ChevronDown, ChevronRight, Copy, Crosshair, File, FilePlus2, Folder, FolderPlus, Home, LockKeyhole, Pencil, RefreshCw, SquareTerminal, Trash2 } from '@lucide/vue'
import { api, errorText, formatBytes, type Directory, type RemoteEntry } from '../api'
import { buildRemoteTree, canExtractArchive, remoteAncestors, remoteParent, type RemoteAction, type RemoteTreeNode } from '../remote-files'

const props = defineProps<{
  sessionId: string; home: string; directory: Directory | null; loading: boolean;
  error: string; connected: boolean; workingPath: string;
  listDirectory?: (path: string) => Promise<Directory>;
}>()
const emit = defineEmits<{
  navigate: [path: string]; refresh: [path: string]; select: [entry: RemoteEntry];
  action: [action: RemoteAction]; upload: [path: string]; download: [path: string];
  terminal: [path: string]; copy: [text: string]; entries: [entries: RemoteEntry[]];
}>()
interface TreeState { listings: Map<string, RemoteEntry[]>; collapsed: Set<string>; selected: string }
const states = new Map<string, TreeState>()
const treeState = ref<TreeState>({ listings: new Map(), collapsed: new Set(), selected: '' })
const pathInput = ref('')
const ancestorsLoading = ref(false)
const ancestorError = ref('')
let refreshAncestors = false
let generation = 0
const currentPath = computed(() => props.directory?.path || props.home || '.')
const rows = computed(() => buildRemoteTree(currentPath.value, treeState.value.listings, treeState.value.collapsed))

watch(() => props.sessionId, id => {
  generation++
  if (!states.has(id)) states.set(id, { listings: new Map(), collapsed: new Set(), selected: '' })
  treeState.value = states.get(id)!
  pathInput.value = currentPath.value
  ancestorError.value = ''
  refreshAncestors = false
}, { immediate: true })

watch(() => [props.sessionId, props.directory] as const, async ([sessionId, directory]) => {
  if (!directory) return
  const request = ++generation
  const state = treeState.value
  pathInput.value = directory.path
  state.listings.set(directory.path, directory.entries || [])
  for (const path of remoteAncestors(directory.path)) state.collapsed.delete(path)
  publishEntries()
  const missing = remoteAncestors(directory.path).filter(path => path !== directory.path && (refreshAncestors || !state.listings.has(path)))
  refreshAncestors = false
  if (!missing.length || !props.connected) { ancestorsLoading.value = false; return }
  ancestorsLoading.value = true
  ancestorError.value = ''
  const results = await Promise.allSettled(missing.map(path => props.listDirectory ? props.listDirectory(path) : api.ListRemote(sessionId, path)))
  if (generation !== request || props.sessionId !== sessionId) return
  results.forEach((result, index) => {
    if (result.status === 'fulfilled') state.listings.set(missing[index]!, result.value.entries || [])
    else ancestorError.value = `部分父目录无法读取：${errorText(result.reason)}`
  })
  ancestorsLoading.value = false
  publishEntries()
}, { immediate: true })

function publishEntries() {
  const entries = new Map<string, RemoteEntry>()
  for (const node of rows.value) entries.set(node.entry.path, node.entry)
  emit('entries', [...entries.values()])
}

function navigate(path: string) { if (props.connected && path.trim()) emit('navigate', path.trim()) }
function refresh(path: string) { refreshAncestors = true; emit('refresh', path) }
function selectRow(node: RemoteTreeNode) {
  treeState.value.selected = node.entry.path
  emit('select', node.entry)
  if (!node.entry.isDir) return
  if (node.expanded) {
    treeState.value.collapsed.add(node.entry.path)
    publishEntries()
  } else {
    treeState.value.collapsed.delete(node.entry.path)
    navigate(node.entry.path)
  }
}

function menu(command: string, entry: RemoteEntry) {
  const parentPath = entry.isDir ? entry.path : remoteParent(entry.path)
  if (command === 'refresh') refresh(parentPath)
  else if (command === 'upload') emit('upload', parentPath)
  else if (command === 'download') { emit('select', entry); emit('download', entry.path) }
  else if (command === 'terminal') emit('terminal', parentPath)
  else if (command === 'copy-name') emit('copy', entry.name)
  else if (command === 'copy-path') emit('copy', entry.path)
  else emit('action', { kind: command as RemoteAction['kind'], entry, parentPath })
}
</script>

<template>
  <aside class="remote-file-panel legacy-dark-panel" aria-label="远程文件">
    <div class="remote-file-heading"><span class="remote-heading-title"><Folder :size="16" /><strong>远程文件</strong></span><el-button text size="small" :disabled="!connected || loading" @click="refresh(currentPath)">刷新</el-button></div>
    <form class="remote-path-form" @submit.prevent="navigate(pathInput)"><el-input v-model="pathInput" aria-label="远程路径" spellcheck="false" :disabled="!connected" /></form>
    <div class="remote-file-toolbar">
      <el-button text aria-label="主目录" title="主目录" :disabled="!connected" @click="navigate(home)"><Home :size="16" /></el-button>
      <el-button text class="remote-locate" aria-label="定位终端当前目录" title="定位终端当前目录" :disabled="!connected || !workingPath" @click="navigate(workingPath)"><Crosshair :size="16" /></el-button>
      <span class="remote-toolbar-divider" />
      <el-button text aria-label="刷新目录" title="刷新" :disabled="!connected || loading" @click="refresh(currentPath)"><RefreshCw :size="16" :class="{ spinning: loading || ancestorsLoading }" /></el-button>
      <el-button text aria-label="上传到当前目录" title="上传到当前目录" :disabled="!connected" @click="emit('upload', currentPath)"><ArrowUpFromLine :size="16" /></el-button>
    </div>
    <div v-if="loading || ancestorsLoading" class="remote-file-notice" role="status">正在读取目录…</div>
    <div v-if="error" class="remote-file-error" role="alert">{{ error }}<el-button text size="small" @click="refresh(currentPath)">重试</el-button></div>
    <div v-if="ancestorError" class="remote-file-notice remote-ancestor-error" :title="ancestorError">部分父目录无读取权限，已保留可用目录。</div>
    <div class="remote-file-tree" role="tree" aria-label="远程目录树">
      <el-dropdown v-for="node in rows" :key="node.entry.path" trigger="contextmenu" placement="bottom-start" :disabled="!connected" popper-class="legacy-remote-menu" @command="menu($event, node.entry)">
        <button class="remote-tree-row" :class="{ current: node.current, selected: treeState.selected === node.entry.path }" role="treeitem" :aria-level="node.depth + 1" :aria-expanded="node.entry.isDir ? node.expanded : undefined" :aria-selected="treeState.selected === node.entry.path" :style="{ paddingLeft: `${8 + node.depth * 22}px` }" :title="node.entry.path" :disabled="!connected" @click="selectRow(node)">
          <ChevronDown v-if="node.entry.isDir && node.expanded" :size="12" class="tree-arrow" /><ChevronRight v-else-if="node.entry.isDir" :size="12" class="tree-arrow" /><span v-else class="tree-arrow" />
          <Folder v-if="node.entry.isDir" :size="16" class="tree-folder" /><File v-else :size="16" class="tree-file" />
          <span class="tree-name">{{ node.entry.name }}<small v-if="node.entry.isSymlink"> ↗</small></span><span v-if="!node.entry.isDir" class="tree-size">{{ formatBytes(node.entry.size) }}</span>
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="refresh"><RefreshCw :size="14" />刷新</el-dropdown-item>
            <el-dropdown-item command="folder"><FolderPlus :size="14" />新建文件夹</el-dropdown-item>
            <el-dropdown-item command="file"><FilePlus2 :size="14" />新建文件</el-dropdown-item>
            <el-dropdown-item command="rename"><Pencil :size="14" />重命名</el-dropdown-item>
            <el-dropdown-item command="archive"><Archive :size="14" />压缩为 .tar.gz</el-dropdown-item>
            <el-dropdown-item v-if="canExtractArchive(node.entry)" command="extract"><ArchiveRestore :size="14" />解压到新文件夹</el-dropdown-item>
            <el-dropdown-item command="chmod"><LockKeyhole :size="14" />修改权限</el-dropdown-item>
            <el-dropdown-item command="download" divided><ArrowDownToLine :size="14" />下载</el-dropdown-item>
            <el-dropdown-item command="upload"><ArrowUpFromLine :size="14" />上传到此目录</el-dropdown-item>
            <el-dropdown-item command="terminal"><SquareTerminal :size="14" />终端</el-dropdown-item>
            <el-dropdown-item command="copy-name" divided><Copy :size="14" />复制文件名</el-dropdown-item>
            <el-dropdown-item command="copy-path"><Copy :size="14" />复制绝对路径</el-dropdown-item>
            <el-dropdown-item command="delete" divided class="remote-delete-command"><Trash2 :size="14" />删除</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <div v-if="!rows.length && !loading && !ancestorsLoading" class="remote-tree-empty"><Folder :size="26" /><strong>暂无文件</strong><small>目录为空，或尚未加载</small></div>
    </div>
  </aside>
</template>

<style scoped>
.remote-file-panel {
  width: 280px;
  flex: 0 0 280px;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 14px 12px 10px;
  box-sizing: border-box;
  overflow: hidden;
  border-right: 1px solid var(--ui-border);
  background: var(--ui-panel);
  color: var(--ui-text);
  font-size: 12px;
}
.remote-file-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 28px;
  margin-bottom: 12px;
}
.remote-heading-title { display: flex; align-items: center; gap: 8px; }
.remote-heading-title > svg { color: var(--ui-muted); }
.remote-file-heading strong { font-size: 13px; font-weight: 600; }
.remote-file-heading .el-button { color: var(--ui-muted); font-size: 11px; }
.remote-path-form { margin: 0; }
.remote-path-form :deep(.el-input__wrapper) {
  min-height: 34px;
  box-sizing: border-box;
  background: var(--ui-bg);
  border-radius: 7px;
}
.remote-path-form :deep(input) {
  font-family: Menlo, Monaco, Consolas, monospace;
  font-size: 11px;
  color: var(--ui-text);
}
.remote-file-toolbar {
  display: flex;
  align-items: center;
  gap: 5px;
  margin: 8px 0;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--ui-border);
}
.remote-file-toolbar .el-button {
  width: 30px;
  height: 28px;
  margin: 0;
  padding: 6px;
  border-radius: 6px;
  color: var(--ui-muted);
}
.remote-file-toolbar .el-button:not(:disabled):hover { background: var(--ui-hover); color: var(--ui-text); }
.remote-file-toolbar .remote-locate:not(:disabled) { color: var(--ui-accent); }
.remote-file-toolbar .el-button:disabled { opacity: .38; }
.remote-toolbar-divider { width: 1px; height: 16px; margin: 0 3px; background: var(--ui-border); }
.remote-file-notice { padding: 5px 4px 8px; font-size: 11px; color: var(--ui-accent); line-height: 1.5; }
.remote-ancestor-error { color: var(--ui-muted); }
.remote-file-error {
  padding: 8px;
  margin-bottom: 8px;
  border: 1px solid rgb(244 126 132 / 20%);
  border-radius: 6px;
  background: rgb(244 126 132 / 7%);
  color: #f48d94;
  font-size: 11px;
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.remote-file-tree {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding-bottom: 8px;
  scrollbar-width: thin;
  scrollbar-color: var(--ui-border) transparent;
}
.remote-file-tree :deep(.el-dropdown) { display: block; width: 100%; line-height: normal; }
.remote-tree-row {
  position: relative;
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  height: 32px;
  margin: 1px 0;
  padding-right: 8px;
  box-sizing: border-box;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--ui-text);
  font: inherit;
  text-align: left;
  cursor: pointer;
  outline-offset: -2px;
  transition: background .12s ease;
}
.remote-tree-row:hover { background: var(--ui-hover); }
.remote-tree-row:focus-visible { outline: 1px solid var(--ui-accent); }
.remote-tree-row.current { background: rgb(66 197 138 / 12%); color: #a5ebc8; }
.remote-tree-row.current::before { content: ''; position: absolute; left: 0; top: 8px; bottom: 8px; width: 2px; border-radius: 2px; background: var(--ui-accent); }
.remote-tree-row.selected:not(.current) { background: var(--ui-hover); }
.remote-tree-row:disabled { cursor: default; opacity: .55; }
.tree-arrow { width: 12px; flex: 0 0 12px; color: var(--ui-muted); }
.tree-folder { color: #d9b56f; fill: rgb(217 181 111 / 12%); }
.tree-file { color: #8eabc8; }
.tree-folder, .tree-file { flex-shrink: 0; stroke-width: 1.7; }
.tree-name { min-width: 20px; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.tree-size { flex-shrink: 0; font-size: 11px; color: var(--ui-muted); font-variant-numeric: tabular-nums; }
.remote-tree-empty { display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 34px 8px; color: var(--ui-muted); line-height: 1.6; }
.remote-tree-empty > svg { margin-bottom: 5px; opacity: .6; stroke-width: 1.5; }
.remote-tree-empty strong { font-size: 12px; font-weight: 500; }
.remote-tree-empty small { font-size: 11px; opacity: .7; }
</style>

<style>
.legacy-remote-menu {
  --el-bg-color-overlay: var(--ui-raised);
  --el-fill-color-light: var(--ui-hover);
  --el-text-color-primary: var(--ui-text);
  --el-text-color-regular: var(--ui-text);
  --el-border-color-light: var(--ui-border);
  --el-color-primary: var(--ui-accent);
  border-radius: 9px !important;
  box-shadow: 0 12px 40px rgb(0 0 0 / 32%) !important;
}
.legacy-remote-menu .el-dropdown-menu { padding: 5px; border-radius: 9px; }
.legacy-remote-menu .el-dropdown-menu__item { gap: 9px; min-width: 158px; min-height: 31px; padding: 5px 10px; border-radius: 5px; font-size: 12px; line-height: 20px; }
.legacy-remote-menu .el-dropdown-menu__item > svg { flex-shrink: 0; color: var(--ui-muted); }
.legacy-remote-menu .el-dropdown-menu__item:focus > svg { color: var(--ui-accent); }
.legacy-remote-menu .el-dropdown-menu__item--divided { margin-top: 6px; }
.legacy-remote-menu .remote-delete-command, .legacy-remote-menu .remote-delete-command > svg { color: #f48d94; }
.legacy-remote-menu .remote-delete-command:focus { color: #ffacb1; background: rgb(244 126 132 / 10%); }
.legacy-remote-menu .remote-delete-command:focus > svg { color: #ffacb1; }
.legacy-remote-menu .el-popper__arrow:before { background: var(--ui-raised); }
</style>
