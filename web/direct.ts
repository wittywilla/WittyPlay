// WebRTC with the browser's own video path taken out. Frames still arrive as RTP, with lost
// packets asked for again, but they're taken as soon as they're whole and decoded here with
// WebCodecs, then drawn straight onto a canvas that doesn't wait for the page's compositor.
// There's nothing between the network and the display that could hold a frame back, which
// can show a frame a whole refresh sooner. The price is that a frame can be drawn while the
// display is partway through showing the last one, so it's a choice, not the default.
//
// It needs a browser with encoded streams and WebCodecs. Where one is missing, connect fails
// and the player tries the next way.

import { TIMING_DIRECT, TIMING_KEYFRAME } from './latency'

export const hasDirect = () =>
  typeof (window as any).VideoDecoder !== 'undefined' &&
  typeof (window as any).EncodedVideoChunk !== 'undefined' &&
  typeof RTCRtpReceiver !== 'undefined' && 'createEncodedStreams' in RTCRtpReceiver.prototype

export type DirectFrame = { rtp: number; received: number; drawn: number }

export class DirectVideo {
  readonly canvas = document.createElement('canvas')
  onFrame?: (f: DirectFrame) => void
  framesDrawn = 0
  private ctx: CanvasRenderingContext2D
  private decoder: any = null
  private needKey = true
  private closed = false
  private receivedAt = new Map<number, number>()
  private lastAsk = 0

  constructor(private signal: (b: ArrayBuffer) => void) {
    this.canvas.width = 256
    this.canvas.height = 224
    this.ctx = this.canvas.getContext('2d', { alpha: false, desynchronized: true } as CanvasRenderingContext2DSettings) as CanvasRenderingContext2D
  }

  async start() {
    const VD = (window as any).VideoDecoder
    const config = { codec: 'avc1.42E01F', optimizeForLatency: true, hardwareAcceleration: 'prefer-hardware' }
    const ok = await VD.isConfigSupported(config).catch(() => null)
    const use = ok?.supported ? config : { codec: 'avc1.42E01F', optimizeForLatency: true }
    this.decoder = new VD({
      output: (f: any) => this.draw(f),
      error: () => {
        this.needKey = true
        this.askForKey()
        if (!this.closed) this.restart(use)
      },
    })
    this.decoder.configure(use)
    this.tellServer()
  }

  private restart(config: any) {
    try { this.decoder?.close() } catch { /* already closed */ }
    const VD = (window as any).VideoDecoder
    this.decoder = new VD({ output: (f: any) => this.draw(f), error: () => { this.needKey = true; this.askForKey() } })
    this.decoder.configure(config)
  }

  // Tells the server that the browser's own requests for keyframes mean nothing now. Its
  // decoder never sees a frame, so it keeps asking.
  tellServer() {
    const b = new ArrayBuffer(1)
    new DataView(b).setUint8(0, TIMING_DIRECT)
    this.signal(b)
  }

  private askForKey() {
    const now = performance.now()
    if (now - this.lastAsk < 500) return
    this.lastAsk = now
    const b = new ArrayBuffer(1)
    new DataView(b).setUint8(0, TIMING_KEYFRAME)
    this.signal(b)
  }

  // Takes whole frames from the receiver for as long as there are any.
  async pump(readable: ReadableStream<any>) {
    const reader = readable.getReader()
    for (;;) {
      const { value: frame, done } = await reader.read()
      if (done || this.closed) return
      const key = frame.type === 'key'
      if (this.needKey && !key) { this.askForKey(); continue }
      if (!this.decoder || this.decoder.state !== 'configured') continue
      // A decoder that's behind can't catch up by being given more.
      if (this.decoder.decodeQueueSize > 2 && !key) { this.needKey = true; this.askForKey(); continue }
      this.needKey = false
      const rtp = frame.timestamp >>> 0
      this.receivedAt.set(rtp, performance.now())
      if (this.receivedAt.size > 16) this.receivedAt.delete(this.receivedAt.keys().next().value as number)
      const Chunk = (window as any).EncodedVideoChunk
      this.decoder.decode(new Chunk({ type: key ? 'key' : 'delta', timestamp: rtp, data: frame.data }))
    }
  }

  private draw(f: any) {
    if (this.canvas.width !== f.displayWidth || this.canvas.height !== f.displayHeight) {
      this.canvas.width = f.displayWidth
      this.canvas.height = f.displayHeight
    }
    this.ctx.drawImage(f, 0, 0)
    const rtp = f.timestamp >>> 0
    f.close()
    this.framesDrawn++
    const received = this.receivedAt.get(rtp)
    this.receivedAt.delete(rtp)
    this.onFrame?.({ rtp, received: received ?? performance.now(), drawn: performance.now() })
  }

  close() {
    this.closed = true
    try { this.decoder?.close() } catch { /* already closed */ }
  }
}
