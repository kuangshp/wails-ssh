import assert from 'node:assert/strict'
import test from 'node:test'
import { CommandHistoryCache } from '../src/terminal/history-store.ts'

test('a delayed history load cannot erase a newly submitted command', async () => {
  let resolve!: (commands: string[]) => void
  const loaded = new Promise<string[]>(done => { resolve = done })
  const events: string[] = []
  let visible: string[] = []
  const cache = new CommandHistoryCache({
    load: () => loaded,
    save: async (_, command) => { events.push(command) },
  }, (_, commands) => { visible = commands })
  const reading = cache.load(1)
  const writing = cache.record(1, 'git status')
  resolve(['pwd', 'ls'])
  await Promise.all([reading, writing])
  assert.deepEqual(visible, ['git status', 'pwd', 'ls'])
  assert.deepEqual(events, ['git status'])
})

test('hosts persist independently and repeated commands move to the front', async () => {
  let release!: () => void
  const blocked = new Promise<void>(resolve => { release = resolve })
  const visible = new Map<number, string[]>()
  const cache = new CommandHistoryCache({
    load: async () => ['pwd', 'ls'],
    save: async id => { if (id === 1) await blocked },
  }, (id, commands) => { visible.set(id, commands) })
  await Promise.all([cache.load(1), cache.load(2)])
  const first = cache.record(1, 'hostname')
  await cache.record(2, 'ls')
  assert.deepEqual(visible.get(2), ['ls', 'pwd'])
  assert.deepEqual(visible.get(1), ['pwd', 'ls'])
  release()
  await first
  assert.deepEqual(visible.get(1), ['hostname', 'pwd', 'ls'])
})

test('failed writes remain retryable and non-command input is not stored', async () => {
  let fail = true
  const saved: string[] = []
  const cache = new CommandHistoryCache({
    load: async () => [],
    save: async (_, command) => { if (fail) throw new Error('database busy'); saved.push(command) },
  }, () => {})
  await assert.rejects(cache.record(1, 'pwd'), /database busy/)
  fail = false
  await cache.record(1, 'pwd')
  for (const input of [' secret', 'one\ntwo', '\x1b[A', '', '  ']) await cache.record(1, input)
  assert.deepEqual(saved, ['pwd'])
})
