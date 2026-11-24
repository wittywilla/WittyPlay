// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// The console photos switches (setup's Games step, WittyPlay's Settings): each is a whole row with a name of its own, which a
// screen reader and the TV walk read, and not a bare 26px switch named only by the words beside it.
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server.browser'
import { createElement } from 'react'
import { SwitchRow } from '../app/pages/settings/SwitchRow'

test('a switch row is one named button, its words inside it', () => {
  const html = renderToStaticMarkup(createElement(SwitchRow, { name: 'Remove plain backgrounds', more: 'Takes the white away.', checked: true, onChange: () => {} }))
  assert.match(html, /^<button type="button" role="switch" aria-checked="true" aria-label="Remove plain backgrounds" aria-describedby="[^"]+" class="wplay-photos-switch">/)
  assert.match(html, /Remove plain backgrounds<span class="muted" id="[^"]+"> · Takes the white away\.<\/span>/)
  assert.match(html, /<span class="set-switch on" aria-hidden="true"><i><\/i><\/span><\/button>$/)
  assert.equal((html.match(/<button/g) || []).length, 1, 'no second button inside')
})

test('a switch row with no more words has nothing to describe it', () => {
  const html = renderToStaticMarkup(createElement(SwitchRow, { name: 'Download console photos from Wikimedia', checked: false, disabled: true, onChange: () => {} }))
  assert.ok(!html.includes('aria-describedby'), html)
  assert.match(html, /aria-checked="false"/)
  assert.match(html, / disabled=""/)
})
