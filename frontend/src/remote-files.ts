import type { RemoteEntry } from './api'

export type RemoteActionKind = 'file' | 'folder' | 'rename' | 'chmod' | 'delete' | 'archive' | 'extract'
export interface RemoteAction { kind: RemoteActionKind; entry: RemoteEntry; parentPath: string }
export interface RemoteTreeNode { entry: RemoteEntry; depth: number; expanded: boolean; current: boolean }

export function remoteParent(path: string): string {
  const normalized = path.replace(/\/+$/, '')
  return normalized.slice(0, normalized.lastIndexOf('/')) || '/'
}

export function remoteAncestors(path: string): string[] {
  if (!path.startsWith('/')) return [path || '.']
  const paths = ['/']
  for (const part of path.split('/').filter(Boolean)) paths.push(`${paths.at(-1) === '/' ? '' : paths.at(-1)}/${part}`)
  return paths
}

export function sortRemoteEntries(entries: RemoteEntry[]): RemoteEntry[] {
  return [...entries].sort((a, b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name))
}

// The old desktop tree expands only the selected directory and its ancestors.
// Keep siblings visible, rather than replacing the tree with a flat directory listing.
export function buildRemoteTree(currentPath: string, listings: ReadonlyMap<string, RemoteEntry[]>, collapsed: ReadonlySet<string>): RemoteTreeNode[] {
  const ancestors = remoteAncestors(currentPath)
  const nodes: RemoteTreeNode[] = []
  function append(path: string, depth: number) {
    for (const entry of sortRemoteEntries(listings.get(path) || [])) {
      const current = entry.path === currentPath
      const onBranch = ancestors.includes(entry.path)
      const expanded = entry.isDir && (current || onBranch) && !collapsed.has(entry.path)
      nodes.push({ entry, depth, expanded, current })
      if (expanded) append(entry.path, depth + 1)
    }
  }
  // If an ancestor is unreadable, use the first complete readable suffix so the
  // current directory remains accessible even when a higher listing succeeded.
  const root = ancestors.find((_, index) => ancestors.slice(index).every(path => listings.has(path))) || currentPath
  append(root, 0)
  return nodes
}

export function canExtractArchive(entry: RemoteEntry): boolean {
  return !entry.isDir && /\.(tar|tar\.gz|tgz|tar\.bz2|tbz2|tar\.xz|txz|zip|gz)$/i.test(entry.name)
}

export const transferFinished = (status: string) => ['completed', 'failed', 'cancelled', 'canceled', 'skipped'].includes(status)
export const transferStatus = (status: string) => ({ queued: '等待中', preparing: '正在准备文件', running: '传输中', retrying: '等待重试', pending: '等待中', in_progress: '传输中', conflict: '等待处理冲突', waiting: '等待处理冲突', completed: '已完成', skipped: '已跳过', failed: '失败', cancelling: '正在停止', cancelled: '已停止', canceled: '已停止' }[status] || status)

export function transferRetryLabel(item: { attempt?: number; delaySeconds?: number }): string {
  const attempt = item.attempt && item.attempt > 0 ? `第 ${item.attempt} 次重试` : '自动重试'
  return item.delaySeconds && item.delaySeconds > 0 ? `${item.delaySeconds} 秒后${attempt}` : attempt
}

export function syncDownloadPath(selected: string, previousDirectory: string, nextDirectory: string): string {
  return nextDirectory.trim() && (!selected.trim() || selected === previousDirectory) ? nextDirectory : selected
}
