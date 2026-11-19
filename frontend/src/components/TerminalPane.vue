<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, errorText, listen } from '../api'
import { TerminalTransport } from '../terminal/transport'
import { oscWorkingDirectory, promptWorkingDirectory } from '../terminal/cwd'
import { afterTerminalDrain, historyKeyAction, safeHistoryCommand, ShellHistoryTracker } from '../terminal/history'

const props = defineProps<{ sessionId: string; visible: boolean }>()
const emit = defineEmits<{ closed: [error: string]; error: [message: string]; cwd: [path: string]; command: [command: string]; history: [] }>()
const host = ref<HTMLDivElement>()
const commandTracker = new ShellHistoryTracker()
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
  if (buffer.type !== 'normal') { commandTracker.reset(); return }
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
  await clearing
  focus()
}

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
  commandTracker.reset()
  term.clear()
  if (!ended) void transport?.write('\x0c')
}
function paste(text: string) { if (!disposed && !ended) { term.paste(text); focus() } }
async function execute(command: string) {
  requireShellInput(command)
  const version = connectionVersion
  commandTracker.reset()
  await transport!.write(`\x15${command}\r`)
  if (version === connectionVersion && !disposed && !ended) { emit('command', command); focus() }
}
defineExpose({ focus, clear, paste, execute, replaceInput })

async function connect(sessionId: string) {
  const version = ++connectionVersion
  transport?.dispose()
  commandTracker.reset()
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
    transport?.dispose()
    afterTerminalDrain((data, done) => term.write(data, done), () => !disposed && version === connectionVersion && event.sessionId === activeSessionId, () => {
      readPrompt(true)
      drainingClose = false
      commandTracker.reset()
      term.writeln('\r\n\x1b[90m[连接已结束]\x1b[0m')
      emit('closed', event.error || '')
    })
  }))
  const data = term.onData(value => {
    if (disposed || ended) return
    commandTracker.input(value, term.buffer.active)
    void transport?.write(value)
  })
  term.attachCustomKeyEventHandler(event => {
    const action = historyKeyAction(event, {
      ready: !disposed && !ended && commandTracker.query(term.buffer.active) !== undefined,
      visible: props.visible,
    })
    if (action === 'pass') return true
    event.preventDefault()
    emit('history')
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
  commandTracker.reset()
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
  </div>
</template>

<style scoped>
.terminal-pane { position: relative; height: 100%; width: 100%; padding: 10px 8px; box-sizing: border-box; overflow: hidden; background: #000; }
.terminal-host { height: 100%; width: 100%; overflow: hidden; }
.terminal-host :deep(.xterm) { height: 100%; }
.terminal-host :deep(.xterm-viewport) { scrollbar-width: thin; scrollbar-color: #354256 transparent; }
</style>
