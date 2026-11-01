// How frames reach the browser. Three ways, tried in this order, all behind one
// interface so the rest of the player never knows which is in use:
//   1. WebRTC: lowest latency, the normal case
//   2. WebSocket with WebCodecs: when UDP is blocked or only HTTP gets through
//   3. WebSocket with JPEG: when nothing else works
// docs/GAMING.md has the reasoning.

import { DirectVideo, hasDirect } from './direct'
import { PhaseLock, SteadinessWatch, TIMING_LATENCY, TIMING_SHIFT, TIMING_SUBSCRIBE, watchVideo, type LatencyMeter } from './latency'

export type LatencyMode = 'auto' | 'fastest' | 'balanced' | 'steady'
export type TransportKind = 'webrtc' | 'webrtc-direct' | 'ws-h264' | 'ws-jpeg'
export type Mode = 'auto' | TransportKind

export const TRANSPORT_LABELS: Record<TransportKind, string> = {
  webrtc: 'WebRTC (lowest latency)',
  'webrtc-direct': 'WebRTC, drawn directly (can be faster on some screens, and can tear)',
  'ws-h264': 'WebSocket with H.264',
  'ws-jpeg': 'WebSocket with JPEG (most compatible)',
}

export type Stats = { rttMs?: number; fps?: number; kbps?: number; jitterMs?: number; lost?: number; dropped?: number; codec?: string; decodeMs?: number }

// How much sound to hold back before playing it, against crackling on a poor connection.
export type AudioBuffer_ = 'lowest' | 'balanced' | 'smooth'
const AUDIO_HOLD: Record<AudioBuffer_, number> = { lowest: 0, balanced: 0.06, smooth: 0.15 }

export type PlayEvent = { type: string; port?: number; strength?: number; text?: string; effect?: number; aspect?: number; width?: number; height?: number; turns?: number; index?: number; count?: number }

export interface Transport {
  readonly kind: TransportKind
  readonly element: HTMLVideoElement | HTMLCanvasElement
  connect(): Promise<void> // resolves once the first frame has arrived
  send(msg: Uint8Array): void
  close(): void
  stats(): Promise<Stats>
  // True when the browser refused to start sound without a tap.
  needsUnmute(): boolean
  unmute(): void
  setVolume(volume: number, muted: boolean): void
  setAudioBuffer(b: AudioBuffer_): void
  // The stream a clip records, when this way of connecting has one.
  captureStream(): MediaStream | null
  // Measures every stage of the path into a meter; null stops. Costs nothing until asked.
  measure(meter: LatencyMeter | null): void
  // How to trade speed against steadiness, where this way of connecting has the choice.
  setLatency(mode: LatencyMode): void
  onEvent?: (e: PlayEvent) => void
  onLost?: () => void
}

// The timing messages' common half: pings to line the clocks up, quickly at first and
// then once in a while, and a request for frame records.
class TimingLink {
  meter: LatencyMeter | null = null
  private timer = 0
  private count = 0
  constructor(private out: (b: ArrayBuffer) => void) {}

  start(meter: LatencyMeter | null) {
    this.meter = meter
    this.stop()
    const sub = new ArrayBuffer(2)
    new DataView(sub).setUint8(0, TIMING_SUBSCRIBE)
    new DataView(sub).setUint8(1, meter ? 1 : 0)
    this.out(sub)
    if (!meter) return
    this.count = 0
    const tick = () => {
      if (!this.meter) return
      this.out(this.meter.clock.ping())
      this.timer = window.setTimeout(tick, ++this.count < 20 ? 50 : 1000)
    }
    tick()
  }

  stop() { clearTimeout(this.timer) }

  incoming(b: ArrayBuffer) { this.meter?.message(b) }

  setLatency(mode: LatencyMode) {
    const b = new ArrayBuffer(2)
    new DataView(b).setUint8(0, TIMING_LATENCY)
    new DataView(b).setUint8(1, mode === 'balanced' ? 1 : mode === 'steady' ? 2 : 0)
    this.out(b)
  }

  shift(ms: number) {
    const b = new ArrayBuffer(3)
    new DataView(b).setUint8(0, TIMING_SHIFT)
    new DataView(b).setInt16(1, Math.max(-32000, Math.min(32000, Math.round(ms * 10))), true)
    this.out(b)
  }
}

export type Signal = {
  answer(sdp: string, prefer?: string): Promise<string>
  wsUrl(video: string): string
}

const hasWebRTC = () => typeof RTCPeerConnection !== 'undefined'
const hasWebCodecs = () => typeof (window as any).VideoDecoder !== 'undefined' && typeof (window as any).EncodedVideoChunk !== 'undefined'

// What this browser can try, best first.
// The server says which modes it has (`allowed`); this says which this browser can use.
export function plan(mode: Mode, allowed: string[]): TransportKind[] {
  // Drawn directly is one that someone has to ask for. If it can't work here, the usual
  // way takes over.
  if (mode === 'webrtc-direct') return ['webrtc-direct', ...plan('auto', allowed)]
  if (mode !== 'auto') return [mode]
  const out: TransportKind[] = []
  if (hasWebRTC()) out.push('webrtc')
  if (hasWebCodecs()) out.push('ws-h264')
  out.push('ws-jpeg')
  return out.filter((k) => allowed.includes(k))
}

export function make(kind: TransportKind, signal: Signal): Transport {
  if (kind === 'webrtc') return new WebRTCTransport(signal)
  if (kind === 'webrtc-direct') return new DirectTransport(signal)
  return new WSTransport(signal, kind)
}

const FIRST_FRAME_MS = 12000
const DISCONNECTED_MS = 2500
const STALLED_MS = 4000

class WebRTCTransport implements Transport {
  readonly kind = 'webrtc'
  readonly element: HTMLVideoElement
  onEvent?: (e: PlayEvent) => void
  onLost?: () => void
  private pc: RTCPeerConnection | null = null
  private input: RTCDataChannel | null = null
  private muted = false
  private userMuted = false
  private audioRx: RTCRtpReceiver | null = null
  private hold = 0
  private prev: { t: number; bytes: number; frames: number } | null = null
  private closed = false
  private timingDc: RTCDataChannel | null = null
  private link = new TimingLink((b) => { if (this.timingDc?.readyState === 'open') { try { this.timingDc.send(b) } catch { /* the next one will do */ } } })
  private wanted: LatencyMeter | null = null
  private unwatch: (() => void) | null = null
  private latency: LatencyMode = 'auto'
  private unlock: (() => void) | null = null
  lock: PhaseLock | null = null // what's moving the frames, for the test page to look at
  private leaving = 0
  private watchdog = 0
  private framesSeen = -1
  private framesAt = 0
  private gone = false

  constructor(private signal: Signal) {
    const v = document.createElement('video')
    v.playsInline = true
    v.autoplay = true
    v.setAttribute('playsinline', '')
    v.disablePictureInPicture = true
    this.element = v
  }

  async connect() {
    const pc = new RTCPeerConnection()
    this.pc = pc
    const stream = new MediaStream()
    this.element.srcObject = stream
    pc.ontrack = (e) => {
      stream.addTrack(e.track)
      // Ask the browser to keep its receive buffer as small as it can.
      const r = e.receiver as any
      if ('playoutDelayHint' in r) r.playoutDelayHint = 0
      if ('jitterBufferTarget' in r) r.jitterBufferTarget = 0
      if (e.track.kind === 'audio') {
        this.audioRx = e.receiver
        this.applyHold()
      }
    }
    pc.addTransceiver('video', { direction: 'recvonly' })
    pc.addTransceiver('audio', { direction: 'recvonly' })
    this.input = pc.createDataChannel('input', { ordered: false, maxRetransmits: 0 })
    this.input.binaryType = 'arraybuffer'
    const events = pc.createDataChannel('events')
    events.onmessage = (m) => { try { this.onEvent?.(JSON.parse(m.data)) } catch { /* not ours */ } }
    const timing = pc.createDataChannel('timing', { ordered: false, maxRetransmits: 0 })
    timing.binaryType = 'arraybuffer'
    timing.onmessage = (m) => { if (m.data instanceof ArrayBuffer) this.link.incoming(m.data) }
    timing.onopen = () => {
      if (this.wanted) this.measure(this.wanted)
      this.setLatency(this.latency)
    }
    this.timingDc = timing
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === 'failed' || pc.connectionState === 'closed') this.lost()
      // A browser waits half a minute before it calls this failed. Nobody playing a game should.
      clearTimeout(this.leaving)
      if (pc.connectionState === 'disconnected') this.leaving = window.setTimeout(() => this.lost(), DISCONNECTED_MS)
    }
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    await gathered(pc, 1500)
    const answer = await this.signal.answer(pc.localDescription!.sdp)
    await pc.setRemoteDescription({ type: 'answer', sdp: answer })
    await this.play()
    await firstFrame(this.element, FIRST_FRAME_MS)
    this.watchdog = window.setInterval(() => this.checkFrames(), 1000)
  }

  private lost() {
    if (this.closed || this.gone) return
    this.gone = true
    this.onLost?.()
  }

  // The server keeps sending frames even while a game is paused or loading, so a picture
  // that has stopped on a page someone can see means something in between has stuck: the
  // decoder, the network or the browser. Starting the connection again clears any of them.
  private checkFrames() {
    const now = performance.now()
    if (document.hidden) { this.framesAt = now; return } // nothing is drawn in a hidden tab
    const count = this.element.getVideoPlaybackQuality?.().totalVideoFrames ?? -1
    if (count !== this.framesSeen) {
      this.framesSeen = count
      this.framesAt = now
    } else if (count >= 0 && now - this.framesAt > STALLED_MS) this.lost()
  }

  private async play() {
    try { await this.element.play() } catch {
      // Autoplay rules: start without sound, and the player offers a tap.
      this.muted = true
      this.element.muted = true
      try { await this.element.play() } catch { /* a tap will start it */ }
    }
  }

  needsUnmute() { return this.muted && !this.userMuted }
  unmute() { this.muted = false; this.element.muted = this.userMuted; void this.element.play().catch(() => {}) }
  setVolume(volume: number, muted: boolean) {
    this.userMuted = muted
    this.element.volume = Math.max(0, Math.min(1, volume))
    this.element.muted = muted || this.muted
  }
  setAudioBuffer(b: AudioBuffer_) { this.hold = AUDIO_HOLD[b] ?? 0; this.applyHold() }
  private applyHold() {
    const r = this.audioRx as any
    if (r && 'jitterBufferTarget' in r) r.jitterBufferTarget = this.hold * 1000
  }
  captureStream() { return this.element.srcObject as MediaStream | null }

  send(msg: Uint8Array) {
    if (this.input?.readyState === 'open') {
      try { this.input.send(msg as Uint8Array<ArrayBuffer>) } catch { /* the next state corrects it */ }
    }
  }

  close() {
    this.closed = true
    this.link.stop()
    this.unwatch?.()
    this.unlock?.()
    clearTimeout(this.leaving)
    clearInterval(this.watchdog)
    this.pc?.close()
    this.element.srcObject = null
  }

  measure(meter: LatencyMeter | null) {
    this.wanted = meter
    this.unwatch?.()
    this.unwatch = meter ? watchVideo(this.element, (p) => meter.shown(p)) : null
    if (this.timingDc?.readyState === 'open' || !meter) this.link.start(meter)
  }

  // Says how to trade speed against steadiness. Fastest and balanced also land frames just
  // before the display wants them. Both hold good for the rest of the connection.
  setLatency(mode: LatencyMode) {
    this.latency = mode
    if (this.timingDc?.readyState !== 'open') return // sent when it opens
    this.unlock?.()
    this.unlock = null
    if (mode === 'steady') {
      this.link.setLatency('steady')
      return
    }
    // Automatic starts as fastest and goes to balanced while frames arrive unevenly.
    let now: 'fastest' | 'balanced' = mode === 'balanced' ? 'balanced' : 'fastest'
    this.link.setLatency(now)
    const margin = () => (now === 'fastest' ? 3 : 6)
    const lock = new PhaseLock((ms) => this.link.shift(ms), margin())
    this.lock = lock
    const auto = mode === 'auto' ? new SteadinessWatch((uneven) => {
      const next = uneven ? 'balanced' : 'fastest'
      if (next === now) return
      now = next
      lock.margin = margin()
      this.link.setLatency(next)
    }) : null
    this.unlock = watchVideo(this.element, (p) => { lock.shown(p); auto?.shown(p) })
  }

  async stats(): Promise<Stats> {
    const out: Stats = {}
    if (!this.pc) return out
    const report = await this.pc.getStats()
    const codecs = new Map<string, string>()
    report.forEach((s: any) => { if (s.type === 'codec') codecs.set(s.id, s.mimeType) })
    report.forEach((s: any) => {
      if (s.type === 'inbound-rtp' && s.kind === 'video') {
        out.lost = s.packetsLost
        out.dropped = s.framesDropped
        out.jitterMs = s.jitter != null ? Math.round(s.jitter * 1000) : undefined
        out.codec = codecs.get(s.codecId)?.replace('video/', '')
        if (s.framesDecoded && s.totalDecodeTime) out.decodeMs = Math.round((s.totalDecodeTime / s.framesDecoded) * 1000 * 10) / 10
        const now = s.timestamp as number
        if (this.prev && now > this.prev.t) {
          const dt = (now - this.prev.t) / 1000
          out.kbps = Math.round(((s.bytesReceived - this.prev.bytes) * 8) / 1000 / dt)
          out.fps = Math.round((s.framesDecoded - this.prev.frames) / dt)
        }
        this.prev = { t: now, bytes: s.bytesReceived, frames: s.framesDecoded }
      }
      if (s.type === 'candidate-pair' && s.nominated && s.currentRoundTripTime != null) out.rttMs = Math.round(s.currentRoundTripTime * 1000)
    })
    return out
  }
}

// WebRTC with frames taken as soon as they're whole, and drawn at once: see direct.ts.
class DirectTransport implements Transport {
  readonly kind = 'webrtc-direct'
  readonly element: HTMLCanvasElement
  onEvent?: (e: PlayEvent) => void
  onLost?: () => void
  private pc: RTCPeerConnection | null = null
  private input: RTCDataChannel | null = null
  private timingDc: RTCDataChannel | null = null
  private link = new TimingLink((b) => { if (this.timingDc?.readyState === 'open') { try { this.timingDc.send(b) } catch { /* the next one will do */ } } })
  private video: DirectVideo
  private sound = document.createElement('audio')
  private muted = false
  private userMuted = false
  private closed = false
  private gone = false
  private meter: LatencyMeter | null = null
  private watchdog = 0
  private seen = 0
  private seenAt = 0
  private leaving = 0
  private prev: { t: number; bytes: number; frames: number } | null = null

  constructor(private signal: Signal) {
    this.video = new DirectVideo((b) => { if (this.timingDc?.readyState === 'open') { try { this.timingDc.send(b) } catch { /* it will ask again */ } } })
    this.element = this.video.canvas
    this.video.onFrame = (f) => this.meter?.shown({ rtp: f.rtp, received: f.received, presented: f.drawn, displayAt: f.drawn })
  }

  async connect() {
    if (!hasDirect()) throw new Error('this browser cannot take frames as they arrive')
    await this.video.start()
    const pc = new RTCPeerConnection({ encodedInsertableStreams: true } as RTCConfiguration)
    this.pc = pc
    const sound = new MediaStream()
    this.sound.srcObject = sound
    this.sound.autoplay = true
    pc.ontrack = (e) => {
      const r = e.receiver as any
      const { readable, writable } = r.createEncodedStreams()
      if (e.track.kind === 'video') {
        void this.video.pump(readable).catch(() => this.lost())
        return
      }
      if ('jitterBufferTarget' in r) r.jitterBufferTarget = 0
      void readable.pipeTo(writable).catch(() => {})
      sound.addTrack(e.track)
    }
    pc.addTransceiver('video', { direction: 'recvonly' })
    pc.addTransceiver('audio', { direction: 'recvonly' })
    this.input = pc.createDataChannel('input', { ordered: false, maxRetransmits: 0 })
    this.input.binaryType = 'arraybuffer'
    const events = pc.createDataChannel('events')
    events.onmessage = (m) => { try { this.onEvent?.(JSON.parse(m.data)) } catch { /* not ours */ } }
    const timing = pc.createDataChannel('timing', { ordered: false, maxRetransmits: 0 })
    timing.binaryType = 'arraybuffer'
    timing.onmessage = (m) => { if (m.data instanceof ArrayBuffer) this.link.incoming(m.data) }
    timing.onopen = () => {
      this.video.tellServer()
      if (this.meter) this.link.start(this.meter)
    }
    this.timingDc = timing
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === 'failed' || pc.connectionState === 'closed') this.lost()
      clearTimeout(this.leaving)
      if (pc.connectionState === 'disconnected') this.leaving = window.setTimeout(() => this.lost(), DISCONNECTED_MS)
    }
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    await gathered(pc, 1500)
    const answer = await this.signal.answer(pc.localDescription!.sdp)
    await pc.setRemoteDescription({ type: 'answer', sdp: answer })
    // Not waited for: it finishes when sound arrives, and some games take a while to make any.
    void this.sound.play().catch(() => { this.muted = true })
    const start = performance.now()
    while (this.video.framesDrawn === 0) {
      if (performance.now() - start > FIRST_FRAME_MS) throw new Error('no picture arrived')
      await new Promise((r) => setTimeout(r, 20))
    }
    this.seenAt = performance.now()
    this.watchdog = window.setInterval(() => {
      if (document.hidden || this.video.framesDrawn !== this.seen) {
        this.seen = this.video.framesDrawn
        this.seenAt = performance.now()
      } else if (performance.now() - this.seenAt > STALLED_MS) this.lost()
    }, 1000)
  }

  private lost() {
    if (this.closed || this.gone) return
    this.gone = true
    this.onLost?.()
  }

  send(msg: Uint8Array) {
    if (this.input?.readyState === 'open') {
      try { this.input.send(msg as Uint8Array<ArrayBuffer>) } catch { /* the next state corrects it */ }
    }
  }

  close() {
    this.closed = true
    this.link.stop()
    clearTimeout(this.leaving)
    clearInterval(this.watchdog)
    this.video.close()
    this.pc?.close()
    this.sound.srcObject = null
  }

  measure(meter: LatencyMeter | null) {
    this.meter = meter
    if (this.timingDc?.readyState === 'open' || !meter) this.link.start(meter)
  }

  // There's no buffer here to choose the size of.
  setLatency(_mode: LatencyMode) {}

  needsUnmute() { return this.muted && !this.userMuted }
  unmute() { this.muted = false; this.sound.muted = this.userMuted; void this.sound.play().catch(() => {}) }
  setVolume(volume: number, muted: boolean) {
    this.userMuted = muted
    this.sound.volume = Math.max(0, Math.min(1, volume))
    this.sound.muted = muted
  }
  setAudioBuffer(_b: AudioBuffer_) {}
  captureStream() { return (this.element as any).captureStream?.(30) || null }

  async stats(): Promise<Stats> {
    const out: Stats = { codec: 'H264' }
    if (!this.pc) return out
    const report = await this.pc.getStats()
    report.forEach((s: any) => {
      if (s.type === 'inbound-rtp' && s.kind === 'video') {
        out.lost = s.packetsLost
        out.jitterMs = s.jitter != null ? Math.round(s.jitter * 1000) : undefined
        const now = s.timestamp as number
        if (this.prev && now > this.prev.t) {
          const dt = (now - this.prev.t) / 1000
          out.kbps = Math.round(((s.bytesReceived - this.prev.bytes) * 8) / 1000 / dt)
          out.fps = Math.round((this.video.framesDrawn - this.prev.frames) / dt)
        }
        this.prev = { t: now, bytes: s.bytesReceived, frames: this.video.framesDrawn }
      }
      if (s.type === 'candidate-pair' && s.nominated && s.currentRoundTripTime != null) out.rttMs = Math.round(s.currentRoundTripTime * 1000)
    })
    return out
  }
}

function gathered(pc: RTCPeerConnection, maxMs: number) {
  return new Promise<void>((resolve) => {
    if (pc.iceGatheringState === 'complete') return resolve()
    const done = () => { if (pc.iceGatheringState === 'complete') resolve() }
    pc.addEventListener('icegatheringstatechange', done)
    setTimeout(resolve, maxMs) // what was found by now is enough on most networks
  })
}

function firstFrame(v: HTMLVideoElement, ms: number) {
  return new Promise<void>((resolve, reject) => {
    const t = setTimeout(() => { clearInterval(iv); reject(new Error('no picture arrived')) }, ms)
    const iv = setInterval(() => {
      if (v.videoWidth > 0) { clearTimeout(t); clearInterval(iv); resolve() }
    }, 50)
  })
}

// ---- WebSocket ----

const MSG_H264 = 1, MSG_JPEG = 2, MSG_PCM = 3

class WSTransport implements Transport {
  readonly element: HTMLCanvasElement
  onEvent?: (e: PlayEvent) => void
  onLost?: () => void
  private ws: WebSocket | null = null
  private ctx: CanvasRenderingContext2D
  private decoder: any = null
  private audio: AudioContext | null = null
  private audioAt = 0
  private frames = 0
  private bytes = 0
  private statT = performance.now()
  private ts = 0
  private needKey = true
  private first: (() => void) | null = null
  private closed = false
  private drawing = false // JPEG: the newest image wins
  private gain: GainNode | null = null
  private volume = 1
  private userMuted = false
  private hold = 0
  private meter: LatencyMeter | null = null
  private link = new TimingLink((b) => { if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(b) })

  constructor(private signal: Signal, readonly kind: 'ws-h264' | 'ws-jpeg') {
    this.element = document.createElement('canvas')
    this.element.width = 256
    this.element.height = 224
    this.ctx = this.element.getContext('2d', { alpha: false, desynchronized: true } as CanvasRenderingContext2DSettings) as CanvasRenderingContext2D
  }

  async connect() {
    if (this.kind === 'ws-h264') await this.makeDecoder()
    await new Promise<void>((resolve, reject) => {
      const t = setTimeout(() => reject(new Error('no picture arrived')), FIRST_FRAME_MS)
      this.first = () => { clearTimeout(t); resolve() }
      const ws = new WebSocket(this.signal.wsUrl(this.kind === 'ws-h264' ? 'h264' : 'jpeg'))
      ws.binaryType = 'arraybuffer'
      this.ws = ws
      ws.onmessage = (m) => this.message(m.data)
      ws.onerror = () => { clearTimeout(t); reject(new Error('the connection failed')) }
      ws.onclose = () => { if (!this.closed) this.onLost?.() }
    })
  }

  private async makeDecoder() {
    const VD = (window as any).VideoDecoder
    const config = { codec: 'avc1.42E01F', optimizeForLatency: true }
    const support = await VD.isConfigSupported(config).catch(() => null)
    if (!support?.supported) throw new Error('this browser cannot decode H.264 here')
    this.decoder = new VD({
      output: (f: any) => {
        if (this.element.width !== f.displayWidth || this.element.height !== f.displayHeight) {
          this.element.width = f.displayWidth
          this.element.height = f.displayHeight
        }
        this.ctx.drawImage(f, 0, 0)
        if (this.meter) {
          const now = performance.now()
          this.meter.shown({ seq: f.timestamp, presented: now, displayAt: now })
        }
        f.close()
        this.frames++
        this.first?.()
        this.first = null
      },
      error: () => { this.needKey = true; if (!this.closed) void this.remake() },
    })
    this.decoder.configure(config)
  }

  private async remake() {
    try { this.decoder?.close() } catch { /* already closed */ }
    await this.makeDecoder().catch(() => this.onLost?.())
  }

  private message(data: ArrayBuffer | string) {
    if (typeof data === 'string') {
      try { const e = JSON.parse(data); if (e.type !== 'hello') this.onEvent?.(e) } catch { /* not ours */ }
      return
    }
    const b = new Uint8Array(data)
    this.bytes += b.length
    if (b[0] >= 0xf0) {
      // The newest record is the next picture's, as the server sends them in that order.
      if (b[0] === 0xf4) this.nextSeq = new DataView(data).getUint32(2, true)
      this.link.incoming(data)
      return
    }
    switch (b[0]) {
      case MSG_H264: this.h264(b); break
      case MSG_JPEG: this.jpeg(b); break
      case MSG_PCM: this.pcm(data); break
    }
  }

  private h264(b: Uint8Array) {
    const key = b[1] === 1
    if (this.needKey && !key) return
    if (!this.decoder || this.decoder.state !== 'configured') return
    // A decoder that has fallen behind is skipped ahead, because a late frame is
    // worth nothing in a game.
    if (this.decoder.decodeQueueSize > 3 && !key) { this.needKey = true; return }
    this.needKey = false
    const Chunk = (window as any).EncodedVideoChunk
    // A frame's number goes through the decoder as its timestamp when it's being measured.
    this.ts += 16667
    this.decoder.decode(new Chunk({ type: key ? 'key' : 'delta', timestamp: this.meter ? this.nextSeq : this.ts, data: b.subarray(2) }))
  }

  private nextSeq = 0
  measure(meter: LatencyMeter | null) { this.meter = meter; this.link.start(meter) }
  setLatency(mode: LatencyMode) { this.link.setLatency(mode) }

  private jpeg(b: Uint8Array) {
    if (this.drawing) return
    this.drawing = true
    const blob = new Blob([b.subarray(1) as Uint8Array<ArrayBuffer>], { type: 'image/jpeg' })
    createImageBitmap(blob).then((img) => {
      if (this.element.width !== img.width || this.element.height !== img.height) {
        this.element.width = img.width
        this.element.height = img.height
      }
      this.ctx.drawImage(img, 0, 0)
      img.close()
      this.frames++
      this.first?.()
      this.first = null
    }).catch(() => {}).finally(() => { this.drawing = false })
  }

  // Sound is scheduled in small pieces. Falling too far behind means skipping
  // ahead instead of letting delay grow.
  private pcm(data: ArrayBuffer) {
    if (!this.audio) {
      try { this.audio = new AudioContext({ latencyHint: 'interactive' }) } catch { return }
    }
    if (this.audio.state === 'suspended') return // waiting for a tap
    const rate = new DataView(data).getUint32(1, true)
    const n = Math.floor((data.byteLength - 5) / 4)
    if (n === 0 || rate < 3000 || rate > 384000) return
    const buf = this.audio.createBuffer(2, n, rate)
    const l = buf.getChannelData(0), r = buf.getChannelData(1)
    const v = new DataView(data, 5)
    for (let i = 0; i < n; i++) {
      l[i] = v.getInt16(i * 4, true) / 32768
      r[i] = v.getInt16(i * 4 + 2, true) / 32768
    }
    const now = this.audio.currentTime
    const lead = 0.03 + this.hold
    if (this.audioAt < now) this.audioAt = now + lead
    if (this.audioAt > now + 0.25 + this.hold) this.audioAt = now + lead
    if (!this.gain) {
      this.gain = this.audio.createGain()
      this.gain.connect(this.audio.destination)
      this.applyVolume()
    }
    const src = this.audio.createBufferSource()
    src.buffer = buf
    src.connect(this.gain)
    src.start(this.audioAt)
    this.audioAt += n / rate
  }

  needsUnmute() { return !this.userMuted && (!this.audio || this.audio.state === 'suspended') }
  setVolume(volume: number, muted: boolean) {
    this.volume = Math.max(0, Math.min(1, volume))
    this.userMuted = muted
    this.applyVolume()
  }
  private applyVolume() { if (this.gain) this.gain.gain.value = this.userMuted ? 0 : this.volume }
  setAudioBuffer(b: AudioBuffer_) { this.hold = AUDIO_HOLD[b] ?? 0 }
  captureStream() { return (this.element as any).captureStream?.(30) || null }
  unmute() {
    if (!this.audio) { try { this.audio = new AudioContext({ latencyHint: 'interactive' }) } catch { return } }
    void this.audio.resume().catch(() => {})
  }

  send(msg: Uint8Array) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(msg as Uint8Array<ArrayBuffer>)
  }

  close() {
    this.closed = true
    this.link.stop()
    this.ws?.close()
    try { this.decoder?.close() } catch { /* already closed */ }
    void this.audio?.close().catch(() => {})
  }

  async stats(): Promise<Stats> {
    const now = performance.now()
    const dt = (now - this.statT) / 1000
    const out: Stats = dt > 0 ? { fps: Math.round(this.frames / dt), kbps: Math.round((this.bytes * 8) / 1000 / dt), codec: this.kind === 'ws-h264' ? 'H264' : 'JPEG' } : {}
    this.frames = 0
    this.bytes = 0
    this.statT = now
    return out
  }
}
