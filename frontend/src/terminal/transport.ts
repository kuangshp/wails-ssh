export interface TerminalBackend {
  start(): Promise<void>
  resize(cols: number, rows: number): Promise<void>
  write(data: string): Promise<void>
}

/** Keep the PTY's size and input in the same order as the terminal UI. */
export class TerminalTransport {
  private tail: Promise<void> = Promise.resolve()
  private active = true
  private starting = false
  private started = false
  private requestedSize = ''
  private backend: TerminalBackend
  private onError: (error: unknown) => void

  constructor(backend: TerminalBackend, onError: (error: unknown) => void) {
    this.backend = backend
    this.onError = onError
  }

  start(cols: number, rows: number): Promise<void> {
    if (this.starting) return this.tail
    this.starting = true
    this.requestedSize = `${cols}:${rows}`
    const startup = this.enqueue(async () => {
      try {
        await this.backend.resize(cols, rows)
        if (!this.active) return
        await this.backend.start()
        this.started = this.active
      } catch (error) {
        this.active = false
        throw error
      }
    }, false)
    // A fit during Start must reach the PTY before startup is considered ready.
    // xterm's protocol replies during startup are queued here too, never dropped.
    return startup.then(() => this.tail)
  }

  resize(cols: number, rows: number): Promise<void> {
    if (!this.starting || !this.active || cols < 2 || rows < 1) return this.tail
    const size = `${cols}:${rows}`
    if (size === this.requestedSize) return this.tail
    this.requestedSize = size
    return this.enqueue(async () => {
      try { await this.backend.resize(cols, rows) }
      catch (error) { this.requestedSize = ''; throw error }
    })
  }

  write(data: string): Promise<void> {
    if (!this.starting || !this.active || !data) return this.tail
    return this.enqueue(async () => {
      if (this.started) await this.backend.write(data)
    })
  }

  dispose() { this.active = false }

  private enqueue(action: () => Promise<void>, report = true): Promise<void> {
    const operation = this.tail.then(() => this.active ? action() : undefined)
    this.tail = operation.catch(error => { if (this.active && report) this.onError(error) })
    return operation
  }
}
