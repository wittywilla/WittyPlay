// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// The watchdog that says a WebRTC picture has stopped, with the clock and the browser stood in
// for. Run with: node --test (Node 22 needs --experimental-strip-types).
import assert from 'node:assert/strict'
import { test } from 'node:test'

let now = 0
globalThis.performance = { now: () => now }
globalThis.document = { hidden: false }
globalThis.window = { setInterval: () => 1, clearInterval: () => {}, setTimeout: () => 1, clearTimeout: () => {} }
const { countFrames, Watchdog } = await import('./watchdog.ts')

// A connection whose stats say how much has arrived and been decoded.
function connection() {
  const c = { bytes: 0, decoded: 0, connectionState: 'connected' }
  c.getStats = async () => ({ forEach: (fn) => fn({ type: 'inbound-rtp', kind: 'video', bytesReceived: c.bytes, framesDecoded: c.decoded }) })
  return c
}

// One second of the watchdog's life: the stream moves on (or not), and it looks.
async function second(wd, pc, { shown, decoding }, count) {
  now += 1000
  if (decoding) { pc.bytes += 50000; pc.decoded += 60 }
  if (shown) count.n += 60
  wd.tick()
  await new Promise((r) => setTimeout(r, 0))
  await new Promise((r) => setTimeout(r, 0))
}

function watch(pc, count) {
  const seen = { stalls: [], lost: [] }
  const wd = new Watchdog(pc, () => count.n, (why) => seen.lost.push(why), (on) => seen.stalls.push(on))
  wd.start()
  return { wd, seen }
}

test('Firefox: a count stuck at 0 while pictures are decoded never says the picture has stopped', async () => {
  now = 1000
  const pc = connection(), count = { n: 0 }
  const { wd, seen } = watch(pc, count)
  for (let i = 0; i < 20; i++) await second(wd, pc, { shown: false, decoding: true }, count)
  assert.deepEqual(seen.stalls.filter((s) => s), [], 'never stalled')
  assert.deepEqual(seen.lost, [])
})

test('a picture that really stops is said to have stopped, and to have started again', async () => {
  now = 1000
  const pc = connection(), count = { n: 0 }
  const { wd, seen } = watch(pc, count)
  for (let i = 0; i < 3; i++) await second(wd, pc, { shown: true, decoding: true }, count)
  for (let i = 0; i < 4; i++) await second(wd, pc, { shown: false, decoding: false }, count)
  assert.equal(seen.stalls.at(-1), true, 'stopped')
  await second(wd, pc, { shown: true, decoding: true }, count)
  assert.equal(seen.stalls.at(-1), false, 'started again')
  assert.deepEqual(seen.lost, [])
})

test('Firefox, then a real stop: decoding that stops is still a stop, without a count to go on', async () => {
  now = 1000
  const pc = connection(), count = { n: 0 }
  const { wd, seen } = watch(pc, count)
  for (let i = 0; i < 5; i++) await second(wd, pc, { shown: false, decoding: true }, count)
  for (let i = 0; i < 4; i++) await second(wd, pc, { shown: false, decoding: false }, count)
  assert.equal(seen.stalls.at(-1), true)
  for (let i = 0; i < 2; i++) await second(wd, pc, { shown: false, decoding: true }, count)
  assert.equal(seen.stalls.at(-1), false)
})

test('data arriving with nothing decoded for 5 s is a stuck decoder', async () => {
  now = 1000
  const pc = connection(), count = { n: 0 }
  const { wd, seen } = watch(pc, count)
  await second(wd, pc, { shown: true, decoding: true }, count)
  for (let i = 0; i < 9; i++) { pc.bytes += 50000; await second(wd, pc, { shown: false, decoding: false }, count) }
  assert.equal(seen.lost[0], 'decoder stuck', 'the transport takes the first and ignores the rest')
})

test('pictures are counted from requestVideoFrameCallback where there is one', () => {
  const callbacks = []
  const v = { requestVideoFrameCallback: (cb) => { callbacks.push(cb); return callbacks.length }, cancelVideoFrameCallback() {}, getVideoPlaybackQuality: () => ({ totalVideoFrames: 0 }) }
  const c = countFrames(v)
  for (let i = 0; i < 5; i++) callbacks[callbacks.length - 1]()
  assert.equal(c.count(), 5, 'not getVideoPlaybackQuality, which Firefox leaves at 0')
  c.stop()
  const before = callbacks.length
  callbacks[callbacks.length - 1]()
  assert.equal(callbacks.length, before, 'stopped: it asks for no more')
  assert.equal(countFrames({ getVideoPlaybackQuality: () => ({ totalVideoFrames: 7 }) }).count(), 7)
  assert.equal(countFrames({}).count(), -1)
})
