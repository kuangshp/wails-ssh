import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime'

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
interface Backend {
  ListProfiles(): Promise<Profile[]>; SaveProfile(profile: Profile, secret: string, keepSecret: boolean): Promise<Profile>; DeleteProfile(id: number): Promise<void>;
  ListGroups(): Promise<string[]>; CreateGroup(name: string): Promise<void>; ImportSSHConfig(): Promise<Profile[]>; ImportLegacyDatabase(): Promise<number>; PickPrivateKey(): Promise<string>;
  TestConnection(profile: Profile, secret: string, keepSecret: boolean): Promise<void>; CopyProfileSecret(profileId: number): Promise<void>;
  RevealProfileSecret(profileId: number): Promise<string>; CopyProfileCredentials(profileId: number): Promise<void>;
  Connect(profileId: number, secret: string, cols: number, rows: number): Promise<Connection>; StartTerminal(sessionId: string): Promise<void>; Disconnect(sessionId: string): Promise<void>;
  WriteTerminal(sessionId: string, data: string): Promise<void>; ResizeTerminal(sessionId: string, cols: number, rows: number): Promise<void>; TrustHost(host: string, port: number, key: string): Promise<void>;
  ListRemote(sessionId: string, path: string): Promise<Directory>; CreateRemote(sessionId: string, path: string, isDir: boolean): Promise<void>; RenameRemote(sessionId: string, oldPath: string, newPath: string): Promise<void>;
  ChmodRemote(sessionId: string, path: string, mode: string): Promise<void>; DeleteRemote(sessionId: string, path: string): Promise<void>;
  CompressRemote(sessionId: string, path: string): Promise<string>; ExtractRemote(sessionId: string, path: string): Promise<string>;
  PickUploadSources(): Promise<{ native: boolean; entries: LocalEntry[] }>; ListLocalDirectory(directory: string): Promise<LocalDirectory>; PickDownloadDirectory(): Promise<string>;
  Upload(sessionId: string, sources: string[], remoteDir: string): Promise<string>; Download(sessionId: string, remotePath: string, localDir: string): Promise<string>;
  CancelTransfer(id: string): Promise<void>; ResolveConflict(id: string, choice: ConflictChoice): Promise<void>;
  CommandHistory(profileId: number): Promise<string[]>; RecordCommand(profileId: number, command: string): Promise<void>; GetAppInfo(): Promise<{ version: string; dataDir: string }>;
}
declare global { interface Window { go?: { main?: { App?: Backend } }; runtime?: unknown } }
export const isDesktop = () => !!window.go?.main?.App
export const api = new Proxy({} as Backend, {
  get: (_, key: keyof Backend) => (...args: unknown[]) => {
    const bridge = window.go?.main?.App
    if (!bridge) return Promise.reject(new Error('请在 云桥桌面应用中使用此功能。浏览器仅提供界面预览。'))
    const fn = bridge[key] as (...args: unknown[]) => Promise<unknown>
    return fn(...args)
  },
})
const channels = new Map<string, Set<(event: any) => void>>()
export function listen<T>(name: string, callback: (event: T) => void): () => void {
  if (!isDesktop()) return () => {}
  let callbacks = channels.get(name)
  if (!callbacks) {
    callbacks = new Set(); channels.set(name, callbacks)
    EventsOn(name, (event: T) => channels.get(name)?.forEach(fn => fn(event)))
  }
  callbacks.add(callback)
  return () => {
    const set = channels.get(name); set?.delete(callback)
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
