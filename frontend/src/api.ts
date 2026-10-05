import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'
import type { model } from '../wailsjs/go/models'
import { createDesktopBridge } from './bridge/desktop'

export interface Profile {
  id: number; name: string; host: string; port: number; username: string;
  authKind: 'password' | 'private_key'; keyPath: string; groupName: string; remark: string;
  lastConnectedAt: string; osId: string; cpuCores: number; memoryBytes: number; diskBytes: number; hasSecret: boolean
}
export interface Connection { id: string; profileId: number; name: string; home: string }
export interface RemoteEntry { name: string; path: string; isDir: boolean; isSymlink: boolean; size: number; modifiedAt: string; mode: string }
export interface LocalEntry { name: string; path: string; isDir: boolean; isSymlink: boolean; size: number }
export interface LocalDirectory { path: string; parentPath: string; homePath: string; entries: LocalEntry[] }
export interface Directory { path: string; entries: RemoteEntry[] }
export interface Transfer { id: string; sessionId: string; direction: string; path: string; destination?: string; status: string; transferred: number; total: number; error?: string; attempt?: number; delaySeconds?: number; activeFiles?: number; filesTotal?: number; filesDone?: number; filesSkipped?: number }
export interface Conflict { id: string; sessionId: string; source: string; target: string }
export interface HostKey { host: string; port: number; key: string; fingerprint: string }
export type ConflictChoice = 'overwrite' | 'skip' | 'overwrite_all' | 'skip_all' | 'cancel'
type GeneratedBackend = typeof import('../wailsjs/go/main/App')
// Storage validates the supported authentication kinds. Keep the UI's narrower
// Profile type while deriving every method name and parameter from Wails.
type FrontendResult<T> = T extends model.Profile ? T & Pick<Profile, 'authKind'>
  : T extends Array<infer Item> ? FrontendResult<Item>[] : T
type Backend = {
  [Key in keyof GeneratedBackend]: GeneratedBackend[Key] extends (...args: infer Args) => Promise<infer Result>
    ? (...args: Args) => Promise<FrontendResult<Result>>
    : never
}
declare global { interface Window { go?: { main?: { App?: Backend } }; runtime?: unknown } }
export const isDesktop = () => !!window.go?.main?.App
export const api = createDesktopBridge<Backend>(() => window.go?.main?.App)
const channels = new Map<string, Set<(event: unknown) => void>>()
export function listen<T>(name: string, callback: (event: T) => void): () => void {
  if (!isDesktop()) return () => {}
  let callbacks = channels.get(name)
  if (!callbacks) {
    callbacks = new Set(); channels.set(name, callbacks)
    EventsOn(name, (event: unknown) => channels.get(name)?.forEach(fn => fn(event)))
  }
  const listener = (event: unknown) => callback(event as T)
  callbacks.add(listener)
  return () => {
    const set = channels.get(name); set?.delete(listener)
    if (set?.size === 0) { EventsOff(name); channels.delete(name) }
  }
}
export function errorText(error: unknown): string { return error instanceof Error ? error.message : String(error) }
export function formatBytes(n: number): string {
  if (!n) return '0 B'
  const index = Math.min(Math.floor(Math.log(n) / Math.log(1024)), 4)
  return `${(n / 1024 ** index).toFixed(index ? 1 : 0)} ${['B', 'KB', 'MB', 'GB', 'TB'][index]}`
}
export const joinPath = (directory: string, name: string) => `${directory.replace(/\/$/, '')}/${name}`
export const baseName = (path: string) => path.split(/[\\/]/).filter(Boolean).pop() || path
