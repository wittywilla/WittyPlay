// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'
import { canPause, desktopSrc, firstState, isGone, nextState, pictureBox, playersOf, pointOnPicture, problemOf, showingOf, usualControls, type StreamEvent, type StreamState } from '../emulator'
import { compile, effectiveKeys, newReading, readPad, type PadLike } from '../mapping'

// A standalone emulator's player, the parts with no browser in them. Run with: npm test

const run = (events: StreamEvent[], from: StreamState = firstState) => events.reduce(nextState, from)
const failed = (message = 'no picture arrived'): StreamEvent => ({ type: 'error', gone: false, message })

describe('where the stream stands', () => {
  test('it starts by connecting, and plays once connected', () => {
    assert.equal(firstState.phase, 'connecting')
    assert.equal(run([{ type: 'connected' }]).phase, 'playing')
  })

  test('a lost connection is tried again once by itself', () => {
    const lost = run([{ type: 'connected' }, { type: 'lost' }])
    assert.equal(lost.phase, 'reconnecting')
    assert.equal(lost.attempt, 2)
    assert.equal(lost.retried, true)
    // That one try failing is said, with Retry, rather than tried for ever.
    const gaveUp = nextState(lost, failed())
    assert.equal(gaveUp.phase, 'failed')
    assert.equal(gaveUp.message, 'no picture arrived')
    assert.equal(gaveUp.attempt, 2)
  })

  test('a connection that came back may be lost again later, and is tried again then too', () => {
    const back = run([{ type: 'connected' }, { type: 'lost' }, { type: 'connected' }])
    assert.equal(back.phase, 'playing')
    assert.equal(back.retried, false)
    assert.equal(nextState(back, { type: 'lost' }).phase, 'reconnecting')
  })

  test('Retry starts a new try, only from a failure', () => {
    const again = run([failed(), { type: 'retry' }])
    assert.equal(again.phase, 'connecting')
    assert.equal(again.attempt, 2)
    assert.equal(again.message, '')
    assert.equal(nextState(run([{ type: 'connected' }]), { type: 'retry' }).attempt, 1)
  })

  test('a game that has gone ends, from anywhere, and stays ended', () => {
    for (const before of [[], [{ type: 'connected' }], [{ type: 'connected' }, { type: 'lost' }], [failed()]] as StreamEvent[][]) {
      const s = run([...before, { type: 'error', gone: true, message: 'not found' }])
      assert.equal(s.phase, 'ended')
      assert.equal(nextState(s, { type: 'retry' }).phase, 'ended')
      assert.equal(nextState(s, { type: 'connected' }).phase, 'ended')
    }
    assert.equal(run([{ type: 'connected' }, { type: 'ended', message: 'Stopped by an administrator' }]).message, 'Stopped by an administrator')
  })

  test('what arrives late is ignored', () => {
    const playing = run([{ type: 'connected' }])
    assert.deepEqual(nextState(playing, failed()), playing)
    assert.deepEqual(nextState(playing, { type: 'connected' }), playing)
    assert.deepEqual(nextState(firstState, { type: 'lost' }), firstState)
  })
})

describe('what went wrong, in words', () => {
  test('a game that has ended offers only Back', () => {
    const p = problemOf('ended', '', 'PCSX2')
    assert.equal(p?.title, 'This game has ended')
    assert.ok(!p?.retry)
    assert.match(p!.body, /PCSX2/)
    assert.equal(problemOf('ended', 'Stopped by an administrator', 'PCSX2')?.body, 'Stopped by an administrator')
  })
  test('a failed connection can be tried again, and says what the browser said', () => {
    const p = problemOf('failed', 'no picture arrived', 'PCSX2')
    assert.equal(p?.retry, true)
    assert.match(p!.body, /^no picture arrived /)
  })
  test('nothing is wrong while connecting or playing', () => {
    for (const phase of ['connecting', 'playing', 'reconnecting'] as const) assert.equal(problemOf(phase, '', 'PCSX2'), null)
  })
})

describe('the session', () => {
  test('a stream or a desktop, by its backend', () => {
    assert.equal(showingOf({ backend: 'docker+webrtc', connect: '/c' }), 'stream')
    assert.equal(showingOf({ backend: 'local+webrtc', connect: '/c' }), 'stream')
    assert.equal(showingOf({ backend: 'host+webrtc', connect: '/c' }), 'stream')
    assert.equal(showingOf({ backend: 'docker+kasmvnc', kasmUrl: '/k/1/' }), 'desktop')
    // What it needs is missing: nothing can be shown, and the player says so.
    assert.equal(showingOf({ backend: 'docker+webrtc' }), 'none')
    assert.equal(showingOf({ backend: 'docker+kasmvnc', connect: '/c' }), 'none')
    assert.equal(showingOf({ backend: 'something+new', kasmUrl: '/k/' }), 'desktop')
  })

  test('pause is offered only with both of its halves', () => {
    assert.equal(canPause({ pause: '/p', resume: '/r' }), true)
    assert.equal(canPause({ pause: '/p' }), false)
    assert.equal(canPause({}), false)
  })

  test('one to four players', () => {
    assert.deepEqual([0, 1, 2, 4, 9, NaN].map((players) => playersOf({ players })), [1, 1, 2, 4, 4, 1])
  })

  test('gone is a 404 or the server saying no-session', () => {
    assert.equal(isGone({ status: 404 }), true)
    assert.equal(isGone({ status: 409, code: 'no-session' }), true)
    assert.equal(isGone({ status: 410, data: { code: 'no-session' } }), true)
    assert.equal(isGone({ status: 503 }), false)
    assert.equal(isGone(new Error('no picture arrived')), false)
    assert.equal(isGone(null), false)
  })

  test('only this server’s own pages go in the frame', () => {
    assert.equal(desktopSrc('/api/v1/modules/wittyplay/kasm/abc/t/'), '/api/v1/modules/wittyplay/kasm/abc/t/')
    assert.equal(desktopSrc('/api/v1/x/', (p) => 'https://media.example' + p), 'https://media.example/api/v1/x/')
    for (const bad of [undefined, '', 'https://elsewhere.example/', '//elsewhere.example/', 'javascript:alert(1)', '/\\elsewhere.example']) assert.equal(desktopSrc(bad), '', String(bad))
  })
})

describe('a press on the picture', () => {
  const screen = { left: 0, top: 0, width: 1600, height: 900 }

  test('a picture the shape of the screen fills it', () => {
    assert.deepEqual(pictureBox(screen, 1280, 720), screen)
    assert.deepEqual(pointOnPicture(screen, 1280, 720, 800, 450), { x: 0.5, y: 0.5, inside: true })
    assert.deepEqual(pointOnPicture(screen, 1280, 720, 0, 900), { x: 0, y: 1, inside: true })
  })

  test('a 4:3 picture has bars at the sides, and a press is measured from the picture', () => {
    assert.deepEqual(pictureBox(screen, 640, 480), { left: 200, top: 0, width: 1200, height: 900 })
    assert.deepEqual(pointOnPicture(screen, 640, 480, 200, 0), { x: 0, y: 0, inside: true })
    assert.deepEqual(pointOnPicture(screen, 640, 480, 1400, 900), { x: 1, y: 1, inside: true })
    assert.deepEqual(pointOnPicture(screen, 640, 480, 500, 225), { x: 0.25, y: 0.25, inside: true })
  })

  test('a wide picture has bars above and below, wherever the element is on the page', () => {
    const el = { left: 100, top: 50, width: 800, height: 800 }
    assert.deepEqual(pictureBox(el, 1920, 960), { left: 100, top: 250, width: 800, height: 400 })
    assert.deepEqual(pointOnPicture(el, 1920, 960, 500, 450), { x: 0.5, y: 0.5, inside: true })
  })

  test('a press on a bar is outside, and a drag onto one stops at the edge', () => {
    assert.deepEqual(pointOnPicture(screen, 640, 480, 100, 450), { x: 0, y: 0.5, inside: false })
    assert.deepEqual(pointOnPicture(screen, 640, 480, 1599, 950), { x: 1, y: 1, inside: false })
  })

  test('a size not known yet takes the whole element', () => {
    assert.deepEqual(pointOnPicture(screen, 0, 0, 400, 450), { x: 0.25, y: 0.5, inside: true })
    assert.deepEqual(pointOnPicture({ left: 0, top: 0, width: 0, height: 0 }, 640, 480, 5, 5), { x: 0, y: 0, inside: false })
  })
})

describe('the usual controller', () => {
  const c = usualControls()
  const targets = new Set(c.targets.map((t) => t.id))

  test('every button and stick direction has a place on a pad, and none is used twice', () => {
    assert.equal(targets.size, 24)
    const seen = new Set<string>()
    for (const id of targets) {
      const b = c.defaults.gamepad[id]
      assert.ok(b, `nothing presses ${id}`)
      const key = JSON.stringify(b)
      assert.ok(!seen.has(key), `${key} twice`)
      seen.add(key)
    }
  })

  test('no key does two things', () => {
    const keys = Object.values(effectiveKeys(c.defaults.keyboard, targets, null))
    assert.equal(new Set(keys).size, keys.length)
    assert.ok(!keys.includes('Escape')) // Esc is the player's own
  })

  test('a standard pad reads as its positions say', () => {
    const pad = { id: 'Test pad (STANDARD GAMEPAD)', index: 0, mapping: 'standard', connected: true, timestamp: 1, buttons: Array.from({ length: 17 }, () => ({ pressed: false, value: 0 })), axes: [0, 0, 0, 0] } as unknown as PadLike & { buttons: { pressed: boolean; value: number }[]; axes: number[] }
    pad.buttons[0] = { pressed: true, value: 1 } // the bottom face button is B
    pad.buttons[9] = { pressed: true, value: 1 } // Start
    pad.buttons[12] = { pressed: true, value: 1 } // up
    pad.axes[0] = -1
    const r = newReading()
    readPad(compile(pad, c.defaults.gamepad, null, {}), pad, 0, r)
    assert.equal(r.mask & 0xffff, (1 << 0) | (1 << 3) | (1 << 4))
    assert.ok(r.lx < -0.9)
  })
})
