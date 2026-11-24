// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// This module's stylesheets load for the whole of WittyMediaServer's web app, so a rule for one of the server's own
// Settings classes restyles every Settings page on the server, not only WittyPlay's. An old copy of the server's
// settings.css did that (wittywilla/WittyPlay#121): every page's title off to one side, the menu under the top bar.
// WittyPlay's settings pages use the server's classes as they are; a rule for one belongs in the server.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const web = fileURLToPath(new URL('.', import.meta.url))
const css = (d) => readdirSync(d).flatMap((f) => { const p = join(d, f); return statSync(p).isDirectory() ? css(p) : f.endsWith('.css') ? [p] : [] })

// The server's Settings frame: its menu, page, heads, strip of links and cards.
const HOST = /(^|[\s,>+~(])\.(set-(shell|nav|main|head|toc|card|back|help)|sm-(home|left|right|nav|row|rows|group-h))(?![\w-])/

test("no stylesheet restyles the server's Settings frame", () => {
  const found = []
  for (const f of css(web)) {
    const text = readFileSync(f, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
    for (const m of text.matchAll(/([^{}]+)\{/g)) {
      const sel = m[1].trim()
      if (sel.startsWith('@')) continue
      // A rule scoped to WittyPlay's own part of a page (inside one of its classes) is its own business.
      for (const part of sel.split(',')) if (HOST.test(part) && !/\.(wplay|wps|play|games?|gm)[\w-]*/.test(part)) found.push(`${f.slice(web.length)}: ${part.trim()}`)
    }
  }
  assert.deepEqual(found, [])
})
