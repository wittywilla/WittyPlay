// The parts of the player with no browser in them. Run with: node --test
// (Node 22 needs --experimental-strip-types.)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { computeLayout } from './layout.ts'
import { Hotkeys } from './hotkeys.ts'
import { explain, familyOf } from './words.ts'
import { rateOf } from './refresh.ts'
import { ArrivalDelay } from './arrival.ts'
import { BackEntry } from './exits.ts'

const base = { areaW: 1920, areaH: 1080, srcW: 256, srcH: 240, aspect: 4 / 3, mode: 'original', custom: 0, integer: false, cropX: 0, cropY: 0, turns: 0, inset: 0 }

test('the picture fills the room it has, in its own shape', () => {
  const l = computeLayout(base)
  assert.equal(l.boxH, 1080)
  assert.equal(l.boxW, 1440)
})

test('fill uses all of it, whatever the shape', () => {
  const l = computeLayout({ ...base, mode: 'stretch' })
  assert.deepEqual([l.boxW, l.boxH], [1920, 1080])
})

test('square pixels are the source shape', () => {
  const l = computeLayout({ ...base, mode: 'square' })
  assert.equal(l.boxH, 1080)
  assert.equal(l.boxW, Math.round((1080 * 256) / 240))
})

test('whole-number scale gives every game pixel the same size', () => {
  const l = computeLayout({ ...base, integer: true })
  assert.equal(l.picH % 240, 0)
  assert.equal(l.picH, 960) // 4x, because 5x wouldn't fit
  assert.equal(l.scale, 4)
})

test('whole-number scale with square pixels has whole pixels in both directions', () => {
  const l = computeLayout({ ...base, integer: true, mode: 'square' })
  assert.equal(l.picW % 256, 0)
  assert.equal(l.picH % 240, 0)
})

test('it never gets smaller than one to one, even with no room', () => {
  const l = computeLayout({ ...base, areaW: 100, areaH: 100, integer: true })
  assert.ok(l.picW >= 1 && l.picH >= 1)
  assert.ok(l.boxW <= 100 + 1)
})

test('a sideways picture swaps the box', () => {
  const l = computeLayout({ ...base, turns: 1 })
  assert.equal(l.picW, l.boxH)
  assert.equal(l.picH, l.boxW)
  assert.ok(l.boxH <= 1080 && l.boxW <= 1920)
})

test('a crop shows less of the picture at the same size', () => {
  const l = computeLayout({ ...base, cropX: 10, cropY: 10 })
  assert.ok(Math.abs(l.scaleX - 1 / 0.8) < 1e-9)
  assert.ok(l.boxH === 1080 || l.boxW === 1920)
})

test('what surrounds the picture takes its share of the room', () => {
  const l = computeLayout({ ...base, inset: 25 })
  assert.equal(l.boxH, 810)
})

test('zero sizes before the page has any room do no harm', () => {
  const l = computeLayout({ ...base, areaW: 0, areaH: 0, srcW: 0, srcH: 0, aspect: 0 })
  assert.ok(l.boxW >= 1 && l.boxH >= 1 && Number.isFinite(l.scale))
})

const BIT = { b: 1, select: 1 << 2, a: 1 << 8, l: 1 << 10, r: 1 << 11 }
const keys = { 'hotkeys.padCombos': true, 'hotkeys.padModifier': 'select', 'hotkeys.pad.fastForward': 'r', 'hotkeys.pad.rewind': 'l', 'hotkeys.pad.quickSave': 'none', 'hotkeys.pad.screenshot': 'a' }

function hot() {
  const fired = []
  const h = new Hotkeys((a, down) => fired.push(`${a}:${down ? 'down' : 'up'}`), () => false)
  h.configure(keys)
  return { h, fired }
}

test('a button alone reaches the game, and nothing else happens', () => {
  const { h, fired } = hot()
  assert.equal(h.filter(BIT.r), BIT.r)
  assert.equal(h.filter(0), 0)
  assert.deepEqual(fired, [])
})

test('with the shortcut button held, a button does the shortcut instead of reaching the game', () => {
  const { h, fired } = hot()
  assert.equal(h.filter(BIT.select), BIT.select)
  assert.equal(h.filter(BIT.select | BIT.r), BIT.select)
  assert.deepEqual(fired, ['fastForward:down'])
  assert.equal(h.filter(BIT.select), BIT.select)
  assert.deepEqual(fired, ['fastForward:down', 'fastForward:up'])
})

test('letting go of the shortcut button first still keeps the other from the game until released', () => {
  const { h, fired } = hot()
  h.filter(BIT.select)
  h.filter(BIT.select | BIT.r)
  assert.equal(h.filter(BIT.r), 0, 'R is still kept from the game')
  assert.deepEqual(fired, ['fastForward:down'], 'and fast-forward holds')
  assert.equal(h.filter(0), 0)
  assert.deepEqual(fired, ['fastForward:down', 'fastForward:up'])
  assert.equal(h.filter(BIT.r), BIT.r, 'R is the game’s again')
})

test('pressing the shortcut button after another button does nothing', () => {
  const { h, fired } = hot()
  h.filter(BIT.r)
  assert.equal(h.filter(BIT.r | BIT.select), BIT.r | BIT.select)
  assert.deepEqual(fired, [])
})

test('something that happens once only fires once', () => {
  const { h, fired } = hot()
  h.filter(BIT.select)
  h.filter(BIT.select | BIT.a)
  h.filter(BIT.select | BIT.a)
  h.filter(BIT.select)
  h.filter(BIT.select | BIT.a)
  assert.deepEqual(fired, ['screenshot:down', 'screenshot:down'])
})

test('turning shortcuts off leaves every button to the game', () => {
  const { h, fired } = hot()
  h.configure({ ...keys, 'hotkeys.padCombos': false })
  assert.equal(h.filter(BIT.select | BIT.r), BIT.select | BIT.r)
  assert.deepEqual(fired, [])
})

test('a button with no shortcut stays with the game while the shortcut button is held', () => {
  const { h, fired } = hot()
  h.filter(BIT.select)
  assert.equal(h.filter(BIT.select | BIT.b), BIT.select | BIT.b)
  assert.deepEqual(fired, [])
})

test('a disabled menu state passes everything through', () => {
  const { h, fired } = hot()
  h.enabled = false
  assert.equal(h.filter(BIT.select | BIT.r), BIT.select | BIT.r)
  assert.deepEqual(fired, [])
})

test('a controller is known by whatever the browser calls it', () => {
  assert.equal(familyOf('Xbox 360 Controller (XInput STANDARD GAMEPAD Vendor: 045e Product: 028e)'), 'xbox')
  assert.equal(familyOf('DualSense Wireless Controller (STANDARD GAMEPAD Vendor: 054c Product: 0ce6)'), 'playstation')
  assert.equal(familyOf('054c-0ce6-Wireless Controller'), 'playstation')
  assert.equal(familyOf('Pro Controller (STANDARD GAMEPAD Vendor: 057e Product: 2009)'), 'nintendo')
  assert.equal(familyOf('USB Gamepad (Vendor: 0810 Product: 0001)'), 'generic')
})

test('a problem says what can be done, and only an administrator is sent to fix it', () => {
  assert.equal(explain('No core is installed for PS2.', { needCore: true }, true, 'PS2').fix, 'settings')
  assert.equal(explain('No core is installed for PS2.', { needCore: true }, false, 'PS2').fix, undefined)
  assert.equal(explain('needs a BIOS', { needBios: true }, false, 'PS2').fix, undefined)
  const core = explain('Needs ParaLLEl N64.', { otherCore: true }, true, 'N64')
  assert.equal(core.fix, 'settings')
  assert.equal(core.retry, undefined, 'trying again on the same core gets nowhere')
  assert.equal(explain('Needs ParaLLEl N64.', { otherCore: true }, false, 'N64').fix, undefined)
  assert.equal(explain('nope', { encrypted: true }, true, '3DS').retry, undefined, 'trying an encrypted file again gets nowhere')
  assert.ok(explain('too many games', { status: 503 }, true, 'NES').retry)
  const other = explain('The core stopped', null, true, 'NES')
  assert.ok(other.retry && other.defaults)
  assert.equal(other.body, 'The core stopped')
})

test('a screen\'s rate is read from its frames, and snapped to a usual one', () => {
  const at = (hz, n = 90) => Array.from({ length: n }, () => 1000 / hz)
  assert.equal(rateOf(at(60.002)), 60)
  assert.equal(rateOf(at(59.94)), 59.94)
  assert.equal(rateOf(at(50)), 50)
  assert.equal(rateOf(at(144)), 144)
  // A busy page misses frames now and then; the rest still say the rate.
  const busy = at(60)
  busy[10] = 33.3; busy[40] = 50
  assert.equal(rateOf(busy), 60)
  // Gaps all over the place say nothing.
  assert.equal(rateOf([5, 40, 12, 90, 3, 60, 22]), 0)
})

test('how late frames arrive is measured against the quickest lately', () => {
  const d = new ArrivalDelay()
  // The clocks differ by 1000 ms; frames take 20 ms, then a queue adds 80.
  d.note(0, 1020)
  d.note(16, 1036)
  assert.equal(d.take(), 0)
  d.note(33, 1133)
  assert.equal(Math.round(d.take()), 80)
  assert.equal(d.take(), 0, 'each report says what happened since the last')
})


// Leaving what opens over a game. A browser's history, as far as the menu sees it: Back is
// answered later (popstate), as browsers do.
class FakeHistory {
  constructor() { this.entries = [{ idx: 3, key: 'play' }]; this.at = 0; this.waiting = 0 }
  get state() { return this.entries[this.at] }
  pushState(s) { this.entries = this.entries.slice(0, this.at + 1); this.entries.push(s); this.at++ }
  back() { this.waiting++ }
  // The browser gets round to the Back it was asked for.
  settle(entry) { while (this.waiting > 0) { this.waiting--; if (this.at > 0) { this.at--; entry.popped() } } }
  // Someone presses Back: a phone's gesture, the browser's button, a mouse's side button.
  userBack(entry) { if (this.at > 0) { this.at--; entry.popped() } }
}

function menu(levels) {
  const h = new FakeHistory()
  const open = { level: 0 }
  // What going Back does in the player: up a level, and closed from the top.
  const entry = new BackEntry(h, () => { open.level = Math.max(0, open.level - 1); return open.level > 0 }, 20)
  const show = (n) => { open.level = n; if (n > 0) entry.opened(); else entry.closed() }
  show(levels)
  return { h, entry, open, show }
}

test('Back with the menu open closes it, and stays in the game', () => {
  const { h, entry, open } = menu(1)
  assert.equal(h.entries.length, 2, 'one entry while the menu is open')
  h.userBack(entry)
  assert.equal(open.level, 0)
  assert.equal(h.at, 0, 'the game’s own entry is where Back leaves you')
  assert.equal(entry.open, false)
})

test('Back from a panel goes up a level, and the next Back closes the menu', () => {
  const { h, entry, open } = menu(2)
  assert.equal(h.entries.length, 2, 'going a level deeper adds no entry')
  h.userBack(entry)
  assert.equal(open.level, 1)
  assert.equal(h.at, 1, 'an entry is there for the next Back')
  h.userBack(entry)
  assert.equal(open.level, 0)
  assert.equal(h.at, 0)
})

test('closing with × or Esc takes the entry back out, and that is not taken for a Back', () => {
  const { h, entry, open, show } = menu(2)
  show(0)
  h.settle(entry)
  assert.equal(h.at, 0, 'Back from here leaves the game, as before the menu opened')
  assert.equal(open.level, 0)
  show(1)
  h.userBack(entry)
  assert.equal(open.level, 0, 'the menu opened again closes with Back again')
})

test('the page’s own state rides along, so its router sees the same place', () => {
  const { h } = menu(1)
  assert.equal(h.state.key, 'play')
  assert.equal(h.state.idx, 3)
  assert.equal(h.state.wpMenu, true)
})

test('a Back the menu refuses (an unsaved settings file) keeps it open, with its entry', () => {
  const h = new FakeHistory()
  const entry = new BackEntry(h, () => true, 20)
  entry.opened()
  h.userBack(entry)
  assert.equal(entry.open, true)
  assert.equal(h.entries.length, 2)
  assert.equal(h.at, 1)
})

test('leaving the game takes the entry out first, then leaves', () => {
  const { h, entry } = menu(1)
  let left = 0
  entry.release(() => left++)
  assert.equal(left, 0, 'not before the browser has gone back')
  h.settle(entry)
  assert.equal(left, 1)
  assert.equal(h.at, 0, 'what replaces the page replaces the game’s entry, not the menu’s')
})

test('leaving with nothing open leaves at once', () => {
  const { entry } = menu(0)
  let left = 0
  entry.release(() => left++)
  assert.equal(left, 1)
})

test('leaving still happens when a browser never says it went back', async () => {
  const { entry } = menu(1)
  let left = 0
  entry.release(() => left++)
  await new Promise((r) => setTimeout(r, 40))
  assert.equal(left, 1)
  entry.popped()
  assert.equal(left, 1, 'once')
})

test('a sheet the page opens over the menu, with its own entry, closes first; the menu stays', () => {
  const { h, entry, open } = menu(2)
  h.pushState({ ...h.state, sheet: true }, '') // the website's own sheet, over the game menu
  h.userBack(entry)
  assert.equal(open.level, 2, 'Back closed the sheet, not a level of the menu')
  assert.equal(entry.open, true)
  h.userBack(entry)
  assert.equal(open.level, 1, 'the next Back is the menu’s')
})
