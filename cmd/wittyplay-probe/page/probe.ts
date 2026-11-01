// The test page for wittyplay-probe. It connects the same way the player does, using the
// player's own code, presses buttons on a timer, reads the numbers host/stamp.go draws into
// each picture, and posts what it measured. scripts/build-probe.mjs bundles it.

import { LatencyMeter, Series, StampReader, watchVideo, type InputTimes, type Presented } from '../../../web/latency'
import { make, type LatencyMode, type TransportKind } from '../../../web/transport'

type Setup = { seconds: number; kind: TransportKind; pressMs: number; label: string }

const say = (s: string) => { const e = document.getElementById('status'); if (e) e.textContent = s }

async function main() {
  const q = new URLSearchParams(location.search)
  const setup: Setup = { seconds: Number(q.get('seconds') || 30), kind: (q.get('kind') || 'webrtc') as TransportKind, pressMs: Number(q.get('pressMs') || 250), label: q.get('label') || '' }
  const signal = {
    answer: async (sdp: string) => {
      const r = await fetch('/answer', { method: 'POST', body: sdp })
      if (!r.ok) throw new Error(await r.text())
      return r.text()
    },
    wsUrl: (video: string) => `ws://${location.host}/ws?video=${video}`,
  }
  const t = make(setup.kind, signal)
  const meter = new LatencyMeter()
  meter.log = []
  t.onLost = () => post({ error: 'lost' })
  if (q.get('latency')) t.setLatency(q.get('latency') as LatencyMode)
  document.getElementById('stage')!.appendChild(t.element)
  say('connecting')
  try { await t.connect() } catch (e: any) { await post({ error: String(e?.message || e) }); return }
  t.unmute()
  t.setVolume(0, true)
  say('warming up')
  t.measure(meter)
  await sleep(3000)
  for (const s of [...Object.values(meter.frames), ...Object.values(meter.presses)]) s.clear()
  meter.log = []
  meter.framesSeen = 0

  // From the pixels alone: which frame is on the screen, and which press it has seen.
  const reader = new StampReader()
  const stampLatency = new Series(100000)
  const glass = new Series(100000)
  const photon = new Series(100000)
  const presses = new Map<number, number>()
  let bad = 0, read = 0, firstSeq = -1, lastSeq = -1, skipped = 0, out = 0
  let seenInput = 0
  const v = t.element as HTMLVideoElement
  const interval = new Series(100000)
  const misfiled: Record<string, number> = {}
  const later = new Map<number, Presented>()
  const settle = () => {
    for (const [seq, p] of later) {
      const r = meter.bySequence(seq)
      if (!r) continue
      later.delete(seq)
      glass.add(p.displayAt - r.runEnd)
      stampLatency.add(p.presented - r.runEnd)
      if (p.rtp != null) {
        const byTime = meter.byRtp(p.rtp)
        if (byTime) { const k = String((byTime.seq - r.seq) | 0); misfiled[k] = (misfiled[k] || 0) + 1 }
      }
    }
  }
  let lastShown = 0
  const waitTrace: number[] = []
  let traceWaits: number[] = []
  const stop = watchVideo(v, (p: Presented) => {
    const s = reader.read(v, v.videoWidth, v.videoHeight)
    if (lastShown) interval.add(p.presented - lastShown)
    lastShown = p.presented
    traceWaits.push(p.displayAt - p.presented)
    if (traceWaits.length >= 30) {
      traceWaits.sort((a, b) => a - b)
      waitTrace.push(round(traceWaits[15]))
      traceWaits = []
    }
    if (!s) { bad++; return }
    read++
    if (firstSeq < 0) firstSeq = s.seq
    if (lastSeq >= 0) {
      const d = (s.seq - lastSeq) & 0xffff
      if (d > 1000 || d === 0) out++
      else if (d > 1) skipped += d - 1
    }
    lastSeq = s.seq
    later.set(s.seq, p)
    settle()
    if (s.input !== seenInput) {
      seenInput = s.input
      const at = presses.get(s.input)
      if (at != null) { photon.add(p.displayAt - at); presses.delete(s.input) }
    }
  })

  // Presses come at no fixed rhythm against the frames, as a person's do. The same state
  // never repeats, so each one is followed from here to the screen.
  const msg = new Uint8Array(17)
  const view = new DataView(msg.buffer)
  let held = false, pressCount = 0
  const press = () => {
    held = !held
    view.setUint16(1, held ? 1 << 8 : 0, true) // RetroPad A
    const at = performance.now()
    const seq = meter.press(at)
    view.setUint32(13, seq, true)
    presses.set(seq & 0xffff, at)
    t.send(msg)
    pressCount++
    timer = window.setTimeout(press, setup.pressMs * (0.6 + Math.random() * 0.8))
  }
  let timer = window.setTimeout(press, 100)
  say('measuring')
  await sleep(setup.seconds * 1000)
  clearTimeout(timer)
  await sleep(200)
  settle()
  stop()
  const stats = await chromeStats(t)
  const summary = (s: Series) => ({ p50: round(s.median), p95: round(s.p95), n: s.count })
  await post({
    label: setup.label, kind: setup.kind, seconds: setup.seconds, ua: navigator.userAgent,
    frames: Object.fromEntries(Object.entries(meter.frames).map(([k, s]) => [k, summary(s)])),
    presses: Object.fromEntries(Object.entries(meter.presses).map(([k, s]) => [k, summary(s)])),
    glass: summary(glass), photon: summary(photon), madeToHanded: summary(stampLatency), interval: summary(interval),
    pixels: { read, bad, skipped, out, pressCount }, misfiled, stats, waitTrace, shifted: (t as any).lock?.shifted,
    pressLog: (meter.log as InputTimes[]).slice(0, 8),
  })
  say('done')
}

const round = (n: number) => Math.round(n * 100) / 100
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))
const post = (o: unknown) => fetch('/results', { method: 'POST', body: JSON.stringify(o) })

async function chromeStats(t: any) {
  const pc: RTCPeerConnection | undefined = t.pc
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
      out.assemblyMs = s.framesAssembledFromMultiplePackets ? round((s.totalAssemblyTime / s.framesAssembledFromMultiplePackets) * 1000) : null
      out.interFrameMs = s.framesDecoded ? round((s.totalInterFrameDelay / s.framesDecoded) * 1000) : null
    }
    if (s.type === 'candidate-pair' && s.nominated && s.currentRoundTripTime != null) out.rttMs = round(s.currentRoundTripTime * 1000)
  })
  return out
}

main().catch((e) => { post({ error: String(e) }) })
