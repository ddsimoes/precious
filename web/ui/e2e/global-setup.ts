import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import { mkdirSync, openSync, writeFileSync } from 'node:fs'
import { chmod, copyFile, mkdir, mkdtemp, realpath, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

import { adminPassword, bulkFiles, bulkFolders } from './env'

const uiDir = fileURLToPath(new URL('..', import.meta.url))
const repoRoot = fileURLToPath(new URL('../../..', import.meta.url))

// globalSetup builds the UI and the binary that embeds it, writes the
// regression corpus and a bulk folder (large enough for a scan's progress to
// be seen) into a temporary disk folder, configures a state directory there,
// sets the administrator password through the real interactive command, and
// starts `precious serve`. The returned function stops the server and
// removes everything; the server's log is kept as test-results/serve.log.
export default async function globalSetup(): Promise<() => Promise<void>> {
  // Canonical, as the server resolves the allowed roots and shows sources.
  const tmp = await realpath(await mkdtemp(join(tmpdir(), 'precious-e2e-')))
  const corpus = join(tmp, 'disk', 'corpus')
  let server: ChildProcess | undefined

  const teardown = async () => {
    if (server !== undefined && server.exitCode === null && server.signalCode === null) {
      const exited = once(server, 'exit')
      server.kill('SIGTERM')
      await exited
    }
    // The corpus's privado folder is mode 000 (unreadable on purpose).
    await chmod(join(corpus, 'privado'), 0o755).catch(() => {})
    await mkdir(join(uiDir, 'test-results'), { recursive: true })
    await copyFile(join(tmp, 'serve.log'), join(uiDir, 'test-results', 'serve.log')).catch(() => {})
    await rm(tmp, { recursive: true, force: true })
  }

  try {
    run('npm', ['run', 'build'], uiDir)
    const binary = join(tmp, 'precious')
    run('go', ['build', '-o', binary, './cmd/precious'], repoRoot)
    run('go', ['run', './tools/gencorpus', '-out', corpus], repoRoot)
    writeBulk(join(tmp, 'disk', 'bulk'))

    const port = await freePort()
    const origin = `http://127.0.0.1:${port}`
    const config = join(tmp, 'precious.toml')
    await writeFile(
      config,
      [
        `state_dir = ${JSON.stringify(join(tmp, 'state'))}`,
        '',
        '[server]',
        `listen = "127.0.0.1:${port}"`,
        `external_origin = "${origin}"`,
        'allow_insecure_http = true',
        '',
        '[sources]',
        `allowed_roots = [${JSON.stringify(join(tmp, 'disk'))}]`,
        '',
      ].join('\n'),
    )
    setPassword(binary, config)

    const log = openSync(join(tmp, 'serve.log'), 'w')
    server = spawn(binary, ['serve', '--config', config], { stdio: ['ignore', log, log] })
    await waitForSession(origin, server)

    process.env.PRECIOUS_E2E_ORIGIN = origin
    process.env.PRECIOUS_E2E_TRUTH = join(tmp, 'disk', 'ground_truth.json')
    process.env.PRECIOUS_E2E_CORPUS = corpus
  } catch (error) {
    await teardown()
    throw error
  }
  return teardown
}

function run(command: string, args: string[], cwd: string) {
  execFileSync(command, args, { cwd, stdio: ['ignore', 'inherit', 'inherit'] })
}

// writeBulk writes bulkFolders folders of bulkFiles 100-byte files each.
function writeBulk(dir: string) {
  const data = Buffer.alloc(100, 'x')
  for (let d = 0; d < bulkFolders; d++) {
    const folder = join(dir, `g${d % 40}`, `f${d}`)
    mkdirSync(folder, { recursive: true })
    for (let f = 0; f < bulkFiles; f++) {
      writeFileSync(join(folder, `file${f}.dat`), data)
    }
  }
}

// setPassword runs `precious admin set-password`, which reads the password
// only from a terminal, under a pseudo-terminal made by util-linux script(1),
// typing the password and its confirmation. The terminal's output, which
// echoes the typed-ahead input, is in the error only if the command fails.
function setPassword(binary: string, config: string) {
  const command = [binary, 'admin', 'set-password', '--config', config].map(shellQuote).join(' ')
  execFileSync('script', ['-qec', command, '/dev/null'], {
    input: `${adminPassword}\n${adminPassword}\n`,
    stdio: 'pipe',
    timeout: 30_000,
  })
}

function shellQuote(s: string): string {
  return `'${s.replaceAll("'", `'\\''`)}'`
}

async function freePort(): Promise<number> {
  const srv = createServer()
  const { promise, resolve, reject } = Promise.withResolvers<void>()
  srv.once('error', reject)
  srv.listen(0, '127.0.0.1', resolve)
  await promise
  const address = srv.address()
  srv.close()
  if (address === null || typeof address === 'string') {
    throw new Error('no port')
  }
  return address.port
}

async function waitForSession(origin: string, server: ChildProcess) {
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    if (server.exitCode !== null) {
      throw new Error(`precious serve exited with code ${server.exitCode}`)
    }
    try {
      const resp = await fetch(`${origin}/api/session`)
      if (resp.ok) {
        return
      }
    } catch {
      // Not listening yet.
    }
    await sleep(100)
  }
  throw new Error('precious serve did not answer /api/session within 30 s')
}
