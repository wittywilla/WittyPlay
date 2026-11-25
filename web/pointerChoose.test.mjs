// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { clickOpens, pressTracker } from './app/pointerChoose.ts'

// A grid as Consoles and All games keep it: the chosen card, set by the focus (arrows, a remote, a pad) or by a click.
function grid() {
  const g = { chosen: 'a', opened: [] }
  const t = pressTracker()
  g.press = (id, detail = 1) => {
    t.press(g.chosen === id) // as the press begins
    g.chosen = id // the focus the press brings chooses it
    if (t.opens(detail)) g.opened.push(id)
  }
  g.enter = (id) => { g.chosen = id; if (t.opens(0)) g.opened.push(id) }
  return g
}

test('moving the pointer across cards and then clicking one chooses it; nothing opens', () => {
  const g = grid()
  // Hovering runs nothing: there's no handler for it. The click lands on the card under the pointer.
  g.press('c')
  assert.equal(g.chosen, 'c')
  assert.deepEqual(g.opened, [])
})

test('a second click on the chosen card, or a double-click, opens it', () => {
  const g = grid()
  g.press('c'); g.press('c')
  assert.deepEqual(g.opened, ['c'])
  const d = grid()
  d.press('x', 1); d.press('x', 2)
  assert.deepEqual(d.opened, ['x'])
})

test('Enter from a keyboard, a remote or a pad opens the focused card at once', () => {
  const g = grid()
  g.enter('b')
  assert.deepEqual(g.opened, ['b'])
})

test('clickOpens', () => {
  assert.equal(clickOpens(0, false), true)
  assert.equal(clickOpens(1, false), false)
  assert.equal(clickOpens(1, true), true)
  assert.equal(clickOpens(2, false), true)
})

// No WittyPlay grid chooses on hover: the cards of Consoles and All games go through the shared rule, and nothing in the
// app sets what's chosen from a pointer entering, moving or going over a card.
test('no grid chooses on hover', () => {
  for (const f of ['app/pages/PlayConsoles.tsx', 'app/pages/PlayLibraryView.tsx']) {
    const src = readFileSync(new URL(f, import.meta.url), 'utf8')
    assert.match(src, /useChooseThenOpen\(\)/, f)
    assert.doesNotMatch(src, /on(Pointer|Mouse)(Enter|Over|Move)=/, f)
  }
})
