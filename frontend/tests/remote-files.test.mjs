import test from 'node:test'
import assert from 'node:assert/strict'
import { buildRemoteTree, remoteAncestors, remoteParent, syncDownloadPath, transferFinished, transferRetryLabel, transferStatus } from '../src/remote-files.ts'

const entry = (path, isDir = true) => ({ path, name: path.split('/').pop(), isDir, isSymlink: false, size: 0, modifiedAt: '', mode: '755' })

test('directory tree preserves ancestor siblings and expands only the selected branch', () => {
  const listings = new Map([
    ['/', [entry('/var'), entry('/home')]],
    ['/home', [entry('/home/b'), entry('/home/a')]],
    ['/home/a', [entry('/home/a/z.txt', false), entry('/home/a/src')]],
  ])
  const rows = buildRemoteTree('/home/a', listings, new Set())
  assert.deepEqual(rows.map(row => [row.entry.path, row.depth, row.expanded, row.current]), [
    ['/home', 0, true, false], ['/home/a', 1, true, true],
    ['/home/a/src', 2, false, false], ['/home/a/z.txt', 2, false, false],
    ['/home/b', 1, false, false], ['/var', 0, false, false],
  ])
  assert.deepEqual(buildRemoteTree('/home/a', listings, new Set(['/home'])).map(row => row.entry.path), ['/home', '/var'])
})

test('a listing is still usable when a parent directory cannot be read', () => {
  const listings = new Map([
    ['/', [entry('/restricted')]],
    ['/restricted/home', [entry('/restricted/home/file', false)]],
  ])
  assert.deepEqual(buildRemoteTree('/restricted/home', listings, new Set()).map(row => row.entry.path), ['/restricted/home/file'])
})

test('remote root and nested paths are normalized without introducing duplicate separators', () => {
  assert.deepEqual(remoteAncestors('/'), ['/'])
  assert.deepEqual(remoteAncestors('/home/app/'), ['/', '/home', '/home/app'])
  assert.equal(remoteParent('/home/app/'), '/home')
  assert.equal(remoteParent('/home'), '/')
  assert.equal(remoteParent('/'), '/')
})

test('directory changes follow a default download source and preserve an explicitly selected file', () => {
  assert.equal(syncDownloadPath('', '', '/home'), '/home')
  assert.equal(syncDownloadPath('/home', '/home', '/home/app'), '/home/app')
  assert.equal(syncDownloadPath('/home/archive.tar.gz', '/home', '/tmp'), '/home/archive.tar.gz')
  assert.equal(syncDownloadPath('/home', '/home', ''), '/home')
})

test('a retry remains an active transfer with a readable delay and attempt', () => {
  assert.equal(transferFinished('retrying'), false)
  assert.equal(transferStatus('retrying'), '等待重试')
  assert.equal(transferRetryLabel({ attempt: 3, delaySeconds: 6 }), '6 秒后第 3 次重试')
  assert.equal(transferRetryLabel({}), '自动重试')
})
