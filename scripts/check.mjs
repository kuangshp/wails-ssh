import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../', import.meta.url))
const frontend = fileURLToPath(new URL('../frontend/', import.meta.url))

function run(command, args = [], { cwd = root, capture = false } = {}) {
  console.log(`\n> ${command} ${args.join(' ')}`)
  // npm is a .cmd script on Windows. These arguments are fixed below, never
  // user input; Go and Git still run directly, including paths with spaces.
  const windowsNpm = process.platform === 'win32' && command === 'npm'
  const result = spawnSync(
    windowsNpm ? (process.env.ComSpec || 'cmd.exe') : command,
    windowsNpm ? ['/d', '/s', '/c', `npm ${args.join(' ')}`] : args,
    { cwd, stdio: capture ? 'pipe' : 'inherit', encoding: 'utf8' },
  )
  if (result.error) throw result.error
  if (result.status !== 0) {
    if (capture) process.stderr.write(result.stderr || result.stdout || '')
    throw new Error(`${command} failed (${result.signal || result.status})`)
  }
  return result.stdout || ''
}

function checkFrontend() {
  run('npm', ['ci'], { cwd: frontend })
  run('npm', ['test'], { cwd: frontend })
  // The build script runs vue-tsc before Vite and creates Go's embedded assets.
  run('npm', ['run', 'build'], { cwd: frontend })
}

function checkFormat() {
  const files = run('git', ['ls-files', '--cached', '--others', '--exclude-standard', '-z', '--', '*.go'], { capture: true })
    .split('\0').filter(Boolean)
  let unformatted = ''
  // Bound command length for Windows; include new source files before commit.
  for (let index = 0; index < files.length; index += 50) {
    unformatted += run('gofmt', ['-l', ...files.slice(index, index + 50)], { capture: true })
  }
  if (unformatted.trim()) {
    throw new Error(`Go files need gofmt (no files were modified):\n${unformatted}`)
  }
}

try {
  if (Number(process.versions.node.split('.')[0]) < 24) {
    throw new Error('Node.js 24 or newer is required; see .node-version.')
  }
  const mode = process.argv[2] || 'check'
  if (process.argv.length > 3 || !['check', 'frontend', 'format'].includes(mode)) {
    throw new Error('Usage: node scripts/check.mjs [check|frontend|format]')
  }
  if (mode === 'format') {
    checkFormat()
  } else {
    if (mode === 'check') {
      // Archive safety tests otherwise silently skip on a machine without Python.
      run('python3', ['--version'])
    }
    checkFrontend()
    if (mode === 'check') {
      checkFormat()
      run('go', ['test', '-race', './...'])
      run('go', ['vet', './...'])
    }
  }
  console.log(`\n${mode}: passed`)
} catch (error) {
  console.error(`\nCheck failed: ${error instanceof Error ? error.message : error}`)
  process.exitCode = 1
}
