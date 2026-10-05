import assert from 'node:assert/strict'
import test from 'node:test'
import { TerminalTransport } from '../src/terminal/transport.ts'
import { oscWorkingDirectory, promptWorkingDirectory } from '../src/terminal/cwd.ts'

function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>(done => { resolve = done })
  return { promise, resolve }
}

// This is the startup race from the UI: StartTerminal resolves after the layout
// changes (for example, the file pane opens), before the user types a command.
test('a fit while the shell starts reaches the PTY before typing', async () => {
  const shell = deferred()
  const began = deferred()
  const calls: string[] = []
  const transport = new TerminalTransport({
    resize: async (cols, rows) => { calls.push(`resize ${cols} ${rows}`) },
    start: async () => { calls.push('start'); began.resolve(); await shell.promise },
    write: async data => { calls.push(`write ${data}`) },
  }, error => { throw error })
  const ready = transport.start(120, 40)
  await began.promise
  const resized = transport.resize(78, 30)
  const written = transport.write('cd h')
  assert.deepEqual(calls, ['resize 120 40', 'start'])
  shell.resolve()
  await Promise.all([ready, resized, written])
  assert.deepEqual(calls, ['resize 120 40', 'start', 'resize 78 30', 'write cd h'])
})

test('terminal protocol replies are retained during startup', async () => {
  const shell = deferred()
  const began = deferred()
  const inputs: string[] = []
  const transport = new TerminalTransport({
    resize: async () => {},
    start: async () => { began.resolve(); await shell.promise },
    write: async data => { inputs.push(data) },
  }, error => { throw error })
  const ready = transport.start(80, 24)
  await began.promise
  const reply = transport.write('\x1b[1;1R')
  shell.resolve()
  await Promise.all([ready, reply])
  assert.deepEqual(inputs, ['\x1b[1;1R'])
})

test('resize and input order is preserved under asynchronous IPC', async () => {
  const blocked = deferred()
  const entered = deferred()
  const calls: string[] = []
  const transport = new TerminalTransport({
    resize: async (cols, rows) => {
      calls.push(`resize ${cols} ${rows}`)
      if (cols === 90) { entered.resolve(); await blocked.promise }
    },
    start: async () => {},
    write: async data => { calls.push(`write ${data}`) },
  }, error => { throw error })
  await transport.start(80, 24)
  void transport.resize(90, 25)
  await entered.promise
  void transport.resize(90, 25)
  void transport.resize(100, 26)
  const written = transport.write('ls\r')
  assert.deepEqual(calls, ['resize 80 24', 'resize 90 25'])
  blocked.resolve()
  await written
  assert.deepEqual(calls, ['resize 80 24', 'resize 90 25', 'resize 100 26', 'write ls\r'])
})

test('unmount cancels queued operations without reporting a false error', async () => {
  const blocked = deferred()
  const entered = deferred()
  const inputs: string[] = []
  const transport = new TerminalTransport({
    resize: async () => {},
    start: async () => { entered.resolve(); await blocked.promise },
    write: async data => { inputs.push(data) },
  }, error => { throw error })
  const ready = transport.start(80, 24)
  await entered.promise
  void transport.write('ls\r')
  transport.dispose()
  blocked.resolve()
  await ready
  assert.deepEqual(inputs, [])
})

test('startup failure rejects and cancels queued user input', async () => {
  const inputs: string[] = []
  const transport = new TerminalTransport({
    resize: async () => {}, start: async () => { throw new Error('closed') },
    write: async data => { inputs.push(data) },
  }, error => { throw error })
  const ready = transport.start(80, 24)
  const written = transport.write('ls\r')
  await assert.rejects(ready, /closed/)
  await written
  assert.deepEqual(inputs, [])
})

test('late completion from a replaced connection cannot replay input in its replacement', async () => {
  const oldShell = deferred()
  const entered = deferred()
  const inputs: string[] = []
  const old = new TerminalTransport({
    resize: async () => {}, start: async () => { entered.resolve(); await oldShell.promise },
    write: async data => { inputs.push(`old:${data}`) },
  }, error => { throw error })
  const oldReady = old.start(80, 24)
  await entered.promise
  void old.write('old command')
  old.dispose()
  const replacement = new TerminalTransport({
    resize: async () => {}, start: async () => {},
    write: async data => { inputs.push(`new:${data}`) },
  }, error => { throw error })
  await replacement.start(100, 30)
  await replacement.write('new command')
  oldShell.resolve()
  await oldReady
  assert.deepEqual(inputs, ['new:new command'])
})

test('cwd detection accepts only completed absolute or home prompts', () => {
  assert.equal(promptWorkingDirectory('root@server:/home# '), '/home')
  assert.equal(promptWorkingDirectory('deploy@server:~/代码$ '), '~/代码')
  assert.equal(promptWorkingDirectory('deploy@server:~$ '), '~')
  assert.equal(promptWorkingDirectory('[root@server /home/app]# '), '/home/app')
  assert.equal(promptWorkingDirectory('[root@server app]# '), undefined)
  assert.equal(promptWorkingDirectory('root@server:/home# cd h'), undefined)
  assert.equal(promptWorkingDirectory("root@server:/home# echo '#"), undefined)
  assert.equal(promptWorkingDirectory('error output /home# '), undefined)
})

test('OSC7 decodes valid file paths without accepting control or non-file URLs', () => {
  assert.equal(oscWorkingDirectory('file://server/home/my%20project'), '/home/my project')
  assert.equal(oscWorkingDirectory('file:///home/%E4%BB%A3%E7%A0%81'), '/home/代码')
  assert.equal(oscWorkingDirectory('https://example.com/home'), undefined)
  assert.equal(oscWorkingDirectory('file:///home%0Adelete'), undefined)
  assert.equal(oscWorkingDirectory('file:///broken%ZZ'), undefined)
})
