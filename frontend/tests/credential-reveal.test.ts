import assert from 'node:assert/strict'
import test from 'node:test'
import { useCredentialReveal } from '../src/credential-reveal.ts'

function deferred() {
  let resolve!: (value: string) => void
  let reject!: (error: Error) => void
  const promise = new Promise<string>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('saved credentials are read only on demand and viewing never edits the saved value', async () => {
  const reads: number[] = []
  const editor = useCredentialReveal(async id => { reads.push(id); return 'fixture-only' })
  assert.equal(editor.displayValue.value, '')
  assert.deepEqual(reads, [])
  await editor.toggle(7, true)
  assert.deepEqual(reads, [7])
  assert.equal(editor.displayValue.value, 'fixture-only')
  assert.equal(editor.secret.value, '') // Save continues to use keepSecret.
  assert.equal(editor.visible.value, true)
  await editor.toggle(7, true)
  assert.equal(editor.displayValue.value, '')
  assert.equal(editor.visible.value, false)
  await editor.toggle(7, true)
  assert.deepEqual(reads, [7, 7]) // Hiding cleared the saved preview.
})

test('new or edited credentials toggle locally without retrieving the saved secret', async () => {
  let reads = 0
  const editor = useCredentialReveal(async () => { reads++; return 'old-fixture' })
  await editor.toggle(0, false)
  editor.update('new-fixture')
  await editor.toggle(0, false)
  assert.equal(editor.secret.value, 'new-fixture')
  assert.equal(editor.visible.value, false)
  await editor.toggle(7, true)
  assert.equal(editor.displayValue.value, 'new-fixture')
  assert.equal(reads, 0)
})

test('typing during a pending reveal cannot be replaced by the delayed saved password', async () => {
  const pending = deferred()
  const editor = useCredentialReveal(() => pending.promise)
  const revealing = editor.toggle(7, true)
  editor.update('typed-fixture')
  pending.resolve('old-fixture')
  await revealing
  assert.equal(editor.secret.value, 'typed-fixture')
  assert.equal(editor.displayValue.value, 'typed-fixture')
  assert.equal(editor.visible.value, false)
  assert.equal(editor.loading.value, false)
})

test('closing or changing authentication discards a pending reveal', async () => {
  const pending = deferred()
  const editor = useCredentialReveal(() => pending.promise)
  const revealing = editor.toggle(7, true)
  editor.reset()
  pending.resolve('old-fixture')
  await revealing
  assert.equal(editor.secret.value, '')
  assert.equal(editor.displayValue.value, '')
  assert.equal(editor.visible.value, false)
  assert.equal(editor.loading.value, false)
})

test('a stale response or error cannot change the next profile or its loading state', async () => {
  for (const fail of [false, true]) {
    const first = deferred(), second = deferred()
    const editor = useCredentialReveal(id => id === 1 ? first.promise : second.promise)
    const oldRequest = editor.toggle(1, true)
    editor.reset()
    const currentRequest = editor.toggle(2, true)
    if (fail) first.reject(new Error('obsolete request'))
    else first.resolve('first-fixture')
    await oldRequest
    assert.equal(editor.displayValue.value, '')
    assert.equal(editor.loading.value, true)
    second.resolve('second-fixture')
    await currentRequest
    assert.equal(editor.displayValue.value, 'second-fixture')
    assert.equal(editor.secret.value, '')
  }
})

test('a failed current reveal stays hidden and can be retried', async () => {
  let attempts = 0
  const editor = useCredentialReveal(async () => {
    if (++attempts === 1) throw new Error('fixture failure')
    return 'retry-fixture'
  })
  await assert.rejects(editor.toggle(7, true), /fixture failure/)
  assert.equal(editor.loading.value, false)
  assert.equal(editor.visible.value, false)
  assert.equal(editor.displayValue.value, '')
  await editor.toggle(7, true)
  assert.equal(editor.displayValue.value, 'retry-fixture')
})
