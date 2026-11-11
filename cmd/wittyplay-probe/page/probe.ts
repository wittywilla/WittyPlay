// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// The test page for wittyplay-probe. It connects the same way the player does, using the
// player's own code, presses buttons on a timer, reads the numbers host/stamp.go draws into
// each picture, and posts what it measured. scripts/build-probe.mjs bundles it.

import { LatencyMeter, Series, StampReader, watchVideo, type Presented } from '../../../web/latency'
import { make, whenShown, type Hello, type LatencyMode, type Signal, type StreamInfo, type Transport, type TransportKind } from '../../../web/transport'

const q = new URLSearchParams(location.search)
const setup = {
  seconds: Number(q.get('seconds') || 30),
  kind: (q.get('kind') || 'webrtc') as TransportKind,
  pressMs: Number(q.get('pressMs') || 250),
  label: q.get('label') || '',
  every: Number(q.get('every') || 0), // post what's been seen this often, for a long run
  latency: q.get('latency') as LatencyMode | null,
  // How a new stream's picture replaces the old: as the player does now, or "0.3.4", as it did
  // up to then, to compare the two.
  swap: q.get('swap') || 'player',
  reconnectEvery: Number(q.get('reconnectEvery') || 0), // seconds; connects again this often, for testing the swap
}

const say = (s: string) => { const e = document.getElementById('status'); if (e) e.textContent = s }
const round = (n: number) => Math.round(n * 100) / 100
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const post = (path: string, o: unknown) => fetch(path, { method: 'POST', body: JSON.stringify(o) })
const summary = (s: Series) => ({ p50: round(s.median), p95: round(s.p95), n: s.count })

const signal = {
  answer: async (sdp: string) => {
    const r = await fetch('/answer', { method: 'POST', body: sdp })
    if (!r.ok) throw new Error(await r.text())
    return r.text()
  },
  wsUrl: (video: string) => `ws://${location.host}/ws?video=${video}`,
  // Version 2 of the stream, when asked for: the player's own way on a server that has it.
  ...(q.get('v2') ? {
    stream: async (hello: Hello, sdp: string) => {
      const r = await fetch('/stream', { method: 'POST', body: JSON.stringify({ hello, sdp }) })
      if (!r.ok) throw new Error(await r.text())
      return r.json() as Promise<{ stream: StreamInfo; sdp: string }>
    },
    streamUrl: () => `ws://${location.host}/stream`,
    hello: (): Hello => ({ v: 2, app: 'wittyplay-probe', device: 'probe', seats: [0] }),
  } : {}),
} as Signal

// One stretch of measuring. A long run has many.
class Window {
  meter = new LatencyMeter()
  glass = new Series(100000)
  photon = new Series(100000)
  handed = new Series(100000)
  gaps = new Series(100000)
  misfiled: Record<string, number> = {}
  waitTrace: number[] = []
  bad = 0
  dark = 0 // frames decoded black after a picture had been seen
  darkRuns = 0
  read = 0
  skipped = 0
  out = 0
  presses = 0
}

let current: Transport | null = null
let win = new Window()
let meterShared = win.meter
let lostAt = 0
const recoveries: number[] = []
const dropWhy: Record<string, number> = {}
let totalFrames = 0
let lastFrameSeq = 0
let reconnecting = false
let forced = 0

// The player's way of coming back: the old picture stays up until a new one is ready.
async function connect(first: boolean) {
  const t = make(setup.kind, signal)
  // What the player shows as "Waiting for the picture": kept where a test driving the browser can
  // read it, since a picture said to have stopped while it plays is a bug a person sees.
  t.onStall = (on) => {
    const w = window as any
    w.__stalls = (w.__stalls || 0) + (on ? 1 : 0)
    w.__stalledNow = on
  }
  t.onLost = (why) => {
    if (current !== t || reconnecting) return
    dropWhy[why] = (dropWhy[why] || 0) + 1
    lostAt = performance.now()
    void reconnect()
  }
  await t.connect()
  const old = current
  current = t
  t.unmute()
  t.setVolume(0, true)
  if (setup.latency) t.setLatency(setup.latency)
  const stage = document.getElementById('stage')!
  if (setup.swap === '0.3.4' && old) {
    // As the player was: the old connection closed at once, and the new picture put on the page
    // by an effect that React runs after the browser has painted.
    old.close()
    await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)))
    stage.replaceChildren(t.element)
  } else {
    // As the player is: the new picture goes underneath, and the old goes once it's shown.
    stage.prepend(t.element)
    if (old) whenShown(t.element, () => { old.element.remove(); old.close() })
  }
  t.measure(meterShared)
  watch(t)
  if (lostAt) {
    recoveries.push(round(performance.now() - lostAt))
    lostAt = 0
  }
  if (first) say('warming up')
}

async function reconnect() {
  reconnecting = true
  for (let i = 0; i < 30; i++) {
    try {
      await connect(false)
      reconnecting = false
      return
    } catch { await sleep(1000) }
  }
  reconnecting = false
  await post('/results', { error: 'it never came back' })
}

// From the pixels alone: which frame is on the screen, and which press it has seen.
const reader = new StampReader()

// Whether a frame the browser decoded is black, from the middle of the picture: the stamps
// are along the top. A black frame only counts once the game has shown something else.
class Darkness {
  private c: CanvasRenderingContext2D
  private lit = false
  private inRun = false
  constructor() {
    const canvas = document.createElement('canvas')
    canvas.width = 16
    canvas.height = 16
    this.c = canvas.getContext('2d', { willReadFrequently: true })!
  }
  check(source: HTMLVideoElement | HTMLCanvasElement, w: number, h: number, win: Window) {
    if (!w || !h) return
    try {
      this.c.drawImage(source, w / 4, h / 4, w / 2, h / 2, 0, 0, 16, 16)
      const d = this.c.getImageData(0, 0, 16, 16).data
      let sum = 0
      for (let i = 0; i < d.length; i += 4) sum += 0.299 * d[i] + 0.587 * d[i + 1] + 0.114 * d[i + 2]
      const dark = sum / 256 < 12
      if (!dark) { this.lit = true; this.inRun = false; return }
      if (!this.lit) return
      win.dark++
      if (!this.inRun) win.darkRuns++
      this.inRun = true
    } catch { /* a frame that can't be read is counted elsewhere */ }
  }
}
const darkness = new Darkness()
const pressedAt = new Map<number, number>()
const later = new Map<number, Presented>()
let unwatch: (() => void) | null = null
let lastSeq = -1
let seenInput = 0
let lastShown = 0
let traceWaits: number[] = []

function settle() {
  for (const [seq, p] of later) {
    const r = win.meter.bySequence(seq)
    if (!r) {
      if (later.size > 64) later.delete(seq)
      continue
    }
    later.delete(seq)
    win.glass.add(p.displayAt - r.runEnd)
    win.handed.add(p.presented - r.runEnd)
    if (p.rtp != null) {
      const byTime = win.meter.byRtp(p.rtp)
      if (byTime) { const k = String((byTime.seq - r.seq) | 0); win.misfiled[k] = (win.misfiled[k] || 0) + 1 }
    }
  }
}

function watch(t: Transport) {
  unwatch?.()
  const v = t.element as HTMLVideoElement
  const seen = (p: Presented) => {
    totalFrames++
    if (lastShown) win.gaps.add(p.presented - lastShown)
    lastShown = p.presented
    traceWaits.push(p.displayAt - p.presented)
    if (traceWaits.length >= 30) {
      traceWaits.sort((a, b) => a - b)
      win.waitTrace.push(round(traceWaits[15]))
      traceWaits = []
    }
    const size = (v as any).videoWidth ? [v.videoWidth, v.videoHeight] : [(v as any).width, (v as any).height]
    darkness.check(v, size[0], size[1], win)
    const s = reader.read(v, size[0], size[1])
    if (!s) { win.bad++; return }
    win.read++
    if (lastSeq >= 0) {
      const d = (s.seq - lastSeq) & 0xffff
      if (d > 1000 || d === 0) win.out++
      else if (d > 1) win.skipped += d - 1
    }
    lastSeq = s.seq
    lastFrameSeq = s.seq
    later.set(s.seq, p)
    settle()
    if (s.input !== seenInput) {
      seenInput = s.input
      const at = pressedAt.get(s.input)
      if (at != null) { win.photon.add(p.displayAt - at); pressedAt.delete(s.input) }
    }
  }
  if (v.tagName === 'VIDEO') { unwatch = watchVideo(v, seen); return }
  // Drawn directly: it's on the canvas the moment the transport says so.
  const direct = (t as any).video
  if (direct) {
    const inner = direct.onFrame
    direct.onFrame = (f: any) => {
      inner?.(f)
      seen({ rtp: f.rtp, received: f.received, presented: f.drawn, displayAt: f.drawn })
    }
    unwatch = () => { direct.onFrame = inner }
  }
}

// Presses come at no fixed rhythm against the frames, as a person's do. The same state
// never repeats, so each one is followed from here to the screen.
function startPressing() {
  const msg = new Uint8Array(17)
  const view = new DataView(msg.buffer)
  let held = false
  const press = () => {
    held = !held
    view.setUint16(1, held ? 1 << 8 : 0, true) // RetroPad A
    const at = performance.now()
    const seq = meterShared.press(at)
    view.setUint32(13, seq, true)
    pressedAt.set(seq & 0xffff, at)
    if (pressedAt.size > 256) pressedAt.delete(pressedAt.keys().next().value as number)
    current?.send(msg)
    win.presses++
    setTimeout(press, setup.pressMs * (0.6 + Math.random() * 0.8))
  }
  setTimeout(press, 100)
}

function sendBeats() {
  setInterval(() => {
    void post('/beat', { frames: totalFrames, seq: lastFrameSeq, reconnects: recoveries.length, recoveries, dropWhy, forced, heap: (performance as any).memory?.usedJSHeapSize || 0, hidden: document.hidden }).catch(() => {})
  }, 250)
}

function report(w: Window, stats: unknown) {
  const m = w.meter
  return {
    label: setup.label, kind: setup.kind, seconds: setup.seconds, ua: navigator.userAgent,
    frames: Object.fromEntries(Object.entries(m.frames).map(([k, s]) => [k, summary(s)])),
    presses: Object.fromEntries(Object.entries(m.presses).map(([k, s]) => [k, summary(s)])),
    glass: summary(w.glass), photon: summary(w.photon), madeToHanded: summary(w.handed), interval: summary(w.gaps),
    pixels: { read: w.read, bad: w.bad, skipped: w.skipped, out: w.out, pressCount: w.presses, dark: w.dark, darkRuns: w.darkRuns }, misfiled: w.misfiled, stats, waitTrace: w.waitTrace, shifted: (current as any)?.lock?.shifted,
    recoveries, dropWhy, forced, heap: (performance as any).memory?.usedJSHeapSize || 0,
  }
}

// A fresh window of numbers that keeps the one meter, whose clocks are already lined up.
function nextWindow(): Window {
  const old = win
  const fresh = new Window()
  fresh.meter = old.meter
  for (const s of [...Object.values(old.meter.frames), ...Object.values(old.meter.presses)]) s.clear()
  win = fresh
  return old
}

async function main() {
  say('connecting')
  try { await connect(true) } catch (e: any) { await post('/results', { error: String(e?.message || e) }); return }
  sendBeats()
  if (setup.reconnectEvery > 0) {
    setInterval(() => { if (!reconnecting) { forced++; void reconnect() } }, setup.reconnectEvery * 1000)
  }
  await sleep(3000)
  nextWindow()
  startPressing()
  say('measuring')
  if (setup.every > 0) {
    // A long run: a report each stretch, until the program that started this stops it.
    for (;;) {
      await sleep(setup.every * 1000)
      settle()
      const stats = await chromeStats(current)
      await post('/partial', report(win, stats))
      nextWindow()
    }
  }
  await sleep(setup.seconds * 1000)
  await sleep(200)
  settle()
  const stats = await chromeStats(current)
  await post('/shot', await picture())
  await post('/results', { ...report(win, stats), colours: current && reader.colours(current.element, (current.element as any).videoWidth || (current.element as any).width, (current.element as any).videoHeight || (current.element as any).height) })
  say('done')
}

// What's on the screen now, exactly as the browser decoded it.
async function picture() {
  const v = current?.element as any
  const w = v?.videoWidth || v?.width, h = v?.videoHeight || v?.height
  if (!w) return { png: '' }
  const c = document.createElement('canvas')
  c.width = w
  c.height = h
  c.getContext('2d')!.drawImage(v, 0, 0)
  return { png: c.toDataURL('image/png'), seq: reader.read(v, w, h)?.seq }
}

async function chromeStats(t: any) {
  const pc: RTCPeerConnection | undefined = t?.pc
  if (!pc) return {}
  const out: any = {}
  const report = await pc.getStats()
  report.forEach((s: any) => {
    if (s.type === 'inbound-rtp' && s.kind === 'video') {
      out.decoder = s.decoderImplementation
      out.powerEfficient = s.powerEfficientDecoder
      out.frames = s.framesDecoded
      out.dropped = s.framesDropped
      out.lost = s.packetsLost
      out.nacks = s.nackCount
      out.plis = s.pliCount
      out.jitterBufferMs = s.jitterBufferEmittedCount ? round((s.jitterBufferDelay / s.jitterBufferEmittedCount) * 1000) : null
      out.jitterTargetMs = s.jitterBufferEmittedCount ? round((s.jitterBufferTargetDelay / s.jitterBufferEmittedCount) * 1000) : null
      out.decodeMs = s.framesDecoded ? round((s.totalDecodeTime / s.framesDecoded) * 1000) : null
      out.interFrameMs = s.framesDecoded ? round((s.totalInterFrameDelay / s.framesDecoded) * 1000) : null
      out.kbps = s.bytesReceived
    }
    if (s.type === 'inbound-rtp' && s.kind === 'audio') {
      out.audioBufferMs = s.jitterBufferEmittedCount ? round((s.jitterBufferDelay / s.jitterBufferEmittedCount) * 1000) : null
      out.audioConcealed = s.concealedSamples
      out.audioSamples = s.totalSamplesReceived
      out.audioFaster = s.removedSamplesForAcceleration
      out.audioSlower = s.insertedSamplesForDeceleration
    }
    if (s.type === 'candidate-pair' && s.nominated && s.currentRoundTripTime != null) out.rttMs = round(s.currentRoundTripTime * 1000)
  })
  return out
}

main().catch((e) => { void post('/results', { error: String(e) }) })
