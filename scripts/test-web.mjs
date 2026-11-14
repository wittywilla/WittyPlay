// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// Runs WittyPlay's browser tests, which feed simulated gamepads to the real code in
// plain Node: esbuild (a development dependency, as vite 8 no longer brings it) puts each
// together, and Node's own test runner runs it.
import { spawnSync } from 'node:child_process'
import { existsSync, readdirSync, realpathSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'

// The app's own parts a test uses (@wms/app/<path>, as the screens import them) come from a
// WittyMediaServer checkout: WITTYMEDIASERVER, else one beside this repository.
const here = fileURLToPath(new URL('..', import.meta.url))
const server = [process.env.WITTYMEDIASERVER, join(here, '../WittyMediaServer')].filter(Boolean).map((d) => resolve(d, 'web/src')).find((d) => existsSync(d))
const app = {
  name: 'wms-app',
  setup(b) {
    b.onResolve({ filter: /^@wms\/app\// }, (a) => {
      if (!server) return { errors: [{ text: `${a.path}: no WittyMediaServer checkout (set WITTYMEDIASERVER)` }] }
      const base = join(server, a.path.slice('@wms/app/'.length))
      const file = ['.ts', '.tsx', '/index.ts'].map((e) => base + e).find((f) => existsSync(f))
      return file ? { path: file } : { errors: [{ text: `${a.path}: not in ${server}` }] }
    })
  },
}

const dir = realpathSync(fileURLToPath(new URL('../web/tests/', import.meta.url))) + '/'
let failed = false
for (const f of readdirSync(dir).filter((n) => n.endsWith('.test.ts'))) {
  const out = dir + f.replace(/\.ts$/, '.tmp.mjs') // beside the test, as it finds its data from there
  try {
    await build({ entryPoints: [dir + f], outfile: out, bundle: true, format: 'esm', platform: 'node', logLevel: server ? 'warning' : 'silent', jsx: 'automatic', plugins: [app] })
  } catch (e) {
    // A test of a screen built from the app's parts needs the app's source. Without a
    // WittyMediaServer checkout (a module's own CI, before the server it is for is published)
    // it is skipped, and says so; beside a checkout, every test runs.
    if (!server) {
      console.log(`skipped ${f}: it uses the app's own parts, and there is no WittyMediaServer checkout (set WITTYMEDIASERVER)`)
      continue
    }
    throw e
  }
  const r = spawnSync(process.execPath, ['--test', '--test-timeout=30000', '--test-force-exit', out], { stdio: 'inherit' })
  rmSync(out, { force: true })
  if (r.status !== 0) failed = true
}
process.exit(failed ? 1 : 0)
