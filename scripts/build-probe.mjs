#!/usr/bin/env node
// Bundles the test page for wittyplay-probe, which embeds what this writes. Run it after
// changing web/transport.ts or web/latency.ts, with esbuild from any checkout of the server:
//
//   ESBUILD=D:/projects/WittyMediaServer/web/node_modules/esbuild/lib/main.js node scripts/build-probe.mjs
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(pathToFileURL(join(root, 'x.js')))
const esbuild = require(process.env.ESBUILD || 'esbuild')
await esbuild.build({
  entryPoints: [join(root, 'cmd/wittyplay-probe/page/probe.ts')],
  outfile: join(root, 'cmd/wittyplay-probe/page/probe.js'),
  bundle: true,
  format: 'iife',
  target: 'es2022',
  logLevel: 'info',
})
