// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// WittyPlay's spec pages (web/app/spec/specs): the games library's filters, a library's review, fixing a game,
// a console's cores and the emulator choices, each checked with the app's own spec model (@wms/app/spec/model,
// from the server checkout scripts/test-web.mjs finds).
import test from 'node:test'
import assert from 'node:assert/strict'
import { handledIds, problems, reachable, withIds } from '@wms/app/spec/model'
import { gameFiltersSpec, letterSpec, splitFilterId } from '../app/spec/specs/gameLibrary'
import { bulkGroups, kindWord, reviewSpec } from '../app/spec/specs/gameReview'
import { coresSpec } from '../app/spec/specs/consoleCores'
import { emulatorChoicesSpec } from '../app/spec/specs/emulatorChoices'
import { manualSpec } from '../app/spec/specs/manualEmulator'
import { fixGameSpec, versionLinksSpec } from '../app/spec/specs/fixgame'

const ok = (p: any) => assert.deepEqual(problems(withIds(p)), [], p.key) // eslint-disable-line @typescript-eslint/no-explicit-any
const ids = (p: any): string[] => p.sections.flatMap((s: any) => (s.items || []).map((i: any) => i.id)).concat(p.actions.map((a: any) => a.id)) // eslint-disable-line @typescript-eslint/no-explicit-any


test('game filters: sort first, empty groups left out, chosen value on', () => {
  const p = gameFiltersSpec('year', [{ id: 'title', label: 'Title' }, { id: 'year', label: 'Year' }], [
    { name: 'genre', title: 'Genre', options: [{ value: 'RPG', label: 'RPG (3)' }] }, { name: 'series', title: 'Series', options: [] },
  ], { genre: 'RPG' })
  assert.deepEqual(p.sections.map((s) => s.h), ['Sort', 'Genre'])
  assert.deepEqual(p.sections[0].items.map((c) => c.on), [false, true])
  assert.equal(p.sections[1].items[0].on, true)
  assert.deepEqual(problems(withIds(p)), [])
})

test('filter ids split at the first colon', () => {
  assert.deepEqual(splitFilterId('genre:Action: Adventure'), ['genre', 'Action: Adventure'])
  assert.deepEqual(splitFilterId('sort:title'), ['sort', 'title'])
})

test('letters: a chip per letter carrying its offset', () => {
  const p = letterSpec([{ label: 'A', offset: 0, count: 5 }, { label: 'B', offset: 5, count: 2 }])
  assert.deepEqual([...handledIds(withIds(p))], ['letter:0', 'letter:5'])
})

const files = [
  { id: 1, path: '/g/a.bin', name: 'a.bin', kind: 'game', reason: 'No header', suggest: [{ console: 'snes', short: 'SNES' }] },
  { id: 2, path: '/g/b.bin', name: 'b.bin', kind: 'game', reason: 'No header', suggest: [{ console: 'snes', short: 'SNES' }] },
  { id: 3, path: 'c.zip', name: 'c.zip', kind: 'duplicate', reason: 'Same as a.bin', suggest: [] },
  { id: 4, path: '/g/d', name: 'd', kind: 'bios', reason: 'A BIOS', suggest: [], itemId: 9 },
]
const consoles = [{ id: 'snes', name: 'Super Nintendo' }, { id: 'nes', name: 'NES' }]

test('review: needs tab has bulk chips and per-file choices; duplicates have none', () => {
  const p = reviewSpec('needs', files, { busy: false, consoles })
  assert.deepEqual(problems(withIds(p)), [])
  assert.equal(p.sections[0].h, 'Many at once')
  assert.deepEqual(p.sections[0].items.map((c) => c.id), ['bulk.snes'])
  const rows = p.sections[1].items
  assert.deepEqual(rows[0].actions.map((a) => a.id), ['add.1', 'other.1', 'ignore.1'])
  assert.equal(rows[2].actions, undefined)
  assert.equal(rows[1].sub, '/g/b.bin · No header')
  assert.equal(rows[2].sub, 'Same as a.bin')
})

test('review: other tabs and the console choice', () => {
  const left = reviewSpec('left', files.slice(3), { busy: true, consoles })
  assert.equal(left.sections[0].items[0].value, 'BIOS')
  assert.equal(left.sections[0].items[0].actions.every((a) => a.disabled), true)
  const blocked = reviewSpec('blocked', files.slice(3), { busy: false, consoles })
  assert.equal(blocked.sections[0].items[0].to, '/item/9')
  const ig = reviewSpec('ignored', files, { busy: false, consoles })
  assert.deepEqual(ig.sections[0].items[0].actions.map((a) => a.id), ['forget.1'])
  const pick = reviewSpec('needs', files, { busy: false, consoles, choosing: 2 })
  const last = pick.sections.at(-1)
  assert.equal(last.h, 'Console for b.bin')
  assert.deepEqual(last.items.map((c) => c.id), ['pick.2.snes', 'pick.2.nes', 'closepick'])
  assert.equal(pick.sections[1].items[1].on, true)
  assert.equal(kindWord('dlc'), 'Extra content')
  assert.equal(kindWord('weird'), 'weird')
})

test('review: bulk groups need more than one file', () => {
  assert.deepEqual(bulkGroups(files).map((g) => [g.console, g.ids]), [['snes', [1, 2]]])
  assert.deepEqual(bulkGroups(files.slice(0, 1)), [])
})

test('console cores: a row per core with where it runs', () => {
  const p = coresSpec('SNES', [{ id: 'a', name: 'Alpha', installed: true, here: 'drawn on the GPU.', runs: 'Runs on Linux.' }, { id: 'b', name: 'Beta', installed: false, here: 'not published.', runs: 'No server platform yet.' }])
  assert.equal(p.title, 'Where SNES games run')
  const rows = p.sections[0].items
  assert.deepEqual([rows[0].value, rows[0].on, rows[1].value], ['Installed', true, undefined])
  assert.equal(rows[0].sub, 'On this server: drawn on the GPU. Runs on Linux.')
  assert.deepEqual(problems(withIds(p)), [])
})

test('emulator choices: ready ones link, others say why; settings are actions', () => {
  const p = emulatorChoicesSpec([{ id: 'x', name: 'X', ready: true }, { id: 'y', name: 'Y', ready: false, why: 'Not installed', experimental: true }], (id) => `/play/1?emulator=${id}`)
  const rows = p.sections[0].items
  assert.equal(rows[0].to, '/play/1?emulator=x')
  assert.equal(rows[1].disabled, true)
  assert.equal(rows[1].sub, 'Not installed')
  assert.equal(rows[1].value, 'Experimental')
  assert.deepEqual(rows[0].actions.map((a) => a.id), ['settings.x'])
  assert.deepEqual(problems(withIds(p)), [])
  assert.ok(reachable(withIds(p), new Set(['settings.x', 'settings.y'])).includes('play.x'))
})

test('manual emulator: found note or nothing found, one field with its error', () => {
  const facts = { name: 'Dolphin', id: 'dolphin', system: 'linux', programs: ['dolphin-emu'], dropFolder: '/config/emus', file: 'emulators.json' }
  const none = manualSpec(facts, '', '')
  assert.match(none.sections[0].text, /Nothing was found.* \/config\/emus/)
  const f = none.sections[1].items[0]
  assert.match(f.hint, /It runs on Linux, as dolphin-emu/)
  const found = manualSpec({ ...facts, detected: { program: '/usr/bin/dolphin-emu', version: '5' } }, '/x', 'Not an emulator')
  assert.match(found.sections[0].text, /Found: .*version 5/)
  assert.equal(found.sections[1].items[0].error, 'Not an emulator')
  assert.equal(found.sections[1].items[0].value, '/x')
  assert.deepEqual(problems(withIds(found)), [])
})

test('fixGameSpec: how it was found, versions, archive entries, console and not-a-game', () => {
  const p = fixGameSpec({ how: 'Going by the folder it’s in', sure: 80, why: 'Because.', discs: ['A', 'B'], others: [{ id: 7, label: 'Rev 1' }, { id: 8, label: '' }], inside: [{ entry: 'a.sfc', label: 'USA', current: true }, { entry: 'b.sfc', label: 'EU', altered: true }], choosing: true, busy: false })
  ok(p)
  assert.equal(p.sections[0].text, 'Going by the folder it’s in, 80% sure. Because.')
  assert.match(p.sections[1].text, /^2 discs\./)
  const all = ids(p)
  for (const i of ['open.7', 'split.7', 'split.8', 'play.a.sfc', 'play.b.sfc', 'console', 'notgame']) assert.ok(all.includes(i), i)
  assert.equal(p.sections.find((s) => s.items?.[0]?.id === 'play.a.sfc').items[0].disabled, true)
  assert.equal(p.sections.at(-1).items[0].on, true)
  assert.equal(p.sections.find((s) => s.items?.[1]?.id === 'split.8').items[1].title, 'Make this version a game of its own')
  const q = fixGameSpec({ how: '', others: [], choosing: false, busy: true }); ok(q)
  assert.equal(q.sections.length, 1); assert.ok(q.sections[0].items.every((c) => c.disabled))
  ok(versionLinksSpec([{ id: 1, label: '' }]))
})
