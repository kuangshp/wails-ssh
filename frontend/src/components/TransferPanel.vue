<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import LocalSourcePicker from './LocalSourcePicker.vue'
import { ArrowDownToLine, ArrowUpFromLine, Check, File, Folder, ShieldCheck, Square, X } from '@lucide/vue'
import { api, baseName, errorText, formatBytes, type RemoteEntry, type Transfer, type Conflict, type ConflictChoice, type LocalEntry } from '../api'
import { syncDownloadPath, transferFinished, transferRetryLabel, transferStatus } from '../remote-files'
import { createUploadRequest, transferDraftForContext, type TransferDraft } from '../transfer-draft'

interface TransferProgress extends Transfer { attempt?: number; delaySeconds?: number }

const props = defineProps<{
  sessionId: string; connected: boolean; currentPath: string; home: string;
  workingPath: string; entries: RemoteEntry[]; transfers: TransferProgress[]; uploadBusy?: boolean; downloadBusy?: boolean;
  conflicts: Conflict[]; resolvingConflicts: Set<string>;
  cancellingTransfers: Set<string>;
}>()
const emit = defineEmits<{
  upload: [request: { paths: string[]; destination: string }];
  download: [request: { path: string; destination: string }];
  resolve: [request: { id: string; choice: ConflictChoice }];
  cancel: [id: string]; remove: [id: string]; clear: []; error: [message: string];
}>()
const drafts = new Map<string, TransferDraft>()
const state = ref<TransferDraft>({ direction: 'upload', paths: [], uploadDirectory: '', downloadPath: '', localDirectory: '', previousDirectory: '', previousWorkingPath: '' })
const picking = ref(false)
const sourcePickerOpen = ref(false)
const sourceKinds = ref(new Map<string, boolean>())
let pickerDraft: TransferDraft | null = null
const sessionTransfers = computed(() => props.transfers.filter(item => item.sessionId === props.sessionId))
const sessionConflicts = computed(() => props.conflicts.filter(item => item.sessionId === props.sessionId))
const conflict = computed(() => sessionConflicts.value[0])
const conflictBusy = computed(() => !!conflict.value && props.resolvingConflicts.has(conflict.value.id))
const hasFinished = computed(() => sessionTransfers.value.some(item => transferFinished(item.status)))
function resolve(choice: ConflictChoice) { if (conflict.value && !conflictBusy.value) emit('resolve', { id: conflict.value.id, choice }) }
const active = computed(() => sessionTransfers.value.some(item => !transferFinished(item.status)))
const done = computed(() => sessionTransfers.value.filter(item => transferFinished(item.status)).length)
const uploads = computed(() => sessionTransfers.value.filter(item => item.direction === 'upload').length)
const downloads = computed(() => sessionTransfers.value.length - uploads.value)
const completed = computed(() => sessionTransfers.value.filter(item => item.status === 'completed').length)
const skipped = computed(() => sessionTransfers.value.filter(item => item.status === 'skipped').length)
const terminalDirectory = computed(() => props.workingPath || props.currentPath || props.home || '')
const remoteChoices = computed(() => {
  const choices = new Map<string, { name: string; path: string; isDir: boolean }>()
  for (const path of [props.workingPath, props.currentPath, props.home]) {
    if (path) choices.set(path, { name: baseName(path), path, isDir: true })
  }
  for (const entry of props.entries) choices.set(entry.path, entry)
  return [...choices.values()]
})

watch(() => [props.sessionId, props.currentPath, props.workingPath, props.home] as const, ([id, browsePath, workingPath]) => {
  const currentDirectory = workingPath || browsePath || props.home || '.'
  const draft = transferDraftForContext(drafts.get(id), browsePath, workingPath, props.home)
  state.value = draft
  // Store the reactive object so later cwd updates notify the rendered input,
  // while asynchronous file pickers keep their original session's draft.
  drafts.set(id, state.value)
  state.value.downloadPath = syncDownloadPath(state.value.downloadPath, state.value.previousDirectory, currentDirectory)
  state.value.previousDirectory = currentDirectory
}, { immediate: true })

function selectDownload(path: string, switchMode = true) {
  state.value.downloadPath = path
  if (switchMode) state.value.direction = 'download'
}
async function prepareUpload(path: string, pick = true) {
  state.value.direction = 'upload'
  state.value.uploadDirectory = path || props.currentPath || '.'
  if (pick) chooseSources()
}
defineExpose({ selectDownload, prepareUpload })

async function chooseSources() {
  if (picking.value || sourcePickerOpen.value || !props.connected) return
  const draft = state.value
  picking.value = true
  try {
    const result = await api.PickUploadSources()
    if (result.native) appendSources(draft, result.entries)
    else { pickerDraft = draft; sourcePickerOpen.value = true }
  } catch (error) { emit('error', errorText(error)) }
  finally { picking.value = false }
}
function appendSources(draft: TransferDraft, entries: LocalEntry[]) {
  draft.paths = [...new Set([...draft.paths, ...entries.map(entry => entry.path)])]
  for (const entry of entries) sourceKinds.value.set(entry.path, entry.isDir)
}
function acceptSources(entries: LocalEntry[]) {
  if (pickerDraft) appendSources(pickerDraft, entries)
  pickerDraft = null
}
async function chooseLocalDirectory() {
  if (picking.value) return
  const draft = state.value
  picking.value = true
  try { const path = await api.PickDownloadDirectory(); if (path) draft.localDirectory = path }
  catch (error) { emit('error', errorText(error)) } finally { picking.value = false }
}
function setDirection(direction: 'upload' | 'download') {
  state.value.direction = direction
  if (direction === 'download') {
    const path = props.workingPath || props.currentPath
    state.value.downloadPath = syncDownloadPath(state.value.downloadPath, state.value.previousDirectory, path)
    state.value.previousDirectory = path
  }
}
function startUpload() {
  if (!props.connected || props.uploadBusy || !state.value.paths.length || !state.value.uploadDirectory.trim()) return
  emit('upload', createUploadRequest(state.value.paths, state.value.uploadDirectory))
}
function startDownload() {
  if (!props.connected || props.downloadBusy || !state.value.downloadPath.trim() || !state.value.localDirectory.trim()) return
  emit('download', { path: state.value.downloadPath.trim(), destination: state.value.localDirectory.trim() })
}
function percentage(item: Transfer) {
  if (item.status === 'completed') return 100
  return item.total > 0 ? Math.min(100, Math.round(item.transferred / item.total * 100)) : 0
}
function stopping(item: Transfer) { return !transferFinished(item.status) && props.cancellingTransfers.has(item.id) }
</script>

<template>
  <aside class="legacy-transfer-panel legacy-dark-panel" aria-label="传输管理">
    <div class="legacy-transfer-heading"><span class="transfer-heading-symbol"><ArrowUpFromLine :size="14" /><ArrowDownToLine :size="14" /></span><div><strong>传输管理</strong><small>SFTP 上传与下载</small></div><span class="transfer-state" :class="{ busy: active }"><span />{{ active ? '传输中' : '空闲' }}</span></div>
    <el-radio-group :model-value="state.direction" class="legacy-transfer-direction" aria-label="传输方向" @change="setDirection($event as 'upload' | 'download')">
      <el-radio-button value="upload"><ArrowUpFromLine :size="14" />上传</el-radio-button>
      <el-radio-button value="download"><ArrowDownToLine :size="14" />下载</el-radio-button>
    </el-radio-group>
    <div v-if="state.direction === 'upload'" class="legacy-transfer-card" role="region" aria-label="上传到服务器">
      <div class="transfer-card-title"><strong>上传到服务器</strong><small>{{ state.paths.length }} 项</small></div>
      <div class="upload-source-actions">
        <el-button class="upload-source-picker" :loading="picking" :disabled="!connected || picking || sourcePickerOpen" @click="chooseSources"><Folder :size="14" />选择文件 / 文件夹</el-button>
        <el-button text :disabled="!state.paths.length" @click="state.paths = []">清空</el-button>
      </div>
      <div class="legacy-upload-sources" role="region" aria-label="已选择的上传来源" tabindex="0">
        <div v-for="path in state.paths" :key="path" :title="path"><Folder v-if="sourceKinds.get(path)" :size="13" /><File v-else :size="13" /><span>{{ baseName(path) }}</span><button aria-label="移除上传来源" :disabled="uploadBusy" @click="state.paths = state.paths.filter(source => source !== path)"><X :size="11" /></button></div>
        <small v-if="!state.paths.length"><File :size="18" /><span>选择要上传的文件或文件夹</span></small>
      </div>
      <div class="transfer-field-heading"><label class="transfer-field-label" for="remote-upload-directory">远程目标目录</label><el-button text size="small" :disabled="!connected || uploadBusy || !terminalDirectory.trim()" @click="state.uploadDirectory = terminalDirectory">使用当前目录</el-button></div>
      <div class="transfer-path-input"><el-input id="remote-upload-directory" v-model="state.uploadDirectory" spellcheck="false" :disabled="uploadBusy" /><el-dropdown trigger="click" popper-class="legacy-remote-menu legacy-path-picker" @command="state.uploadDirectory = $event"><el-button :disabled="!connected || uploadBusy">选择</el-button><template #dropdown><el-dropdown-menu><el-dropdown-item v-for="entry in remoteChoices.filter(item => item.isDir)" :key="entry.path" :command="entry.path" :title="entry.path"><Folder :size="14" /><span>{{ entry.path }}</span></el-dropdown-item></el-dropdown-menu></template></el-dropdown></div>
      <p class="transfer-directory-hint">目录变化只影响新任务，进行中的上传地址不变。</p>
      <el-button class="transfer-start-button" type="primary" :loading="uploadBusy" :disabled="!connected || uploadBusy || !state.paths.length || !state.uploadDirectory.trim()" @click="startUpload"><ArrowUpFromLine v-if="!uploadBusy" :size="14" />开始上传</el-button>
    </div>
    <div v-else class="legacy-transfer-card" role="region" aria-label="下载到电脑">
      <div class="transfer-card-title"><strong>下载到电脑</strong></div>
      <label class="transfer-field-label" for="remote-download-path">远程文件或文件夹路径</label>
      <div class="transfer-path-input"><el-input id="remote-download-path" v-model="state.downloadPath" spellcheck="false" :disabled="downloadBusy" /><el-dropdown trigger="click" popper-class="legacy-remote-menu legacy-path-picker" @command="state.downloadPath = $event"><el-button :disabled="!connected || downloadBusy">选择</el-button><template #dropdown><el-dropdown-menu><el-dropdown-item v-for="entry in remoteChoices" :key="entry.path" :command="entry.path" :title="entry.path"><Folder v-if="entry.isDir" :size="14" /><File v-else :size="14" /><span>{{ entry.path }}</span></el-dropdown-item></el-dropdown-menu></template></el-dropdown></div>
      <label class="transfer-field-label" for="local-download-directory">本地保存目录</label>
      <div class="transfer-path-input"><el-input id="local-download-directory" v-model="state.localDirectory" placeholder="选择本地保存目录" spellcheck="false" :disabled="downloadBusy" /><el-button :disabled="picking || downloadBusy" @click="chooseLocalDirectory">浏览</el-button></div>
      <el-button class="transfer-start-button" type="primary" :loading="downloadBusy" :disabled="!connected || downloadBusy || !state.downloadPath.trim() || !state.localDirectory.trim()" @click="startDownload"><ArrowDownToLine v-if="!downloadBusy" :size="14" />开始下载</el-button>
    </div>
    <div class="legacy-transfer-safety"><ShieldCheck :size="13" /><span>通过 SFTP 加密传输</span></div>
    <section v-if="conflict" class="transfer-conflict" aria-label="当前会话文件冲突">
      <div class="transfer-conflict-heading"><strong>同名文件需要处理</strong><el-tag type="warning" size="small">{{sessionConflicts.length}} 项</el-tag></div>
      <p>仅此任务等待选择，其他传输继续。</p>
      <dl><dt>源文件</dt><dd>{{conflict.source}}</dd><dt>目标文件</dt><dd>{{conflict.target}}</dd></dl>
      <div class="transfer-conflict-actions"><el-button size="small" :disabled="conflictBusy" @click="resolve('skip')">跳过</el-button><el-button size="small" type="primary" :disabled="conflictBusy" @click="resolve('overwrite')">覆盖</el-button><el-button size="small" :disabled="conflictBusy" @click="resolve('skip_all')">全部跳过</el-button><el-button size="small" :disabled="conflictBusy" @click="resolve('overwrite_all')">全部覆盖</el-button></div>
      <el-button class="transfer-conflict-cancel" size="small" text type="danger" :loading="conflictBusy" @click="resolve('cancel')">取消此任务</el-button>
    </section>
    <div class="legacy-transfer-tasks-heading"><strong>传输任务 <span>{{ done }} / {{ sessionTransfers.length }}</span></strong><el-button text size="small" :disabled="!hasFinished" @click="emit('clear')">清空</el-button></div>
    <div v-if="sessionTransfers.length" class="legacy-transfer-summary"><span>全部 {{ sessionTransfers.length }}</span><span class="summary-upload">↑ {{ uploads }}</span><span>↓ {{ downloads }}</span><span class="summary-upload">✓ {{ completed }}</span><span v-if="skipped">– {{ skipped }}</span></div>
    <div class="legacy-transfer-tasks" aria-live="polite">
      <div v-if="!sessionTransfers.length" class="legacy-transfer-no-tasks"><span><ArrowUpFromLine :size="20" /><ArrowDownToLine :size="20" /></span><strong>暂无传输任务</strong><small>传输进度将在这里显示</small></div>
      <div v-for="item in sessionTransfers" :key="item.id" class="legacy-transfer-task">
        <div class="transfer-task-name"><ArrowUpFromLine v-if="item.direction === 'upload'" :size="13" /><ArrowDownToLine v-else :size="13" /><strong :title="item.path">{{ baseName(item.path) }}</strong><el-button v-if="transferFinished(item.status)" text aria-label="移除传输任务" title="移除记录" @click="emit('remove', item.id)"><X :size="12" /></el-button></div>
        <div v-if="item.direction === 'upload' && item.destination" class="transfer-task-destination"><span>上传目录</span><span :title="item.destination">{{ item.destination }}</span></div>
        <div class="transfer-task-path" :title="item.path">{{ item.path }}</div>
        <el-progress :percentage="percentage(item)" :status="item.status === 'completed' ? 'success' : item.status === 'failed' ? 'exception' : undefined" :show-text="false" :stroke-width="4" :indeterminate="!item.total && !transferFinished(item.status)" />
        <div class="transfer-task-meta"><span :class="{ failed: item.status === 'failed', retrying: item.status === 'retrying', finished: item.status === 'completed' }"><Check v-if="item.status === 'completed'" :size="10" />{{ transferStatus(stopping(item) ? 'cancelling' : item.status) }}</span><span>{{ formatBytes(item.transferred) }}<template v-if="item.total"> / {{ formatBytes(item.total) }}</template></span></div>
        <p v-if="item.filesTotal" class="transfer-file-count">{{Math.min(item.filesTotal,item.filesDone || 0)}} / {{item.filesTotal}} 个文件<template v-if="item.activeFiles && !transferFinished(item.status) && !stopping(item)"> · {{item.activeFiles}} 个传输中</template><template v-if="item.filesSkipped"> · 跳过 {{item.filesSkipped}}</template></p>
        <div v-if="!transferFinished(item.status)" class="transfer-task-actions"><small>已完成的文件会保留</small><el-button type="danger" plain size="small" :loading="stopping(item)" :disabled="stopping(item)" @click="emit('cancel', item.id)"><Square v-if="!stopping(item)" :size="12" />{{stopping(item) ? '正在停止' : '停止传输'}}</el-button></div>
        <p v-if="['cancelled','canceled'].includes(item.status)" class="transfer-stop-notice">已停止，已完成的文件保留。</p>
        <p v-if="item.status === 'retrying' && !stopping(item)" class="transfer-retry-notice">连接中断，{{ transferRetryLabel(item) }}</p>
        <p v-if="item.error" class="transfer-task-error" :class="{ retrying: item.status === 'retrying' }">{{ item.error }}</p>
      </div>
    </div>
  </aside>
  <LocalSourcePicker v-model="sourcePickerOpen" @select="acceptSources" />
</template>

<style scoped>
.transfer-file-count{font-size:11px;line-height:1.6;margin:5px 0 0;color:var(--ui-muted)}
.transfer-task-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 10px; padding-top: 9px; border-top: 1px solid var(--ui-border); }
.transfer-task-actions small, .transfer-stop-notice { color: var(--ui-muted); font-size: 10px; line-height: 1.5; }
.transfer-task-actions .el-button { flex-shrink: 0; height: 27px; margin: 0; padding: 0 9px; font-size: 11px; }
.transfer-task-actions :deep(.el-button > span) { gap: 5px; }
.transfer-stop-notice { margin: 8px 0 0; }
.transfer-conflict{flex-shrink:0;padding:12px;margin:0 0 16px;border:1px solid #665339;border-radius:9px;background:#29231b}
.transfer-conflict-heading{display:flex;align-items:center;justify-content:space-between;gap:6px}.transfer-conflict-heading strong{font-size:12px;color:#e4c18a}
.transfer-conflict p{font-size:11px;line-height:1.7;margin:6px 0 10px;color:#c6b394}.transfer-conflict dl{margin:0 0 12px}.transfer-conflict dt{font-size:11px;color:#ab9576;margin-top:7px}.transfer-conflict dd{margin:3px 0 0;font:11px/1.65 Menlo,monospace;color:#dfd4c3;overflow-wrap:anywhere}
.transfer-conflict-actions{display:grid;grid-template-columns:1fr 1fr;gap:7px}.transfer-conflict-actions .el-button{margin:0;min-width:0}.transfer-conflict-cancel{margin-top:8px;width:100%}

.legacy-transfer-panel {
  width: 310px;
  flex: 0 0 310px;
  min-height: 0;
  box-sizing: border-box;
  padding: 14px 12px 12px;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  overflow-x: hidden;
  border-left: 1px solid var(--ui-border);
  background: var(--ui-panel);
  color: var(--ui-text);
  font-size: 12px;
  scrollbar-width: thin;
  scrollbar-color: var(--ui-border) transparent;
}
.legacy-transfer-heading { display: flex; align-items: center; gap: 9px; min-height: 36px; padding-bottom: 14px; flex-shrink: 0; }
.transfer-heading-symbol { display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; border: 1px solid rgb(66 197 138 / 15%); background: rgb(66 197 138 / 8%); color: var(--ui-accent); border-radius: 9px; }
.legacy-transfer-heading strong { font-size: 13px; font-weight: 600; }
.legacy-transfer-heading small { display: block; font-size: 11px; color: var(--ui-muted); margin-top: 4px; }
.transfer-state { display: flex; align-items: center; gap: 5px; margin-left: auto; white-space: nowrap; font-size: 11px; color: var(--ui-muted); }
.transfer-state > span { width: 5px; height: 5px; border-radius: 50%; background: currentColor; opacity: .7; }
.transfer-state.busy { color: var(--ui-accent); }
.legacy-transfer-direction { display: flex; gap: 4px; margin: 0 0 12px; padding: 4px; border: 1px solid var(--ui-border); border-radius: 8px; background: var(--ui-bg); flex-shrink: 0; }
.legacy-transfer-direction :deep(.el-radio-button) { flex: 1; min-width: 0; }
.legacy-transfer-direction :deep(.el-radio-button__inner) { display: flex; align-items: center; justify-content: center; gap: 6px; width: 100%; height: 30px; padding: 0 10px; margin: 0; border: 1px solid transparent; border-radius: 5px; background: transparent; color: var(--ui-muted); font-size: 12px; box-shadow: none; }
.legacy-transfer-direction :deep(.el-radio-button:not(.is-disabled) .el-radio-button__inner:hover) { color: var(--ui-text); background: var(--ui-hover); }
.legacy-transfer-direction :deep(.el-radio-button.is-active .el-radio-button__inner) { color: var(--ui-accent); border-color: var(--ui-border); background: var(--ui-raised); box-shadow: 0 2px 4px rgb(0 0 0 / 12%); }
.legacy-transfer-direction :deep(.el-radio-button:focus-visible .el-radio-button__inner) { outline: 1px solid var(--ui-accent); outline-offset: 1px; }
.legacy-transfer-direction :deep(.el-radio-button.is-disabled) { opacity: .55; }
.legacy-transfer-card { padding: 13px; border: 1px solid var(--ui-border); border-radius: 9px; background: var(--ui-raised); flex-shrink: 0; }
.transfer-card-title { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.transfer-card-title strong { font-size: 12px; font-weight: 600; }
.transfer-card-title small { padding: 2px 6px; border-radius: 4px; background: var(--ui-hover); color: var(--ui-muted); font-size: 11px; }
.upload-source-actions { display: flex; align-items: center; gap: 6px; margin-bottom: 8px; }
.upload-source-actions > .upload-source-picker { flex: 1; min-width: 0; }
.upload-source-actions .el-button { height: 32px; padding: 0 8px; font-size: 11px; }
.upload-source-actions :deep(.el-button > span) { gap: 6px; }
.upload-source-actions > .el-button { margin: 0; }
.upload-source-actions > .el-button.is-text { padding: 0 3px; color: var(--ui-muted); }
.legacy-upload-sources { box-sizing: border-box; height: 100px; margin-bottom: 12px; padding: 6px; overflow-y: auto; overflow-x: hidden; overscroll-behavior-y: contain; scrollbar-gutter: stable; border: 1px dashed var(--ui-border); border-radius: 6px; background: var(--ui-bg); scrollbar-width: thin; scrollbar-color: var(--ui-border) transparent; }
.legacy-upload-sources:focus-visible { outline: 1px solid var(--ui-accent); outline-offset: 2px; }
.legacy-upload-sources::-webkit-scrollbar { width: 6px; }
.legacy-upload-sources::-webkit-scrollbar-thumb { background: var(--ui-border); border-radius: 3px; }
.legacy-upload-sources > div { display: flex; align-items: center; gap: 6px; min-height: 26px; }
.legacy-upload-sources > div > svg { flex-shrink: 0; color: var(--ui-muted); }
.legacy-upload-sources > div span { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 11px; }
.legacy-upload-sources button { display: flex; flex-shrink: 0; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 2px; border: 0; border-radius: 4px; background: none; color: var(--ui-muted); cursor: pointer; }
.legacy-upload-sources button:not(:disabled):hover { background: var(--ui-hover); color: var(--ui-text); }
.legacy-upload-sources button:disabled { opacity: .35; cursor: default; }
.legacy-upload-sources button:focus-visible { outline: 1px solid var(--ui-accent); }
.legacy-upload-sources small { display: flex; height: 100%; flex-direction: column; align-items: center; justify-content: center; gap: 7px; color: var(--ui-muted); font-size: 11px; line-height: 1.5; }
.legacy-upload-sources small > svg { opacity: .65; }
.transfer-field-label { display: block; margin: 12px 0 7px; font-size: 11px; color: var(--ui-muted); }
.transfer-field-heading { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin: 9px 0 4px; }
.transfer-field-heading .transfer-field-label { margin: 0; }
.transfer-field-heading .el-button { height: 24px; margin: 0; padding: 0 4px; font-size: 11px; }
.transfer-directory-hint { margin: 6px 0 0; color: var(--ui-muted); font-size: 11px; line-height: 1.5; }
.transfer-path-input { display: flex; gap: 6px; min-width: 0; }
.transfer-path-input > .el-input { flex: 1; min-width: 0; }
.transfer-path-input .el-button { height: 32px; margin: 0; padding: 0 10px; font-size: 11px; }
.transfer-path-input :deep(.el-input__wrapper) { min-height: 32px; box-sizing: border-box; padding: 0 8px; }
.transfer-path-input :deep(input) { font-family: Menlo, Monaco, Consolas, monospace; font-size: 11px; }
.transfer-start-button { width: 100%; height: 33px; margin-top: 14px; padding: 0 14px; font-size: 12px; }
.transfer-start-button :deep(span) { gap: 6px; }
.legacy-transfer-safety { display: flex; align-items: center; justify-content: center; gap: 6px; margin: 10px 0 16px; color: var(--ui-muted); font-size: 11px; line-height: 16px; flex-shrink: 0; }
.legacy-transfer-safety svg { flex-shrink: 0; color: var(--ui-accent); opacity: .8; }
.legacy-transfer-tasks-heading { display: flex; align-items: center; justify-content: space-between; min-height: 32px; padding-top: 9px; border-top: 1px solid var(--ui-border); flex-shrink: 0; }
.legacy-transfer-tasks-heading strong { display: flex; align-items: center; gap: 7px; font-size: 12px; font-weight: 600; }
.legacy-transfer-tasks-heading strong span { color: var(--ui-muted); font-size: 11px; font-weight: 400; font-variant-numeric: tabular-nums; }
.legacy-transfer-tasks-heading .el-button { font-size: 11px; color: var(--ui-muted); }
.legacy-transfer-summary { display: flex; gap: 10px; padding: 3px 0 8px; font-size: 11px; color: var(--ui-muted); flex-shrink: 0; }
.summary-upload { color: var(--ui-accent); }
.legacy-transfer-tasks { flex: 1; min-height: 100px; overflow-y: auto; overflow-x: hidden; padding: 4px 0; scrollbar-width: thin; scrollbar-color: var(--ui-border) transparent; }
.legacy-transfer-no-tasks { display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 26px 0; color: var(--ui-muted); }
.legacy-transfer-no-tasks > span { display: flex; align-items: center; gap: 2px; margin-bottom: 5px; opacity: .5; }
.legacy-transfer-no-tasks strong { font-size: 12px; font-weight: 500; }
.legacy-transfer-no-tasks small { font-size: 11px; opacity: .7; }
.legacy-transfer-task { min-width: 0; margin-bottom: 8px; padding: 10px; border: 1px solid var(--ui-border); border-radius: 7px; background: var(--ui-raised); }
.transfer-task-name { display: flex; align-items: center; gap: 6px; min-width: 0; }
.transfer-task-name > svg { color: var(--ui-accent); flex-shrink: 0; }
.transfer-task-name strong { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; font-size: 12px; }
.transfer-task-name .el-button { width: 24px; height: 24px; padding: 2px; margin: 0; color: var(--ui-muted); }
.transfer-task-destination { display: flex; gap: 6px; margin-top: 6px; min-width: 0; font-size: 11px; color: var(--ui-muted); }
.transfer-task-destination > span:first-child { flex-shrink: 0; }
.transfer-task-destination > span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: Menlo, Monaco, Consolas, monospace; color: var(--ui-text); }
.transfer-task-path { margin: 4px 0 10px; font-family: Menlo, Monaco, Consolas, monospace; font-size: 11px; color: var(--ui-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.transfer-task-meta { display: flex; align-items: center; justify-content: space-between; gap: 4px; margin-top: 7px; font-size: 11px; color: var(--ui-muted); font-variant-numeric: tabular-nums; }
.transfer-task-meta > span:first-child { display: flex; align-items: center; gap: 3px; }
.transfer-task-meta > span:last-child { white-space: nowrap; }
.transfer-task-meta .failed, .transfer-task-error { color: #f48d94; }
.transfer-task-meta .finished { color: var(--ui-accent); }
.transfer-retry-notice, .transfer-task-error.retrying, .transfer-task-meta .retrying { color: #dbb773; }
.transfer-retry-notice { margin: 6px 0 0; font-size: 11px; line-height: 1.5; }
.transfer-task-error { margin: 6px 0 0; font-size: 11px; line-height: 1.5; overflow-wrap: anywhere; }
</style>

<style>
.legacy-dark-panel {
  --el-color-primary: var(--ui-accent);
  --el-color-primary-light-3: #68d6a4;
  --el-color-primary-light-5: #34936a;
  --el-color-primary-light-7: #255440;
  --el-color-primary-light-8: #213d33;
  --el-color-primary-light-9: #1b302a;
  --el-color-primary-dark-2: #33a874;
  --el-text-color-primary: var(--ui-text);
  --el-text-color-regular: var(--ui-text);
  --el-text-color-secondary: var(--ui-muted);
  --el-text-color-placeholder: #708094;
  --el-text-color-disabled: #5c6a7b;
  --el-border-color: var(--ui-border);
  --el-border-color-light: var(--ui-border);
  --el-border-color-lighter: var(--ui-border);
  --el-fill-color: var(--ui-raised);
  --el-fill-color-light: var(--ui-hover);
  --el-fill-color-lighter: var(--ui-raised);
  --el-fill-color-blank: var(--ui-raised);
  --el-bg-color: var(--ui-panel);
  --el-bg-color-overlay: var(--ui-raised);
  --el-disabled-bg-color: var(--ui-panel);
  --el-disabled-border-color: var(--ui-border);
  --el-disabled-text-color: #5c6a7b;
}
.legacy-dark-panel .el-input__wrapper { border-radius: 6px; background: var(--ui-bg); box-shadow: 0 0 0 1px var(--ui-border) inset; }
.legacy-dark-panel .el-input__wrapper:hover { box-shadow: 0 0 0 1px #48596c inset; }
.legacy-dark-panel .el-input__wrapper.is-focus { box-shadow: 0 0 0 1px var(--ui-accent) inset; }
.legacy-dark-panel .el-input.is-disabled .el-input__wrapper { background: var(--ui-panel); box-shadow: 0 0 0 1px var(--ui-border) inset; }
.legacy-dark-panel .el-button--primary {
  --el-button-bg-color: #24895f;
  --el-button-border-color: #2a9b6c;
  --el-button-hover-bg-color: #2ba572;
  --el-button-hover-border-color: #37b17e;
  --el-button-disabled-bg-color: #1c392e;
  --el-button-disabled-border-color: #264a3c;
  --el-button-disabled-text-color: #718a7e;
  color: #f1fff8;
}
.legacy-path-picker { max-width: 340px; }
.legacy-path-picker .el-dropdown-menu { max-height: 280px; overflow-y: auto; scrollbar-width: thin; scrollbar-color: var(--ui-border) transparent; }
.legacy-path-picker .el-dropdown-menu__item { max-width: 320px; }
.legacy-path-picker .el-dropdown-menu__item > span { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: Menlo, Monaco, Consolas, monospace; font-size: 11px; }
</style>
