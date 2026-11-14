// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'
import { batteryLabel, batteryText, deviceReading, isLow, LowBatteryNotes, padBattery, padReadings, type BatteryReading } from '../battery'
import { inviteWords, joinedGame, newInvites, type InviteView } from '../app/shell/invites'

// Batteries and invites, with no browser: what's shown, what's said, and that it's said once.

const pad = (index: number, id: string, battery?: unknown) => ({ index, id, connected: true, battery })

describe('batteries', () => {
  test('a pad battery is read only where the browser gives a real one', () => {
    assert.deepEqual(padBattery(pad(0, 'x', { level: 0.5, charging: true })), { level: 0.5, charging: true })
    assert.deepEqual(padBattery(pad(0, 'x', { level: 1 })), { level: 1, charging: false })
    assert.equal(padBattery(pad(0, 'x')), null)
    assert.equal(padBattery(pad(0, 'x', { level: 50 })), null)
    assert.equal(padBattery(pad(0, 'x', { level: NaN })), null)
    assert.equal(padBattery(pad(0, 'x', 'full')), null)
    assert.equal(padBattery(null), null)
  })

  test('pads are named by player, counting only the connected ones', () => {
    const list = [null, pad(1, 'Xbox Wireless Controller (STANDARD GAMEPAD Vendor: 045e)', { level: 0.72 }), { index: 2, id: 'gone', connected: false, battery: { level: 1 } }, pad(3, 'DualSense'), pad(4, '8BitDo', { level: 0.1 })]
    const got = padReadings(list)
    assert.deepEqual(got.map((r) => [r.id, r.name, r.detail]), [['pad-1', 'P1', 'Xbox Wireless Controller'], ['pad-4', 'P3', '8BitDo']])
  })

  test('the device: none for a computer with no battery, or one full on its charger', () => {
    assert.equal(deviceReading({ level: 1, charging: true, chargingTime: 0, dischargingTime: Infinity }), null)
    assert.equal(deviceReading(null), null)
    assert.deepEqual(deviceReading({ level: 0.64, charging: false, chargingTime: Infinity, dischargingTime: 7200 }), { id: 'device', kind: 'device', name: 'This device', level: 0.64, charging: false })
    assert.equal(deviceReading({ level: 0.9, charging: true, chargingTime: 600 })?.charging, true)
  })

  test('words: short on the chip, in full for a screen reader', () => {
    const p1: BatteryReading = { id: 'pad-0', kind: 'controller', name: 'P1', detail: 'DualSense', level: 0.153, charging: false }
    assert.equal(batteryText(p1), 'P1 15%')
    assert.equal(batteryLabel(p1), 'Controller P1, DualSense: battery 15%, low')
    assert.equal(batteryLabel({ ...p1, charging: true }), 'Controller P1, DualSense: battery 15%, charging')
    const dev: BatteryReading = { id: 'device', kind: 'device', name: 'This device', level: 0.64, charging: false }
    assert.equal(batteryText(dev), 'Battery 64%')
    assert.equal(batteryLabel(dev), 'This device: battery 64%')
  })

  test('low is a fifth or under, not charging', () => {
    const r = (level: number, charging = false) => ({ level, charging })
    assert.equal(isLow(r(0.2)), true)
    assert.equal(isLow(r(0.21)), false)
    assert.equal(isLow(r(0.05, true)), false)
  })

  test('a low battery is said once, and again only after it was charged', () => {
    const notes = new LowBatteryNotes()
    const p = (level: number, charging = false): BatteryReading => ({ id: 'pad-0', kind: 'controller', name: 'P1', level, charging })
    const d = (level: number): BatteryReading => ({ id: 'device', kind: 'device', name: 'This device', level, charging: false })
    assert.deepEqual(notes.check([p(0.5), d(0.5)]), [])
    assert.deepEqual(notes.check([p(0.19), d(0.5)]), ['Controller P1’s battery is low: 19%. Charge it or change its batteries soon.'])
    assert.deepEqual(notes.check([p(0.15), d(0.5)]), [], 'lower still: not said again')
    assert.deepEqual(notes.check([p(0.22), d(0.5)]), [], 'wobbling over the line')
    assert.deepEqual(notes.check([p(0.19), d(0.5)]), [], 'and back: still not again')
    assert.deepEqual(notes.check([p(0.19, true), d(0.1)]), ['This device’s battery is low: 10%. Plug it in soon.'])
    assert.deepEqual(notes.check([p(0.18), d(0.1)]), ['Controller P1’s battery is low: 18%. Charge it or change its batteries soon.'], 'charged, then low again')
    assert.deepEqual(notes.check([d(0.1)]), [], 'the pad went')
    assert.equal(notes.check([p(0.1), d(0.1)]).length, 1, 'a pad that comes back low is said again')
  })
})

describe('invites', () => {
  const inv = (id: string, sentAt: number): InviteView => ({ id, from: 3, fromName: 'sam', to: 2, sid: 's1', itemId: 7, title: 'Star Tide', sentAt, expiresAt: sentAt + 600 })

  test('each new invite is said once, oldest first; one sent again is new', () => {
    const seen = new Set<string>()
    assert.deepEqual(newInvites([inv('b', 20), inv('a', 10)], seen), ['sam invites you to play Star Tide', 'sam invites you to play Star Tide'])
    assert.deepEqual(newInvites([inv('b', 20), inv('a', 10)], seen), [])
    assert.deepEqual(newInvites([inv('b', 20)], seen), [])
    assert.deepEqual(newInvites([inv('a', 30), inv('b', 20)], seen), ['sam invites you to play Star Tide'])
    assert.equal(inviteWords({ fromName: '', title: '' }), 'A friend invites you to play a game')
  })

  test("the game page's address: an invite first, then a seat, and nothing odd", () => {
    assert.deepEqual(joinedGame(new URLSearchParams('invite=ab12&join=s1')), { invite: 'ab12' })
    assert.deepEqual(joinedGame(new URLSearchParams('join=3f9e')), { join: '3f9e' })
    assert.deepEqual(joinedGame(new URLSearchParams('resume=1')), {})
    assert.deepEqual(joinedGame(new URLSearchParams('join=../x')), {})
  })
})
