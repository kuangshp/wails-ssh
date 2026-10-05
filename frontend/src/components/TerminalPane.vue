<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, errorText, listen } from '../api'
import { TerminalTransport } from '../terminal/transport'
import { oscWorkingDirectory, promptWorkingDirectory } from '../terminal/cwd'
import { afterTerminalDrain, historyKeyAction, historyMatches, safeHistoryCommand, ShellHistoryTracker } from '../terminal/history'

const props = defineProps<{ sessionId: string; visible: boolean; history?: string[] }>()
const emit = defineEmits<{ closed: [error: string]; error: [message: string]; cwd: [path: string]; command: [command: string]; history: [] }>()
const host = ref<HTMLDivElement>()
const commandTracker = new ShellHistoryTracker()
const inputQuery = ref<string>()
const dismissedQuery = ref<string>()
const suggestions = computed(() => props.visible && inputQuery.value !== undefined && dismissedQuery.value !== inputQuery.value ? historyMatches(props.history || [], inputQuery.value) : [])
const term = new Terminal({
  cursorBlink: true, cursorStyle: 'bar', fontSize: 12.5, lineHeight: 1.4,
  fontFamily: '"SFMono-Regular", Menlo, Monaco, Consolas, monospace',
  scrollback: 10000, allowProposedApi: false,
  theme: { background: '#000000', foreground: '#e2e7eb', cursor: '#71daca', selectionBackground: '#3c536c', black: '#202b3a', red: '#f18e96', green: '#a1d4a1', yellow: '#ebcd8d', blue: '#88b7e9', magenta: '#bca4e7', cyan: '#7ecdd2', white: '#d9e2ef' },
})
const fit = new FitAddon()
let transport: TerminalTransport | undefined
let observer: ResizeObserver | undefined
let disposed = false
let ended = false
let drainingClose = false
let mounted = false
let activeSessionId = ''
let connectionVersion = 0
let resizeFrame = 0
let lastDirectory = ''
let acceptingDirectory = true
const cleanups: (() => void)[] = []

function reportDirectory(path: string | undefined) {
  if (!disposed && !ended && acceptingDirectory && path && path !== lastDirectory) { lastDirectory = path; emit('cwd', path) }
}

function readPrompt(final = false) {
  if (disposed || !acceptingDirectory || ended && !drainingClose) return
  const buffer = term.buffer.active
  // vim/top can switch screens in the same output chunk as the command echo.
  // Finish that pending shell line in the saved normal buffer, then discard all
  // editing context while the application's alternate screen is active.
  const command = commandTracker.observe(buffer.type === 'normal' ? buffer : term.buffer.normal, offset => buffer.type === 'normal' ? term.registerMarker(offset) : undefined, Date.now(), final || buffer.type !== 'normal')
  if (command) emit('command', command)
  if (buffer.type !== 'normal') { resetHistoryInput(); return }
  updateHistoryQuery()
  const cursorRow = buffer.baseY + buffer.cursorY
  const cursorLine = buffer.getLine(cursorRow)
  if (!cursorLine || cursorLine.translateToString(true, buffer.cursorX).trim()) return
  let line = cursorLine.translateToString(false, 0, buffer.cursorX)
  // A long prompt can wrap at the right edge; work in terminal cells, not JS chars.
  for (let row = cursorRow; row > 0 && buffer.getLine(row)?.isWrapped; row--) {
    line = (buffer.getLine(row - 1)?.translateToString(false) || '') + line
  }
  reportDirectory(promptWorkingDirectory(line))
}

function updateHistoryQuery() {
  const query = disposed || ended ? undefined : commandTracker.query(term.buffer.active)
  if (query !== undefined && query !== dismissedQuery.value) dismissedQuery.value = undefined
  inputQuery.value = query
}

function resetHistoryInput() {
  commandTracker.reset()
  inputQuery.value = undefined
  dismissedQuery.value = undefined
}

function requireShellInput(command: string) {
  if (disposed || ended || !transport) throw new Error('终端未连接')
  if (!safeHistoryCommand(command)) throw new Error('只能回填或执行单行命令')
  if (!props.visible || commandTracker.query(term.buffer.active) === undefined) throw new Error('请等待 Shell 提示符，并将光标移到命令行末尾后再试')
}

async function replaceInput(command: string) {
  requireShellInput(command)
  // Both writes share the transport queue. xterm.paste respects bracketed
  // paste mode, and neither write contains the Enter key.
  const clearing = transport!.write('\x15')
  term.paste(command)
  updateHistoryQuery()
  await clearing
  focus()
}

function fillSuggestion(command: string) { void replaceInput(command).catch(error => emit('error', errorText(error))) }

function fitVisible() {
  if (!props.visible || !host.value?.clientWidth || !host.value.clientHeight || disposed || ended) return
  fit.fit()
}

function resize() {
  cancelAnimationFrame(resizeFrame)
  resizeFrame = requestAnimationFrame(fitVisible)
}

function focus() { if (!disposed && !ended && props.visible) term.focus() }
function clear() {
  if (disposed) return
  resetHistoryInput()
  term.clear()
  if (!ended) void transport?.write('\x0c')
}
function paste(text: string) { if (!disposed && !ended) { term.paste(text); focus() } }
async function execute(command: string) {
  requireShellInput(command)
  const version = connectionVersion
  resetHistoryInput()
  await transport!.write(`\x15${command}\r`)
  if (version === connectionVersion && !disposed && !ended) { emit('command', command); focus() }
}
defineExpose({ focus, clear, paste, execute, replaceInput })

async function connect(sessionId: string) {
  const version = ++connectionVersion
  transport?.dispose()
  resetHistoryInput()
  activeSessionId = sessionId
  ended = false
  drainingClose = false
  lastDirectory = ''
  if (version > 1) {
    acceptingDirectory = false
    // End an interrupted application's alternate screen/mouse modes, preserving
    // the normal scrollback. A soft reset does not erase the previous session.
    term.write('\x18\x1b[?47l\x1b[!p\x1b[?1000;1002;1003;1006l\r\n', () => {
      if (version === connectionVersion) acceptingDirectory = true
    })
  }
  const current = new TerminalTransport({
    start: () => api.StartTerminal(sessionId),
    resize: (cols, rows) => api.ResizeTerminal(sessionId, cols, rows),
    write: data => api.WriteTerminal(sessionId, data),
  }, error => { if (version === connectionVersion && !disposed) emit('error', errorText(error)) })
  transport = current
  fitVisible()
  try {
    await current.start(term.cols, term.rows)
    if (version === connectionVersion && !disposed && !ended) focus()
  } catch (error) {
    if (version === connectionVersion && !disposed && !ended) { ended = true; emit('closed', errorText(error)) }
  }
}

onMounted(() => {
  term.loadAddon(fit)
  term.open(host.value!)
  fitVisible()
  cleanups.push(listen<{sessionId: string; data: string}>('terminal:data', event => {
    if (event.sessionId !== activeSessionId || disposed || ended) return
    try { term.write(Uint8Array.from(atob(event.data), char => char.charCodeAt(0))) }
    catch (error) { emit('error', errorText(error)) }
  }))
  cleanups.push(listen<{sessionId: string; error?: string}>('terminal:closed', event => {
    if (event.sessionId !== activeSessionId || disposed || ended) return
    const version = connectionVersion
    ended = true
    drainingClose = true
    inputQuery.value = undefined
    transport?.dispose()
    afterTerminalDrain((data, done) => term.write(data, done), () => !disposed && version === connectionVersion && event.sessionId === activeSessionId, () => {
      readPrompt(true)
      drainingClose = false
      resetHistoryInput()
      term.writeln('\r\n\x1b[90m[连接已结束]\x1b[0m')
      emit('closed', event.error || '')
    })
  }))
  const data = term.onData(value => {
    if (disposed || ended) return
    commandTracker.input(value, term.buffer.active)
    updateHistoryQuery()
    void transport?.write(value)
  })
  term.attachCustomKeyEventHandler(event => {
    const query = commandTracker.query(term.buffer.active)
    const matches = query === undefined ? [] : historyMatches(props.history || [], query)
    const action = historyKeyAction(event, {
      ready: !disposed && !ended && query !== undefined,
      visible: props.visible,
      hasMatch: !!matches.length,
      dismissed: query !== undefined && dismissedQuery.value === query,
    })
    if (action === 'pass') return true
    event.preventDefault()
    if (action === 'complete') fillSuggestion(matches[0]!)
    else if (action === 'dismiss') dismissedQuery.value = query
    else emit('history')
    return false
  })
  // onResize fires only when rows/columns actually change, including font/zoom fits.
  // Its requests share one queue with input so an older PTY size cannot win a race.
  const dimensions = term.onResize(({ cols, rows }) => { void transport?.resize(cols, rows) })
  const cwd = term.parser.registerOscHandler(7, value => { reportDirectory(oscWorkingDirectory(value)); return true })
  const parsed = term.onWriteParsed(() => readPrompt())
  cleanups.push(() => data.dispose(), () => dimensions.dispose(), () => cwd.dispose(), () => parsed.dispose())
  observer = new ResizeObserver(resize)
  observer.observe(host.value!)
  mounted = true
  void connect(props.sessionId)
})

watch(() => props.sessionId, sessionId => { if (mounted && !disposed) void connect(sessionId) })

watch(() => props.visible, async visible => {
  if (!visible) return
  await nextTick()
  if (!disposed) { fitVisible(); focus() }
})
onBeforeUnmount(() => {
  disposed = true
  resetHistoryInput()
  transport?.dispose()
  cleanups.forEach(fn => fn())
  observer?.disconnect()
  cancelAnimationFrame(resizeFrame)
  term.dispose()
})
</script>

<template>
  <div class="terminal-pane">
    <div ref="host" class="terminal-host" />
    <aside v-if="suggestions.length" class="terminal-history-suggestions" aria-label="历史命令提示">
      <div class="terminal-history-heading"><span>Tab 补全 · Esc 隐藏</span><button type="button" title="搜索历史（Ctrl/Cmd+Shift+H）" @mousedown.prevent @click="emit('history')">搜索历史</button></div>
      <button v-for="(command, index) in suggestions" :key="command" type="button" class="terminal-history-option" :class="{ first: index === 0 }" :title="command" @mousedown.prevent @click="fillSuggestion(command)"><span>{{ command }}</span><small v-if="index === 0">Tab</small></button>
    </aside>
  </div>
</template>

<style scoped>
.terminal-pane { position: relative; height: 100%; width: 100%; padding: 10px 8px; box-sizing: border-box; overflow: hidden; background: #000; }
.terminal-host { height: 100%; width: 100%; overflow: hidden; }
.terminal-host :deep(.xterm) { height: 100%; }
.terminal-host :deep(.xterm-viewport) { scrollbar-width: thin; scrollbar-color: #354256 transparent; }
.terminal-history-suggestions { position: absolute; right: 16px; bottom: 38px; z-index: 2; width: min(480px, calc(100% - 32px)); padding: 6px; border: 1px solid #35483f; border-radius: 8px; background: #19221ef5; box-shadow: 0 6px 24px #0008; }
.terminal-history-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 3px 6px 6px; color: #92aa9d; font-size: 10px; }
.terminal-history-heading button { padding: 2px 0; border: 0; color: #a1d4b8; background: transparent; cursor: pointer; font-size: 10px; }
.terminal-history-option { display: flex; align-items: center; gap: 12px; width: 100%; padding: 6px 8px; border: 0; border-radius: 4px; background: transparent; color: #dce6df; cursor: pointer; text-align: left; font: 11px/1.5 Menlo, Monaco, Consolas, monospace; }
.terminal-history-option span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.terminal-history-option small { margin-left: auto; flex-shrink: 0; color: #7fb794; font-size: 9px; }
.terminal-history-option.first, .terminal-history-option:hover { background: #294033; }
.terminal-history-option:focus-visible, .terminal-history-heading button:focus-visible { outline: 1px solid #71daca; }
</style>
