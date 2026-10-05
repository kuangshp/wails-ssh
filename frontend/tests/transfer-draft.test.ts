import test from 'node:test'
import assert from 'node:assert/strict'
import { createUploadRequest, transferDraftForContext, type TransferDraft } from '../src/transfer-draft.ts'

test('terminal cd updates the upload draft before the browser listing arrives or succeeds', () => {
  const initial = transferDraftForContext(undefined, '/srv/site', '/srv/site', '/home/user')
  const afterCd = transferDraftForContext(initial, '/srv/site', '/srv/next', '/home/user')
  assert.equal(afterCd, initial)
  assert.equal(afterCd.uploadDirectory, '/srv/next')

  // A failed listing may leave the old directory in place or no directory at all.
  assert.equal(transferDraftForContext(afterCd, '/srv/site', '/srv/next', '/home/user').uploadDirectory, '/srv/next')
  assert.equal(transferDraftForContext(afterCd, '', '/srv/next', '/home/user').uploadDirectory, '/srv/next')
})

test('a delayed or manually browsed directory cannot roll the upload draft back from terminal cwd', () => {
  const draft = transferDraftForContext(undefined, '/srv/site', '/srv/site', '/home/user')
  transferDraftForContext(draft, '/srv/site', '/srv/next', '/home/user')
  transferDraftForContext(draft, '/srv/site', '/srv/latest', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/latest')

  transferDraftForContext(draft, '/srv/next', '/srv/latest', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/latest')
  transferDraftForContext(draft, '/var/manually-browsed', '/srv/latest', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/latest')
})

test('manual destinations survive same-cwd refreshes but the next cd follows the terminal', () => {
  const draft = transferDraftForContext(undefined, '/srv/site', '/srv/site', '/home/user')
  draft.uploadDirectory = '/srv/manual'
  assert.equal(transferDraftForContext(draft, '/srv/site', '/srv/site', '/home/user').uploadDirectory, '/srv/manual')
  assert.equal(transferDraftForContext(draft, '/var/browsed', '/srv/site', '/home/user').uploadDirectory, '/srv/manual')

  transferDraftForContext(draft, '/var/browsed', '/srv/next', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/next')
  draft.uploadDirectory = ''
  assert.equal(transferDraftForContext(draft, '/srv/next', '/srv/next', '/home/user').uploadDirectory, '')
  assert.equal(transferDraftForContext(draft, '/srv/next', '/srv/final', '/home/user').uploadDirectory, '/srv/final')
})

test('session drafts and pending picker selections remain isolated when switching tabs', () => {
  const drafts = new Map<string, TransferDraft>()
  const first = transferDraftForContext(drafts.get('first'), '/srv/first', '/srv/first', '/home/first')
  drafts.set('first', first)
  first.uploadDirectory = '/srv/first-release'
  first.localDirectory = '/local/downloads'
  const pickerDraft = first

  const second = transferDraftForContext(drafts.get('second'), '/srv/second', '/srv/second', '/home/second')
  drafts.set('second', second)
  second.paths.push('/local/second')
  transferDraftForContext(second, '/srv/second', '/srv/second-next', '/home/second')
  pickerDraft.paths.push('/local/first')

  const restored = transferDraftForContext(drafts.get('first'), '/tmp/browsed', '/srv/first', '/home/first')
  assert.equal(restored, first)
  assert.equal(restored.uploadDirectory, '/srv/first-release')
  assert.equal(restored.localDirectory, '/local/downloads')
  assert.deepEqual(restored.paths, ['/local/first'])
  assert.equal(second.uploadDirectory, '/srv/second-next')
  assert.deepEqual(second.paths, ['/local/second'])

  assert.equal(transferDraftForContext(first, '/tmp/browsed', '/srv/first-next', '/home/first'), pickerDraft)
  pickerDraft.paths.push('/local/first-late')
  assert.equal(first.uploadDirectory, '/srv/first-next')
  assert.deepEqual(first.paths, ['/local/first', '/local/first-late'])
  assert.equal(second.uploadDirectory, '/srv/second-next')
  assert.deepEqual(second.paths, ['/local/second'])
})

test('initial upload directory prefers terminal cwd and falls back to browser, home, then dot', () => {
  const initial = transferDraftForContext(undefined, '/srv/site', '/tmp/working', '/home/user')
  assert.equal(initial.uploadDirectory, '/tmp/working')
  assert.equal(initial.downloadPath, '/tmp/working')
  assert.equal(initial.previousDirectory, '/tmp/working')
  assert.equal(transferDraftForContext(undefined, '/srv/site', '', '/home/user').uploadDirectory, '/srv/site')
  assert.equal(transferDraftForContext(undefined, '', '', '/home/user').uploadDirectory, '/home/user')
  assert.equal(transferDraftForContext(undefined, '', '', '').uploadDirectory, '.')
})

test('browser context follows only until terminal cwd is known', () => {
  const draft = transferDraftForContext(undefined, '/srv/browsed', '', '/home/user')
  transferDraftForContext(draft, '/srv/browsed-next', '', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/browsed-next')
  transferDraftForContext(draft, '/srv/browsed-next', '/srv/working', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/working')
  transferDraftForContext(draft, '/srv/browsed', '/srv/working', '/home/user')
  assert.equal(draft.uploadDirectory, '/srv/working')
})

test('submitted uploads keep their copied destination while a new upload uses the new terminal cwd', () => {
  const draft = transferDraftForContext(undefined, '/srv/release', '/srv/release', '')
  draft.paths.push('/local/dist')
  const emitted = createUploadRequest(draft.paths, ` ${draft.uploadDirectory} `)
  const submitted = createUploadRequest(emitted.paths, emitted.destination)

  transferDraftForContext(draft, '/srv/release', '/srv/next', '')
  draft.paths.push('/local/new-file')
  emitted.paths[0] = '/local/replaced'
  emitted.destination = '/srv/changed'
  const next = createUploadRequest(draft.paths, draft.uploadDirectory)

  assert.deepEqual(submitted, { paths: ['/local/dist'], destination: '/srv/release' })
  assert.deepEqual(next, { paths: ['/local/dist', '/local/new-file'], destination: '/srv/next' })

  transferDraftForContext(draft, '/srv/release', '/srv/final', '')
  draft.paths.length = 0
  assert.deepEqual(submitted, { paths: ['/local/dist'], destination: '/srv/release' })
  assert.deepEqual(next, { paths: ['/local/dist', '/local/new-file'], destination: '/srv/next' })
})
