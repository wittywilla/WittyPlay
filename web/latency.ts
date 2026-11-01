// Where the time goes between a button and the screen, measured on real frames and
// real presses. The server sends each frame's record, the browser adds what it saw, and
// the two clocks are lined up with pings. Nothing here runs unless something asks for
// numbers: the on-screen stats, or the test page in cmd/wittyplay-probe.

export type FrameRecord = {
  key: boolean
  seq: number
  inputSeq: number
  rtp: number
  size: number
  // All in the browser's clock, milliseconds, performance.now() style.
  inputServer: number
  inputHost: number
  runStart: number
  runEnd: number
  serverIn: number
  encIn: number
  encOut: number
  sent: number
}

export type StageTimes = {
  core: number // the core making the frame, and reading it back from the graphics card
  toServer: number // from the runner to the server
  queue: number // waiting for the encoder
  encode: number
  send: number // from the encoder to the network
  network: number
  decode: number // arrival to the decoded picture being ready, jitter buffer included
  display: number // waiting for the display to show it
  total: number // the frame's first pixel being ready, to the screen
}

export type InputTimes = StageTimes & {
  uplink: number // browser to server
  toRunner: number // server to runner
  waitCore: number // waiting for the core to look
  photon: number // the press to the screen
}

export const TIMING_PING = 0xf1
export const TIMING_PONG = 0xf2
export const TIMING_SUBSCRIBE = 0xf3
export const TIMING_RECORD = 0xf4

const RECORD_SIZE = 1 + 1 + 4 * 4 + 8 * 8

// Reads a frame's record. The server's nanoseconds become the browser's milliseconds.
export function parseRecord(b: ArrayBuffer, toLocal: (unixNanos: bigint) => number): FrameRecord | null {
  if (b.byteLength !== RECORD_SIZE) return null
  const v = new DataView(b)
  if (v.getUint8(0) !== TIMING_RECORD) return null
  const at = (i: number) => {
    const n = v.getBigInt64(18 + i * 8, true)
    return n === 0n ? 0 : toLocal(n)
  }
  return {
    key: v.getUint8(1) === 1, seq: v.getUint32(2, true), inputSeq: v.getUint32(6, true), rtp: v.getUint32(10, true), size: v.getInt32(14, true),
    inputServer: at(0), inputHost: at(1), runStart: at(2), runEnd: at(3), serverIn: at(4), encIn: at(5), encOut: at(6), sent: at(7),
  }
}

// ClockSync lines the server's clock up with this page's, from the fastest pings: a
// ping that came straight back says the most about what the clocks are doing.
export class ClockSync {
  private best = Infinity
  private bestAt = 0
  private offset = 0n // server nanos minus (page ms * 1e6)
  private ready = false

  ping(): ArrayBuffer {
    const b = new ArrayBuffer(9)
    const v = new DataView(b)
    v.setUint8(0, TIMING_PING)
    v.setFloat64(1, performance.now(), true)
    return b
  }

  // Takes a reply. Answers false if it isn't one.
  pong(b: ArrayBuffer): boolean {
    if (b.byteLength !== 17 || new DataView(b).getUint8(0) !== TIMING_PONG) return false
    const v = new DataView(b)
    const sent = v.getFloat64(1, true)
    const server = v.getBigInt64(9, true)
    const now = performance.now()
    const rtt = now - sent
    // An old best stops counting after a while, as clocks drift apart slowly.
    if (rtt <= this.best * 1.2 || now - this.bestAt > 20000) {
      this.best = Math.min(rtt, this.best * 1.2)
      this.bestAt = now
      this.offset = server - BigInt(Math.round(((sent + now) / 2) * 1e6))
      this.ready = true
    }
    return true
  }

  get synced() { return this.ready }
  get rttMs() { return this.best }
  toLocal = (unixNanos: bigint) => Number(unixNanos - this.offset) / 1e6
}

// A running middle value and a high one over the last few hundred samples.
export class Series {
  private a: number[] = []
  constructor(private cap = 600) {}
  add(v: number) {
    if (!Number.isFinite(v)) return
    if (this.a.length >= this.cap) this.a.shift()
    this.a.push(v)
  }
  get count() { return this.a.length }
  percentile(p: number) {
    if (!this.a.length) return NaN
    const s = [...this.a].sort((x, y) => x - y)
    return s[Math.min(s.length - 1, Math.floor(p * s.length))]
  }
  get median() { return this.percentile(0.5) }
  get p95() { return this.percentile(0.95) }
  clear() { this.a = [] }
}

export type StageName = keyof StageTimes
export type InputStageName = 'uplink' | 'toRunner' | 'waitCore' | 'photon'

// What's seen of a frame on the screen. A video element says all of it. A canvas has no
// such callback, so the path that draws on one says when each frame was drawn.
export type Presented = {
  rtp?: number
  seq?: number
  received?: number // when the last of its packets arrived, if known
  presented: number // when it was handed over for showing
  displayAt: number // when it should reach the glass
}

const KEEP = 256

// LatencyMeter joins records to what's on the screen. Records can arrive just before or
// just after their frame shows, so both are held for a moment.
export class LatencyMeter {
  readonly clock = new ClockSync()
  readonly frames: Record<StageName, Series> = {
    core: new Series(), toServer: new Series(), queue: new Series(), encode: new Series(), send: new Series(),
    network: new Series(), decode: new Series(), display: new Series(), total: new Series(),
  }
  readonly presses: Record<InputStageName, Series> = { uplink: new Series(100), toRunner: new Series(100), waitCore: new Series(100), photon: new Series(100) }
  framesSeen = 0
  private records = new Map<number, FrameRecord>()
  private bySeq = new Map<number, FrameRecord>()
  private waiting = new Map<number, Presented>()
  private pressSeq = 0
  private pressAt = new Map<number, number>()
  private lastInput = 0
  // Every press's whole journey, for the test page. Off in normal use.
  log: InputTimes[] | null = null

  // Takes what the server sends on the timing channel.
  message(b: ArrayBuffer) {
    if (this.clock.pong(b)) return
    if (!this.clock.synced) return
    const r = parseRecord(b, this.clock.toLocal)
    if (!r) return
    const pending = this.waiting.get(r.rtp)
    this.records.set(r.rtp, r)
    this.bySeq.set(r.seq & 0xffff, r)
    if (pending) {
      this.waiting.delete(r.rtp)
      this.join(r, pending)
    }
    if (this.records.size > KEEP) {
      const first = this.records.keys().next().value as number
      this.records.delete(first)
    }
    if (this.bySeq.size > KEEP) {
      const first = this.bySeq.keys().next().value as number
      this.bySeq.delete(first)
    }
  }

  // A frame's record by its number, or null if it hasn't arrived.
  bySequence(seq: number): FrameRecord | null { return this.bySeq.get(seq & 0xffff) ?? null }
  byRtp(rtp: number): FrameRecord | null { return this.records.get(rtp) ?? null }

  // Says that a frame has reached the screen.
  shown(p: Presented) {
    this.framesSeen++
    const r = p.seq != null ? this.bySeq.get(p.seq & 0xffff) : p.rtp != null ? this.records.get(p.rtp) : undefined
    if (r) {
      this.join(r, p)
      return
    }
    if (p.rtp != null) {
      this.waiting.set(p.rtp, p)
      if (this.waiting.size > 32) this.waiting.delete(this.waiting.keys().next().value as number)
    }
  }

  // Numbers a press, and says the number to send with it.
  press(at = performance.now()): number {
    this.pressSeq = (this.pressSeq + 1) >>> 0
    if (this.pressSeq === 0) this.pressSeq = 1
    this.pressAt.set(this.pressSeq, at)
    if (this.pressAt.size > 64) this.pressAt.delete(this.pressAt.keys().next().value as number)
    return this.pressSeq
  }

  private join(r: FrameRecord, p: Presented) {
    const f = this.frames
    const t: StageTimes = {
      core: r.runEnd - r.runStart, toServer: r.serverIn - r.runEnd, queue: r.encIn - r.serverIn, encode: r.encOut - r.encIn, send: r.sent - r.encOut,
      network: p.received != null ? p.received - r.sent : NaN, decode: p.received != null ? p.presented - p.received : NaN, display: p.displayAt - p.presented,
      total: p.displayAt - r.runEnd,
    }
    for (const k of Object.keys(t) as StageName[]) f[k].add(t[k])
    this.finishPresses(r, p, t)
  }

  // The first frame on the screen that saw a press is what that press looked like.
  private finishPresses(r: FrameRecord, p: Presented, t: StageTimes) {
    if (!r.inputSeq || r.inputSeq === this.lastInput) return
    this.lastInput = r.inputSeq
    for (const [seq, at] of this.pressAt) {
      if (seq > r.inputSeq) continue
      this.pressAt.delete(seq)
      if (seq !== r.inputSeq) continue // an older press that a newer state replaced
      const it: InputTimes = {
        ...t, uplink: r.inputServer - at, toRunner: r.inputHost - r.inputServer, waitCore: r.runStart - r.inputHost, photon: p.displayAt - at,
      }
      this.presses.uplink.add(it.uplink)
      this.presses.toRunner.add(it.toRunner)
      this.presses.waitCore.add(it.waitCore)
      this.presses.photon.add(it.photon)
      this.log?.push(it)
    }
  }
}

// How a video element reports each frame it shows.
export function watchVideo(v: HTMLVideoElement, onFrame: (p: Presented, meta: any) => void): () => void {
  const el = v as any
  if (!el.requestVideoFrameCallback) return () => {}
  let stop = false
  let id = 0
  const step = (_now: number, meta: any) => {
    if (stop) return
    onFrame({ rtp: meta.rtpTimestamp, received: meta.receiveTime, presented: meta.presentationTime, displayAt: meta.expectedDisplayTime }, meta)
    id = el.requestVideoFrameCallback(step)
  }
  id = el.requestVideoFrameCallback(step)
  return () => { stop = true; el.cancelVideoFrameCallback?.(id) }
}

// The number strip host/stamp.go draws, for the test page. Each cell is read at its middle.
export const STAMP_BITS = 32

export class StampReader {
  private c: CanvasRenderingContext2D
  private w = 320
  private h = 8
  constructor() {
    const canvas = document.createElement('canvas')
    canvas.width = this.w
    canvas.height = this.h
    this.c = canvas.getContext('2d', { willReadFrequently: true })!
    this.c.imageSmoothingEnabled = false
  }

  read(source: HTMLVideoElement | HTMLCanvasElement, srcW: number, srcH: number): { seq: number; input: number } | null {
    if (!srcW || !srcH) return null
    const cw = Math.max(Math.floor(srcW / 40), 4)
    const ch = Math.max(Math.floor(srcH / 28), 4)
    if (cw * STAMP_BITS > srcW) return null
    try {
      this.c.drawImage(source, 0, 0, cw * STAMP_BITS, ch, 0, 0, this.w, this.h)
      const d = this.c.getImageData(0, this.h >> 1, this.w, 1).data
      let bits = 0
      const cell = this.w / STAMP_BITS
      for (let i = 0; i < STAMP_BITS; i++) {
        const x = Math.floor((i + 0.5) * cell)
        const luma = d[x * 4] * 0.299 + d[x * 4 + 1] * 0.587 + d[x * 4 + 2] * 0.114
        bits = (bits * 2) + (luma > 128 ? 1 : 0)
      }
      return { seq: Math.floor(bits / 65536) & 0xffff, input: bits % 65536 }
    } catch { return null }
  }
}
