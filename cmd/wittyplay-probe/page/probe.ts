// The test page for wittyplay-probe. It connects the same way the player does, using the
// player's own code, presses buttons on a timer, reads the numbers host/stamp.go draws into
// each picture, and posts what it measured. scripts/build-probe.mjs bundles it.

import { LatencyMeter, Series, StampReader, watchVideo, type Presented } from '../../../web/latency'
import { make, type LatencyMode, type Transport, type TransportKind } from '../../../web/transport'

const q = new URLSearchParams(location.search)
const setup = {
  seconds: Number(q.get('seconds') || 30),
  kind: (q.get('kind') || 'webrtc') as TransportKind,
  pressMs: Number(q.get('pressMs') || 250),
  label: q.get('label') || '',
  every: Number(q.get('every') || 0), // post what's been seen this often, for a long run
  latency: q.get('latency') as LatencyMode | null,
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
}

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
let totalFrames = 0
let lastFrameSeq = 0
let reconnecting = false

// The player's way of coming back: the old picture stays up until a new one is ready.
async function connect(first: boolean) {
  const t = make(setup.kind, signal)
  t.onLost = () => {
    if (current !== t || reconnecting) return
    lostAt = performance.now()
    void reconnect()
  }
  await t.connect()
  const old = current
  current = t
  t.unmute()
  t.setVolume(0, true)
  if (setup.latency) t.setLatency(setup.latency)
  document.getElementById('stage')!.replaceChildren(t.element)
  old?.close()
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
  if (v.tagName !== 'VIDEO') return
  unwatch = watchVideo(v, (p: Presented) => {
    totalFrames++
    if (lastShown) win.gaps.add(p.presented - lastShown)
    lastShown = p.presented
    traceWaits.push(p.displayAt - p.presented)
    if (traceWaits.length >= 30) {
      traceWaits.sort((a, b) => a - b)
      win.waitTrace.push(round(traceWaits[15]))
      traceWaits = []
    }
    const s = reader.read(v, v.videoWidth, v.videoHeight)
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
  })
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
    void post('/beat', { frames: totalFrames, seq: lastFrameSeq, reconnects: recoveries.length, recoveries, heap: (performance as any).memory?.usedJSHeapSize || 0, hidden: document.hidden }).catch(() => {})
  }, 250)
}

function report(w: Window, stats: unknown) {
  const m = w.meter
  return {
    label: setup.label, kind: setup.kind, seconds: setup.seconds, ua: navigator.userAgent,
    frames: Object.fromEntries(Object.entries(m.frames).map(([k, s]) => [k, summary(s)])),
    presses: Object.fromEntries(Object.entries(m.presses).map(([k, s]) => [k, summary(s)])),
    glass: summary(w.glass), photon: summary(w.photon), madeToHanded: summary(w.handed), interval: summary(w.gaps),
    pixels: { read: w.read, bad: w.bad, skipped: w.skipped, out: w.out, pressCount: w.presses }, misfiled: w.misfiled, stats, waitTrace: w.waitTrace, shifted: (current as any)?.lock?.shifted,
    recoveries, heap: (performance as any).memory?.usedJSHeapSize || 0,
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
  await post('/results', report(win, stats))
  say('done')
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
