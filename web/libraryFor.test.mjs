// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import test from 'node:test'
import assert from 'node:assert/strict'
import { libraryFor } from './app/shell/playPaths.ts'

const libs = [
  { id: 1, type: 'games', console: 'auto' },
  { id: 2, type: 'games', console: 'auto' },
  { id: 3, type: 'games', console: 'snes' },
  { id: 9, type: 'movies' },
]

test('a console opens the library the server says holds its games, not the first games library', () => {
  assert.equal(libraryFor(libs, 'genesis', 2)?.id, 2)
})

test('without the server\'s word: the library kept for the console, else the first for every console', () => {
  assert.equal(libraryFor(libs, 'snes')?.id, 3)
  assert.equal(libraryFor(libs, 'nes')?.id, 1)
})

test('a library the person can\'t see (not in the list) is passed over', () => {
  assert.equal(libraryFor(libs, 'genesis', 42)?.id, 1)
  assert.equal(libraryFor(libs, 'genesis', 9)?.id, 1) // not a games library
})
