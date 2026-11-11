// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'
import { adviceFor, ClockSync, LatencyMeter, parseRecord, PhaseLock, Series, SteadinessWatch, TIMING_PONG, TIMING_RECORD } from '../latency'

// What's measured, and what's done about it. Run with: npm run test:play (in web/)

const FRAME = 16.7

function record(seq: number, inputSeq: number, rtp: number, at: (name: string) => bigint) {
  const b = new ArrayBuffer(82)
  const v = new DataView(b)
  v.setUint8(0, TIMING_RECORD)
  v.setUint8(1, 0)
  v.setUint32(2, seq, true)
  v.setUint32(6, inputSeq, true)
  v.setUint32(10, rtp, true)
  v.setInt32(14, 1000, true)
  ;['inputServer', 'inputHost', 'runStart', 'runEnd', 'serverIn', 'encIn', 'encOut', 'sent'].forEach((n, i) => v.setBigInt64(18 + i * 8, at(n), true))
  return b
}

describe('a frame record', () => {
  test('reads back, in the page clock', () => {
    const r = parseRecord(record(7, 3, 900, () => 2000000000n), (n) => Number(n) / 1e6)
    assert.equal(r?.seq, 7)
    assert.equal(r?.inputSeq, 3)
    assert.equal(r?.rtp, 900)
    assert.equal(r?.size, 1000)
    assert.equal(r?.runEnd, 2000)
  })
  test('of the wrong size or kind is nothing', () => {
    assert.equal(parseRecord(new ArrayBuffer(10), Number), null)
    const b = record(1, 1, 1, () => 1n)
    new DataView(b).setUint8(0, 0)
    assert.equal(parseRecord(b, Number), null)
  })
  test('with no time for a stage says so rather than a time near 1970', () => {
    const r = parseRecord(record(1, 0, 1, (n) => (n === 'inputServer' ? 0n : 5000000n)), (n) => Number(n) / 1e6)
    assert.equal(r?.inputServer, 0)
    assert.equal(r?.encIn, 5)
  })
})

describe('the clocks', () => {
  function pong(sent: number, server: bigint) {
    const b = new ArrayBuffer(17)
    const v = new DataView(b)
    v.setUint8(0, TIMING_PONG)
    v.setFloat64(1, sent, true)
    v.setBigInt64(9, server, true)
    return b
  }
  test('line up within half the round trip', () => {
    const c = new ClockSync()
    assert.equal(c.synced, false)
    const sent = performance.now()
    assert.equal(c.pong(pong(sent, 5000000000000n)), true)
    assert.equal(c.synced, true)
    // The server's clock read 5000 s at about now, so that moment is about now here.
    assert.ok(Math.abs(c.toLocal(5000000000000n) - performance.now()) < 50)
  })
  test('ignore what is not a reply', () => {
    assert.equal(new ClockSync().pong(new ArrayBuffer(9)), false)
  })
})

describe('the meter', () => {
  function meter() {
    const m = new LatencyMeter()
    // A server whose clock is this page's, in nanoseconds.
    const b = new ArrayBuffer(17)
    const v = new DataView(b)
    v.setUint8(0, TIMING_PONG)
    const now = performance.now()
    v.setFloat64(1, now, true)
    v.setBigInt64(9, BigInt(Math.round(now * 1e6)), true)
    m.message(b)
    return m
  }
  const stamp = (t: number) => BigInt(Math.round(t * 1e6))
  function send(m: LatencyMeter, seq: number, inputSeq: number, base: number) {
    const times: Record<string, number> = { inputServer: base + 1, inputHost: base + 1.5, runStart: base + 2, runEnd: base + 4, serverIn: base + 5, encIn: base + 5.5, encOut: base + 8, sent: base + 8.5 }
    m.message(record(seq, inputSeq, seq * 1500, (n) => stamp(times[n])))
  }
  test('joins a frame to its record in either order', () => {
    const m = meter()
    const t = performance.now()
    send(m, 1, 0, t)
    m.shown({ rtp: 1500, received: t + 9, presented: t + 10, displayAt: t + 18 })
    assert.equal(m.frames.encode.count, 1)
    m.shown({ rtp: 3000, received: t + 9, presented: t + 10, displayAt: t + 18 })
    assert.equal(m.frames.encode.count, 1)
    send(m, 2, 0, t)
    assert.equal(m.frames.encode.count, 2)
    assert.ok(Math.abs(m.frames.encode.median - 2.5) < 0.01)
    assert.ok(Math.abs(m.frames.network.median - 0.5) < 2, 'across the clocks, it can only be so exact')
    assert.ok(Math.abs(m.frames.display.median - 8) < 0.01)
  })
  test('follows a press to the first frame that saw it', () => {
    const m = meter()
    const t = performance.now()
    const seq = m.press(t)
    send(m, 1, 0, t) // saw nothing yet
    m.shown({ rtp: 1500, received: t + 9, presented: t + 10, displayAt: t + 18 })
    assert.equal(m.presses.photon.count, 0)
    send(m, 2, seq, t)
    m.shown({ rtp: 3000, received: t + 25, presented: t + 26, displayAt: t + 30 })
    assert.equal(m.presses.photon.count, 1)
    assert.ok(Math.abs(m.presses.photon.median - 30) < 0.01)
    send(m, 3, seq, t)
    m.shown({ rtp: 4500, received: t + 41, presented: t + 42, displayAt: t + 46 })
    assert.equal(m.presses.photon.count, 1, 'only the first frame counts')
  })
  test('holds a bounded amount however long it runs', () => {
    const m = meter()
    for (let i = 1; i < 2000; i++) {
      send(m, i, 0, performance.now())
      m.press()
      if (i % 3) m.shown({ rtp: 1 + i * 7, presented: 1, displayAt: 2 })
    }
    assert.ok((m as any).records.size <= 257)
    assert.ok((m as any).bySeq.size <= 257)
    assert.ok((m as any).pressAt.size <= 65)
    assert.ok((m as any).waiting.size <= 33)
  })
  test('has nothing to say until it has seen enough', () => {
    const m = meter()
    assert.equal(m.summary(), null)
  })
})

describe('landing frames before the display wants them', () => {
  // A display that takes what has arrived at each refresh: the wait is how long until then.
  function display(shifts: number[], phase0: number, pipeline = 8) {
    let phase = phase0
    const lock = new PhaseLock((ms) => { shifts.push(ms); phase = ((phase - ms) % FRAME + FRAME) % FRAME })
    return (seconds: number) => {
      let t = 1000
      for (let i = 0; i < seconds * 60; i++) {
        t += FRAME
        lock.shown({ presented: t, displayAt: t + pipeline + phase })
      }
      return { phase, lock }
    }
  }
  test('moves frames later until the wait is small, and then stays', () => {
    const shifts: number[] = []
    const run = display(shifts, 12)
    const { phase } = run(20)
    assert.ok(shifts.length > 0)
    assert.ok(phase < 7, `the wait should have come down, and it is ${phase}`)
    const moved = shifts.length
    const after = run(20)
    assert.ok(shifts.length - moved <= 3, 'it should have settled')
    assert.ok(after.phase < 7)
  })
  test('gives up on a browser whose wait has nothing to do with arrival', () => {
    const shifts: number[] = []
    const lock = new PhaseLock((ms) => shifts.push(ms))
    let t = 1000
    for (let i = 0; i < 60 * 120; i++) {
      t += FRAME
      lock.shown({ presented: t, displayAt: t + 9 })
    }
    const total = shifts.reduce((a, b) => a + b, 0)
    assert.ok(total <= FRAME * 1.5, `it moved frames by ${total} ms for nothing`)
    assert.ok(shifts.length <= 15)
  })
  test('copes with nonsense', () => {
    const shifts: number[] = []
    const lock = new PhaseLock((ms) => shifts.push(ms))
    for (let i = 0; i < 200; i++) lock.shown({ presented: i * 16, displayAt: NaN })
    for (let i = 0; i < 200; i++) lock.shown({ presented: i * 16, displayAt: i * 16 - 5 })
    assert.equal(shifts.length, 0)
  })
})

describe('a connection turning uneven', () => {
  test('is noticed after a few seconds, and forgiven after a long while', () => {
    const said: boolean[] = []
    const w = new SteadinessWatch((u) => said.push(u))
    let t = 0
    for (let i = 0; i < 300; i++) { t += FRAME; w.shown({ presented: t, displayAt: t }) }
    assert.deepEqual(said, [])
    for (let i = 0; i < 300; i++) { t += i % 4 ? 8 : 60; w.shown({ presented: t, displayAt: t }) }
    assert.deepEqual(said, [true])
    for (let i = 0; i < 400; i++) { t += FRAME; w.shown({ presented: t, displayAt: t }) }
    assert.deepEqual(said, [true], 'seven seconds of calm is not enough')
    for (let i = 0; i < 2000; i++) { t += FRAME; w.shown({ presented: t, displayAt: t }) }
    assert.deepEqual(said, [true, false])
  })
})

describe('what a person is told', () => {
  const fine = { frames: 1000, total: 15, worst: 20, core: 2, encode: 3, toNetwork: 1, network: 1, decode: 1, display: 6 }
  test('is nothing when all is well', () => {
    assert.equal(adviceFor(fine, { fps: 60, gameFps: 60, rttMs: 2, jitterMs: 1 }), '')
  })
  test('names the biggest real limit', () => {
    assert.match(adviceFor(fine, { fps: 60, gameFps: 30, rttMs: 2 }), /full speed/)
    assert.match(adviceFor(fine, { fps: 60, gameFps: 60, rttMs: 80 }), /network/)
    assert.match(adviceFor({ ...fine, worst: 100 }, { fps: 60, gameFps: 60, rttMs: 2 }), /uneven/)
    assert.match(adviceFor({ ...fine, encode: 30 }, { fps: 60, gameFps: 60, rttMs: 2 }), /encode/)
    assert.match(adviceFor({ ...fine, decode: 30 }, { fps: 60, gameFps: 60, rttMs: 2 }), /decode/)
  })
})

describe('a running middle', () => {
  test('forgets what is old', () => {
    const s = new Series(3)
    for (const v of [100, 100, 100, 1, 2, 3]) s.add(v)
    assert.equal(s.count, 3)
    assert.equal(s.median, 2)
    assert.equal(s.p95, 3)
  })
  test('ignores what is not a number', () => {
    const s = new Series()
    s.add(NaN)
    s.add(Infinity)
    assert.equal(s.count, 0)
  })
})
