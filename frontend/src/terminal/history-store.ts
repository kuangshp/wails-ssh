export interface HistoryBackend {
  load(profileId: number): Promise<string[]>
  save(profileId: number, command: string): Promise<void>
}

function singleLine(command: string): string | undefined {
  if (!command || /^\s/.test(command) || /[\x00-\x1f\x7f]/.test(command) || command.length > 65536) return
  return command.trimEnd() || undefined
}

/** Serialize persistence per host so an older load cannot replace a new command. */
export class CommandHistoryCache {
  private entries = new Map<number, string[]>()
  private queues = new Map<number, Promise<unknown>>()
  private backend: HistoryBackend
  private changed: (profileId: number, commands: string[]) => void

  constructor(backend: HistoryBackend, changed: (profileId: number, commands: string[]) => void) {
    this.backend = backend
    this.changed = changed
  }

  load(profileId: number, refresh = false): Promise<void> {
    return this.enqueue(profileId, async () => {
      if (!refresh && this.entries.has(profileId)) return
      const commands = (await this.backend.load(profileId) || []).map(singleLine).filter((value): value is string => !!value)
      this.publish(profileId, [...new Set(commands)].slice(0, 200))
    })
  }

  record(profileId: number, value: string): Promise<void> {
    const command = singleLine(value)
    if (!command) return Promise.resolve()
    return this.enqueue(profileId, async () => {
      await this.backend.save(profileId, command)
      this.publish(profileId, [command, ...(this.entries.get(profileId) || []).filter(item => item !== command)].slice(0, 200))
    })
  }

  private publish(profileId: number, commands: string[]) {
    this.entries.set(profileId, commands)
    this.changed(profileId, commands)
  }

  private enqueue(profileId: number, action: () => Promise<void>): Promise<void> {
    const pending = (this.queues.get(profileId) || Promise.resolve()).catch(() => {}).then(action)
    this.queues.set(profileId, pending)
    // Retain only pending operations; failures must not block later history writes.
    void pending.then(() => this.release(profileId, pending), () => this.release(profileId, pending))
    return pending
  }

  private release(profileId: number, pending: Promise<unknown>) {
    if (this.queues.get(profileId) === pending) this.queues.delete(profileId)
  }
}
