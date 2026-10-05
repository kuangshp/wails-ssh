// Run with: node reports/terminal-backpressure-probe-2026-10-05.js
// Deterministic parser-queue limit probe; this does not measure browser/SSH throughput.
// Reuse a read-only zero-filled chunk to keep physical allocation small while
// queuing logical pending bytes before the event loop can drain xterm writes.
import xterm from '../frontend/node_modules/@xterm/xterm/lib/xterm.js';

const term = new xterm.Terminal({ cols: 100, rows: 30, scrollback: 10000 });
const chunk = new Uint8Array(64 * 1024);
let accepted = 0;
let failure = '';
for (let index = 0; index < 800; index++) {
  try { term.write(chunk); accepted += chunk.length; }
  catch (error) { failure = error.message; break; }
}
console.log(JSON.stringify({
  scenario: 'local synchronous output burst, no browser or SSH',
  acceptedBytesBeforeError: accepted,
  error: failure,
}));
term.dispose();
// Exit before queued parsing: only the deterministic enqueue boundary is under test.
process.exit(failure ? 0 : 1);
