(() => {
  // web/latency.ts
  var TIMING_PING = 241;
  var TIMING_PONG = 242;
  var TIMING_SUBSCRIBE = 243;
  var TIMING_RECORD = 244;
  var TIMING_LATENCY = 245;
  var TIMING_SHIFT = 246;
  var TIMING_KEYFRAME = 247;
  var TIMING_DIRECT = 248;
  var RECORD_SIZE = 1 + 1 + 4 * 4 + 8 * 8;
  function parseRecord(b, toLocal) {
    if (b.byteLength !== RECORD_SIZE) return null;
    const v = new DataView(b);
    if (v.getUint8(0) !== TIMING_RECORD) return null;
    const at = (i) => {
      const n = v.getBigInt64(18 + i * 8, true);
      return n === 0n ? 0 : toLocal(n);
    };
    return {
      key: v.getUint8(1) === 1,
      seq: v.getUint32(2, true),
      inputSeq: v.getUint32(6, true),
      rtp: v.getUint32(10, true),
      size: v.getInt32(14, true),
      inputServer: at(0),
      inputHost: at(1),
      runStart: at(2),
      runEnd: at(3),
      serverIn: at(4),
      encIn: at(5),
      encOut: at(6),
      sent: at(7)
    };
  }
  var ClockSync = class {
    best = Infinity;
    bestAt = 0;
    offset = 0n;
    // server nanos minus (page ms * 1e6)
    ready = false;
    ping() {
      const b = new ArrayBuffer(9);
      const v = new DataView(b);
      v.setUint8(0, TIMING_PING);
      v.setFloat64(1, performance.now(), true);
      return b;
    }
    // Takes a reply. Answers false if it isn't one.
    pong(b) {
      if (b.byteLength !== 17 || new DataView(b).getUint8(0) !== TIMING_PONG) return false;
      const v = new DataView(b);
      const sent = v.getFloat64(1, true);
      const server = v.getBigInt64(9, true);
      const now = performance.now();
      const rtt = now - sent;
      if (rtt <= this.best * 1.2 || now - this.bestAt > 2e4) {
        this.best = Math.min(rtt, this.best * 1.2);
        this.bestAt = now;
        this.offset = server - BigInt(Math.round((sent + now) / 2 * 1e6));
        this.ready = true;
      }
      return true;
    }
    get synced() {
      return this.ready;
    }
    get rttMs() {
      return this.best;
    }
    toLocal = (unixNanos) => Number(unixNanos - this.offset) / 1e6;
  };
  var Series = class {
    constructor(cap = 600) {
      this.cap = cap;
    }
    a = [];
    add(v) {
      if (!Number.isFinite(v)) return;
      if (this.a.length >= this.cap) this.a.shift();
      this.a.push(v);
    }
    get count() {
      return this.a.length;
    }
    percentile(p) {
      if (!this.a.length) return NaN;
      const s = [...this.a].sort((x, y) => x - y);
      return s[Math.min(s.length - 1, Math.floor(p * s.length))];
    }
    get median() {
      return this.percentile(0.5);
    }
    get p95() {
      return this.percentile(0.95);
    }
    clear() {
      this.a = [];
    }
  };
  var KEEP = 256;
  var LatencyMeter = class {
    clock = new ClockSync();
    frames = {
      core: new Series(),
      toServer: new Series(),
      queue: new Series(),
      encode: new Series(),
      send: new Series(),
      network: new Series(),
      decode: new Series(),
      display: new Series(),
      total: new Series()
    };
    presses = { uplink: new Series(100), toRunner: new Series(100), waitCore: new Series(100), photon: new Series(100) };
    framesSeen = 0;
    records = /* @__PURE__ */ new Map();
    bySeq = /* @__PURE__ */ new Map();
    waiting = /* @__PURE__ */ new Map();
    pressSeq = 0;
    pressAt = /* @__PURE__ */ new Map();
    lastInput = 0;
    // Every press's whole journey, for the test page. Off in normal use.
    log = null;
    // Takes what the server sends on the timing channel.
    message(b) {
      if (this.clock.pong(b)) return;
      if (!this.clock.synced) return;
      const r = parseRecord(b, this.clock.toLocal);
      if (!r) return;
      const pending = this.waiting.get(r.rtp);
      this.records.set(r.rtp, r);
      this.bySeq.set(r.seq & 65535, r);
      if (pending) {
        this.waiting.delete(r.rtp);
        this.join(r, pending);
      }
      if (this.records.size > KEEP) {
        const first = this.records.keys().next().value;
        this.records.delete(first);
      }
      if (this.bySeq.size > KEEP) {
        const first = this.bySeq.keys().next().value;
        this.bySeq.delete(first);
      }
    }
    // A frame's record by its number, or null if it hasn't arrived.
    bySequence(seq) {
      return this.bySeq.get(seq & 65535) ?? null;
    }
    byRtp(rtp) {
      return this.records.get(rtp) ?? null;
    }
    // Says that a frame has reached the screen.
    shown(p) {
      this.framesSeen++;
      const r = p.seq != null ? this.bySeq.get(p.seq & 65535) : p.rtp != null ? this.records.get(p.rtp) : void 0;
      if (r) {
        this.join(r, p);
        return;
      }
      if (p.rtp != null) {
        this.waiting.set(p.rtp, p);
        if (this.waiting.size > 32) this.waiting.delete(this.waiting.keys().next().value);
      }
    }
    // Numbers a press, and says the number to send with it.
    press(at = performance.now()) {
      this.pressSeq = this.pressSeq + 1 >>> 0;
      if (this.pressSeq === 0) this.pressSeq = 1;
      this.pressAt.set(this.pressSeq, at);
      if (this.pressAt.size > 64) this.pressAt.delete(this.pressAt.keys().next().value);
      return this.pressSeq;
    }
    // What the numbers on screen show: the middle of recent frames and presses.
    summary() {
      const f = this.frames;
      if (f.total.count < 10) return null;
      const value = (s) => s.count ? s.median : void 0;
      return {
        press: this.presses.photon.count >= 3 ? this.presses.photon.median : void 0,
        frames: f.total.count,
        total: f.total.median,
        core: value(f.core),
        encode: value(f.encode),
        toNetwork: (value(f.toServer) ?? 0) + (value(f.queue) ?? 0) + (value(f.send) ?? 0),
        network: value(f.network),
        decode: value(f.decode),
        display: value(f.display),
        worst: f.total.p95
      };
    }
    join(r, p) {
      const f = this.frames;
      const t = {
        core: r.runEnd - r.runStart,
        toServer: r.serverIn - r.runEnd,
        queue: r.encIn - r.serverIn,
        encode: r.encOut - r.encIn,
        send: r.sent - r.encOut,
        network: p.received != null ? p.received - r.sent : NaN,
        decode: p.received != null ? p.presented - p.received : NaN,
        display: p.displayAt - p.presented,
        total: p.displayAt - r.runEnd
      };
      for (const k of Object.keys(t)) f[k].add(t[k]);
      this.finishPresses(r, p, t);
    }
    // The first frame on the screen that saw a press is what that press looked like.
    finishPresses(r, p, t) {
      if (!r.inputSeq || r.inputSeq === this.lastInput) return;
      this.lastInput = r.inputSeq;
      for (const [seq, at] of this.pressAt) {
        if (seq > r.inputSeq) continue;
        this.pressAt.delete(seq);
        if (seq !== r.inputSeq) continue;
        const it = {
          ...t,
          uplink: r.inputServer - at,
          toRunner: r.inputHost - r.inputServer,
          waitCore: r.runStart - r.inputHost,
          photon: p.displayAt - at
        };
        this.presses.uplink.add(it.uplink);
        this.presses.toRunner.add(it.toRunner);
        this.presses.waitCore.add(it.waitCore);
        this.presses.photon.add(it.photon);
        this.log?.push(it);
      }
    }
  };
  var PhaseLock = class {
    constructor(send, margin = 3) {
      this.send = send;
      this.margin = margin;
    }
    waits = [];
    floor = Infinity;
    lastDecision = 0;
    lastShift = 0;
    settled = false;
    gaveUp = false;
    period = 16.7;
    lastDisplay = 0;
    shifted = 0;
    margin;
    shown(p) {
      const wait = p.displayAt - p.presented;
      if (!Number.isFinite(wait) || wait < 0) return;
      if (this.lastDisplay) {
        const d = p.displayAt - this.lastDisplay;
        if (d > 4 && d < 60) this.period += (d - this.period) * 0.02;
      }
      this.lastDisplay = p.displayAt;
      this.waits.push(wait);
      const now = p.presented;
      if (this.waits.length < 20 || now - this.lastDecision < 500) return;
      const sorted = [...this.waits].sort((a, b) => a - b);
      this.waits = [];
      this.lastDecision = now;
      this.decide(sorted[sorted.length >> 1]);
    }
    decide(wait) {
      const T = this.period;
      if (this.gaveUp) return;
      if (!this.settled && this.shifted > T * 1.2) {
        this.gaveUp = true;
        return;
      }
      if (wait - this.floor > T / 2) {
        const back = T - (wait - this.floor) + this.margin + Math.max(this.lastShift, 0);
        this.move(-Math.min(back, T));
        this.settled = true;
        return;
      }
      if (wait < this.floor) this.floor = wait;
      if (!this.settled) {
        this.move(Math.min(2, T / 8));
        return;
      }
      const over = wait - this.floor - this.margin;
      if (over > 2) this.move(Math.min(over, T / 4));
    }
    move(ms) {
      this.lastShift = ms;
      this.shifted += ms;
      this.send(ms);
    }
  };
  var SteadinessWatch = class {
    constructor(say2) {
      this.say = say2;
    }
    gaps = new Series(120);
    last = 0;
    uneven = false;
    since = 0;
    shown(p) {
      if (this.last) this.gaps.add(p.presented - this.last);
      this.last = p.presented;
      if (this.gaps.count < 120) return;
      const typical = this.gaps.median;
      const bad = this.gaps.p95 > typical * 2.5 + 8;
      if (bad === this.uneven) {
        this.since = p.presented;
        return;
      }
      if (p.presented - this.since > (bad ? 3e3 : 2e4)) {
        this.uneven = bad;
        this.since = p.presented;
        this.say(bad);
      }
    }
  };
  function watchVideo(v, onFrame) {
    const el = v;
    if (!el.requestVideoFrameCallback) return () => {
    };
    let stop = false;
    let id = 0;
    const step = (_now, meta) => {
      if (stop) return;
      onFrame({ rtp: meta.rtpTimestamp, received: meta.receiveTime, presented: meta.presentationTime, displayAt: meta.expectedDisplayTime }, meta);
      id = el.requestVideoFrameCallback(step);
    };
    id = el.requestVideoFrameCallback(step);
    return () => {
      stop = true;
      el.cancelVideoFrameCallback?.(id);
    };
  }
  var STAMP_BITS = 32;
  var STAMP_COLOURS = [[255, 0, 0], [0, 255, 0], [0, 0, 255], [255, 255, 0], [0, 255, 255], [255, 0, 255], [128, 128, 128], [64, 128, 192], [192, 96, 48]];
  var StampReader = class {
    c;
    w = 320;
    h = 8;
    constructor() {
      const canvas = document.createElement("canvas");
      canvas.width = this.w;
      canvas.height = this.h;
      this.c = canvas.getContext("2d", { willReadFrequently: true });
      this.c.imageSmoothingEnabled = false;
    }
    // How far the known colours under the numbers are from what was drawn: the largest error
    // in any channel of each, out of 255.
    colours(source, srcW, srcH) {
      if (!srcW || !srcH) return null;
      const cw = Math.max(Math.floor(srcW / 40), 4);
      const ch = Math.max(Math.floor(srcH / 28), 4);
      const canvas = document.createElement("canvas");
      canvas.width = cw * 2 * STAMP_COLOURS.length;
      canvas.height = ch;
      const c = canvas.getContext("2d", { willReadFrequently: true });
      if (!c) return null;
      try {
        c.drawImage(source, 0, ch, canvas.width, ch, 0, 0, canvas.width, ch);
        const seen = [];
        let error = 0;
        STAMP_COLOURS.forEach((want, i) => {
          const d = c.getImageData(i * cw * 2 + cw, ch >> 1, 1, 1).data;
          seen.push([d[0], d[1], d[2]]);
          for (let k = 0; k < 3; k++) error = Math.max(error, Math.abs(d[k] - want[k]));
        });
        return { error, seen };
      } catch {
        return null;
      }
    }
    read(source, srcW, srcH) {
      if (!srcW || !srcH) return null;
      const cw = Math.max(Math.floor(srcW / 40), 4);
      const ch = Math.max(Math.floor(srcH / 28), 4);
      if (cw * STAMP_BITS > srcW) return null;
      try {
        this.c.drawImage(source, 0, 0, cw * STAMP_BITS, ch, 0, 0, this.w, this.h);
        const d = this.c.getImageData(0, this.h >> 1, this.w, 1).data;
        let bits = 0;
        const cell = this.w / STAMP_BITS;
        for (let i = 0; i < STAMP_BITS; i++) {
          const x = Math.floor((i + 0.5) * cell);
          const luma = d[x * 4] * 0.299 + d[x * 4 + 1] * 0.587 + d[x * 4 + 2] * 0.114;
          bits = bits * 2 + (luma > 128 ? 1 : 0);
        }
        return { seq: Math.floor(bits / 65536) & 65535, input: bits % 65536 };
      } catch {
        return null;
      }
    }
  };

  // web/direct.ts
  var hasDirect = () => typeof window.VideoDecoder !== "undefined" && typeof window.EncodedVideoChunk !== "undefined" && typeof RTCRtpReceiver !== "undefined" && "createEncodedStreams" in RTCRtpReceiver.prototype;
  var DirectVideo = class {
    constructor(signal2) {
      this.signal = signal2;
      this.canvas.width = 256;
      this.canvas.height = 224;
      this.ctx = this.canvas.getContext("2d", { alpha: false, desynchronized: true });
    }
    canvas = document.createElement("canvas");
    onFrame;
    framesDrawn = 0;
    ctx;
    decoder = null;
    needKey = true;
    closed = false;
    receivedAt = /* @__PURE__ */ new Map();
    lastAsk = 0;
    async start() {
      const VD = window.VideoDecoder;
      const config = { codec: "avc1.42E01F", optimizeForLatency: true, hardwareAcceleration: "prefer-hardware" };
      const ok = await VD.isConfigSupported(config).catch(() => null);
      const use = ok?.supported ? config : { codec: "avc1.42E01F", optimizeForLatency: true };
      this.decoder = new VD({
        output: (f) => this.draw(f),
        error: () => {
          this.needKey = true;
          this.askForKey();
          if (!this.closed) this.restart(use);
        }
      });
      this.decoder.configure(use);
      this.tellServer();
    }
    restart(config) {
      try {
        this.decoder?.close();
      } catch {
      }
      const VD = window.VideoDecoder;
      this.decoder = new VD({ output: (f) => this.draw(f), error: () => {
        this.needKey = true;
        this.askForKey();
      } });
      this.decoder.configure(config);
    }
    // Tells the server that the browser's own requests for keyframes mean nothing now. Its
    // decoder never sees a frame, so it keeps asking.
    tellServer() {
      const b = new ArrayBuffer(1);
      new DataView(b).setUint8(0, TIMING_DIRECT);
      this.signal(b);
    }
    askForKey() {
      const now = performance.now();
      if (now - this.lastAsk < 500) return;
      this.lastAsk = now;
      const b = new ArrayBuffer(1);
      new DataView(b).setUint8(0, TIMING_KEYFRAME);
      this.signal(b);
    }
    // Takes whole frames from the receiver for as long as there are any.
    async pump(readable) {
      const reader2 = readable.getReader();
      for (; ; ) {
        const { value: frame, done } = await reader2.read();
        if (done || this.closed) return;
        const key = frame.type === "key";
        if (this.needKey && !key) {
          this.askForKey();
          continue;
        }
        if (!this.decoder || this.decoder.state !== "configured") continue;
        if (this.decoder.decodeQueueSize > 2 && !key) {
          this.needKey = true;
          this.askForKey();
          continue;
        }
        this.needKey = false;
        const rtp = frame.timestamp >>> 0;
        this.receivedAt.set(rtp, performance.now());
        if (this.receivedAt.size > 16) this.receivedAt.delete(this.receivedAt.keys().next().value);
        const Chunk = window.EncodedVideoChunk;
        this.decoder.decode(new Chunk({ type: key ? "key" : "delta", timestamp: rtp, data: frame.data }));
      }
    }
    draw(f) {
      if (this.canvas.width !== f.displayWidth || this.canvas.height !== f.displayHeight) {
        this.canvas.width = f.displayWidth;
        this.canvas.height = f.displayHeight;
      }
      this.ctx.drawImage(f, 0, 0);
      const rtp = f.timestamp >>> 0;
      f.close();
      this.framesDrawn++;
      const received = this.receivedAt.get(rtp);
      this.receivedAt.delete(rtp);
      this.onFrame?.({ rtp, received: received ?? performance.now(), drawn: performance.now() });
    }
    close() {
      this.closed = true;
      try {
        this.decoder?.close();
      } catch {
      }
    }
  };

  // web/transport.ts
  var AUDIO_HOLD = { lowest: 0, balanced: 0.06, smooth: 0.15 };
  var TimingLink = class {
    constructor(out) {
      this.out = out;
    }
    meter = null;
    timer = 0;
    count = 0;
    start(meter) {
      this.meter = meter;
      this.stop();
      const sub = new ArrayBuffer(2);
      new DataView(sub).setUint8(0, TIMING_SUBSCRIBE);
      new DataView(sub).setUint8(1, meter ? 1 : 0);
      this.out(sub);
      if (!meter) return;
      this.count = 0;
      const tick = () => {
        if (!this.meter) return;
        this.out(this.meter.clock.ping());
        this.timer = window.setTimeout(tick, ++this.count < 20 ? 50 : 1e3);
      };
      tick();
    }
    stop() {
      clearTimeout(this.timer);
    }
    incoming(b) {
      this.meter?.message(b);
    }
    setLatency(mode) {
      const b = new ArrayBuffer(2);
      new DataView(b).setUint8(0, TIMING_LATENCY);
      new DataView(b).setUint8(1, mode === "balanced" ? 1 : mode === "steady" ? 2 : 0);
      this.out(b);
    }
    shift(ms) {
      const b = new ArrayBuffer(3);
      new DataView(b).setUint8(0, TIMING_SHIFT);
      new DataView(b).setInt16(1, Math.max(-32e3, Math.min(32e3, Math.round(ms * 10))), true);
      this.out(b);
    }
  };
  function make(kind, signal2) {
    if (kind === "webrtc") return new WebRTCTransport(signal2);
    if (kind === "webrtc-direct") return new DirectTransport(signal2);
    return new WSTransport(signal2, kind);
  }
  var FIRST_FRAME_MS = 12e3;
  var DISCONNECTED_MS = 2500;
  var STALLED_MS = 4e3;
  var WebRTCTransport = class {
    constructor(signal2) {
      this.signal = signal2;
      const v = document.createElement("video");
      v.playsInline = true;
      v.autoplay = true;
      v.setAttribute("playsinline", "");
      v.disablePictureInPicture = true;
      this.element = v;
    }
    kind = "webrtc";
    element;
    onEvent;
    onLost;
    onWaiting;
    pc = null;
    input = null;
    muted = false;
    userMuted = false;
    audioRx = null;
    hold = 0;
    prev = null;
    closed = false;
    timingDc = null;
    link = new TimingLink((b) => {
      if (this.timingDc?.readyState === "open") {
        try {
          this.timingDc.send(b);
        } catch {
        }
      }
    });
    wanted = null;
    unwatch = null;
    latency = "auto";
    unlock = null;
    lock = null;
    // what's moving the frames, for the test page to look at
    leaving = 0;
    watchdog = 0;
    framesSeen = -1;
    framesAt = 0;
    gone = false;
    async connect() {
      const pc = new RTCPeerConnection();
      this.pc = pc;
      const stream = new MediaStream();
      this.element.srcObject = stream;
      pc.ontrack = (e) => {
        stream.addTrack(e.track);
        const r = e.receiver;
        if ("playoutDelayHint" in r) r.playoutDelayHint = 0;
        if ("jitterBufferTarget" in r) r.jitterBufferTarget = 0;
        if (e.track.kind === "audio") {
          this.audioRx = e.receiver;
          this.applyHold();
        }
      };
      pc.addTransceiver("video", { direction: "recvonly" });
      pc.addTransceiver("audio", { direction: "recvonly" });
      this.input = pc.createDataChannel("input", { ordered: false, maxRetransmits: 0 });
      this.input.binaryType = "arraybuffer";
      const events = pc.createDataChannel("events");
      events.onmessage = (m) => {
        try {
          this.onEvent?.(JSON.parse(m.data));
        } catch {
        }
      };
      const timing = pc.createDataChannel("timing", { ordered: false, maxRetransmits: 0 });
      timing.binaryType = "arraybuffer";
      timing.onmessage = (m) => {
        if (m.data instanceof ArrayBuffer) this.link.incoming(m.data);
      };
      timing.onopen = () => {
        if (this.wanted) this.measure(this.wanted);
        this.setLatency(this.latency);
      };
      this.timingDc = timing;
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === "failed" || pc.connectionState === "closed") this.lost();
        clearTimeout(this.leaving);
        if (pc.connectionState === "disconnected") this.leaving = window.setTimeout(() => this.lost(), DISCONNECTED_MS);
      };
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await gathered(pc, 1500);
      const answer = await this.signal.answer(pc.localDescription.sdp);
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
      await this.play();
      this.onWaiting?.();
      await firstFrame(this.element, FIRST_FRAME_MS);
      this.watchdog = window.setInterval(() => this.checkFrames(), 1e3);
    }
    lost() {
      if (this.closed || this.gone) return;
      this.gone = true;
      this.onLost?.();
    }
    // The server keeps sending frames even while a game is paused or loading, so a picture
    // that has stopped on a page someone can see means something in between has stuck: the
    // decoder, the network or the browser. Starting the connection again clears any of them.
    checkFrames() {
      const now = performance.now();
      if (document.hidden) {
        this.framesAt = now;
        return;
      }
      const count = this.element.getVideoPlaybackQuality?.().totalVideoFrames ?? -1;
      if (count !== this.framesSeen) {
        this.framesSeen = count;
        this.framesAt = now;
      } else if (count >= 0 && now - this.framesAt > STALLED_MS) this.lost();
    }
    async play() {
      try {
        await this.element.play();
      } catch {
        this.muted = true;
        this.element.muted = true;
        try {
          await this.element.play();
        } catch {
        }
      }
    }
    needsUnmute() {
      return this.muted && !this.userMuted;
    }
    unmute() {
      this.muted = false;
      this.element.muted = this.userMuted;
      void this.element.play().catch(() => {
      });
    }
    setVolume(volume, muted) {
      this.userMuted = muted;
      this.element.volume = Math.max(0, Math.min(1, volume));
      this.element.muted = muted || this.muted;
    }
    setAudioBuffer(b) {
      this.hold = AUDIO_HOLD[b] ?? 0;
      this.applyHold();
    }
    applyHold() {
      const r = this.audioRx;
      if (r && "jitterBufferTarget" in r) r.jitterBufferTarget = this.hold * 1e3;
    }
    captureStream() {
      return this.element.srcObject;
    }
    send(msg) {
      if (this.input?.readyState === "open") {
        try {
          this.input.send(msg);
        } catch {
        }
      }
    }
    close() {
      this.closed = true;
      this.link.stop();
      this.unwatch?.();
      this.unlock?.();
      clearTimeout(this.leaving);
      clearInterval(this.watchdog);
      this.pc?.close();
      this.element.srcObject = null;
    }
    measure(meter) {
      this.wanted = meter;
      this.unwatch?.();
      this.unwatch = meter ? watchVideo(this.element, (p) => meter.shown(p)) : null;
      if (this.timingDc?.readyState === "open" || !meter) this.link.start(meter);
    }
    // Says how to trade speed against steadiness. Fastest and balanced also land frames just
    // before the display wants them. Both hold good for the rest of the connection.
    setLatency(mode) {
      this.latency = mode;
      if (this.timingDc?.readyState !== "open") return;
      this.unlock?.();
      this.unlock = null;
      if (mode === "steady") {
        this.link.setLatency("steady");
        return;
      }
      let now = mode === "balanced" ? "balanced" : "fastest";
      this.link.setLatency(now);
      const margin = () => now === "fastest" ? 3 : 6;
      const lock = new PhaseLock((ms) => this.link.shift(ms), margin());
      this.lock = lock;
      const auto = mode === "auto" ? new SteadinessWatch((uneven) => {
        const next = uneven ? "balanced" : "fastest";
        if (next === now) return;
        now = next;
        lock.margin = margin();
        this.link.setLatency(next);
      }) : null;
      this.unlock = watchVideo(this.element, (p) => {
        lock.shown(p);
        auto?.shown(p);
      });
    }
    async stats() {
      const out = {};
      if (!this.pc) return out;
      const report2 = await this.pc.getStats();
      const codecs = /* @__PURE__ */ new Map();
      report2.forEach((s) => {
        if (s.type === "codec") codecs.set(s.id, s.mimeType);
      });
      report2.forEach((s) => {
        if (s.type === "inbound-rtp" && s.kind === "video") {
          out.lost = s.packetsLost;
          out.dropped = s.framesDropped;
          out.jitterMs = s.jitter != null ? Math.round(s.jitter * 1e3) : void 0;
          out.codec = codecs.get(s.codecId)?.replace("video/", "");
          if (s.framesDecoded && s.totalDecodeTime) out.decodeMs = Math.round(s.totalDecodeTime / s.framesDecoded * 1e3 * 10) / 10;
          const now = s.timestamp;
          if (this.prev && now > this.prev.t) {
            const dt = (now - this.prev.t) / 1e3;
            out.kbps = Math.round((s.bytesReceived - this.prev.bytes) * 8 / 1e3 / dt);
            out.fps = Math.round((s.framesDecoded - this.prev.frames) / dt);
          }
          this.prev = { t: now, bytes: s.bytesReceived, frames: s.framesDecoded };
        }
        if (s.type === "candidate-pair" && s.nominated && s.currentRoundTripTime != null) out.rttMs = Math.round(s.currentRoundTripTime * 1e3);
      });
      return out;
    }
  };
  var DirectTransport = class {
    constructor(signal2) {
      this.signal = signal2;
      this.video = new DirectVideo((b) => {
        if (this.timingDc?.readyState === "open") {
          try {
            this.timingDc.send(b);
          } catch {
          }
        }
      });
      this.element = this.video.canvas;
      this.video.onFrame = (f) => this.meter?.shown({ rtp: f.rtp, received: f.received, presented: f.drawn, displayAt: f.drawn });
    }
    kind = "webrtc-direct";
    element;
    onEvent;
    onLost;
    onWaiting;
    pc = null;
    input = null;
    timingDc = null;
    link = new TimingLink((b) => {
      if (this.timingDc?.readyState === "open") {
        try {
          this.timingDc.send(b);
        } catch {
        }
      }
    });
    video;
    sound = document.createElement("audio");
    muted = false;
    userMuted = false;
    closed = false;
    gone = false;
    meter = null;
    watchdog = 0;
    seen = 0;
    seenAt = 0;
    leaving = 0;
    prev = null;
    async connect() {
      if (!hasDirect()) throw new Error("this browser cannot take frames as they arrive");
      await this.video.start();
      const pc = new RTCPeerConnection({ encodedInsertableStreams: true });
      this.pc = pc;
      const sound = new MediaStream();
      this.sound.srcObject = sound;
      this.sound.autoplay = true;
      pc.ontrack = (e) => {
        const r = e.receiver;
        const { readable, writable } = r.createEncodedStreams();
        if (e.track.kind === "video") {
          void this.video.pump(readable).catch(() => this.lost());
          return;
        }
        if ("jitterBufferTarget" in r) r.jitterBufferTarget = 0;
        void readable.pipeTo(writable).catch(() => {
        });
        sound.addTrack(e.track);
      };
      pc.addTransceiver("video", { direction: "recvonly" });
      pc.addTransceiver("audio", { direction: "recvonly" });
      this.input = pc.createDataChannel("input", { ordered: false, maxRetransmits: 0 });
      this.input.binaryType = "arraybuffer";
      const events = pc.createDataChannel("events");
      events.onmessage = (m) => {
        try {
          this.onEvent?.(JSON.parse(m.data));
        } catch {
        }
      };
      const timing = pc.createDataChannel("timing", { ordered: false, maxRetransmits: 0 });
      timing.binaryType = "arraybuffer";
      timing.onmessage = (m) => {
        if (m.data instanceof ArrayBuffer) this.link.incoming(m.data);
      };
      timing.onopen = () => {
        this.video.tellServer();
        if (this.meter) this.link.start(this.meter);
      };
      this.timingDc = timing;
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === "failed" || pc.connectionState === "closed") this.lost();
        clearTimeout(this.leaving);
        if (pc.connectionState === "disconnected") this.leaving = window.setTimeout(() => this.lost(), DISCONNECTED_MS);
      };
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await gathered(pc, 1500);
      const answer = await this.signal.answer(pc.localDescription.sdp);
      await pc.setRemoteDescription({ type: "answer", sdp: answer });
      void this.sound.play().catch(() => {
        this.muted = true;
      });
      this.onWaiting?.();
      const start = performance.now();
      while (this.video.framesDrawn === 0) {
        if (performance.now() - start > FIRST_FRAME_MS) throw new Error("no picture arrived");
        await new Promise((r) => setTimeout(r, 20));
      }
      this.seenAt = performance.now();
      this.watchdog = window.setInterval(() => {
        if (document.hidden || this.video.framesDrawn !== this.seen) {
          this.seen = this.video.framesDrawn;
          this.seenAt = performance.now();
        } else if (performance.now() - this.seenAt > STALLED_MS) this.lost();
      }, 1e3);
    }
    lost() {
      if (this.closed || this.gone) return;
      this.gone = true;
      this.onLost?.();
    }
    send(msg) {
      if (this.input?.readyState === "open") {
        try {
          this.input.send(msg);
        } catch {
        }
      }
    }
    close() {
      this.closed = true;
      this.link.stop();
      clearTimeout(this.leaving);
      clearInterval(this.watchdog);
      this.video.close();
      this.pc?.close();
      this.sound.srcObject = null;
    }
    measure(meter) {
      this.meter = meter;
      if (this.timingDc?.readyState === "open" || !meter) this.link.start(meter);
    }
    // There's no buffer here to choose the size of.
    setLatency(_mode) {
    }
    needsUnmute() {
      return this.muted && !this.userMuted;
    }
    unmute() {
      this.muted = false;
      this.sound.muted = this.userMuted;
      void this.sound.play().catch(() => {
      });
    }
    setVolume(volume, muted) {
      this.userMuted = muted;
      this.sound.volume = Math.max(0, Math.min(1, volume));
      this.sound.muted = muted;
    }
    setAudioBuffer(_b) {
    }
    captureStream() {
      return this.element.captureStream?.(30) || null;
    }
    async stats() {
      const out = { codec: "H264" };
      if (!this.pc) return out;
      const report2 = await this.pc.getStats();
      report2.forEach((s) => {
        if (s.type === "inbound-rtp" && s.kind === "video") {
          out.lost = s.packetsLost;
          out.jitterMs = s.jitter != null ? Math.round(s.jitter * 1e3) : void 0;
          const now = s.timestamp;
          if (this.prev && now > this.prev.t) {
            const dt = (now - this.prev.t) / 1e3;
            out.kbps = Math.round((s.bytesReceived - this.prev.bytes) * 8 / 1e3 / dt);
            out.fps = Math.round((this.video.framesDrawn - this.prev.frames) / dt);
          }
          this.prev = { t: now, bytes: s.bytesReceived, frames: this.video.framesDrawn };
        }
        if (s.type === "candidate-pair" && s.nominated && s.currentRoundTripTime != null) out.rttMs = Math.round(s.currentRoundTripTime * 1e3);
      });
      return out;
    }
  };
  function gathered(pc, maxMs) {
    return new Promise((resolve) => {
      if (pc.iceGatheringState === "complete") return resolve();
      const done = () => {
        if (pc.iceGatheringState === "complete") resolve();
      };
      pc.addEventListener("icegatheringstatechange", done);
      setTimeout(resolve, maxMs);
    });
  }
  function firstFrame(v, ms) {
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => {
        clearInterval(iv);
        reject(new Error("no picture arrived"));
      }, ms);
      const iv = setInterval(() => {
        if (v.videoWidth > 0) {
          clearTimeout(t);
          clearInterval(iv);
          resolve();
        }
      }, 50);
    });
  }
  var MSG_H264 = 1;
  var MSG_JPEG = 2;
  var MSG_PCM = 3;
  var WSTransport = class {
    constructor(signal2, kind) {
      this.signal = signal2;
      this.kind = kind;
      this.element = document.createElement("canvas");
      this.element.width = 256;
      this.element.height = 224;
      this.ctx = this.element.getContext("2d", { alpha: false, desynchronized: true });
    }
    element;
    onEvent;
    onLost;
    onWaiting;
    ws = null;
    ctx;
    decoder = null;
    audio = null;
    audioAt = 0;
    frames = 0;
    bytes = 0;
    statT = performance.now();
    ts = 0;
    needKey = true;
    first = null;
    closed = false;
    drawing = false;
    // JPEG: the newest image wins
    gain = null;
    volume = 1;
    userMuted = false;
    hold = 0;
    meter = null;
    link = new TimingLink((b) => {
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(b);
    });
    async connect() {
      if (this.kind === "ws-h264") await this.makeDecoder();
      await new Promise((resolve, reject) => {
        const t = setTimeout(() => reject(new Error("no picture arrived")), FIRST_FRAME_MS);
        this.first = () => {
          clearTimeout(t);
          resolve();
        };
        const ws = new WebSocket(this.signal.wsUrl(this.kind === "ws-h264" ? "h264" : "jpeg"));
        ws.binaryType = "arraybuffer";
        this.ws = ws;
        ws.onopen = () => this.onWaiting?.();
        ws.onmessage = (m) => this.message(m.data);
        ws.onerror = () => {
          clearTimeout(t);
          reject(new Error("the connection failed"));
        };
        ws.onclose = () => {
          if (!this.closed) this.onLost?.();
        };
      });
    }
    async makeDecoder() {
      const VD = window.VideoDecoder;
      const config = { codec: "avc1.42E01F", optimizeForLatency: true };
      const support = await VD.isConfigSupported(config).catch(() => null);
      if (!support?.supported) throw new Error("this browser cannot decode H.264 here");
      this.decoder = new VD({
        output: (f) => {
          if (this.element.width !== f.displayWidth || this.element.height !== f.displayHeight) {
            this.element.width = f.displayWidth;
            this.element.height = f.displayHeight;
          }
          this.ctx.drawImage(f, 0, 0);
          if (this.meter) {
            const now = performance.now();
            this.meter.shown({ seq: f.timestamp, presented: now, displayAt: now });
          }
          f.close();
          this.frames++;
          this.first?.();
          this.first = null;
        },
        error: () => {
          this.needKey = true;
          if (!this.closed) void this.remake();
        }
      });
      this.decoder.configure(config);
    }
    async remake() {
      try {
        this.decoder?.close();
      } catch {
      }
      await this.makeDecoder().catch(() => this.onLost?.());
    }
    message(data) {
      if (typeof data === "string") {
        try {
          const e = JSON.parse(data);
          if (e.type !== "hello") this.onEvent?.(e);
        } catch {
        }
        return;
      }
      const b = new Uint8Array(data);
      this.bytes += b.length;
      if (b[0] >= 240) {
        if (b[0] === 244) this.nextSeq = new DataView(data).getUint32(2, true);
        this.link.incoming(data);
        return;
      }
      switch (b[0]) {
        case MSG_H264:
          this.h264(b);
          break;
        case MSG_JPEG:
          this.jpeg(b);
          break;
        case MSG_PCM:
          this.pcm(data);
          break;
      }
    }
    h264(b) {
      const key = b[1] === 1;
      if (this.needKey && !key) return;
      if (!this.decoder || this.decoder.state !== "configured") return;
      if (this.decoder.decodeQueueSize > 3 && !key) {
        this.needKey = true;
        return;
      }
      this.needKey = false;
      const Chunk = window.EncodedVideoChunk;
      this.ts += 16667;
      this.decoder.decode(new Chunk({ type: key ? "key" : "delta", timestamp: this.meter ? this.nextSeq : this.ts, data: b.subarray(2) }));
    }
    nextSeq = 0;
    measure(meter) {
      this.meter = meter;
      this.link.start(meter);
    }
    setLatency(mode) {
      this.link.setLatency(mode);
    }
    jpeg(b) {
      if (this.drawing) return;
      this.drawing = true;
      const blob = new Blob([b.subarray(1)], { type: "image/jpeg" });
      createImageBitmap(blob).then((img) => {
        if (this.element.width !== img.width || this.element.height !== img.height) {
          this.element.width = img.width;
          this.element.height = img.height;
        }
        this.ctx.drawImage(img, 0, 0);
        img.close();
        this.frames++;
        this.first?.();
        this.first = null;
      }).catch(() => {
      }).finally(() => {
        this.drawing = false;
      });
    }
    // Sound is scheduled in small pieces. Falling too far behind means skipping
    // ahead instead of letting delay grow.
    pcm(data) {
      if (!this.audio) {
        try {
          this.audio = new AudioContext({ latencyHint: "interactive" });
        } catch {
          return;
        }
      }
      if (this.audio.state === "suspended") return;
      const rate = new DataView(data).getUint32(1, true);
      const n = Math.floor((data.byteLength - 5) / 4);
      if (n === 0 || rate < 3e3 || rate > 384e3) return;
      const buf = this.audio.createBuffer(2, n, rate);
      const l = buf.getChannelData(0), r = buf.getChannelData(1);
      const v = new DataView(data, 5);
      for (let i = 0; i < n; i++) {
        l[i] = v.getInt16(i * 4, true) / 32768;
        r[i] = v.getInt16(i * 4 + 2, true) / 32768;
      }
      const now = this.audio.currentTime;
      const lead = 0.03 + this.hold;
      if (this.audioAt < now) this.audioAt = now + lead;
      if (this.audioAt > now + 0.25 + this.hold) this.audioAt = now + lead;
      if (!this.gain) {
        this.gain = this.audio.createGain();
        this.gain.connect(this.audio.destination);
        this.applyVolume();
      }
      const src = this.audio.createBufferSource();
      src.buffer = buf;
      src.connect(this.gain);
      src.start(this.audioAt);
      this.audioAt += n / rate;
    }
    needsUnmute() {
      return !this.userMuted && (!this.audio || this.audio.state === "suspended");
    }
    setVolume(volume, muted) {
      this.volume = Math.max(0, Math.min(1, volume));
      this.userMuted = muted;
      this.applyVolume();
    }
    applyVolume() {
      if (this.gain) this.gain.gain.value = this.userMuted ? 0 : this.volume;
    }
    setAudioBuffer(b) {
      this.hold = AUDIO_HOLD[b] ?? 0;
    }
    captureStream() {
      return this.element.captureStream?.(30) || null;
    }
    unmute() {
      if (!this.audio) {
        try {
          this.audio = new AudioContext({ latencyHint: "interactive" });
        } catch {
          return;
        }
      }
      void this.audio.resume().catch(() => {
      });
    }
    send(msg) {
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(msg);
    }
    close() {
      this.closed = true;
      this.link.stop();
      this.ws?.close();
      try {
        this.decoder?.close();
      } catch {
      }
      void this.audio?.close().catch(() => {
      });
    }
    async stats() {
      const now = performance.now();
      const dt = (now - this.statT) / 1e3;
      const out = dt > 0 ? { fps: Math.round(this.frames / dt), kbps: Math.round(this.bytes * 8 / 1e3 / dt), codec: this.kind === "ws-h264" ? "H264" : "JPEG" } : {};
      this.frames = 0;
      this.bytes = 0;
      this.statT = now;
      return out;
    }
  };

  // cmd/wittyplay-probe/page/probe.ts
  var q = new URLSearchParams(location.search);
  var setup = {
    seconds: Number(q.get("seconds") || 30),
    kind: q.get("kind") || "webrtc",
    pressMs: Number(q.get("pressMs") || 250),
    label: q.get("label") || "",
    every: Number(q.get("every") || 0),
    // post what's been seen this often, for a long run
    latency: q.get("latency")
  };
  var say = (s) => {
    const e = document.getElementById("status");
    if (e) e.textContent = s;
  };
  var round = (n) => Math.round(n * 100) / 100;
  var sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  var post = (path, o) => fetch(path, { method: "POST", body: JSON.stringify(o) });
  var summary = (s) => ({ p50: round(s.median), p95: round(s.p95), n: s.count });
  var signal = {
    answer: async (sdp) => {
      const r = await fetch("/answer", { method: "POST", body: sdp });
      if (!r.ok) throw new Error(await r.text());
      return r.text();
    },
    wsUrl: (video) => `ws://${location.host}/ws?video=${video}`
  };
  var Window = class {
    meter = new LatencyMeter();
    glass = new Series(1e5);
    photon = new Series(1e5);
    handed = new Series(1e5);
    gaps = new Series(1e5);
    misfiled = {};
    waitTrace = [];
    bad = 0;
    dark = 0;
    // frames decoded black after a picture had been seen
    darkRuns = 0;
    read = 0;
    skipped = 0;
    out = 0;
    presses = 0;
  };
  var current = null;
  var win = new Window();
  var meterShared = win.meter;
  var lostAt = 0;
  var recoveries = [];
  var dropWhy = {};
  var totalFrames = 0;
  var lastFrameSeq = 0;
  var reconnecting = false;
  async function connect(first) {
    const t = make(setup.kind, signal);
    t.onLost = (why) => {
      if (current !== t || reconnecting) return;
      dropWhy[why] = (dropWhy[why] || 0) + 1;
      lostAt = performance.now();
      void reconnect();
    };
    await t.connect();
    const old = current;
    current = t;
    t.unmute();
    t.setVolume(0, true);
    if (setup.latency) t.setLatency(setup.latency);
    document.getElementById("stage").replaceChildren(t.element);
    old?.close();
    t.measure(meterShared);
    watch(t);
    if (lostAt) {
      recoveries.push(round(performance.now() - lostAt));
      lostAt = 0;
    }
    if (first) say("warming up");
  }
  async function reconnect() {
    reconnecting = true;
    for (let i = 0; i < 30; i++) {
      try {
        await connect(false);
        reconnecting = false;
        return;
      } catch {
        await sleep(1e3);
      }
    }
    reconnecting = false;
    await post("/results", { error: "it never came back" });
  }
  var reader = new StampReader();
  var Darkness = class {
    c;
    lit = false;
    inRun = false;
    constructor() {
      const canvas = document.createElement("canvas");
      canvas.width = 16;
      canvas.height = 16;
      this.c = canvas.getContext("2d", { willReadFrequently: true });
    }
    check(source, w, h, win2) {
      if (!w || !h) return;
      try {
        this.c.drawImage(source, w / 4, h / 4, w / 2, h / 2, 0, 0, 16, 16);
        const d = this.c.getImageData(0, 0, 16, 16).data;
        let sum = 0;
        for (let i = 0; i < d.length; i += 4) sum += 0.299 * d[i] + 0.587 * d[i + 1] + 0.114 * d[i + 2];
        const dark = sum / 256 < 12;
        if (!dark) {
          this.lit = true;
          this.inRun = false;
          return;
        }
        if (!this.lit) return;
        win2.dark++;
        if (!this.inRun) win2.darkRuns++;
        this.inRun = true;
      } catch {
      }
    }
  };
  var darkness = new Darkness();
  var pressedAt = /* @__PURE__ */ new Map();
  var later = /* @__PURE__ */ new Map();
  var unwatch = null;
  var lastSeq = -1;
  var seenInput = 0;
  var lastShown = 0;
  var traceWaits = [];
  function settle() {
    for (const [seq, p] of later) {
      const r = win.meter.bySequence(seq);
      if (!r) {
        if (later.size > 64) later.delete(seq);
        continue;
      }
      later.delete(seq);
      win.glass.add(p.displayAt - r.runEnd);
      win.handed.add(p.presented - r.runEnd);
      if (p.rtp != null) {
        const byTime = win.meter.byRtp(p.rtp);
        if (byTime) {
          const k = String(byTime.seq - r.seq | 0);
          win.misfiled[k] = (win.misfiled[k] || 0) + 1;
        }
      }
    }
  }
  function watch(t) {
    unwatch?.();
    const v = t.element;
    const seen = (p) => {
      totalFrames++;
      if (lastShown) win.gaps.add(p.presented - lastShown);
      lastShown = p.presented;
      traceWaits.push(p.displayAt - p.presented);
      if (traceWaits.length >= 30) {
        traceWaits.sort((a, b) => a - b);
        win.waitTrace.push(round(traceWaits[15]));
        traceWaits = [];
      }
      const size = v.videoWidth ? [v.videoWidth, v.videoHeight] : [v.width, v.height];
      darkness.check(v, size[0], size[1], win);
      const s = reader.read(v, size[0], size[1]);
      if (!s) {
        win.bad++;
        return;
      }
      win.read++;
      if (lastSeq >= 0) {
        const d = s.seq - lastSeq & 65535;
        if (d > 1e3 || d === 0) win.out++;
        else if (d > 1) win.skipped += d - 1;
      }
      lastSeq = s.seq;
      lastFrameSeq = s.seq;
      later.set(s.seq, p);
      settle();
      if (s.input !== seenInput) {
        seenInput = s.input;
        const at = pressedAt.get(s.input);
        if (at != null) {
          win.photon.add(p.displayAt - at);
          pressedAt.delete(s.input);
        }
      }
    };
    if (v.tagName === "VIDEO") {
      unwatch = watchVideo(v, seen);
      return;
    }
    const direct = t.video;
    if (direct) {
      const inner = direct.onFrame;
      direct.onFrame = (f) => {
        inner?.(f);
        seen({ rtp: f.rtp, received: f.received, presented: f.drawn, displayAt: f.drawn });
      };
      unwatch = () => {
        direct.onFrame = inner;
      };
    }
  }
  function startPressing() {
    const msg = new Uint8Array(17);
    const view = new DataView(msg.buffer);
    let held = false;
    const press = () => {
      held = !held;
      view.setUint16(1, held ? 1 << 8 : 0, true);
      const at = performance.now();
      const seq = meterShared.press(at);
      view.setUint32(13, seq, true);
      pressedAt.set(seq & 65535, at);
      if (pressedAt.size > 256) pressedAt.delete(pressedAt.keys().next().value);
      current?.send(msg);
      win.presses++;
      setTimeout(press, setup.pressMs * (0.6 + Math.random() * 0.8));
    };
    setTimeout(press, 100);
  }
  function sendBeats() {
    setInterval(() => {
      void post("/beat", { frames: totalFrames, seq: lastFrameSeq, reconnects: recoveries.length, recoveries, dropWhy, heap: performance.memory?.usedJSHeapSize || 0, hidden: document.hidden }).catch(() => {
      });
    }, 250);
  }
  function report(w, stats) {
    const m = w.meter;
    return {
      label: setup.label,
      kind: setup.kind,
      seconds: setup.seconds,
      ua: navigator.userAgent,
      frames: Object.fromEntries(Object.entries(m.frames).map(([k, s]) => [k, summary(s)])),
      presses: Object.fromEntries(Object.entries(m.presses).map(([k, s]) => [k, summary(s)])),
      glass: summary(w.glass),
      photon: summary(w.photon),
      madeToHanded: summary(w.handed),
      interval: summary(w.gaps),
      pixels: { read: w.read, bad: w.bad, skipped: w.skipped, out: w.out, pressCount: w.presses, dark: w.dark, darkRuns: w.darkRuns },
      misfiled: w.misfiled,
      stats,
      waitTrace: w.waitTrace,
      shifted: current?.lock?.shifted,
      recoveries,
      dropWhy,
      heap: performance.memory?.usedJSHeapSize || 0
    };
  }
  function nextWindow() {
    const old = win;
    const fresh = new Window();
    fresh.meter = old.meter;
    for (const s of [...Object.values(old.meter.frames), ...Object.values(old.meter.presses)]) s.clear();
    win = fresh;
    return old;
  }
  async function main() {
    say("connecting");
    try {
      await connect(true);
    } catch (e) {
      await post("/results", { error: String(e?.message || e) });
      return;
    }
    sendBeats();
    await sleep(3e3);
    nextWindow();
    startPressing();
    say("measuring");
    if (setup.every > 0) {
      for (; ; ) {
        await sleep(setup.every * 1e3);
        settle();
        const stats2 = await chromeStats(current);
        await post("/partial", report(win, stats2));
        nextWindow();
      }
    }
    await sleep(setup.seconds * 1e3);
    await sleep(200);
    settle();
    const stats = await chromeStats(current);
    await post("/shot", await picture());
    await post("/results", { ...report(win, stats), colours: current && reader.colours(current.element, current.element.videoWidth || current.element.width, current.element.videoHeight || current.element.height) });
    say("done");
  }
  async function picture() {
    const v = current?.element;
    const w = v?.videoWidth || v?.width, h = v?.videoHeight || v?.height;
    if (!w) return { png: "" };
    const c = document.createElement("canvas");
    c.width = w;
    c.height = h;
    c.getContext("2d").drawImage(v, 0, 0);
    return { png: c.toDataURL("image/png"), seq: reader.read(v, w, h)?.seq };
  }
  async function chromeStats(t) {
    const pc = t?.pc;
    if (!pc) return {};
    const out = {};
    const report2 = await pc.getStats();
    report2.forEach((s) => {
      if (s.type === "inbound-rtp" && s.kind === "video") {
        out.decoder = s.decoderImplementation;
        out.powerEfficient = s.powerEfficientDecoder;
        out.frames = s.framesDecoded;
        out.dropped = s.framesDropped;
        out.lost = s.packetsLost;
        out.nacks = s.nackCount;
        out.plis = s.pliCount;
        out.jitterBufferMs = s.jitterBufferEmittedCount ? round(s.jitterBufferDelay / s.jitterBufferEmittedCount * 1e3) : null;
        out.jitterTargetMs = s.jitterBufferEmittedCount ? round(s.jitterBufferTargetDelay / s.jitterBufferEmittedCount * 1e3) : null;
        out.decodeMs = s.framesDecoded ? round(s.totalDecodeTime / s.framesDecoded * 1e3) : null;
        out.interFrameMs = s.framesDecoded ? round(s.totalInterFrameDelay / s.framesDecoded * 1e3) : null;
        out.kbps = s.bytesReceived;
      }
      if (s.type === "inbound-rtp" && s.kind === "audio") {
        out.audioBufferMs = s.jitterBufferEmittedCount ? round(s.jitterBufferDelay / s.jitterBufferEmittedCount * 1e3) : null;
        out.audioConcealed = s.concealedSamples;
        out.audioSamples = s.totalSamplesReceived;
        out.audioFaster = s.removedSamplesForAcceleration;
        out.audioSlower = s.insertedSamplesForDeceleration;
      }
      if (s.type === "candidate-pair" && s.nominated && s.currentRoundTripTime != null) out.rttMs = round(s.currentRoundTripTime * 1e3);
    });
    return out;
  }
  main().catch((e) => {
    void post("/results", { error: String(e) });
  });
})();
