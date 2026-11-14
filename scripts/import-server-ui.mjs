// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// Brings WittyPlay's screens out of WittyMediaServer's web app into this module (module API 5,
// WittyMediaServer docs/plans/MODULAR-MODULES.md, M1): each file is copied under web/app/ at
// the path it had in the app's web/src, so the files keep importing each other as they did. An
// import of anything of the app's that stays in the app becomes @wms/app/<path>, which WittyModule's
// builder turns into the app's own copy at run time. A stylesheet of the app's that a screen
// imports is copied too.
//
// Run once, at the move: from then on these files are WittyPlay's, and changed here.
//
//   node scripts/import-server-ui.mjs <WittyMediaServer checkout>
import { copyFileSync, existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, normalize, relative, resolve } from 'node:path'

const server = resolve(process.argv[2] || '../WittyMediaServer')
const src = join(server, 'web/src')
const out = resolve('web/app')

export const MOVE = [
  'shell/PlayChrome.tsx', 'shell/playPads.tsx', 'shell/playPaths.ts', 'shell/PlayReviewNotice.tsx', 'shell/MainGames.tsx',
  'shell/PlayRedirect.tsx', 'shell/GamePictureArt.tsx', 'shell/GameCase.tsx', 'shell/gameCaseShapes.tsx', 'shell/gamePictures.ts',
  'shell/brandLogos.ts', 'pages/PlayGame.tsx', 'pages/PlayConsoles.tsx', 'pages/PlayLibraryView.tsx', 'pages/PlaySettings.tsx',
  'pages/PlayPictures.tsx', 'pages/Games.tsx', 'pages/GameMeta.tsx', 'pages/GameReview.tsx', 'pages/settings/Gaming.tsx',
  'pages/settings/GamingCustom.tsx', 'pages/settings/RegionOrder.tsx', 'pages/settings/Emulators.tsx', 'pages/settings/GamesSupport.tsx',
  'sdui/render/GameBio.tsx', 'sdui/playModel.ts', 'tvmode/Games.tsx', 'pages/setup/Games.tsx',
]
// pages/games/*, without what belongs to WittyCouch and WittyRigs (they move with those modules).
import { readdirSync } from 'node:fs'
for (const f of readdirSync(join(src, 'pages/games'))) {
  if (/\.(tsx?|css)$/.test(f) && !f.includes('.test.') && !['Couch.tsx', 'VirtualGames.tsx'].includes(f)) MOVE.push('pages/games/' + f)
}
const moving = new Set(MOVE.map(normalize))
const shared = new Set()
const css = new Set()

function resolveFile(from, spec) {
  const p = normalize(join(dirname(from), spec))
  // Files first: a folder of another case (games, Games) is the same name on some systems.
  for (const c of [p + '.ts', p + '.tsx', p, join(p, 'index.ts'), join(p, 'index.tsx')]) {
    const f = join(src, c)
    if (existsSync(f) && statSync(f).isFile() && readdirSync(dirname(f)).includes(c.split('/').pop())) return c
  }
  return null
}

const IMPORT = /((?:import|export)\s[^'"]*?\sfrom\s+|import\s+|import\(\s*)(['"])(\.{1,2}\/[^'"]+)\2/g
for (const file of MOVE) {
  let text = readFileSync(join(src, file), 'utf8')
  text = text.replace(IMPORT, (all, head, q, spec) => {
    const target = resolveFile(file, spec)
    if (!target) throw new Error(`${file}: can't find ${spec}`)
    if (moving.has(target)) return all
    if (target.endsWith('.css')) { css.add(target); return all }
    if (target.endsWith('.json')) throw new Error(`${file} imports ${target}, which isn't moving`)
    const key = target.replace(/\.(tsx?)$/, '')
    shared.add(key)
    return head + q + '@wms/app/' + key + q
  })
  mkdirSync(dirname(join(out, file)), { recursive: true })
  writeFileSync(join(out, file), text)
}
for (const f of css) {
  mkdirSync(dirname(join(out, f)), { recursive: true })
  copyFileSync(join(src, f), join(out, f))
}
console.log(`copied ${MOVE.length} files and ${css.size} stylesheets into ${relative(process.cwd(), out)}; they use ${shared.size} parts of the app:\n  ${[...shared].sort().join('\n  ')}`)
