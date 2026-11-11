// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// When a WebRTC stream has really gone. A picture that stops on a connection that's still up
// is the game, its encoder or this machine being busy, and the server has watchdogs of its own
// for those that start another encoder or game within seconds. A new connection wouldn't help:
// it would cost a black moment, a message, and an encoder started again on a machine already
// short of time. So only these start one: the browser giving up on the connection, a connection
// that stays disconnected, a decoder that has stuck while data still arrives, and, as a last
// resort, nothing arriving at all for longer than the server takes to give up on a game.
export const STALL_MS = 1500 // a picture this late is said to have stopped
const STUCK_MS = 5000 // data arriving and nothing decoded for this long: the decoder is stuck
const SILENT_MS = 35000 // nothing at all for this long, on a connection that says it's up
const DISCONNECTED_MS = 8000 // browsers often come back from disconnected by themselves
const LATE_TICK_MS = 3000 // a check this late means the page was asleep, or too busy to look

// What the watchdog needs of a peer connection: its stats.
type Stats = { getStats(): Promise<{ forEach(fn: (s: any) => void): void }>; readonly connectionState: string }

// countFrames counts the pictures a <video> puts on the screen. requestVideoFrameCallback is the
// count to trust. getVideoPlaybackQuality is only the fallback: Firefox's stays at 0 for a WebRTC
// stream however many pictures it shows (measured, Firefox 159: 0, while 121 were shown in 2 s),
// and a count that never moves looks like a picture that has stopped. -1 is "can't count".
export function countFrames(v: any): { count: () => number; stop: () => void } {
  if (typeof v.requestVideoFrameCallback === 'function') {
    let n = 0, id = 0, stopped = false
    const tick = () => {
      n++
      if (!stopped) id = v.requestVideoFrameCallback(tick)
    }
    id = v.requestVideoFrameCallback(tick)
    return { count: () => n, stop: () => { stopped = true; v.cancelVideoFrameCallback?.(id) } }
  }
  return { count: () => (v.getVideoPlaybackQuality ? v.getVideoPlaybackQuality().totalVideoFrames : -1), stop: () => {} }
}

// Watchdog decides when a WebRTC stream has gone, as above, and says when its picture has stopped
// and started again. count is the pictures shown; decoded, where given, is the pictures decoded, for
// a transport that decodes them itself. A count that stops isn't taken on its own word: the picture
// is said to have stopped only when the connection's own stats say nothing new is being decoded
// either, so a browser that counts badly can't leave "Waiting for the picture" up over a game.
export class Watchdog {
  private timer = 0
  private leaving = 0
  private lastTick = 0
  private shown = -1
  private shownAt = 0
  private bytes = -1
  private bytesAt = 0
  private decoded = -1
  private decodedAt = 0
  private checking = false
  private stalled = false
  private done = false

  private pc: Stats
  private count: () => number
  private lost: (why: string) => void
  private onStall: (stalled: boolean) => void
  private ownDecoded?: () => number

  // Written out rather than as parameter properties, so plain Node can run its tests.
  constructor(pc: Stats, count: () => number, lost: (why: string) => void, onStall: (stalled: boolean) => void, ownDecoded?: () => number) {
    this.pc = pc
    this.count = count
    this.lost = lost
    this.onStall = onStall
    this.ownDecoded = ownDecoded
  }

  start() {
    const now = performance.now()
    this.lastTick = this.shownAt = this.bytesAt = this.decodedAt = now
    this.timer = window.setInterval(() => this.tick(), 1000)
  }

  stop() {
    this.done = true
    clearInterval(this.timer)
    clearTimeout(this.leaving)
  }

  // From the connection's state changes.
  state(st: string) {
    if (st === 'failed' || st === 'closed') return this.lost(st)
    clearTimeout(this.leaving)
    if (st === 'disconnected') this.leaving = window.setTimeout(() => this.disconnected(), DISCONNECTED_MS)
  }

  // Still disconnected. Pictures still arriving mean it isn't gone at all.
  private disconnected() {
    if (this.done || this.pc.connectionState !== 'disconnected') return
    if (performance.now() - this.shownAt < STALL_MS) {
      this.leaving = window.setTimeout(() => this.disconnected(), DISCONNECTED_MS)
      return
    }
    this.lost('disconnected')
  }

  private setStalled(on: boolean) {
    if (on === this.stalled) return
    this.stalled = on
    this.onStall(on)
  }

  tick() {
    const now = performance.now()
    const late = now - this.lastTick > LATE_TICK_MS
    this.lastTick = now
    // Nothing is drawn in a hidden tab, and a page that has been asleep has seen nothing.
    if (document.hidden || late) {
      this.shownAt = this.bytesAt = this.decodedAt = now
      return
    }
    const n = this.count()
    if (n < 0) return
    if (n !== this.shown) {
      this.shown = n
      this.shownAt = now
      this.setStalled(false)
      return
    }
    if (now - this.shownAt < STALL_MS || this.checking) return
    this.checking = true
    // Only now is it worth asking what's arriving, and whether pictures are still being made.
    void this.pc.getStats().then((report) => {
      if (this.done) return
      let bytes = 0, decoded = 0
      report.forEach((s: any) => {
        if (s.type === 'inbound-rtp' && s.kind === 'video') { bytes = s.bytesReceived || 0; decoded = s.framesDecoded || 0 }
      })
      if (this.ownDecoded) decoded = this.ownDecoded()
      const t = performance.now()
      const first = this.decoded < 0
      if (bytes !== this.bytes) { this.bytes = bytes; this.bytesAt = t }
      if (decoded !== this.decoded) {
        this.decoded = decoded
        this.decodedAt = t
        // Pictures are still being made, so they're being shown: the count just isn't counting
        // them. The first look only learns where decoding is.
        if (!first) {
          this.shownAt = t
          this.setStalled(false)
          return
        }
      }
      if (first) return
      this.setStalled(true)
      const arriving = t - this.bytesAt < STALL_MS + 1000
      if (arriving && t - this.decodedAt > STUCK_MS) this.lost('decoder stuck')
      else if (t - this.bytesAt > SILENT_MS) this.lost('nothing arriving')
    }).catch(() => {}).finally(() => { this.checking = false })
  }
}
