<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus/es/components/message/index'
import { ArrowLeft, ArrowRight, CircleHelp, Clock3, Code2, Folder, History, KeyRound, LayoutGrid, Plus, RefreshCw, Server, ShieldCheck, SquareTerminal, Unplug, X } from '@lucide/vue'
import TerminalPane from './components/TerminalPane.vue'
import ConnectionLibrary from './components/ConnectionLibrary.vue'
import ProfileEditorDialog from './components/ProfileEditorDialog.vue'
import AppDialog from './components/AppDialog.vue'
import RemoteFilePanel from './components/RemoteFilePanel.vue'
import TransferPanel from './components/TransferPanel.vue'
import { remoteParent, type RemoteAction } from './remote-files'
import { createUploadRequest } from './transfer-draft'
import { CommandHistoryCache } from './terminal/history-store'
import { ClipboardSetText } from '../wailsjs/runtime/runtime'
import { api, errorText, isDesktop, joinPath, listen, type Profile, type Connection, type Directory, type RemoteEntry, type Transfer, type Conflict, type ConflictChoice, type HostKey } from './api'

interface Session { tabId: string; retryAttempt: number; connection: Connection; profile: Profile; status: 'connected' | 'closed' | 'reconnecting'; error: string; directory: Directory | null; pathInput: string; filesLoading: boolean; filesError: string; request: number; workingPath: string; treeEntries: RemoteEntry[] }
const profiles = ref<Profile[]>([])
const groups = ref<string[]>([])
const sessions = ref<Session[]>([])
const sessionSecrets = new Map<string, string>()
const connectionTabs = new Map<string, string>()
const cancelledJobs = new Set<string>()
const cancellingTransfers = ref(new Set<string>())
const reconnectTokens = new Map<string, { cancelled: boolean; timer?: ReturnType<typeof setTimeout>; wake?: () => void }>()
const activeId = ref('')
const view = ref<'library' | 'workspace'>('library')
const activeSession = computed(() => sessions.value.find(s => s.tabId === activeId.value))
const search = ref('')
const selectedGroup = ref('*')
const recentOnly = ref(false)
const privateKeyOnly = ref(false)
const featureNotice = ref('')
const pageLoading = ref(false)
const connecting = ref<number | null>(null)
const showInfo = ref(false)
const info = ref({ version: '0.1.0', dataDir: '' })
const filtered = computed(() => profiles.value.filter(profile => {
  const matchesSearch = `${profile.name} ${profile.host} ${profile.username} ${profile.remark}`.toLowerCase().includes(search.value.toLowerCase())
  return matchesSearch && (selectedGroup.value === '*' || profile.groupName === selectedGroup.value) && (!recentOnly.value || !!profile.lastConnectedAt) && (!privateKeyOnly.value || profile.authKind === 'private_key')
}).sort((a,b) => recentOnly.value ? b.lastConnectedAt.localeCompare(a.lastConnectedAt) : b.id - a.id))
const groupTitle = computed(() => privateKeyOnly.value ? 'PEM 私钥' : recentOnly.value ? '最近连接' : selectedGroup.value === '*' ? '所有连接' : selectedGroup.value || '未分组')
function notify(message: string, kind: 'success' | 'error' = 'success') { ElMessage({ message, type: kind, showClose: true, grouping: true, duration: kind === 'error' ? 8000 : 3500 }) }
function fail(error: unknown) { notify(errorText(error), 'error') }
async function refresh() {
  if (!isDesktop()) return
  pageLoading.value = true
  try { const [p,g] = await Promise.all([api.ListProfiles(), api.ListGroups()]); profiles.value = p || []; groups.value = g || [] } catch (error) { fail(error) } finally { pageLoading.value = false }
}
function selectGroup(group: string, recent = false) { selectedGroup.value = group; recentOnly.value = recent; privateKeyOnly.value = false; view.value = 'library' }
function newProfile(): Profile { return { id: 0, name: '', host: '', port: 22, username: 'root', authKind: 'password', keyPath: '', groupName: selectedGroup.value === '*' ? '' : selectedGroup.value, remark: '', lastConnectedAt: '', osId: '', cpuCores: 0, memoryBytes: 0, diskBytes: 0, hasSecret: false } }
const editor = ref<Profile | null>(null)
const saving = ref(false)
function editProfile(profile?: Profile) { editor.value = profile ? { ...profile } : newProfile() }
async function saveProfile(payload: { profile: Profile; secret: string; keepSecret: boolean }) {
  if (saving.value) return
  saving.value = true
  try {
    await api.SaveProfile(payload.profile, payload.secret, payload.keepSecret)
    editor.value = null
    await refresh()
    notify('连接已保存')
  } catch (error) { fail(error) } finally { saving.value = false }
}
const newGroup = ref(false)
const groupName = ref('')
async function saveGroup() { if (saving.value) return; if (!groupName.value.trim()) return; saving.value = true; try { await api.CreateGroup(groupName.value.trim()); newGroup.value = false; await refresh(); selectGroup(groupName.value.trim()); groupName.value = ''; notify('分组已创建') } catch (error) { fail(error) } finally { saving.value = false } }
const deleteProfile = ref<Profile | null>(null)
async function removeProfile() { if (saving.value) return; if (!deleteProfile.value) return; saving.value = true; try { await api.DeleteProfile(deleteProfile.value.id); deleteProfile.value = null; await refresh(); notify('连接配置已删除') } catch (error) { fail(error) } finally { saving.value = false } }
async function importConfig(legacy = false) { try { const result = legacy ? await api.ImportLegacyDatabase() : await api.ImportSSHConfig(); await refresh(); const count = typeof result === 'number' ? result : (result || []).length; if (count) notify(`已导入 ${count} 个连接`) } catch (error) { fail(error) } }
const authPrompt = ref<Profile | null>(null)
const authSecret = ref('')
const trustPrompt = ref<{ key: HostKey; profile: Profile; secret: string } | null>(null)
async function connectProfile(profile: Profile, secret?: string) {
  if (connecting.value !== null) return
  if (!isDesktop()) return fail(new Error('请在 云桥桌面应用中连接服务器。浏览器仅提供界面预览。'))
  if (secret === undefined && !profile.hasSecret && profile.authKind === 'password') { authPrompt.value = profile; authSecret.value = ''; return }
  connecting.value = profile.id
  try {
    const connection = await api.Connect(profile.id, secret || '', 100, 30)
    const session: Session = { tabId: connection.id, retryAttempt: 0, connection, profile: { ...profile }, status: 'connected', error: '', directory: null, pathInput: connection.home || '.', filesLoading: false, filesError: '', request: 0, workingPath: connection.home || '.', treeEntries: [] }
    sessionSecrets.set(session.tabId, secret || ''); connectionTabs.set(connection.id, session.tabId)
    sessions.value.push(session); activeId.value = session.tabId; view.value = 'workspace'
    authPrompt.value = null; authSecret.value = ''
    void loadDirectory(sessions.value[sessions.value.length - 1], connection.home || '.')
    void loadHistory(profile.id)
    void refresh()
  } catch (error) {
    const message = errorText(error); const marker = message.indexOf('HOST_KEY_UNKNOWN:')
    if (marker >= 0) { try { trustPrompt.value = { key: JSON.parse(message.slice(marker + 'HOST_KEY_UNKNOWN:'.length)), profile, secret: secret || '' }; authPrompt.value = null } catch { fail(error) } }
    else { fail(error); if (profile.authKind === 'private_key' && !secret) { authPrompt.value = profile; authSecret.value = '' } }
  } finally { connecting.value = null }
}
async function acceptHost() { if (!trustPrompt.value) return; const pending = trustPrompt.value; saving.value = true; try { await api.TrustHost(pending.key.host, pending.key.port, pending.key.key); trustPrompt.value = null; await connectProfile(pending.profile, pending.secret) } catch (error) { fail(error) } finally { saving.value = false } }
function isNetworkClosure(error: string) {
  return /(?:EOF|connection (?:reset|closed|lost)|broken pipe|closed network|network is unreachable|timed? ?out|timeout|without exit status or exit signal|心跳超时|连接已断开)/i.test(error)
}
function terminalClosed(session: Session, error: string) {
  if (!sessions.value.includes(session) || reconnectTokens.has(session.tabId)) return
  session.status = 'closed'; session.error = error
  if (error && isNetworkClosure(error)) void reconnect(session, true)
  else if (error) notify(error, 'error')
}
function stopReconnect(tabId: string) {
  const token = reconnectTokens.get(tabId)
  if (token) { token.cancelled = true; if (token.timer) clearTimeout(token.timer); token.wake?.(); reconnectTokens.delete(tabId) }
}
async function closeSession(session: Session) {
  stopReconnect(session.tabId)
  sessionSecrets.delete(session.tabId)
  // Remove the tab before awaiting so pending connection results cannot revive it.
  sessions.value = sessions.value.filter(s => s.tabId !== session.tabId)
  const connectionIDs = [...connectionTabs].filter(([,tabId]) => tabId === session.tabId).map(([id]) => id)
  const closed = await Promise.allSettled(connectionIDs.map(id => api.Disconnect(id)))
  closed.forEach(result => { if (result.status === 'rejected') fail(result.reason) })
  const taskIds = new Set(transfers.value.filter(t => t.sessionId === session.tabId).map(t => t.id))
  taskIds.forEach(id => cancelledJobs.add(id))
  await Promise.allSettled([...taskIds].filter(id => transfers.value.some(t => t.id === id && !isFinished(t.status))).map(id => api.CancelTransfer(id)))
  conflicts.value = conflicts.value.filter(c => c.sessionId !== session.tabId && !taskIds.has(c.id))
  selectedRemote.value = null
  if (activeId.value === session.tabId) activeId.value = sessions.value.at(-1)?.tabId || ''
  if (!sessions.value.length) view.value = 'library'
}
async function reconnect(session: Session, automatic = false) {
  if (reconnectTokens.has(session.tabId)) return
  const token = { cancelled: false } as { cancelled: boolean; timer?: ReturnType<typeof setTimeout>; wake?: () => void }
  reconnectTokens.set(session.tabId, token)
  session.status = 'reconnecting'; session.retryAttempt = 0
  try {
    do {
      session.retryAttempt++
      if (automatic) {
        const delay = Math.min(session.retryAttempt, 5) * 2
        session.error = `${delay} 秒后进行第 ${session.retryAttempt} 次重连…`
        await new Promise<void>(resolve => { token.wake = resolve; token.timer = setTimeout(resolve, delay * 1000) })
        if (token.cancelled) return
      }
      try {
        const profile = profiles.value.find(p => p.id === session.profile.id) || session.profile
        const connection = await api.Connect(profile.id, sessionSecrets.get(session.tabId) || '', 100, 30)
        if (token.cancelled || !sessions.value.some(s => s.tabId === session.tabId)) { await api.Disconnect(connection.id); return }
        connectionTabs.set(connection.id, session.tabId)
        session.profile = {...profile}; session.connection = connection; session.status = 'connected'; session.error = ''; session.retryAttempt = 0
        session.directory = null; session.workingPath = connection.home || '.'; session.pathInput = session.workingPath
        void loadDirectory(session, session.workingPath)
        void refresh()
        return
      } catch (error) {
        if (token.cancelled || reconnectTokens.get(session.tabId) !== token || !sessions.value.some(s => s.tabId === session.tabId)) return
        session.error = errorText(error)
        if (!automatic || !isNetworkClosure(session.error) && !/(?:无法连接|connection refused|no route|network)/i.test(session.error)) { session.status = 'closed'; fail(error); return }
      }
    } while (!token.cancelled)
  } finally { if (reconnectTokens.get(session.tabId) === token) reconnectTokens.delete(session.tabId) }
}
function cancelReconnect(session: Session) { stopReconnect(session.tabId); session.status = 'closed'; session.error = '已取消自动重连' }
function showSession(session: Session) { selectedRemote.value = null; activeId.value = session.tabId; view.value = 'workspace'; void nextTick(() => terminals.get(session.tabId)?.focus()) }
const selectedRemote = ref<RemoteEntry | null>(null)
async function loadDirectory(session: Session, path?: string) {
  const request = ++session.request; session.filesLoading = true; session.filesError = ''
  try {
    const directory = await api.ListRemote(session.connection.id, path || session.directory?.path || session.connection.home || '.')
    if (request !== session.request) return
    directory.entries = (directory.entries || []).sort((a,b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name))
    session.directory = directory; session.pathInput = directory.path; if (activeId.value === session.tabId) selectedRemote.value = null
  } catch (error) { if (request === session.request) session.filesError = errorText(error) } finally { if (request === session.request) session.filesLoading = false }
}
const remoteDialog = ref<{ kind: 'file' | 'folder' | 'rename' | 'chmod' | 'delete'; entry?: RemoteEntry; session: Session; value: string; parentPath: string } | null>(null)
const remoteTitle = computed(() => ({ file: '新建文件', folder: '新建文件夹', rename: '重命名', chmod: '修改权限', delete: '删除远程项目' }[remoteDialog.value?.kind || 'file']))
const permissionRows = [{name:'所有者',bits:[256,128,64]}, {name:'用户组',bits:[32,16,8]}, {name:'其他用户',bits:[4,2,1]}]
function permissionChecked(bit: number) { return !!(parseInt(remoteDialog.value?.value || '0',8) & bit) }
function togglePermission(bit: number, checked: boolean | string | number) { const dialog=remoteDialog.value; if(!dialog || saving.value)return; const mode=parseInt(dialog.value || '0',8) || 0; dialog.value=(checked ? mode|bit : mode&~bit).toString(8).padStart(3,'0') }
function remoteAction(kind: 'file' | 'folder' | 'rename' | 'chmod' | 'delete', entry?: RemoteEntry, parentPath?: string) { if (!activeSession.value) return; remoteDialog.value = { kind, entry, session: activeSession.value, parentPath: parentPath || activeSession.value.directory?.path || activeSession.value.pathInput, value: kind === 'chmod' ? entry?.mode || '' : kind === 'rename' ? entry?.name || '' : '' } }
async function submitRemote() {
  if (saving.value) return
  const dialog = remoteDialog.value; if (!dialog) return
  const value = dialog.value.trim()
  if (dialog.kind !== 'delete' && !value) return
  if (dialog.kind === 'chmod' && !/^[0-7]{3,4}$/.test(value)) return notify('请输入 3 或 4 位八进制权限，例如 755。', 'error')
  if (['file', 'folder', 'rename'].includes(dialog.kind) && (value.includes('/') || value === '.' || value === '..')) return notify('名称不能包含 /，也不能为 . 或 ..。', 'error')
  saving.value = true
  try {
    const id = dialog.session.connection.id; const path = joinPath(dialog.kind === 'rename' ? remoteParent(dialog.entry!.path) : dialog.parentPath, value)
    if (dialog.kind === 'delete') await api.DeleteRemote(id, dialog.entry!.path)
    else if (dialog.kind === 'rename') await api.RenameRemote(id, dialog.entry!.path, path)
    else if (dialog.kind === 'chmod') await api.ChmodRemote(id, dialog.entry!.path, value)
    else await api.CreateRemote(id, path, dialog.kind === 'folder')
    remoteDialog.value = null; await loadDirectory(dialog.session, ['rename', 'delete'].includes(dialog.kind) ? remoteParent(dialog.entry!.path) : ['file', 'folder'].includes(dialog.kind) ? dialog.parentPath : undefined); notify('远程文件操作已完成')
  } catch (error) { fail(error) } finally { saving.value = false }
}
const archiving = ref(false)
async function archiveRemote(entry: RemoteEntry, extract = false) { const session = activeSession.value; if (!session || archiving.value) return; archiving.value = true; try { const result = extract ? await api.ExtractRemote(session.connection.id, entry.path) : await api.CompressRemote(session.connection.id, entry.path); await loadDirectory(session, remoteParent(entry.path)); notify(`${extract ? '已解压到' : '已创建归档'} ${result}`) } catch (error) { fail(error) } finally { archiving.value = false } }
async function copy(value: string) { try { if (isDesktop()) { const copied = await ClipboardSetText(value); if (!copied) throw new Error('复制失败，请重试') } else await navigator.clipboard.writeText(value); notify('已复制') } catch (error) { fail(error) } }
const transfers = ref<Transfer[]>([])
const transferPanel = ref<InstanceType<typeof TransferPanel>>()
const transferSubmissions = ref(new Set<string>())
function isTransferSubmitting(direction: 'upload' | 'download', tabId = activeSession.value?.tabId) { return !!tabId && transferSubmissions.value.has(`${tabId}:${direction}`) }
function clearTransfers() { transfers.value = transfers.value.filter(t => t.sessionId !== activeSession.value?.tabId || !isFinished(t.status)) }
type TerminalHandle = { focus(): void; clear(): void; paste(text: string): void; replaceInput(command: string): Promise<void>; execute(command: string): Promise<void> }
const terminals = new Map<string, TerminalHandle>()
function registerTerminal(id: string, instance: unknown) { if (instance) terminals.set(id, instance as TerminalHandle); else terminals.delete(id) }
const historyByProfile = ref<Record<number, string[]>>({})
const historyCache = new CommandHistoryCache({ load: id => api.CommandHistory(id), save: (id, command) => api.RecordCommand(id, command) }, (id, commands) => { historyByProfile.value[id] = commands })
async function loadHistory(profileId: number, refresh = false) { try { await historyCache.load(profileId, refresh) } catch (error) { fail(error) } }
async function recordCommand(session: Session, command: string) { try { await historyCache.record(session.profile.id, command) } catch (error) { fail(error) } }
function focusTerminal() { if (view.value === 'workspace' && activeSession.value) terminals.get(activeSession.value.tabId)?.focus() }
function clearTerminal() { if (activeSession.value) terminals.get(activeSession.value.tabId)?.clear() }
async function newTerminal() { if (activeSession.value) await connectProfile(profiles.value.find(p=>p.id===activeSession.value!.profile.id) || activeSession.value.profile, sessionSecrets.get(activeSession.value.tabId) || undefined); else view.value = 'library' }
function terminalCwd(session: Session, value: string) {
  const path = value === '~' ? session.connection.home : value.startsWith('~/') ? joinPath(session.connection.home, value.slice(2)) : value
  if (!path.startsWith('/') || session.workingPath === path) return
  session.workingPath = path
  if (session.status === 'connected') void loadDirectory(session, path)
}
async function terminalDirectory(path: string) {
  const session = activeSession.value
  if (!session || session.status !== 'connected' || /[\r\n\0]/.test(path)) return
  const quoted = "'" + path.replaceAll("'", "'\"'\"'") + "'"
  const command = `cd -- ${quoted}`
  try { await terminals.get(session.tabId)?.execute(command); terminals.get(session.tabId)?.focus() } catch (error) { fail(error) }
}
function handleRemoteAction(action: RemoteAction) {
  if (action.kind === 'archive' || action.kind === 'extract') { void archiveRemote(action.entry, action.kind === 'extract'); return }
  remoteAction(action.kind, action.entry, action.parentPath)
}
async function copyCredentials(profile: Profile) { try { await api.CopyProfileCredentials(profile.id); notify('已复制 IP、端口、账号和' + (profile.authKind === 'private_key' ? '私钥口令' : '密码')) } catch (error) { fail(error) } }
async function submitUpload(upload: { paths: string[]; destination: string }) {
  const session = activeSession.value
  const request = createUploadRequest(upload.paths, upload.destination)
  if (!session || isTransferSubmitting('upload', session.tabId) || !request.paths.length || !request.destination) return
  const submissionKey = `${session.tabId}:upload`
  transferSubmissions.value.add(submissionKey)
  try {
    const id = await api.Upload(session.connection.id, request.paths, request.destination)
    if (!sessions.value.some(s => s.tabId === session.tabId)) { cancelledJobs.add(id); await api.CancelTransfer(id); return }
    const transfer = transfers.value.find(t => t.id === id)
    if (transfer) transfer.destination ||= request.destination
    else transfers.value.unshift({ id, sessionId: session.tabId, direction: 'upload', path: request.destination, destination: request.destination, status: 'queued', transferred: 0, total: 0 })
  } catch (error) { fail(error) } finally { transferSubmissions.value.delete(submissionKey) }
}
async function submitDownload(download: { path: string; destination: string }) {
  const session = activeSession.value
  if (!session || isTransferSubmitting('download', session.tabId) || !download.path.trim() || !download.destination.trim()) return
  const submissionKey = `${session.tabId}:download`
  transferSubmissions.value.add(submissionKey)
  try {
    const id = await api.Download(session.connection.id, download.path, download.destination)
    if (!sessions.value.some(s => s.tabId === session.tabId)) { cancelledJobs.add(id); await api.CancelTransfer(id); return }
    if (!transfers.value.some(t => t.id === id)) transfers.value.unshift({ id, sessionId: session.tabId, direction: 'download', path: download.path, status: 'queued', transferred: 0, total: 0 })
  } catch (error) { fail(error) } finally { transferSubmissions.value.delete(submissionKey) }
}
async function cancelTransfer(id: string) {
  const transfer = transfers.value.find(item => item.id === id)
  if (!transfer || isFinished(transfer.status) || cancellingTransfers.value.has(id)) return
  cancelledJobs.add(id)
  cancellingTransfers.value.add(id)
  try { await api.CancelTransfer(id); conflicts.value = conflicts.value.filter(c => c.id !== id) }
  catch (error) {
    cancellingTransfers.value.delete(id)
    if (!isFinished(transfers.value.find(item => item.id === id)?.status || '')) {
      cancelledJobs.delete(id); fail(error)
    }
  }
}
const conflicts = ref<Conflict[]>([])
const resolvingConflicts = ref(new Set<string>())
function sessionConflictCount(tabId: string) { return conflicts.value.filter(conflict => conflict.sessionId === tabId).length }
async function resolveConflict(request: { id: string; choice: ConflictChoice }) {
  const conflict = conflicts.value.find(item => item.id === request.id)
  if (!conflict || resolvingConflicts.value.has(conflict.id)) return
  resolvingConflicts.value.add(conflict.id)
  try {
    await api.ResolveConflict(conflict.id, request.choice)
    // The next conflict from this job can arrive before the RPC response.
    if (request.choice === 'cancel') { cancelledJobs.add(conflict.id); conflicts.value = conflicts.value.filter(item => item.id !== conflict.id) }
    else conflicts.value = conflicts.value.filter(item => item !== conflict)
  } catch (error) { fail(error) }
  finally { resolvingConflicts.value.delete(conflict.id) }
}
function isFinished(status: string) { return ['completed', 'failed', 'cancelled', 'canceled', 'skipped'].includes(status) }
const historyOpen = ref(false)
const historySessionId = ref('')
const historySession = computed(() => sessions.value.find(session => session.tabId === historySessionId.value))
const historyLoading = ref(false)
const historySending = ref(false)
const history = computed(() => historySession.value ? historyByProfile.value[historySession.value.profile.id] || [] : [])
const commonCommands = ['cat /etc/os-release', 'uname -a', 'hostnamectl', 'pwd', 'ls -lah', 'df -h', 'free -h', 'top -b -n 1 | head -25', 'ps aux --sort=-%mem | head', 'systemctl --failed', 'journalctl -p err -n 50', 'ss -lntup', 'ip addr', 'whoami', 'uptime']
const commandText = ref('')
const matchingHistory = computed(() => history.value.filter(command => !commandText.value || command.toLowerCase().includes(commandText.value.toLowerCase())))
const matchingCommon = computed(() => commonCommands.filter(command => !history.value.includes(command) && (!commandText.value || command.toLowerCase().includes(commandText.value.toLowerCase()))))
async function openHistory(session: Session | undefined = activeSession.value) {
  if (!session) return
  historySessionId.value = session.tabId
  commandText.value = ''
  historyOpen.value = true
  historyLoading.value = true
  try { await loadHistory(session.profile.id, true) } finally { historyLoading.value = false }
}
async function sendCommand(execute = false) {
  const session = historySession.value
  const value = commandText.value.trim()
  if (!session || session.status !== 'connected' || !value || historySending.value) return
  const terminal = terminals.get(session.tabId)
  if (!terminal) return
  historySending.value = true
  try {
    if (execute) await terminal.execute(value)
    else await terminal.replaceInput(value)
    historyOpen.value = false
    showSession(session)
  } catch (error) { fail(error) } finally { historySending.value = false }
}
const subscriptions: (() => void)[] = []
onMounted(() => {
  void refresh()
  if (!isDesktop()) return
  void api.GetAppInfo().then(value => info.value = value).catch(fail)
  subscriptions.push(listen<number>('profile:updated', () => { void refresh() }))
  subscriptions.push(listen<Transfer>('transfer:progress', raw => {
    const tabId = connectionTabs.get(raw.sessionId)
    if (!tabId) return // Events from another window belong to that window.
    const event = { ...raw, sessionId: tabId }
    if (isFinished(event.status)) cancellingTransfers.value.delete(event.id)
    if (connectionTabs.has(raw.sessionId) && !sessions.value.some(s => s.tabId === event.sessionId)) {
      if (!isFinished(event.status) && !cancelledJobs.has(event.id)) { cancelledJobs.add(event.id); void api.CancelTransfer(event.id).catch(fail) }
      conflicts.value = conflicts.value.filter(c => c.id !== event.id)
      return
    }
    const index = transfers.value.findIndex(t => t.id === event.id)
    // Older backends omit destination; retain only that immutable submission value.
    if (index >= 0 && !event.destination) event.destination = transfers.value[index].destination
    // Other progress fields are full snapshots; merging would retain stale errors/counts.
    if (index >= 0) transfers.value[index] = event; else transfers.value.unshift(event)
    if (isFinished(event.status)) conflicts.value = conflicts.value.filter(c => c.id !== event.id)
    if (event.status === 'completed') { const session = sessions.value.find(s => s.tabId === event.sessionId); if (session && event.direction === 'upload' && !session.filesLoading) void loadDirectory(session) }
  }))
  subscriptions.push(listen<Conflict>('transfer:conflict', raw => {
    const tabId = connectionTabs.get(raw.sessionId)
    if (!tabId) return
    if (cancelledJobs.has(raw.id) || !sessions.value.some(session => session.tabId === tabId)) {
      cancelledJobs.add(raw.id); void api.CancelTransfer(raw.id).catch(fail); return
    }
    conflicts.value.push({ ...raw, sessionId: tabId })
  }))
})
onBeforeUnmount(() => { for (const id of reconnectTokens.keys()) stopReconnect(id); sessionSecrets.clear(); subscriptions.forEach(fn => fn()); ElMessage.closeAll() })
</script>

<template>
  <div class="legacy-app">
    <section v-show="view === 'library'" class="library-screen">
      <header class="protocol-bar"><el-button text class="protocol-selected">SSH</el-button><el-button v-for="protocol in ['RDP', 'Telnet', '隧道']" :key="protocol" text @click="featureNotice = protocol">{{ protocol }}</el-button><span class="flex-spacer"/><el-button v-if="sessions.length" type="primary" plain @click="showSession(activeSession || sessions[sessions.length - 1])"><SquareTerminal :size="15" />返回终端 · {{ sessions.length }}</el-button><el-button text aria-label="应用信息" @click="showInfo = true"><CircleHelp :size="16" /></el-button></header>
      <div class="library-body">
        <aside class="library-groups">
          <div class="group-heading"><strong>主机分组</strong><el-button text aria-label="刷新主机和分组" @click="refresh"><RefreshCw :size="16" /></el-button></div>
          <div class="group-create"><el-button @click="groupName=''; newGroup=true"><Plus :size="14" />分组</el-button><el-button type="primary" @click="editProfile()"><Plus :size="14" />SSH</el-button></div>
          <p class="group-section-label">快捷筛选</p>
          <button class="library-filter" :class="{selected:selectedGroup==='*' && !recentOnly && !privateKeyOnly}" @click="selectGroup('*')"><LayoutGrid :size="16" /><span>全部主机</span><small>{{profiles.length}}</small></button>
          <button class="library-filter" :class="{selected:privateKeyOnly}" @click="selectGroup('*'); privateKeyOnly=true"><KeyRound :size="16" /><span>PEM 私钥</span><small>{{profiles.filter(p=>p.authKind==='private_key').length}}</small></button>
          <button class="library-filter" :class="{selected:recentOnly}" @click="selectGroup('*',true)"><Clock3 :size="16" /><span>最近连接</span><small>{{profiles.filter(p=>p.lastConnectedAt).length}}</small></button>
          <button class="library-filter library-terminal-link" :disabled="!sessions.length" :title="sessions.length ? '返回上次查看的终端' : '连接主机后可在此切回终端'" @click="sessions.length && showSession(activeSession || sessions[sessions.length - 1])"><SquareTerminal :size="16" /><span>终端会话</span><small>{{sessions.length}}</small></button>
          <template v-if="sessions.length">
            <p class="group-section-label">已打开终端</p>
            <nav class="library-session-list" aria-label="已打开终端">
              <button v-for="(session, index) in sessions" :key="session.tabId" class="library-session" :class="{current:activeId===session.tabId}" :aria-label="`切换到终端 ${index + 1}：${session.profile.name}`" :title="`${session.profile.name} · ${session.profile.username}@${session.profile.host}:${session.profile.port}`" @click="showSession(session)">
                <span class="tab-indicator" :class="{offline:session.status==='closed',reconnecting:session.status==='reconnecting'}" />
                <span class="library-session-detail"><strong>{{session.profile.name}}</strong><small>#{{index + 1}} · {{session.status==='connected' ? '已连接' : session.status==='reconnecting' ? '重连中' : '已断开'}}</small></span>
                <small v-if="sessionConflictCount(session.tabId)" class="transfer-pending-count" title="有文件冲突待处理">{{sessionConflictCount(session.tabId)}}</small><ArrowRight v-else :size="13" />
              </button>
            </nav>
          </template>
          <p class="group-section-label">自定义分组</p>
          <div class="custom-groups"><button v-for="group in groups" :key="group" class="library-filter" :class="{selected:selectedGroup===group}" @click="selectGroup(group)"><Folder :size="16" /><span>{{group}}</span><small>{{profiles.filter(p=>p.groupName===group).length}}</small></button><p v-if="!groups.length" class="group-empty">还没有自定义分组</p></div>
        </aside>
        <ConnectionLibrary :profiles="filtered" :title="groupTitle" :total="profiles.length" v-model:search="search" :loading="pageLoading" :connecting-id="connecting" @create="editProfile()" @edit="editProfile" @delete="deleteProfile=$event" @connect="connectProfile" @import="importConfig" @refresh="refresh" @copy="copy" @copy-credentials="copyCredentials" @reset="search=''; selectGroup('*')" />
      </div>
    </section>

    <section v-show="view === 'workspace'" class="workspace-screen">
      <div class="workspace-main">
        <header class="workspace-topbar"><el-button text @click="view='library'"><ArrowLeft :size="15" />主机库</el-button><span class="toolbar-divider"/><strong>{{activeSession?.profile.name || '终端'}}</strong><code v-if="activeSession">{{activeSession.profile.username}}@{{activeSession.profile.host}}:{{activeSession.profile.port}}</code><span class="flex-spacer"/><span class="workspace-status" :class="{offline:activeSession?.status!=='connected'}">● {{activeSession?.status==='connected' ? '终端已连接' : activeSession?.status==='reconnecting' ? '正在重连…' : '会话已结束'}}</span><el-button text :disabled="!activeSession" @click="activeSession && editProfile(activeSession.profile)">编辑连接</el-button></header>
        <div class="workspace-columns">
          <RemoteFilePanel v-if="activeSession" :session-id="activeSession.connection.id" :home="activeSession.connection.home" :directory="activeSession.directory" :loading="activeSession.filesLoading" :error="activeSession.filesError" :connected="activeSession.status==='connected'" :working-path="activeSession.workingPath" @navigate="loadDirectory(activeSession,$event)" @refresh="loadDirectory(activeSession,$event)" @select="selectedRemote=$event; transferPanel?.selectDownload($event.path,false)" @action="handleRemoteAction" @upload="transferPanel?.prepareUpload($event)" @download="transferPanel?.selectDownload($event)" @terminal="terminalDirectory" @copy="copy" @entries="activeSession.treeEntries=$event" />
          <main class="terminal-column">
            <div class="terminal-tabs-row"><div class="terminal-tabs"><div v-for="session in sessions" :key="session.tabId" class="terminal-tab" :class="{active:activeId===session.tabId}"><button @click="showSession(session)"><span class="tab-indicator" :class="{offline:session.status!=='connected'}"/>{{session.connection.name}}<small v-if="sessionConflictCount(session.tabId)" class="transfer-pending-count" title="有文件冲突待处理">{{sessionConflictCount(session.tabId)}}</small></button><button class="close-terminal" aria-label="关闭终端" @click="closeSession(session)"><X :size="13" /></button></div></div><el-button text aria-label="新建终端" title="为当前主机新建终端" :disabled="connecting!==null" @click="newTerminal"><Plus :size="17" /></el-button><el-button text :disabled="!activeSession" @click="clearTerminal">清屏</el-button><el-button v-if="activeSession?.status==='closed'" text :disabled="connecting!==null" @click="reconnect(activeSession)">重新连接</el-button><el-button text aria-label="命令历史" title="命令历史" :disabled="!activeSession" @click="openHistory()"><History :size="16" /></el-button></div>
            <div class="terminal-stack"><div v-for="session in sessions" v-show="session.tabId===activeId" :key="session.tabId" class="terminal-instance"><TerminalPane :ref="instance=>registerTerminal(session.tabId,instance)" :session-id="session.connection.id" :history="historyByProfile[session.profile.id] || []" :visible="view==='workspace' && session.tabId===activeId" @cwd="terminalCwd(session,$event)" @command="recordCommand(session,$event)" @history="openHistory(session)" @closed="terminalClosed(session,$event)" @error="notify($event,'error')" /><div v-if="session.status!=='connected'" class="terminal-disconnected"><Unplug :size="15"/><span>{{session.error || '会话已结束'}}</span><el-button v-if="session.status==='reconnecting'" text @click="cancelReconnect(session)">取消重连</el-button><el-button v-else text @click="reconnect(session)">重新连接</el-button></div></div><div v-if="!sessions.length" class="terminal-empty">当前没有打开的终端<el-button @click="view='library'">主机库</el-button></div></div>
          </main>
        </div>
      </div>
      <TransferPanel ref="transferPanel" :cancelling-transfers="cancellingTransfers" :upload-busy="isTransferSubmitting('upload')" :download-busy="isTransferSubmitting('download')" :conflicts="conflicts" :resolving-conflicts="resolvingConflicts" @resolve="resolveConflict" :session-id="activeSession?.tabId || ''" :connected="activeSession?.status==='connected'" :current-path="activeSession?.directory?.path || activeSession?.connection.home || '/'" :home="activeSession?.connection.home || '/'" :working-path="activeSession?.workingPath || '/'" :entries="activeSession?.treeEntries || []" :transfers="transfers" @upload="submitUpload" @download="submitDownload" @cancel="cancelTransfer" @remove="transfers=transfers.filter(t=>t.id!==$event || !isFinished(t.status))" @clear="clearTransfers" @error="notify($event,'error')" />
    </section>

    <ProfileEditorDialog
      :model-value="editor !== null" :profile="editor" :groups="groups" :saving="saving"
      @update:model-value="value => { if (!value) editor = null }"
      @submit="saveProfile" @error="notify($event, 'error')"
    />

    <AppDialog v-model="newGroup" title="新建主机分组" :busy="saving">
      <p class="dialog-description">按环境、项目或用途整理你的服务器。</p>
      <el-form label-position="top" @submit.prevent="saveGroup">
        <el-form-item label="分组名称" required>
          <el-input v-model="groupName" placeholder="例如：生产环境" maxlength="100" clearable autofocus />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="newGroup = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!groupName.trim()" @click="saveGroup">创建分组</el-button>
      </template>
    </AppDialog>

    <AppDialog :model-value="deleteProfile !== null" title="删除连接配置？" class="delete-confirm-dialog" :width="460" :busy="saving" @update:model-value="value => { if (!value) deleteProfile = null }">
      <p class="delete-description">将从本地主机库移除以下连接，服务器上的文件不受影响。</p>
      <div class="delete-target"><span class="delete-target-icon" aria-hidden="true"><Server :size="22" /></span><div class="delete-target-details"><strong>{{deleteProfile?.name}}</strong><code>{{deleteProfile?.username}}@{{deleteProfile?.host}}:{{deleteProfile?.port}}</code></div></div>
      <el-alert class="delete-warning" title="本机保存的凭据和命令历史也会一并删除。" type="error" show-icon :closable="false" />
      <template #footer>
        <el-button :disabled="saving" @click="deleteProfile = null">取消</el-button>
        <el-button type="danger" :loading="saving" @click="removeProfile">删除连接</el-button>
      </template>
    </AppDialog>

    <AppDialog
      :model-value="authPrompt !== null"
      :title="authPrompt?.authKind === 'private_key' ? '输入私钥口令' : '输入登录密码'"
      :busy="connecting !== null"
      @update:model-value="value => { if (!value) { authPrompt = null; authSecret = '' } }"
    >
      <template v-if="authPrompt">
        <div class="auth-server"><Server :size="24" /><div><strong>{{ authPrompt.name }}</strong><span>{{ authPrompt.username }}@{{ authPrompt.host }}:{{ authPrompt.port }}</span></div></div>
        <el-form label-position="top" @submit.prevent="connectProfile(authPrompt, authSecret)">
          <el-form-item :label="authPrompt.authKind === 'private_key' ? '私钥口令' : '密码'">
            <el-input v-model="authSecret" type="password" show-password autofocus autocomplete="off" placeholder="仅用于本次连接" :disabled="connecting !== null" />
          </el-form-item>
        </el-form>
      </template>
      <template #footer>
        <el-button :disabled="connecting !== null" @click="authPrompt = null; authSecret = ''">取消</el-button>
        <el-button type="primary" :loading="connecting !== null" @click="authPrompt && connectProfile(authPrompt, authSecret)">连接</el-button>
      </template>
    </AppDialog>

    <AppDialog :model-value="trustPrompt !== null" title="确认服务器身份" :width="520" :busy="saving" @update:model-value="value => { if (!value) trustPrompt = null }">
      <template v-if="trustPrompt">
        <el-alert title="这是你首次连接这台服务器" type="warning" show-icon :closable="false" />
        <p class="dialog-description">请通过可信渠道核对 <strong>{{ trustPrompt.key.host }}:{{ trustPrompt.key.port }}</strong> 的主机密钥指纹。</p>
        <div class="dialog-label">SHA256 指纹</div>
        <code class="path-value">{{ trustPrompt.key.fingerprint }}</code>
        <p class="dialog-hint">确认后会在本机保存这条信任记录，后续连接将自动核验。</p>
      </template>
      <template #footer>
        <el-button :disabled="saving" @click="trustPrompt = null">取消连接</el-button>
        <el-button type="primary" :loading="saving" @click="acceptHost"><ShieldCheck :size="15" />信任并连接</el-button>
      </template>
    </AppDialog>

    <AppDialog :model-value="remoteDialog !== null" :title="remoteDialog?.kind === 'delete' ? (remoteDialog.entry?.isDir ? '删除远程文件夹？' : '删除远程文件？') : remoteTitle" :class="{'delete-confirm-dialog':remoteDialog?.kind==='delete','permission-dialog':remoteDialog?.kind==='chmod'}" :width="remoteDialog?.kind==='chmod' ? 540 : remoteDialog?.kind==='delete' ? 460 : 480" :busy="saving" @closed="focusTerminal" @update:model-value="value => { if (!value) remoteDialog = null }">
      <el-form v-if="remoteDialog" label-position="top" @submit.prevent="submitRemote">
        <template v-if="remoteDialog.kind === 'delete'">
          <p class="delete-description">确定从远程服务器删除以下{{remoteDialog.entry?.isDir ? '文件夹' : '文件'}}？</p>
          <div class="delete-target">
            <span class="delete-target-icon" aria-hidden="true"><Folder v-if="remoteDialog.entry?.isDir" :size="22" /><svg v-else width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/></svg></span>
            <div class="delete-target-details"><strong>{{remoteDialog.entry?.name}}</strong><code>{{remoteDialog.entry?.path}}</code></div>
            <el-tag size="small" type="info" effect="plain">{{remoteDialog.entry?.isDir ? '文件夹' : '文件'}}</el-tag>
          </div>
          <el-alert class="delete-warning" :title="remoteDialog.entry?.isDir ? '文件夹及其全部内容将被永久删除，无法恢复。' : '文件将被永久删除，无法恢复。'" type="error" show-icon :closable="false" />
        </template>
        <template v-else-if="remoteDialog.kind === 'chmod'">
          <div class="remote-target-card">
            <span class="remote-target-icon" aria-hidden="true"><ShieldCheck :size="21" /></span>
            <div><strong>{{remoteDialog.entry?.name}}</strong><code>{{remoteDialog.entry?.path}}</code></div>
            <el-tag size="small" type="info" effect="plain">{{remoteDialog.entry?.isDir ? '文件夹' : '文件'}}</el-tag>
          </div>
          <div class="permission-value-row">
            <el-form-item label="八进制权限" required :error="/^[0-7]{3,4}$/.test(remoteDialog.value) ? '' : '请输入 3–4 位八进制数字（0–7）'">
              <el-input v-model="remoteDialog.value" aria-label="八进制权限" placeholder="例如 644" maxlength="4" :disabled="saving" :spellcheck="false" autocomplete="off" />
            </el-form-item>
            <div class="permission-summary"><span>权限预览</span><code>{{/^[0-7]{3,4}$/.test(remoteDialog.value) ? permissionRows.flatMap(row => row.bits.map((bit, index) => permissionChecked(bit) ? ['r','w','x'][index] : '-')).join('') : '---------'}}<small v-if="/^[0-7]{4}$/.test(remoteDialog.value) && remoteDialog.value[0]!=='0'"> + 特殊位</small></code></div>
          </div>
          <div class="permission-matrix-wrap">
            <table class="permission-matrix" aria-label="文件访问权限"><thead><tr><th scope="col">访问对象</th><th scope="col">读取 <small>r</small></th><th scope="col">写入 <small>w</small></th><th scope="col">执行 <small>x</small></th></tr></thead><tbody>
              <tr v-for="(row, rowIndex) in permissionRows" :key="row.name"><th scope="row">{{row.name}}<small>{{['Owner','Group','Others'][rowIndex]}}</small></th><td v-for="(bit,index) in row.bits" :key="bit"><el-checkbox :model-value="/^[0-7]{3,4}$/.test(remoteDialog.value) && permissionChecked(bit)" :aria-label="`${row.name}${['读取','写入','执行'][index]}`" :disabled="saving || !/^[0-7]{3,4}$/.test(remoteDialog.value)" @change="togglePermission(bit,$event)" /></td></tr>
            </tbody></table>
          </div>
          <div class="permission-presets"><div class="form-section-label">常用权限</div><el-radio-group :model-value="/^[0-7]{3,4}$/.test(remoteDialog.value) ? parseInt(remoteDialog.value,8).toString(8) : ''" :disabled="saving" aria-label="常用权限" @update:model-value="remoteDialog.value=String($event)">
            <el-radio-button v-for="preset in [{mode:'644',name:'普通文件'},{mode:'755',name:'可执行 / 目录'},{mode:'600',name:'私有文件'},{mode:'700',name:'私有目录'}]" :key="preset.mode" :value="preset.mode"><strong>{{preset.mode}}</strong><small>{{preset.name}}</small></el-radio-button>
          </el-radio-group></div>
        </template>
        <template v-else>
          <div v-if="remoteDialog.entry" class="remote-target-card"><span class="remote-target-icon" aria-hidden="true"><Folder :size="21" /></span><div><strong>{{remoteDialog.entry.name}}</strong><code>{{remoteDialog.entry.path}}</code></div></div>
          <p v-else class="dialog-description">将在 <code>{{remoteDialog.parentPath}}</code> 中创建{{remoteDialog.kind==='folder' ? '文件夹' : '文件'}}。</p>
          <el-form-item :label="remoteDialog.kind==='rename' ? '新名称' : '名称'" required>
            <el-input v-model="remoteDialog.value" autofocus placeholder="输入名称" :disabled="saving" />
          </el-form-item>
        </template>
      </el-form>
      <template #footer>
        <el-button :disabled="saving" @click="remoteDialog = null">取消</el-button>
        <el-button :type="remoteDialog?.kind === 'delete' ? 'danger' : 'primary'" :loading="saving" :disabled="remoteDialog?.kind==='chmod' && !/^[0-7]{3,4}$/.test(remoteDialog.value)" @click="submitRemote">{{ remoteDialog?.kind === 'delete' ? '永久删除' : remoteDialog?.kind==='chmod' ? '应用权限' : '确认' }}</el-button>
      </template>
    </AppDialog>


    <AppDialog v-model="historyOpen" :title="`命令历史 · ${historySession?.profile.name || '当前主机'}`" :width="660" :busy="historySending" @closed="focusTerminal">
      <p class="dialog-description">按主机记住最近 200 条命令。终端输入前缀后按 Tab 补全；也可以搜索后回填，编辑好再执行。</p>
      <el-form label-position="top" @submit.prevent="sendCommand()">
        <el-form-item label="搜索或编辑命令"><el-input v-model="commandText" autofocus clearable placeholder="搜索历史命令，或输入一条命令" autocomplete="off" spellcheck="false"><template #prefix><Code2 :size="16" /></template></el-input></el-form-item>
      </el-form>
      <div class="history-list" v-loading="historyLoading">
        <p class="history-section-label">最近使用 <span>{{ matchingHistory.length }}</span></p>
        <el-button v-for="command in matchingHistory" :key="command" text :title="command" @click="commandText = command"><History :size="14" /><code>{{ command }}</code></el-button>
        <p v-if="!matchingHistory.length" class="history-empty">{{ history.length ? '没有匹配的历史命令' : '执行命令后会自动记住，下次连接仍可使用。' }}</p>
        <template v-if="matchingCommon.length">
          <p class="history-section-label">常用命令</p>
          <el-button v-for="command in matchingCommon" :key="`common:${command}`" text :title="command" @click="commandText = command"><Code2 :size="14" /><code>{{ command }}</code></el-button>
        </template>
      </div>
      <template #footer>
        <el-button :disabled="historySending" @click="historyOpen = false">关闭</el-button>
        <el-button :disabled="!commandText.trim() || historySession?.status !== 'connected' || historySending" @click="sendCommand(true)">立即执行<ArrowRight :size="15" /></el-button>
        <el-button type="primary" :loading="historySending" :disabled="!commandText.trim() || historySession?.status !== 'connected'" @click="sendCommand()">填入终端</el-button>
      </template>
    </AppDialog>

    <AppDialog :model-value="!!featureNotice" :title="`${featureNotice} 连接`" @update:model-value="value=>{if(!value)featureNotice=''}"><p class="dialog-description">当前版本暂未启用 {{featureNotice}} 连接后端，可使用 SSH 终端和 SFTP 文件传输。</p><template #footer><el-button type="primary" @click="featureNotice=''">知道了</el-button></template></AppDialog>
    <AppDialog :model-value="connecting!==null && !authPrompt && !trustPrompt" title="正在连接服务器" :dismissible="false" @closed="focusTerminal"><el-progress :percentage="50" :indeterminate="true" :show-text="false"/><p class="dialog-description">正在建立 SSH 连接并验证身份…</p></AppDialog>
    <AppDialog v-model="showInfo" title="关于云桥" :width="500">
      <div class="info-logo"><SquareTerminal :size="32" /></div>
      <h3 style="margin-bottom: 12px">让远程工作，更加专注。</h3>
      <p class="dialog-description">使用 Wails 2、Vue 3 和 Element Plus 构建的 SSH / SFTP 桌面工作空间。</p>
      <el-descriptions :column="1">
        <el-descriptions-item label="应用版本"><el-tag effect="plain" size="small">v{{ info.version }}</el-tag></el-descriptions-item>
        <el-descriptions-item label="运行环境">{{ isDesktop() ? '云桥桌面应用' : '浏览器预览' }}</el-descriptions-item>
      </el-descriptions>
      <template v-if="info.dataDir"><div class="dialog-label">本机数据目录</div><code class="path-value">{{ info.dataDir }}</code></template>
      <template #footer><el-button type="primary" @click="showInfo = false">知道了</el-button></template>
    </AppDialog>
  </div>
</template>
