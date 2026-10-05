import assert from 'node:assert/strict'
import test from 'node:test'
import { useProfiles } from '../src/features/profiles/useProfiles.ts'
import type { Profile } from '../src/api.ts'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function fixture() {
  const requests = Array.from({ length: 2 }, () => ({ profiles: deferred<Profile[]>(), groups: deferred<string[]>() }))
  let profilesIndex = 0, groupsIndex = 0
  const errors: unknown[] = []
  const library = useProfiles({
    ListProfiles: () => requests[profilesIndex++].profiles.promise,
    ListGroups: () => requests[groupsIndex++].groups.promise,
  }, error => errors.push(error))
  return { requests, errors, library }
}

const profile = (name: string): Profile => ({
  id: 1, name, host: 'example.invalid', port: 22, username: 'fixture', authKind: 'password',
  keyPath: '', groupName: '', remark: '', lastConnectedAt: '', osId: '', cpuCores: 0,
  memoryBytes: 0, diskBytes: 0, hasSecret: false,
})

test('late older refresh cannot overwrite the latest profiles and groups', async () => {
  const { library, requests } = fixture()
  const old = library.refresh(), current = library.refresh()
  requests[1].profiles.resolve([profile('new')]); requests[1].groups.resolve(['new'])
  await current
  requests[0].profiles.resolve([profile('old')]); requests[0].groups.resolve(['old'])
  await old
  assert.equal(library.profiles.value[0].name, 'new')
  assert.deepEqual(library.groups.value, ['new'])
  assert.equal(library.pageLoading.value, false)
})

test('obsolete failure cannot end the current loading state or show an error', async () => {
  const { library, requests, errors } = fixture()
  const old = library.refresh(), current = library.refresh()
  requests[0].profiles.reject(new Error('obsolete')); requests[0].groups.resolve([])
  await old
  assert.equal(library.pageLoading.value, true)
  assert.deepEqual(errors, [])
  requests[1].profiles.resolve([profile('new')]); requests[1].groups.resolve(['new'])
  await current
  assert.equal(library.pageLoading.value, false)
})

test('current partial failure retains the prior consistent profiles and groups', async () => {
  const { library, requests, errors } = fixture()
  const first = library.refresh()
  requests[0].profiles.resolve([profile('before')]); requests[0].groups.resolve(['before'])
  await first
  const next = library.refresh()
  const failure = new Error('groups unavailable')
  requests[1].profiles.resolve([profile('after')]); requests[1].groups.reject(failure)
  await next
  assert.equal(library.profiles.value[0].name, 'before')
  assert.deepEqual(library.groups.value, ['before'])
  assert.deepEqual(errors, [failure])
  assert.equal(library.pageLoading.value, false)
})

test('disposing the owner clears data and discards pending results and further requests', async () => {
  const { library, requests, errors } = fixture()
  const pending = library.refresh()
  library.dispose()
  requests[0].profiles.resolve([profile('late')]); requests[0].groups.resolve(['late'])
  await pending
  await library.refresh()
  assert.deepEqual(library.profiles.value, [])
  assert.deepEqual(library.groups.value, [])
  assert.equal(library.pageLoading.value, false)
  assert.deepEqual(errors, [])
})

test('browser preview does not attempt desktop calls', async () => {
  const library = useProfiles({ ListProfiles: async () => { throw new Error('unexpected call') }, ListGroups: async () => [] }, assert.fail, () => false)
  await library.refresh()
  assert.equal(library.pageLoading.value, false)
})
