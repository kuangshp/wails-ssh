/** The small buffer surface needed to inspect echoed shell input, never raw keys. */
export interface HistoryBuffer {
  type: 'normal' | 'alternate'
  baseY: number
  cursorY: number
  cursorX: number
  length: number
  getLine(row: number): { isWrapped: boolean; translateToString(trim?: boolean, start?: number, end?: number): string } | undefined
}
export interface HistoryMarker { line: number; isDisposed: boolean; dispose(): void }
interface EchoLine { text: string; start: number; end: number }
const MAX_COMMAND_LENGTH = 4096
const SUBMISSION_TIMEOUT = 5000

/** xterm.write is asynchronous; a close event must follow already queued echo. */
export function afterTerminalDrain(write: (data: string, done: () => void) => void, isCurrent: () => boolean, finish: () => void) {
  write('', () => { if (isCurrent()) finish() })
}

function logicalLine(buffer: HistoryBuffer, start: number): EchoLine | undefined {
  const first = buffer.getLine(start)
  if (!first || first.isWrapped) return
  let end = start
  let text = ''
  while (end < buffer.length) {
    const line = buffer.getLine(end)
    if (!line) return
    const wraps = !!buffer.getLine(end + 1)?.isWrapped
    // xterm trims unused cells, preserving actual printed spaces. A wide CJK
    // character can wrap early and leave one unused cell at the previous edge.
    text += line.translateToString(true)
    if (text.length > MAX_COMMAND_LENGTH + 1024) return
    if (!wraps) return { text, start, end }
    end++
  }
}

function cursorLine(buffer: HistoryBuffer): EchoLine | undefined {
  let row = buffer.baseY + buffer.cursorY
  while (row > 0 && buffer.getLine(row)?.isWrapped) row--
  return logicalLine(buffer, row)
}

function cursorAtEnd(buffer: HistoryBuffer, line: EchoLine): boolean {
  const row = buffer.baseY + buffer.cursorY
  return row === line.end && !buffer.getLine(row)?.translateToString(true, buffer.cursorX)
}

/** Require a recognizable, empty shell prompt before trusting subsequent echo. */
function idlePrompt(text: string): boolean {
  return /^(?:[^\s@]+@[^\s:]+:(?:\/[^#$%>]*|~(?:\/[^#$%>]*)?)|\[[^\s@]+@[^\s]+ [^\]#$%>]+\])[#$%>]\s*$/.test(text)
}

function echoedCommand(line: EchoLine, prefix: string): string | undefined {
  if (line.text === prefix.trimEnd()) return ''
  return line.text.startsWith(prefix) ? line.text.slice(prefix.length) : undefined
}

export function safeHistoryCommand(command: string): boolean {
  // A leading space is a familiar shell opt-out from history (HISTCONTROL).
  return !!command.trim() && !/^\s/.test(command) && command.length <= MAX_COMMAND_LENGTH && !/[\u0000-\u001f\u007f]/.test(command)
}

/** History arrives newest first. Completion is deliberately prefix-only and case-sensitive. */
export function historyMatches(history: readonly string[], query: string, limit = 5): string[] {
  if (!safeHistoryCommand(query)) return []
  return [...new Set(history)].filter(command => safeHistoryCommand(command) && command !== query && command.startsWith(query)).slice(0, limit)
}

export interface HistoryKey { key: string; type: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; altKey: boolean; isComposing?: boolean }
export function historyKeyAction(event: HistoryKey, state: { ready: boolean; visible: boolean; hasMatch: boolean; dismissed: boolean }): 'complete' | 'dismiss' | 'history' | 'pass' {
  if (event.type !== 'keydown' || event.isComposing || !state.visible || !state.ready) return 'pass'
  if (event.key.toLowerCase() === 'h' && event.shiftKey && (event.ctrlKey || event.metaKey) && !event.altKey) return 'history'
  if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey || state.dismissed || !state.hasMatch) return 'pass'
  if (event.key === 'Tab') return 'complete'
  if (event.key === 'Escape') return 'dismiss'
  return 'pass'
}

/**
 * Arm only at an idle shell prompt. On Enter, keep an xterm marker until the
 * server has echoed the complete line and advanced past it. This also handles
 * typing + Enter before the first echo arrives, edits, paste, and wrapped CJK.
 * Password prompts and alternate screens never create an anchor. Uncertain
 * cases are discarded instead of reconstructing a command from keystrokes.
 */
export class ShellHistoryTracker {
  private anchor?: { marker: HistoryMarker; prefix: string }
  private submittedAt?: number
  private waitingForEcho?: string

  reset() {
    this.anchor?.marker.dispose()
    this.anchor = undefined
    this.submittedAt = undefined
    this.waitingForEcho = undefined
  }

  private fingerprint(buffer: HistoryBuffer): string {
    return `${buffer.baseY + buffer.cursorY}:${buffer.cursorX}:${cursorLine(buffer)?.text ?? ''}`
  }

  input(data: string, buffer: HistoryBuffer, now = Date.now()) {
    if (!this.anchor) return
    if (buffer.type !== 'normal' || this.submittedAt !== undefined || /[\x03\x04\x0c]/.test(data)) { this.reset(); return }
    const line = this.anchor.marker.isDisposed ? undefined : logicalLine(buffer, this.anchor.marker.line)
    const cursor = buffer.baseY + buffer.cursorY
    if (!line || cursor < line.start || cursor > line.end) { this.reset(); return }
    if (data === '\r' || data === '\n') { this.submittedAt = now; return }
    // Multi-line paste may execute several commands or answer an interactive
    // prompt. Do not attempt to infer boundaries or save any of that input.
    if (/[\r\n]/.test(data)) { this.reset(); return }
    this.waitingForEcho = this.fingerprint(buffer)
  }

  /** undefined means history UI must not modify this terminal's current input. */
  query(buffer: HistoryBuffer): string | undefined {
    if (buffer.type !== 'normal' || this.submittedAt !== undefined || this.waitingForEcho !== undefined) return
    const anchor = this.anchor
    if (!anchor || anchor.marker.isDisposed) return
    const line = logicalLine(buffer, anchor.marker.line)
    if (!line || !cursorAtEnd(buffer, line)) return
    return echoedCommand(line, anchor.prefix)
  }

  observe(buffer: HistoryBuffer, marker: (offset: number) => HistoryMarker | undefined, now = Date.now(), final = false): string | undefined {
    if (buffer.type !== 'normal') { this.reset(); return }
    let command: string | undefined
    if (this.anchor) {
      const anchor = this.anchor
      const line = anchor.marker.isDisposed ? undefined : logicalLine(buffer, anchor.marker.line)
      const echoed = line && echoedCommand(line, anchor.prefix)
      const echoEnded = !!line && buffer.baseY + buffer.cursorY > line.end
      if (!line || echoed === undefined || this.submittedAt !== undefined && !echoEnded && now - this.submittedAt > SUBMISSION_TIMEOUT) this.reset()
      else if (this.submittedAt !== undefined && buffer.baseY + buffer.cursorY > line.end) {
        const following = logicalLine(buffer, line.end + 1)
        const continuation = !!following && /^>(?:\s|$)/.test(following.text)
        // A newline and PS2 can arrive in separate chunks. A lone empty next
        // line is not yet evidence that the shell accepted a complete command.
        const awaitingPrompt = following && !following.text.trim() && buffer.baseY + buffer.cursorY === following.end
        if (continuation || final || !awaitingPrompt) {
          if (!continuation && safeHistoryCommand(echoed)) command = echoed
          this.reset()
        }
      } else if (this.submittedAt === undefined && buffer.baseY + buffer.cursorY > line.end) {
        this.reset()
      } else if (this.waitingForEcho !== undefined && this.fingerprint(buffer) !== this.waitingForEcho) this.waitingForEcho = undefined
    }
    if (!this.anchor) {
      const line = cursorLine(buffer)
      if (line && cursorAtEnd(buffer, line) && idlePrompt(line.text)) {
        const promptMarker = marker(line.start - buffer.baseY - buffer.cursorY)
        if (promptMarker) {
          // translateToString(true) strips the prompt's trailing space. Recover
          // its actual echoed cells so a user's leading space stays an opt-out.
          const last = buffer.getLine(line.end)!
          const suffix = last.translateToString(false, 0, buffer.cursorX)
          const prefix = line.text.slice(0, line.text.length - last.translateToString(true).length) + suffix
          this.anchor = { marker: promptMarker, prefix }
        }
      }
    }
    return command
  }
}
