import assert from 'node:assert/strict'
import test from 'node:test'
import { afterTerminalDrain, historyKeyAction, historyMatches, ShellHistoryTracker } from '../src/terminal/history.ts'
import type { HistoryBuffer, HistoryKey, HistoryMarker } from '../src/terminal/history.ts'
import xterm from '@xterm/xterm'

// Model terminal cells (including double-width CJK), not JavaScript offsets.
class Line {
  cells: string[]
  isWrapped: boolean
  constructor(text: string, isWrapped = false) {
    this.isWrapped = isWrapped
    this.cells = [...text].flatMap(char => /[\u2e80-\u9fff]/.test(char) ? [char, ''] : [char])
  }
  translateToString(trim = false, start = 0, end = this.cells.length) {
    const text = this.cells.slice(start, end).join('')
    return trim ? text.trimEnd() : text
  }
}
class Buffer implements HistoryBuffer {
  type: 'normal' | 'alternate' = 'normal'
  baseY = 0
  cursorY = 0
  cursorX = 0
  lines: Line[] = []
  markers: HistoryMarker[] = []
  get length() { return this.lines.length }
  getLine(row: number) { return this.lines[row] }
  screen(rows: (string | [string, boolean])[], cursorRow = rows.length - 1, cursorX?: number) {
    this.lines = rows.map(row => typeof row === 'string' ? new Line(row) : new Line(...row))
    this.cursorY = cursorRow
    this.cursorX = cursorX ?? this.lines[cursorRow]!.cells.length
  }
  marker = (offset: number) => {
    const marker = { line: this.baseY + this.cursorY + offset, isDisposed: false, dispose() { this.isDisposed = true } }
    this.markers.push(marker)
    return marker
  }
}
const prompt = 'root@server:~$ '
function shell(initialPrompt = prompt) {
  const buffer = new Buffer()
  const tracker = new ShellHistoryTracker()
  buffer.screen([initialPrompt])
  tracker.observe(buffer, buffer.marker, 0)
  return { buffer, tracker }
}
function echoedInput(buffer: Buffer, tracker: ShellHistoryTracker, command: string) {
  tracker.input(command, buffer, 1)
  buffer.screen([prompt + command])
  tracker.observe(buffer, buffer.marker, 2)
}

test('quick typing and Enter wait for complete asynchronous echo, including a final late character', () => {
  const { buffer, tracker } = shell()
  tracker.input('git status', buffer, 1)
  tracker.input('\r', buffer, 2)
  assert.equal(tracker.query(buffer), undefined)
  buffer.screen([prompt + 'git statu'])
  assert.equal(tracker.observe(buffer, buffer.marker, 3), undefined)
  buffer.screen([prompt + 'git status', 'On branch main', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 4), 'git status')
  assert.equal(tracker.observe(buffer, buffer.marker, 5), undefined)
  assert.equal(tracker.query(buffer), '')
})

test('records edited echoed line, even when Enter is pressed with the cursor in the middle', () => {
  const { buffer, tracker } = shell()
  echoedInput(buffer, tracker, 'cat old.txt')
  tracker.input('\x1b[D', buffer, 3)
  buffer.screen([prompt + 'cat new.txt'], 0, prompt.length + 5)
  tracker.observe(buffer, buffer.marker, 4)
  assert.equal(tracker.query(buffer), undefined)
  tracker.input('\r', buffer, 5)
  buffer.screen([prompt + 'cat new.txt', 'contents', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 6), 'cat new.txt')
})

test('single-line bracketed paste and wrapped CJK are captured from cells after execution', () => {
  const { buffer, tracker } = shell()
  tracker.input('\x1b[200~echo 中文目录\x1b[201~', buffer, 1)
  tracker.input('\r', buffer, 2)
  buffer.screen([prompt + 'echo 中', ['文目录', true], '中文目录', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 3), 'echo 中文目录')
})

test('password answers are never captured, including ordinary-screen sudo, ssh and mysql prompts', () => {
  for (const passwordPrompt of ['[sudo] password for root: ', "root@other's password: ", 'Enter password: ']) {
    const { buffer, tracker } = shell()
    tracker.input('sudo whoami', buffer, 1)
    tracker.input('\r', buffer, 2)
    buffer.screen([prompt + 'sudo whoami', passwordPrompt])
    assert.equal(tracker.observe(buffer, buffer.marker, 3), 'sudo whoami')
    assert.equal(tracker.query(buffer), undefined)
    tracker.input('secret-never-store', buffer, 4)
    tracker.input('\r', buffer, 5)
    buffer.screen([prompt + 'sudo whoami', passwordPrompt, 'root', prompt])
    assert.equal(tracker.observe(buffer, buffer.marker, 6), undefined)
  }
})

test('no-echo input, unrecognized prompts, multiline paste and repeated Enter are discarded', () => {
  const { buffer, tracker } = shell()
  tracker.input('not-echoed', buffer, 1)
  tracker.input('\r', buffer, 2)
  buffer.screen([prompt, '', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 3), undefined)
  for (const label of ['Password: ', 'mysql> ', '> ', 'output root@server:~$ ']) {
    const untrusted = shell(label)
    assert.equal(untrusted.tracker.query(untrusted.buffer), undefined)
    untrusted.tracker.input('anything\r', untrusted.buffer, 1)
    untrusted.buffer.screen([label + 'anything', prompt])
    assert.equal(untrusted.tracker.observe(untrusted.buffer, untrusted.buffer.marker, 2), undefined)
  }
  for (const inputs of [['echo first\necho second'], ['\x1b[200~first\nsecond\x1b[201~'], ['echo one', '\r', '\r']]) {
    const s = shell()
    inputs.forEach(data => s.tracker.input(data, s.buffer, 1))
    s.buffer.screen([prompt + 'echo one', prompt])
    assert.equal(s.tracker.observe(s.buffer, s.buffer.marker, 2), undefined)
  }
})

test('alternate screens, disconnect reset, expired submission and discarded scrollback cannot record stale input', () => {
  for (const reason of ['alternate', 'reset', 'timeout', 'disposed']) {
    const { buffer, tracker } = shell()
    echoedInput(buffer, tracker, 'echo old')
    tracker.input('\r', buffer, 3)
    if (reason === 'alternate') buffer.type = 'alternate'
    if (reason === 'reset') tracker.reset()
    if (reason === 'disposed') buffer.markers[0]!.dispose()
    buffer.screen(reason === 'timeout' ? [prompt + 'echo old'] : [prompt + 'echo old', prompt])
    assert.equal(tracker.observe(buffer, buffer.marker, reason === 'timeout' ? 6000 : 4), undefined)
    if (reason === 'alternate') assert.equal(tracker.query(buffer), undefined)
  }
})

test('leading spaces opt out of memory and Ctrl+C discards the draft', () => {
  const s = shell()
  echoedInput(s.buffer, s.tracker, ' secret-command')
  s.tracker.input('\r', s.buffer, 3)
  s.buffer.screen([prompt + ' secret-command', prompt])
  assert.equal(s.tracker.observe(s.buffer, s.buffer.marker, 4), undefined)
  const cancelled = shell()
  echoedInput(cancelled.buffer, cancelled.tracker, 'echo never-run')
  cancelled.tracker.input('\x03', cancelled.buffer, 3)
  cancelled.buffer.screen([prompt + 'echo never-run^C', prompt])
  assert.equal(cancelled.tracker.observe(cancelled.buffer, cancelled.buffer.marker, 4), undefined)
})

test('history completion prefers recent unique case-sensitive prefixes and limits to five', () => {
  assert.deepEqual(historyMatches(['git status', 'git log', 'git status', 'echo git', 'Git branch', 'git show', 'git diff', 'git commit', 'git reset', 'git\nunsafe'], 'git'), ['git status', 'git log', 'git show', 'git diff', 'git commit'])
  assert.deepEqual(historyMatches(['git status'], ''), [])
  assert.deepEqual(historyMatches(['git status'], 'Git'), [])
  assert.deepEqual(historyMatches(['git status'], 'status'), [])
  assert.deepEqual(historyMatches(['git status'], 'git status'), [])
})

const tab: HistoryKey = { key: 'Tab', type: 'keydown', shiftKey: false, ctrlKey: false, metaKey: false, altKey: false }
test('actual key routing completes only a ready visible shell prefix; every other Tab reaches the remote', () => {
  const { buffer, tracker } = shell()
  const history = ['git status', 'git log']
  const route = (event: HistoryKey = tab, visible = true, dismissed = false) => {
    const query = tracker.query(buffer)
    return historyKeyAction(event, { ready: query !== undefined, visible, dismissed, hasMatch: query !== undefined && historyMatches(history, query).length > 0 })
  }
  assert.equal(route(), 'pass') // An empty prompt leaves filesystem completion alone.
  tracker.input('git', buffer, 1)
  assert.equal(route(), 'pass') // Unacknowledged echo must not use a stale prefix.
  buffer.screen([prompt + 'git'])
  tracker.observe(buffer, buffer.marker, 2)
  assert.equal(route(), 'complete')
  assert.equal(route(tab, false), 'pass')
  assert.equal(route(tab, true, true), 'pass')
  for (const modifier of ['shiftKey', 'ctrlKey', 'metaKey', 'altKey', 'isComposing']) assert.equal(route({ ...tab, [modifier]: true }), 'pass')
  assert.equal(route({ ...tab, type: 'keyup' }), 'pass')
  assert.equal(route({ ...tab, key: 'Escape' }), 'dismiss')
  assert.equal(route({ ...tab, key: 'ArrowUp' }), 'pass')
  assert.equal(route({ ...tab, key: 'ArrowDown' }), 'pass')
  buffer.cursorX--
  assert.equal(route(), 'pass') // Editing in the middle remains the shell's job.
  buffer.cursorX++
  tracker.input('\r', buffer, 3)
  assert.equal(route(), 'pass') // The command is running.
  buffer.type = 'alternate'
  assert.equal(route(), 'pass') // vim/top must retain Tab.
})

test('history shortcut is explicit, visibility- and shell-gated', () => {
  const state = { ready: true, visible: true, dismissed: false, hasMatch: false }
  assert.equal(historyKeyAction({ ...tab, key: 'H', shiftKey: true, metaKey: true }, state), 'history')
  assert.equal(historyKeyAction({ ...tab, key: 'h', shiftKey: true, ctrlKey: true }, state), 'history')
  assert.equal(historyKeyAction({ ...tab, key: 'H', shiftKey: true, metaKey: true }, { ...state, ready: false }), 'pass')
})

test('real xterm echo, wide-character wrapping and scrollback markers preserve the submitted command', async () => {
  const term = new xterm.Terminal({ cols: 20, rows: 3, allowProposedApi: false })
  const tracker = new ShellHistoryTracker()
  const commands: string[] = []
  const sent: string[] = []
  const observe = () => {
    const normal = term.buffer.active.type === 'normal'
    const command = tracker.observe(normal ? term.buffer.active : term.buffer.normal, offset => normal ? term.registerMarker(offset) : undefined, Date.now(), !normal)
    if (!normal) tracker.reset()
    if (command) commands.push(command)
  }
  const write = (text: string) => new Promise<void>(resolve => term.write(text, () => { observe(); resolve() }))
  const data = term.onData(value => { tracker.input(value, term.buffer.active); sent.push(value) })
  try {
    await write(prompt)
    assert.equal(tracker.query(term.buffer.active), '')
    term.input('echo 中文目录   end')
    term.input('\r')
    await write('echo 中')
    assert.deepEqual(commands, [])
    await write('文目录   end\r\n中文目录   end\r\n' + prompt)
    assert.deepEqual(commands, ['echo 中文目录   end'])
    assert.deepEqual(sent, ['echo 中文目录   end', '\r'])
    assert.equal(tracker.query(term.buffer.active), '')
    // A full-screen application clears the history context even if its screen
    // happens to contain text resembling a shell prompt.
    term.input('vim')
    term.input('\r')
    await write('vim\r\n\x1b[?1049h' + prompt)
    assert.deepEqual(commands, ['echo 中文目录   end', 'vim'])
    assert.equal(tracker.query(term.buffer.active), undefined)
    term.input('password')
    term.input('\r')
    await write('\r\n')
    assert.deepEqual(commands, ['echo 中文目录   end', 'vim'])
  } finally { data.dispose(); tracker.reset(); term.dispose() }
})

test('real xterm backspace editing and a server password prompt do not leak typed secrets', async () => {
  const term = new xterm.Terminal({ cols: 80, rows: 10 })
  const tracker = new ShellHistoryTracker()
  const commands: string[] = []
  const write = (text: string) => new Promise<void>(resolve => term.write(text, () => {
    const command = tracker.observe(term.buffer.active, offset => term.registerMarker(offset))
    if (command) commands.push(command)
    resolve()
  }))
  const data = term.onData(value => tracker.input(value, term.buffer.active))
  try {
    await write(prompt)
    term.input('sudo whami')
    await write('sudo whami')
    term.input('\x7f\x7f\x7f\x7foami')
    await write('\b \b\b \b\b \b\b \bhoami')
    term.input('\r')
    await write('\r\n[sudo] password for root: ')
    assert.deepEqual(commands, ['sudo whoami'])
    term.input('private-password')
    term.input('\r')
    await write('\r\nroot\r\n' + prompt)
    assert.deepEqual(commands, ['sudo whoami'])
  } finally { data.dispose(); tracker.reset(); term.dispose() }
})

test('a fragmented PS2 prompt discards an incomplete multiline command', async () => {
  const term = new xterm.Terminal({ cols: 80, rows: 10 })
  const tracker = new ShellHistoryTracker()
  const commands: string[] = []
  const write = (text: string) => new Promise<void>(resolve => term.write(text, () => {
    const command = tracker.observe(term.buffer.active, offset => term.registerMarker(offset))
    if (command) commands.push(command)
    resolve()
  }))
  try {
    await write(prompt)
    tracker.input('echo "first', term.buffer.active)
    tracker.input('\r', term.buffer.active)
    await write('echo "first\r\n')
    assert.deepEqual(commands, []) // PS2 has not arrived yet.
    await write('> ')
    assert.deepEqual(commands, [])
    assert.equal(tracker.query(term.buffer.active), undefined)
    tracker.input('second"', term.buffer.active)
    tracker.input('\r', term.buffer.active)
    await write('second"\r\nfirst\r\nsecond\r\n' + prompt)
    assert.deepEqual(commands, [])
    assert.equal(tracker.query(term.buffer.active), '')
  } finally { tracker.reset(); term.dispose() }
})

test('PS2 and its continuation received in one batch never save the first line', () => {
  const { buffer, tracker } = shell()
  tracker.input('echo "first', buffer, 1)
  tracker.input('\r', buffer, 2)
  buffer.screen([prompt + 'echo "first', '> second"', 'first', 'second', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 3), undefined)
})

test('a silent long-running command keeps its confirmed echo until the shell prompt returns', () => {
  const { buffer, tracker } = shell()
  tracker.input('sleep 10', buffer, 1)
  tracker.input('\r', buffer, 2)
  buffer.screen([prompt + 'sleep 10', ''])
  assert.equal(tracker.observe(buffer, buffer.marker, 3), undefined)
  assert.equal(tracker.observe(buffer, buffer.marker, 9000), undefined)
  buffer.screen([prompt + 'sleep 10', prompt])
  assert.equal(tracker.observe(buffer, buffer.marker, 10004), 'sleep 10')
})

test('connection close drains queued xterm echo before recording exit and resetting history', async () => {
  const term = new xterm.Terminal({ cols: 80, rows: 10 })
  const tracker = new ShellHistoryTracker()
  const commands: string[] = []
  try {
    await new Promise<void>(resolve => term.write(prompt, resolve))
    tracker.observe(term.buffer.active, offset => term.registerMarker(offset))
    tracker.input('exit', term.buffer.active)
    tracker.input('\r', term.buffer.active)
    // WriteBuffer schedules this asynchronously: closed arrives in the same
    // turn, before the echo has been parsed. No network or DOM is involved.
    term.write('exit\r\n')
    let closed = false
    const drained = new Promise<void>(resolve => afterTerminalDrain((data, done) => term.write(data, done), () => true, () => {
      const command = tracker.observe(term.buffer.active, offset => term.registerMarker(offset), Date.now(), true)
      if (command) commands.push(command)
      tracker.reset()
      closed = true
      resolve()
    }))
    assert.equal(closed, false)
    await drained
    assert.deepEqual(commands, ['exit'])
    assert.equal(tracker.query(term.buffer.active), undefined)
  } finally { tracker.reset(); term.dispose() }
})

test('an old close-drain callback cannot finalize or reset a replacement connection', async () => {
  const term = new xterm.Terminal({ cols: 80, rows: 10 })
  let version = 1
  let finalized = 0
  try {
    const closingVersion = version
    afterTerminalDrain((data, done) => term.write(data, done), () => version === closingVersion, () => { finalized++ })
    version++
    await new Promise<void>(resolve => term.write(prompt, resolve))
    assert.equal(finalized, 0)
    assert.equal(term.buffer.active.getLine(0)?.translateToString(true), prompt)
  } finally { term.dispose() }
})
