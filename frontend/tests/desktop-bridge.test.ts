import assert from 'node:assert/strict'
import test from 'node:test'
import { createDesktopBridge } from '../src/bridge/desktop.ts'

interface FixtureBackend {
  SaveProfile(profileId: number, secret: string): Promise<{ id: number }>
  CopyProfileCredentials(profileId: number): Promise<void>
}

test('a browser preview rejects desktop calls with an actionable message', async () => {
  const api = createDesktopBridge<FixtureBackend>(() => undefined)
  await assert.rejects(api.CopyProfileCredentials(7), /请在 云桥桌面应用中使用此功能/)
})

test('older bundles with a missing or non-function API explain the version mismatch', async () => {
  for (const backend of [{}, { SaveProfile: 'not a function' }]) {
    const api = createDesktopBridge<FixtureBackend>(() => backend)
    await assert.rejects(api.SaveProfile(7, 'fixture-secret'), (error: unknown) => {
      assert.ok(error instanceof Error)
      assert.match(error.message, /版本与页面不匹配.*SaveProfile/)
      assert.match(error.message, /重新安装最新版本/)
      assert.equal(error.message.includes('fixture-secret'), false)
      return true
    })
  }
})

test('calls preserve the receiver, argument values, and returned object', async () => {
  const result = { id: 7 }
  const backend = {
    marker: result,
    SaveProfile(profileId: number, secret: string) {
      assert.equal(this, backend)
      assert.deepEqual([profileId, secret], [7, 'fixture-secret'])
      return Promise.resolve(this.marker)
    },
  }
  const api = createDesktopBridge<FixtureBackend>(() => backend)
  assert.equal(await api.SaveProfile(7, 'fixture-secret'), result)
})

test('backend rejection reasons are preserved without wrapping or swallowing them', async () => {
  for (const reason of [new Error('fixture backend error'), 'fixture backend rejection']) {
    const api = createDesktopBridge<FixtureBackend>(() => ({ CopyProfileCredentials: () => Promise.reject(reason) }))
    await assert.rejects(api.CopyProfileCredentials(7), error => error === reason)
  }
})

test('a synchronous bridge failure is returned as the same rejected error', async () => {
  const reason = new Error('fixture synchronous bridge error')
  const api = createDesktopBridge<FixtureBackend>(() => ({ CopyProfileCredentials: () => { throw reason } }))
  let pending: Promise<void> | undefined
  assert.doesNotThrow(() => { pending = api.CopyProfileCredentials(7) })
  await assert.rejects(pending!, error => error === reason)
})

test('a cached API method resolves the current backend instead of retaining an old bundle', async () => {
  let backend: object | undefined
  const api = createDesktopBridge<FixtureBackend>(() => backend)
  const copy = api.CopyProfileCredentials
  await assert.rejects(copy(7), /桌面应用/)
  const calls: string[] = []
  backend = { CopyProfileCredentials: async (id: number) => { calls.push(`first:${id}`) } }
  await copy(7)
  backend = { CopyProfileCredentials: async (id: number) => { calls.push(`second:${id}`) } }
  await copy(8)
  assert.deepEqual(calls, ['first:7', 'second:8'])
})

test('the API proxy is not treated as a promise and does not request protocol properties', async () => {
  let reads = 0
  const api = createDesktopBridge<FixtureBackend>(() => { reads++; return {} })
  assert.equal(await Promise.resolve(api), api)
  assert.equal(Reflect.get(api, Symbol.toStringTag), undefined)
  assert.equal(reads, 0)
})
