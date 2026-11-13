// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { SELECT, START, type ControlsData, type SaveControl } from './consoles'
import { DisplayEngine } from './display'
import { HOLD_ACTIONS, Hotkeys, type HotAction } from './hotkeys'
import { deviceId, InputManager, load, onTV, padSupport, save } from './input'
import { computeLayout } from './layout'
import type { ControllerData } from './mapping'
import { Osd } from './Osd'
import { MenuFrame, Tile, TileGrid } from './QuickMenu'
import { LoadingScreen, ProblemScreen, type LoadStage } from './Screens'
import { explain, type ProblemData } from './words'
import { BackEntry } from './exits'
import { CheatsPanel, CorePanel, JsonEditor, PatchesPanel, SettingsPanel, SlotsPanel, type LevelChoice } from './Panels'
import { downloadBlob, fileName, loadSchema, useSettings, type Level, type Schema, type SettingsHost } from './settings'
import { ControllerStudio, type PortChoice, type ProfileHost } from './Studio'
import { ConsoleChips } from './Panels'
import { TouchControls, TouchEditor, useOrientation } from './TouchControls'
import { make, plan, TRANSPORT_LABELS, whenShown, type AudioBuffer_, type Hello, type LatencyMode, type Mode, type PlayEvent, type Signal, type StreamInfo, type Transport, type TransportKind } from './transport'
import { measureRefresh, screenNow } from './refresh'
import './play.css'

// The player: one game, full window. The server does the running; this shows it
// and passes controllers along. Everything it needs from the app comes through
// PlayHost, so the module works in any app that can provide one.

export type SessionInfo = { id: string; itemId: number; title: string; console: string; core: string; width: number; height: number; aspect: number; fps: number; sampleRate: number; resumed: boolean; encoder?: string; renderer?: string; turns?: number; disc?: number; discs?: number; modes: { id: string; stream?: number }[]; devices?: PortChoice[]; safeMode?: boolean; maxSpeed?: number; notices?: string[] }
export type SaveSlot = { slot: number; updatedAt: number; size: number; hasThumb: boolean; core?: string }
export type PrepareStatus = { state: 'ready' | 'waiting' | 'reading' | 'unpacking'; done: number; total: number }

export interface PlayHost extends SettingsHost {
  itemId: number
  title: string
  consoleId: string
  consoleName: string
  resume: boolean
  safeMode: boolean
  startSlot?: number // load this save state as soon as the game is running
  cover?: string // the game's cover, for the loading screen and the menu
  openSettings?(): void // where an administrator goes to install a core
  // Gets a game ready, for one that has to be unpacked first. Called until it's ready.
  prepare?(): Promise<PrepareStatus>
  start(resume: boolean, safeMode: boolean): Promise<SessionInfo>
  relaunch(opts: { resume: boolean; safeMode: boolean }): void // start this game again
  stop(sessionId: string): Promise<void>
  answer(sessionId: string, sdp: string, prefer?: string): Promise<string>
  wsUrl(sessionId: string, video: string): string
  // Version 2 of the stream (the server's docs/GAMING-STREAM.md), on servers that have it.
  stream?(sessionId: string, hello: Hello, sdp: string): Promise<{ stream: StreamInfo; sdp: string }>
  streamUrl?(sessionId: string): string
  action(sessionId: string, type: string, slot?: number, on?: boolean, extra?: { port?: number; device?: number; value?: number; key?: string; text?: string }): Promise<any>
  controls(device: string): Promise<ControlsData>
  saveControl(s: SaveControl): Promise<void>
  controllers(): Promise<ControllerData>
  profiles: ProfileHost
  close(): void // leave the player
  report?(level: 'error' | 'warn', message: string): void // for the app's error log, if it keeps one
}

type SettingsGroup = 'display' | 'osd' | 'emulation' | 'audio' | 'states' | 'hotkeys'
type Panel = null | 'menu' | 'more' | 'settings' | 'save' | 'load' | 'controllers' | 'connection' | 'touch' | 'discs' | SettingsGroup | 'core' | 'cheats' | 'patches' | 'json'
// What Back goes to from each screen. Anything not listed goes to the game menu.
const PARENT: Partial<Record<NonNullable<Panel>, NonNullable<Panel>>> = { display: 'settings', osd: 'settings', emulation: 'settings', audio: 'settings', states: 'settings', hotkeys: 'settings', core: 'settings', cheats: 'settings', patches: 'settings', json: 'settings', connection: 'more', discs: 'more' }
const parentOf = (p: NonNullable<Panel>): Panel => (p === 'menu' ? null : PARENT[p] || 'menu')
// Where Esc, a controller's B and Back go from each: up a level, the touch editor as its Cancel.
const upFrom = (p: NonNullable<Panel>): Panel => (p === 'touch' ? 'menu' : parentOf(p))

const MODE_KEY = 'wittyplay.mode'
const TOUCH_KEY = 'wittyplay.touch'
const LEVEL_KEY = 'wittyplay.level'
const RECONNECT_TRIES = 8 // about a minute, with the waits between them
// A connection that comes back this quickly does so without a word: the last picture stays up.
const RECONNECT_QUIET_MS = 1500

// A game that's no longer there starts again by itself, but not twice in a couple of minutes:
// something that keeps stopping it should be seen, not hidden behind a page that keeps reloading.
const RESTART_GAP_MS = 120000
const RESTART_KEY = 'wittyplay.restarted'
function mayRestart(): boolean {
  try {
    const now = Date.now()
    if (now - Number(sessionStorage.getItem(RESTART_KEY) || 0) < RESTART_GAP_MS) return false
    sessionStorage.setItem(RESTART_KEY, String(now))
    return true
  } catch {
    return false
  }
}

// The screens that want the game in view, so that a change can be seen as it's made.
const SIDE_PANELS: Panel[] = ['display', 'osd', 'audio', 'core', 'save', 'load']
const GROUP_TITLES: Record<SettingsGroup, string> = { display: 'Picture', osd: 'On-screen numbers', emulation: 'Speed and rewind', audio: 'Sound', states: 'Saves', hotkeys: 'Shortcuts' }

// prepared waits for a game that has to be unpacked before it can start, saying how
// that's going. A large disc can take minutes on a small server.
async function prepared(host: PlayHost, say: (s: string, percent?: number) => void, cancelled: () => boolean) {
  if (!host.prepare) return
  for (let waited = false; ; waited = true) {
    const p = await host.prepare()
    if (cancelled()) return
    if (p.state === 'ready') {
      if (waited) say('Starting the game…')
      return
    }
    if (p.state === 'waiting') say('Waiting for another game to finish unpacking…')
    else if (p.state === 'unpacking' && p.total > 0) { const n = Math.min(99, Math.floor((p.done / p.total) * 100)); say(`Unpacking the game… ${n}%`, n) } else say('Unpacking the game…')
    await new Promise((r) => setTimeout(r, 1000))
  }
}

export default function GamePlayer({ host }: { host: PlayHost }) {
  const [controls, setControls] = useState<ControlsData | null>(null)
  const [known, setKnown] = useState<ControllerData | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    // A list of pads is a help, not a need: the game runs without it.
    Promise.all([host.controls(deviceId()), host.controllers().catch(() => ({ layouts: {}, pads: [] }))])
      .then(([c, k]) => { setKnown(k); setControls(c) }).catch((e) => setError(e?.message || 'The game could not start.'))
  }, [host])
  if (error) {
    return (
      <div className="wp-root"><ProblemScreen title={host.title} cover={host.cover} problem={explain(error, null, host.isAdmin, host.consoleName)} onBack={() => host.close()} onRetry={() => host.relaunch({ resume: host.resume, safeMode: host.safeMode })} onDefaults={() => host.relaunch({ resume: host.resume, safeMode: true })} /></div>
    )
  }
  return controls ? <Playing host={host} initial={controls} known={known} /> : <div className="wp-root"><LoadingScreen title={host.title} consoleName={host.consoleName} cover={host.cover} stage="prepare" status="Getting the game ready…" onCancel={() => host.close()} /></div>
}

function Playing({ host, initial, known }: { host: PlayHost; initial: ControlsData; known: ControllerData | null }) {
  const [controls, setControls] = useState(initial)
  const [info, setInfo] = useState<SessionInfo | null>(null)
  const [error, setError] = useState('')
  const [errorData, setErrorData] = useState<ProblemData | null>(null)
  const [status, setStatus] = useState('Getting the game ready…')
  const [stage, setStage] = useState<LoadStage>('prepare')
  const [percent, setPercent] = useState<number | undefined>(undefined)
  const [rewindHeld, setRewindHeld] = useState(false) // rewinding from the menu, until anything's pressed
  const [touchOverPad, setTouchOverPad] = useState(false) // asked for touch controls even with a controller
  const [transport, setTransport] = useState<Transport | null>(null)
  const [panel, setPanel] = useState<Panel>(null)
  const panelRef = useRef<Panel>(null)
  panelRef.current = panel
  // Back on a phone, in a browser or on a mouse closes what's open over the game, a level at a
  // time, rather than leaving the game (exits.ts).
  const backOut = useRef<() => boolean>(() => false)
  const backEntry = useMemo(() => new BackEntry(window.history, () => backOut.current()), [])
  const [needsTap, setNeedsTap] = useState(false)
  const [stalled, setStalled] = useState(false) // the picture has stopped on a connection that's up
  const [toast, setToast] = useState('')
  const [schema, setSchema] = useState<Schema | null>(null)
  const [level, setLevelState] = useState<Level>(() => load<Level>(LEVEL_KEY) || 'console')
  const [fastForward, setFastForward] = useState(false)
  const [slow, setSlow] = useState(false)
  const [rewinding, setRewinding] = useState(false)
  const [frozen, setFrozen] = useState(false)
  const [tabHidden, setTabHidden] = useState(false)
  const [heldSpeed, setHeldSpeed] = useState(1)
  const [recording, setRecording] = useState(false)
  const [area, setArea] = useState({ w: 0, h: 0 })
  const [mode, setModeState] = useState<Mode>(() => load<Mode>(MODE_KEY) || 'auto')
  const [touchOn, setTouchOn] = useState<boolean>(() => load<boolean>(TOUCH_KEY) ?? (window.matchMedia?.('(pointer: coarse)').matches ?? false))
  const [padName, setPadName] = useState('')
  const orientation = useOrientation()
  const input = useMemo(() => new InputManager(initial, known, host.itemId), [initial, known, host.itemId])
  const tv = useMemo(onTV, [])
  // Layouts live on the server, so they follow the account to its other devices.
  const saveControl = useCallback(async (s: SaveControl) => {
    await host.saveControl(s)
    const fresh = await host.controls(deviceId())
    setControls(fresh)
    input.setControls(fresh)
  }, [host, input])
  const refreshControls = useCallback(async () => {
    const fresh = await host.controls(deviceId())
    setControls(fresh)
    input.setControls(fresh)
  }, [host, input])
  const pic = useRef<HTMLDivElement>(null)
  const areaRef = useRef<HTMLDivElement>(null)
  const engine = useRef<DisplayEngine | null>(null)
  const turnsRef = useRef(0)
  const root = useRef<HTMLDivElement>(null)
  const sessionRef = useRef<SessionInfo | null>(null)
  const transportRef = useRef<Transport | null>(null)
  const onStage = useRef<Transport[]>([]) // whose pictures are on the page, oldest first
  const closing = useRef(false)
  const attempts = useRef(0)
  const slotLoaded = useRef(false)
  const reconnectNote = useRef(0)

  const say = useCallback((t: string) => setToast(t), [])
  useEffect(() => {
    if (!toast) return
    const t = setTimeout(() => setToast(''), 3500)
    return () => clearTimeout(t)
  }, [toast])

  // A person's settings, or defaults in safe mode: that's its point.
  const settings = useSettings(host, useMemo(() => ({ item: host.itemId }), [host.itemId]), say)
  useEffect(() => { loadSchema(host).then(setSchema).catch(() => {}) }, [host])
  const safe = !!info?.safeMode || host.safeMode
  const values = useMemo<Record<string, unknown>>(() => {
    if (safe) return Object.fromEntries((schema?.settings || []).map((d) => [d.key, d.default]))
    return settings.view?.resolved.values || {}
  }, [safe, schema, settings.view])
  const valuesRef = useRef(values)
  valuesRef.current = values
  const setLevel = (l: Level) => { setLevelState(l); save(LEVEL_KEY, l) }

  const onEvent = useCallback((e: PlayEvent) => {
    if (e.type === 'rumble') input.rumble(e.port || 0, e.strength || 0, e.effect || 0, e.ms || 0)
    else if (e.type === 'error' && e.code === 'seat-taken' && e.text) say(e.text)
    else if (e.type === 'message' && e.text) say(e.text)
    else if (e.type === 'shutdown') { say('The game has ended'); leave() }
    else if (e.type === 'ended') {
      // The server said why, so a closing connection is no reason to try another.
      closing.current = true
      setStatus('')
      setError(e.text || 'The game has stopped.')
    } else if (e.type === 'geometry' || e.type === 'avinfo') {
      if (e.width && e.height) setInfo((i) => i && ({ ...i, width: e.width!, height: e.height!, aspect: e.aspect || 0 }))
    } else if (e.type === 'frame') {
      if (e.width && e.height) setInfo((i) => i && ({ ...i, width: e.width!, height: e.height! }))
    } else if (e.type === 'rotation') setInfo((i) => i && ({ ...i, turns: (e.turns || 0) & 3 }))
    else if (e.type === 'disc') setInfo((i) => i && ({ ...i, disc: e.index, discs: e.count }))
  }, [input])

  // Tries each way of connecting in turn until one shows a picture. A later failure
  // starts the list again, skipping what has already failed this session.
  const connect = useCallback(async (s: SessionInfo, m: Mode, skip: Set<TransportKind> = new Set(), recovering = 0) => {
    // Only the newest attempt counts: changing the mode twice quickly abandons the first.
    const attempt = ++attempts.current
    const stale = () => closing.current || attempt !== attempts.current
    // After a lost connection, the last picture stays up until there's a new one to show.
    const old = transportRef.current
    if (!recovering) {
      old?.close()
      transportRef.current = null
      setTransport(null)
    }
    // Version 2 where the server offers it for every mode this player may use.
    const v2 = !!(host.stream && host.streamUrl) && (s.modes || []).some((x) => x.stream === 2)
    const signal: Signal = {
      answer: (sdp: string, prefer?: string) => host.answer(s.id, sdp, prefer),
      wsUrl: (video: string) => host.wsUrl(s.id, video),
      ...(v2 ? {
        stream: (hello: Hello, sdp: string) => host.stream!(s.id, hello, sdp),
        streamUrl: () => host.streamUrl!(s.id),
        hello: (): Hello => ({ v: 2, app: 'WittyPlay web', device: deviceId(), seats: input.ports(), display: screenNow(), latency: (valuesRef.current['emulation.latency'] as string) || 'auto' }),
      } : {}),
    }
    let last = ''
    for (const kind of plan(m, (s.modes || []).map((x) => x.id))) {
      if (skip.has(kind)) continue
      if (stale()) return
      if (!recovering) {
        setStage('connect')
        setStatus(kind === 'webrtc' || kind === 'webrtc-direct' ? 'Connecting to this device…' : 'Trying another way to connect…')
      }
      const t = make(kind, signal)
      t.onEvent = onEvent
      t.onWaiting = () => {
        if (recovering || stale()) return
        setStage('picture')
        setStatus('Waiting for the first picture…')
      }
      try {
        await t.connect()
        if (stale()) { t.close(); return }
        // A network that changed or a tab that was asleep is no reason to give up on the
        // best way of connecting, so a lost connection starts the whole list again.
        t.onLost = (why) => {
          if (closing.current || transportRef.current !== t) return
          host.report?.('warn', `The game stream was lost (${kind}: ${why}); reconnecting`)
          setStalled(false)
          window.clearTimeout(reconnectNote.current)
          reconnectNote.current = window.setTimeout(() => { if (!closing.current) setStatus('Reconnecting…') }, RECONNECT_QUIET_MS)
          void connect(s, m, new Set(), 1)
        }
        t.onStall = (on) => { if (transportRef.current === t) setStalled(on) }
        setStalled(false) // whatever the last one said
        window.clearTimeout(reconnectNote.current)
        transportRef.current = t
        // The old picture stays up, and its connection open, until the new one is on the screen.
        input.setSender((msg) => t.send(msg))
        setTransport(t)
        setStatus('')
        setNeedsTap(t.needsUnmute())
        if (host.startSlot && !slotLoaded.current) {
          slotLoaded.current = true
          void host.action(s.id, 'load', host.startSlot).then(() => say(`Loaded slot ${host.startSlot}`), (e) => say(e?.message || 'That save couldn’t be loaded'))
        }
        // A picture has arrived, so the real size of the game's pixels is known now.
        host.request<SessionInfo>('GET', `/play/${s.id}?client=browser`).then((now) => setInfo((i) => i && ({ ...i, width: now.width, height: now.height }))).catch(() => {})
        return
      } catch (e: any) {
        t.close()
        if (stale()) return
        // The game isn't there any more: the server or the module restarted, or this device was
        // away too long. No way of connecting can help, so there's no use trying them for a minute.
        if (e?.status === 404) {
          closing.current = true
          host.report?.('warn', 'The game was no longer running on the server')
          if (mayRestart()) {
            setStatus('The game stopped. Starting it again…')
            host.relaunch({ resume: true, safeMode: host.safeMode })
            return
          }
          old?.close()
          transportRef.current = null
          setTransport(null)
          setStatus('')
          setErrorData({ gone: true })
          setError('It has stopped on the server, more than once in the last couple of minutes.')
          return
        }
        last = e?.message || String(e)
        host.report?.('error', `The game stream could not connect (${kind}): ${last}`)
        skip.add(kind)
      }
    }
    // The network may just be down for a moment. The server keeps the game for a minute.
    if (recovering && recovering < RECONNECT_TRIES) {
      setTimeout(() => { if (!stale()) void connect(s, m, new Set(), recovering + 1) }, recovering * 2000)
      return
    }
    if (recovering) {
      window.clearTimeout(reconnectNote.current)
      old?.close()
      transportRef.current = null
      setTransport(null)
    }
    host.report?.('error', 'Every way of connecting to the game failed')
    setErrorData({ connect: true })
    setError('The game is running, but this browser couldn’t connect to it' + (last ? `: ${last}` : '.'))
  }, [host, input, onEvent])

  useEffect(() => {
    let cancelled = false
    closing.current = false
    prepared(host, (t, n) => { setStatus(t); setPercent(n) }, () => cancelled).then(() => {
      if (cancelled) return Promise.reject(new Error('stopped'))
      setPercent(undefined)
      setStage('start')
      setStatus(host.resume ? 'Starting the console, and your save…' : 'Starting the console…')
      return host.start(host.resume, host.safeMode)
    }).then((s) => {
      if (cancelled) { void host.stop(s.id).catch(() => {}); return }
      sessionRef.current = s
      setInfo(s)
      if (s.notices?.length) say(s.notices.join(' '))
      else if (s.safeMode) say('Safe mode: your settings, cheats and patches aren’t being used')
      else if (s.resumed) say('Back where you left off')
      void connect(s, mode)
    }).catch((e) => {
      if (cancelled) return
      host.report?.('error', `The game could not start: ${e?.message || e}`)
      setErrorData({ ...(e?.data || {}), status: e?.status })
      setError(e?.message || 'The game could not start.')
    })
    input.start()
    // The screen's rate, for the server to keep the game to it: said as soon as it's known.
    measureRefresh((hz) => transportRef.current?.control?.({ type: 'report', hz }))
    let held = ''
    input.onChange = () => {
      const pads = input.listPads()
      setPadName(pads.length ? pads.map((p) => `${p.name}: Player ${p.port + 1}`).join(', ') : '')
      // The server is told which players this device holds now: version 2's seats.
      const ports = input.ports()
      if (ports.join() !== held) {
        held = ports.join()
        transportRef.current?.control?.({ type: 'seats', ports })
      }
    }
    input.onChange()
    return () => {
      cancelled = true
      closing.current = true
      window.clearTimeout(reconnectNote.current)
      input.stop()
      transportRef.current?.close()
      for (const t of onStage.current) t.close()
      const s = sessionRef.current
      if (s) void host.stop(s.id).catch(() => {})
    }
  }, [])

  // Tell people what a controller did when it was connected.
  const lastPads = useRef('')
  const noGameRef = useRef(true)
  useEffect(() => {
    if (padName && padName !== lastPads.current) say(padName)
    else if (!padName && lastPads.current) say('Controller disconnected')
    lastPads.current = padName
  }, [padName, say])

  const leaveThen = useCallback((done: () => void) => {
    closing.current = true
    input.stop()
    transportRef.current?.close()
    const s = sessionRef.current
    sessionRef.current = null
    setStatus('Saving…')
    // A menu's entry in the history goes first, so the page left doesn't stay behind it.
    const go = () => backEntry.release(done)
    if (s) host.stop(s.id).then(go, go)
    else go()
  }, [host, input, backEntry])
  const leave = useCallback(() => leaveThen(() => host.close()), [host, leaveThen])
  const leaveRef = useRef(leave)
  leaveRef.current = leave

  // Menus stop the game and take the keyboard. So does stepping away, when that's the
  // setting. Pausing by hand leaves the controls alone, so that a frame can be stepped
  // with buttons held.
  const paused = panel !== null
  const pausedNow = paused || frozen || tabHidden
  useEffect(() => { input.setEnabled(!paused && !tabHidden) }, [paused, tabHidden, input])
  useEffect(() => {
    const s = sessionRef.current
    if (s && transport) void host.action(s.id, 'pause', undefined, pausedNow).catch(() => {})
  }, [pausedNow, host, transport])
  useEffect(() => {
    const check = () => setTabHidden(document.hidden && valuesRef.current['emulation.pauseHidden'] === true)
    document.addEventListener('visibilitychange', check)
    return () => document.removeEventListener('visibilitychange', check)
  }, [])

  // The picture: its size and shape, and whatever's between the game and the screen.
  useEffect(() => {
    const el = areaRef.current
    if (!el) return
    const ro = new ResizeObserver(() => setArea({ w: el.clientWidth, h: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const coreTurns = info?.turns || 0
  const personTurns = Number(values['display.rotate'] || 0) / 90
  const turns = (personTurns - coreTurns + 8) % 4 // a core's are anticlockwise; a person's are clockwise
  turnsRef.current = turns
  const bezel = String(values['display.bezel'] || 'none')
  const layout = useMemo(() => computeLayout({
    areaW: area.w, areaH: area.h, srcW: info?.width || 256, srcH: info?.height || 224, aspect: info?.aspect || 0,
    mode: String(values['display.aspect'] || 'original'), custom: Number(values['display.aspectCustom'] || 0), integer: values['display.integer'] === true,
    cropX: Number(values['display.cropX'] || 0), cropY: Number(values['display.cropY'] || 0), turns, inset: bezel === 'none' ? 0 : Number(values['display.bezelInset'] || 0),
  }), [area, info?.width, info?.height, info?.aspect, values, turns, bezel])
  useEffect(() => {
    if (!pic.current) return
    const e = new DisplayEngine(pic.current)
    engine.current = e
    return () => { e.destroy(); engine.current = null }
  }, [])
  useEffect(() => {
    engine.current?.update(values, { width: info?.width || 256, height: info?.height || 224 }, { picW: layout.picW, picH: layout.picH })
  }, [values, info?.width, info?.height, layout.picW, layout.picH, transport])

  // The video or canvas belongs to the transport; this puts it on the page. A new one goes in
  // underneath what's showing, which goes once the new one has a picture on the screen: taking
  // the old one away first would show black until then, on every reconnection.
  useEffect(() => {
    const el = pic.current
    const retire = (gone: Transport[]) => {
      for (const t of gone) { t.element.remove(); t.close() }
      onStage.current = onStage.current.filter((t) => !gone.includes(t))
    }
    if (!el || !transport) { retire(onStage.current); return }
    const media = transport.element
    media.className = 'wp-media'
    media.setAttribute('aria-label', `${host.title}, playing`)
    const older = onStage.current.filter((t) => t !== transport)
    if (!onStage.current.includes(transport)) {
      el.prepend(media)
      onStage.current = [...onStage.current, transport]
    }
    engine.current?.attach(media)
    if (!older.length) return
    return whenShown(media, () => retire(older))
  }, [transport, host.title])
  useEffect(() => {
    const m = transport?.element
    if (!m) return
    // The crop is the picture being larger than the box it's seen through.
    m.style.width = `${layout.scaleX * 100}%`
    m.style.height = `${layout.scaleY * 100}%`
    m.style.left = `${(100 - layout.scaleX * 100) / 2}%`
    m.style.top = `${(100 - layout.scaleY * 100) / 2}%`
  }, [transport, layout.scaleX, layout.scaleY])

  // Sound.
  useEffect(() => {
    if (!transport) return
    transport.setVolume(typeof values['audio.volume'] === 'number' ? values['audio.volume'] as number : 1, values['audio.mute'] === true)
    transport.setAudioBuffer((values['audio.latency'] as AudioBuffer_) || 'lowest')
  }, [transport, values])

  // How to trade speed against steadiness.
  const latencyChoice = values['emulation.latency'] as LatencyMode | undefined
  useEffect(() => { transport?.setLatency(latencyChoice || 'auto') }, [transport, latencyChoice])
  const stampInput = useCallback((next: (() => number) | null) => input.setStamper(next), [input])

  // How fast the game goes. A speed is always sent by a reliable request: a lost "let go
  // of fast-forward" would be a stuck game.
  const wanted = fastForward ? Number(values['emulation.ffSpeed'] || 4) : slow ? Number(values['emulation.slowSpeed'] || 0.5) : 1
  useEffect(() => {
    const s = sessionRef.current
    if (!s || !transport) return
    host.action(s.id, 'speed', undefined, undefined, { value: wanted }).then((r) => setHeldSpeed(r?.speed ?? wanted)).catch(() => {})
  }, [wanted, host, transport])

  // Things a person does while playing, from any kind of input.
  const quickSlot = () => Number(valuesRef.current['states.quickSlot'] || 1)
  const session = () => sessionRef.current
  const act = useCallback(async (type: string, slot?: number, on?: boolean, extra?: { value?: number }) => {
    const s = session()
    if (!s) return null
    return host.action(s.id, type, slot, on, extra)
  }, [host])
  const screenshot = useCallback(async () => {
    const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
    const name = `${fileName(host.title)} ${stamp}.png`
    const b = await engine.current?.capture()
    if (b) { downloadBlob(b, name); say('Screenshot saved') } else {
      const s = session()
      if (!s) return
      const a = document.createElement('a')
      a.href = host.mediaUrl(`/api/play/${s.id}/screenshot`)
      a.download = name
      a.click()
    }
  }, [host, say])
  const clip = useRef<{ rec: MediaRecorder; timer: number } | null>(null)
  const canClip = typeof MediaRecorder !== 'undefined'
  const toggleClip = useCallback(() => {
    if (clip.current) { clip.current.rec.stop(); return }
    const stream = transportRef.current?.captureStream()
    if (!stream) { say('This way of connecting can’t record'); return }
    const type = ['video/webm;codecs=vp9,opus', 'video/webm;codecs=vp8,opus', 'video/webm', 'video/mp4'].find((t) => MediaRecorder.isTypeSupported(t))
    try {
      const rec = new MediaRecorder(stream, type ? { mimeType: type } : undefined)
      const chunks: Blob[] = []
      rec.ondataavailable = (e) => { if (e.data.size) chunks.push(e.data) }
      rec.onstop = () => {
        const c = clip.current
        clip.current = null
        if (c) window.clearTimeout(c.timer)
        setRecording(false)
        const ext = (type || '').includes('mp4') ? 'mp4' : 'webm'
        if (chunks.length) { downloadBlob(new Blob(chunks, { type: type || 'video/webm' }), `${fileName(host.title)} clip.${ext}`); say('Clip saved') }
      }
      rec.start(1000)
      // Clips stay short: they're kept in this device's memory until they're saved.
      clip.current = { rec, timer: window.setTimeout(() => { try { rec.stop() } catch { /* already stopped */ } }, 60000) }
      setRecording(true)
      say('Recording, for up to a minute')
    } catch { say('This browser can’t record this') }
  }, [host.title, say])

  const fire = useCallback((a: HotAction, down: boolean) => {
    const v = valuesRef.current
    switch (a) {
      case 'fastForward':
        if (v['emulation.ffMode'] === 'toggle') { if (down) setFastForward((x) => !x) } else setFastForward(down)
        break
      case 'slow': setSlow(down); break
      case 'rewind':
        setRewinding(down)
        act('rewind', undefined, down).catch((e) => { setRewinding(false); if (down) say(e?.message || 'Rewind isn’t available') })
        break
      case 'quickSave': act('save', quickSlot()).then(() => say(`Saved to slot ${quickSlot()}`), (e) => say(e?.message || 'That didn’t work')); break
      case 'quickLoad': act('load', quickSlot()).then(() => say(`Loaded slot ${quickSlot()}`), (e) => say(e?.message || 'That didn’t work')); break
      case 'pause': setFrozen((x) => !x); break
      case 'advance':
        if (frozenRef.current) void act('advance', undefined, undefined, { value: 1 }).catch(() => {})
        else setFrozen(true)
        break
      case 'screenshot': void screenshot(); break
      case 'menu': setPanel((p) => (p ? null : 'menu')); break
      case 'mute': settings.set('person', 'audio.mute', v['audio.mute'] !== true); break
      case 'osd': settings.set('person', 'osd.on', v['osd.on'] !== true); break
    }
  }, [act, say, screenshot, settings.set])
  const stopRewind = useCallback(() => {
    setRewindHeld(false)
    setRewinding(false)
    act('rewind', undefined, false).catch(() => {})
  }, [act])
  const rewindHeldRef = useRef(rewindHeld)
  rewindHeldRef.current = rewindHeld
  useEffect(() => {
    if (!rewindHeld) return
    const stop = (e: Event) => { e.preventDefault(); stopRewind() }
    window.addEventListener('keydown', stop, true)
    window.addEventListener('pointerdown', stop, true)
    return () => { window.removeEventListener('keydown', stop, true); window.removeEventListener('pointerdown', stop, true) }
  }, [rewindHeld, stopRewind])
  const frozenRef = useRef(frozen)
  frozenRef.current = frozen
  const hot = useMemo(() => new Hotkeys((a, d) => fireRef.current(a, d), (code) => input.isBound(code)), [input])
  const stopRewindRef = useRef(stopRewind)
  stopRewindRef.current = stopRewind
  const fireRef = useRef(fire)
  fireRef.current = fire
  useEffect(() => { hot.configure(values) }, [hot, values])
  useEffect(() => { hot.enabled = panel === null }, [hot, panel])
  useEffect(() => {
    hot.start()
    input.filter = (port, mask) => {
      if (mask && rewindHeldRef.current) { stopRewindRef.current(); return 0 }
      return port === 0 ? hot.filter(mask) : mask
    }
    return () => { hot.stop(); input.filter = undefined }
  }, [hot, input])
  // Nobody can let go of a button in a menu they've gone into.
  useEffect(() => { if (panel) { setFastForward(false); setSlow(false) } }, [panel])

  // Text typed into the settings file isn't lost to one Esc: the first press only says so.
  const jsonDirty = useRef(false)
  const jsonWarned = useRef(false)
  const noteJsonDirty = useCallback((dirty: boolean) => { jsonDirty.current = dirty; jsonWarned.current = false }, [])
  const mayLeavePanel = () => {
    if (!jsonDirty.current || jsonWarned.current) return true
    jsonWarned.current = true
    say('This file isn’t saved. Press Save to keep it, or do that again to leave without saving')
    return false
  }
  const mayLeave = useRef(mayLeavePanel)
  mayLeave.current = mayLeavePanel
  backOut.current = () => {
    const p = panelRef.current
    if (!p) return false
    if (!mayLeave.current()) return true
    const next = upFrom(p)
    setPanel(next)
    return next !== null
  }
  useEffect(() => {
    const pop = () => backEntry.popped()
    window.addEventListener('popstate', pop)
    return () => window.removeEventListener('popstate', pop)
  }, [backEntry])
  const anyOpen = panel !== null
  useEffect(() => { if (anyOpen) backEntry.opened(); else backEntry.closed() }, [anyOpen, backEntry])
  // Closed: the keyboard, a remote and a controller are the game's again, and it carries on as it was.
  const wasOpen = useRef(false)
  useEffect(() => {
    if (anyOpen) { wasOpen.current = true; return }
    if (wasOpen.current) { wasOpen.current = false; root.current?.focus({ preventScroll: true }) }
  }, [anyOpen])

  // Escape, or Start and Select together on a pad, opens the menu.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      // Escape on a keyboard, and the many names of Back on a TV remote.
      const typing = (e.target as Element | null)?.matches?.('input:not([type=range]):not([type=checkbox]):not([type=color]), textarea')
      if (!['Escape', 'F1', 'GoBack', 'BrowserBack', 'Back', 'XF86Back'].includes(e.key) && !(tv && e.key === 'Backspace' && !typing)) return
      e.preventDefault()
      if (noGameRef.current) { leaveRef.current(); return }
      if (!mayLeave.current()) return
      setPanel((p) => (p ? upFrom(p) : 'menu'))
    }
    window.addEventListener('keydown', key)
    input.onMenu = () => { if (mayLeave.current()) setPanel((x) => (x ? null : 'menu')) }
    return () => { window.removeEventListener('keydown', key); input.onMenu = undefined }
  }, [tv, input])

  // A menu's contents change without it closing, so focus has to be put back in by
  // hand, or a remote would have nothing to move from. Coming back to a screen lands on
  // the tile that was used to leave it.
  const menuRef = useRef<HTMLDivElement>(null)
  const lastTile = useRef<Partial<Record<string, string>>>({})
  useEffect(() => {
    if (menuRef.current) menuRef.current.scrollTop = 0
    const remembered = panel && lastTile.current[panel]
    const back = remembered ? menuRef.current?.querySelector<HTMLElement>(`[data-tile="${remembered}"]:not([disabled])`) : null
    const first = back || menuRef.current?.querySelector<HTMLElement>('button:not([disabled]):not([data-bar]), input:not([disabled]), a[href]')
    first?.focus({ preventScroll: true })
  }, [panel])
  const noteTile = (e: React.MouseEvent) => {
    const id = (e.target as Element).closest?.('[data-tile]')?.getAttribute('data-tile')
    if (id && panel) lastTile.current[panel] = id
  }
  const up = () => setPanel((p) => (p ? parentOf(p) : p))

  const press = (id: number) => { setPanel(null); setTimeout(() => input.pulse(id), 150) }

  const setMode = (m: Mode) => {
    setModeState(m)
    save(MODE_KEY, m === 'auto' ? null : m)
    const s = sessionRef.current
    if (s) void connect(s, m)
  }
  const toggleTouch = () => setTouchOn((v) => { save(TOUCH_KEY, !v); return !v })
  const levels: LevelChoice[] = [
    { level: 'game', label: 'This game' },
    { level: 'console', label: host.consoleName ? `All ${host.consoleName} games` : 'This console' },
    { level: 'person', label: 'All my games' },
  ]
  const fullscreen = () => {
    const el = root.current as any
    if (document.fullscreenElement) void document.exitFullscreen()
    else void (el?.requestFullscreen?.() || el?.webkitRequestFullscreen?.())
  }
  const tap = () => { transport?.unmute(); setNeedsTap(false) }
  const onMask = useCallback((m: number) => input.setTouch(m), [input])
  const onStick = useCallback((x: number, y: number) => input.setTouchStick(x, y), [input])

  // A console's touch screen: a press on the picture is the stylus, wherever it
  // came from. Positions are fractions of the picture, which is all the server wants.
  const hasPointer = !!controls.pad?.pointer
  const onPoint = useCallback((clientX: number, clientY: number, phase: 'down' | 'move' | 'up') => {
    const r = pic.current?.getBoundingClientRect()
    if (!r || r.width === 0 || r.height === 0) return
    // The rectangle already holds the turned shape; undo the turn to reach the game's own axes.
    let x = (clientX - r.left) / r.width, y = (clientY - r.top) / r.height
    const quarter = ((turnsRef.current % 4) + 4) % 4
    if (quarter === 1) [x, y] = [y, 1 - x]
    else if (quarter === 2) [x, y] = [1 - x, 1 - y]
    else if (quarter === 3) [x, y] = [1 - y, x]
    input.setPointer(x, y, phase !== 'up')
  }, [input])
  const stylusDown = useRef(false)
  const stageDown = (e: React.PointerEvent) => {
    if (!hasPointer || paused) return
    e.preventDefault()
    try { e.currentTarget.setPointerCapture(e.pointerId) } catch { /* fine */ }
    stylusDown.current = true
    onPoint(e.clientX, e.clientY, 'down')
  }
  const stageMove = (e: React.PointerEvent) => { if (stylusDown.current) onPoint(e.clientX, e.clientY, 'move') }
  const stageUp = (e: React.PointerEvent) => { if (stylusDown.current) { stylusDown.current = false; onPoint(e.clientX, e.clientY, 'up') } }

  // With a controller in hand, touch controls get out of the way unless asked for.
  const touchShown = touchOn && (!padName || touchOverPad)
  const noGame = !!error || !transport
  noGameRef.current = noGame
  const problem = error ? explain(error, errorData, host.isAdmin, host.consoleName) : null
  const tile = { onClickCapture: noteTile }
  const rewindOn = values['emulation.rewind'] === true
  return (
    <div ref={root} tabIndex={-1} className={`wp-root wp-${orientation}${touchShown ? ' wp-with-touch' : ''}${paused || noGame ? '' : ' wp-playing'}`}>
      <div ref={areaRef} className="wp-video-area" style={bezel === 'color' ? { background: String(values['display.bezelColor'] || '#000') } : undefined}>
        {bezel === 'image' && !!values['display.bezelImage'] && <img className="wp-bezel-img" src={host.mediaUrl(`/api/game-bezels/${values['display.bezelImage']}`)} alt="" />}
        <div className={'wp-stage' + (hasPointer ? ' wp-has-pointer' : '')} style={{ width: layout.boxW, height: layout.boxH }}
          onPointerDown={stageDown} onPointerMove={stageMove} onPointerUp={stageUp} onPointerCancel={stageUp}>
          <div ref={pic} className="wp-pic" style={{ width: layout.picW, height: layout.picH, transform: `translate(-50%, -50%) rotate(${turns * 90}deg)` }} />
        </div>
      </div>
      {status && !error && !transport && <LoadingScreen title={host.title} consoleName={host.consoleName} cover={host.cover} stage={stage} status={status} percent={percent} onCancel={leave} />}
      {(status || stalled) && !error && transport && <div className="wp-status" role="status"><span className="wp-spinner" aria-hidden="true" />{status || 'Waiting for the picture…'}</div>}
      {problem && (
        <ProblemScreen title={host.title} cover={host.cover} problem={problem} onBack={leave}
          onRetry={() => host.relaunch({ resume: host.resume || !!errorData?.gone, safeMode: host.safeMode })}
          onDefaults={safe ? undefined : () => host.relaunch({ resume: host.resume || !!errorData?.gone, safeMode: true })}
          onSettings={host.openSettings ? () => leaveThen(() => host.openSettings!()) : undefined} />
      )}

      {touchShown && transport && !paused && <TouchControls data={controls} orientation={orientation} onMask={onMask} onStick={onStick} onPoint={hasPointer ? onPoint : undefined} />}

      {touchShown && transport && !paused && <button type="button" className="wp-touch-hide" aria-label="Hide touch controls" title="Hide touch controls" onClick={() => { setTouchOverPad(false); toggleTouch() }}><svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M12 6c5 0 8.7 3.6 10 6-.6 1.100-1.600 2.500-3 3.700l-1.400-1.400c1-.8 1.800-1.700 2.200-2.300C18.500 10.100 15.700 8 12 8c-.7 0-1.300.1-1.900.2L8.500 6.600C9.600 6.200 10.800 6 12 6zM3.700 2.300l18 18-1.400 1.400-3.100-3.100c-1.500.8-3.200 1.400-5.200 1.400-5 0-8.700-3.600-10-6 .7-1.300 2-3 3.800-4.300L2.300 3.700zM7.200 8.900C5.900 9.800 4.900 11 4.300 12c1.200 1.900 4 4 7.700 4 1.400 0 2.700-.3 3.800-.8l-1.800-1.800a3 3 0 01-4-4z" /></svg></button>}

      {needsTap && !paused && <button type="button" className="wp-tap" onClick={tap}>Tap for sound</button>}

      {!noGame && !paused && <button type="button" className={'wp-menu-btn' + (padName ? ' faint' : '')} onClick={() => setPanel('menu')} aria-label="Game menu"><svg viewBox="0 0 24 24" width="22" height="22" aria-hidden="true"><path fill="currentColor" d="M4 6h16v2H4zm0 5h16v2H4zm0 5h16v2H4z" /></svg></button>}

      {values['hotkeys.touchBar'] === true && !paused && (
        <div className="wp-touchbar" role="group" aria-label="Shortcuts">
          {([['rewind', 'Rewind'], ['fastForward', 'Fast-forward'], ['quickSave', 'Quick save'], ['quickLoad', 'Quick load']] as [HotAction, string][]).map(([a, label]) => (
            <button key={a} type="button" className="wp-btn" aria-label={label}
              onPointerDown={(e) => { e.preventDefault(); fire(a, true) }}
              onPointerUp={() => { if (HOLD_ACTIONS.includes(a)) fire(a, false) }}
              onPointerCancel={() => { if (HOLD_ACTIONS.includes(a)) fire(a, false) }}
              onPointerLeave={() => { if (HOLD_ACTIONS.includes(a)) fire(a, false) }}>{label}</button>
          ))}
        </div>
      )}

      {values['osd.on'] === true && <Osd host={host} session={info} transport={transport} values={values} stampInput={stampInput} />}

      {!paused && (frozen || rewinding || heldSpeed !== 1 || recording) && (
        <div className="wp-state" role="status">
          {recording && <span>● Recording</span>}
          {rewinding && <span>{rewindHeld ? 'Rewinding: press anything to stop' : 'Rewinding'}</span>}
          {heldSpeed > 1 && <span>{heldSpeed}× speed</span>}
          {heldSpeed < 1 && <span>{heldSpeed}× speed</span>}
          {heldSpeed !== 1 && (fastForward || slow) && <button type="button" className="wp-btn" onClick={() => { setFastForward(false); setSlow(false) }}>Normal speed</button>}
          {frozen && (
            <>
              <span>Paused</span>
              <button type="button" className="wp-btn" onClick={() => fire('advance', true)}>Next frame</button>
              <button type="button" className="wp-btn" onClick={() => setFrozen(false)}>Resume</button>
            </>
          )}
        </div>
      )}

      <div className="wp-toast" role="status" aria-live="polite">{toast}</div>

      {panel === 'touch' && <TouchEditor data={controls} orientation={orientation} onClose={() => setPanel('menu')}
        onSave={(l) => saveControl({ kind: 'touch', key: deviceId(), orientation, data: l })} />}

      {panel === 'controllers' && <ControllerStudio host={host.profiles} data={controls} known={known} input={input} consoleName={host.consoleName} gameId={host.itemId} gameTitle={host.title}
        devices={info?.devices} refresh={refreshControls} onTouch={tv ? undefined : () => setPanel('touch')} onClose={() => setPanel('menu')}
        setDevice={async (port, device) => {
          const s = sessionRef.current
          if (!s) return
          await host.action(s.id, 'device', 0, false, { port, device })
          setInfo((i) => i && { ...i, devices: i.devices?.map((d) => (d.port === port ? { ...d, current: device } : d)) })
        }} />}

      {panel && panel !== 'touch' && panel !== 'controllers' && (
        <div className={'wp-menu-back' + (SIDE_PANELS.includes(panel) ? ' wp-side' : '') + (panel === 'menu' || panel === 'settings' || panel === 'more' ? ' wp-big' : '')} onClick={() => { if (mayLeavePanel()) setPanel(null) }}>
          <div ref={menuRef} className="wp-menu" role="dialog" aria-modal="true" aria-label={panelTitle(panel, host.title)} onClick={(e) => e.stopPropagation()} {...tile}>
            <SheetBar onUp={panel === 'menu' ? undefined : () => { if (mayLeavePanel()) up() }} onClose={() => { if (mayLeavePanel()) setPanel(null) }} />
            {panel === 'menu' && (
              <MenuFrame title={host.title} sub={[host.consoleName, info?.core].filter(Boolean).join(' · ')} cover={host.cover}
                foot={!padName && padSupport() === 'insecure' ? <p className="wp-muted wp-small">Using a gamepad? Most browsers only show them to secure pages. Open this server over https, or at localhost on this computer.</p> : undefined}>
                <TileGrid label="Game menu">
                  <Tile id="resume" icon="play" label="Resume" primary wide glyph="back" onClick={() => setPanel(null)} />
                  <Tile id="save" icon="save" label="Save" sub="Choose a slot" onClick={() => setPanel('save')} />
                  <Tile id="load" icon="load" label="Load" sub="Choose a save" onClick={() => setPanel('load')} />
                  <Tile id="qsave" icon="quickSave" label="Quick save" sub={`Slot ${quickSlot()}`} onClick={() => { fire('quickSave', true); setPanel(null) }} />
                  <Tile id="qload" icon="quickLoad" label="Quick load" sub={`Slot ${quickSlot()}`} onClick={() => { fire('quickLoad', true); setPanel(null) }} />
                  <Tile id="rewind" icon="rewind" label="Rewind" sub={rewindOn ? 'Until you press anything' : 'Off: press to turn on'} onClick={() => {
                    if (!rewindOn) { settings.set('person', 'emulation.rewind', true); say('Rewind is on. It can go back to about now, and no further.'); setPanel(null); return }
                    setPanel(null)
                    window.setTimeout(() => { setRewinding(true); setRewindHeld(true); act('rewind', undefined, true).catch((e) => { setRewinding(false); setRewindHeld(false); say(e?.message || 'Rewind isn’t available') }) }, 80)
                  }} />
                  <Tile id="ff" icon="ff" label={fastForward ? 'Normal speed' : 'Fast forward'} pressed={fastForward} onClick={() => { setPanel(null); window.setTimeout(() => setFastForward((x) => !x), 50) }} />
                  <Tile id="shot" icon="camera" label="Screenshot" onClick={() => { setPanel(null); window.setTimeout(() => void screenshot(), 120) }} />
                  <Tile id="pads" icon="pad" label="Controllers" sub={padName ? 'Connected' : 'Mapping'} onClick={() => setPanel('controllers')} />
                  {!tv && <Tile id="touch" icon="touch" label={touchShown ? 'Hide touch' : 'Show touch'} sub="Controls on screen" pressed={touchShown} onClick={() => { if (touchShown) { setTouchOverPad(false); if (touchOn) toggleTouch() } else { setTouchOverPad(true); if (!touchOn) toggleTouch() } }} />}
                  <Tile id="settings" icon="settings" label="Settings" sub="Picture, sound, cheats" onClick={() => setPanel('settings')} />
                  <Tile id="more" icon="more" label="More" onClick={() => setPanel('more')} />
                  <Tile id="quit" icon="quit" label="Save and quit" danger onClick={leave} />
                </TileGrid>
              </MenuFrame>
            )}
            {panel === 'settings' && (
              <MenuFrame title="Settings" sub={host.title}>
                <TileGrid label="Settings">
                  <Tile id="display" icon="picture" label="Picture" sub="Shape, filters, frame" onClick={() => setPanel('display')} />
                  <Tile id="audio" icon="sound" label="Sound" sub="Volume, delay" onClick={() => setPanel('audio')} />
                  <Tile id="emulation" icon="speed" label="Speed and rewind" onClick={() => setPanel('emulation')} />
                  <Tile id="states" icon="saves" label="Saves" sub="Slots, resuming" onClick={() => setPanel('states')} />
                  <Tile id="cheats" icon="cheat" label="Cheats" onClick={() => setPanel('cheats')} />
                  <Tile id="hotkeys" icon="keys" label="Shortcuts" onClick={() => setPanel('hotkeys')} />
                  <Tile id="osd" icon="numbers" label="On-screen numbers" onClick={() => setPanel('osd')} />
                  <Tile id="core" icon="core" label="Core options" sub="Advanced" onClick={() => setPanel('core')} />
                  <Tile id="patches" icon="patch" label="Patches" sub="Advanced" onClick={() => setPanel('patches')} />
                  <Tile id="json" icon="file" label="Settings file" sub="Advanced" onClick={() => setPanel('json')} />
                  <Tile id="back" icon="back" label="Back" onClick={() => setPanel('menu')} />
                </TileGrid>
              </MenuFrame>
            )}
            {panel === 'more' && (
              <MenuFrame title="More" sub={host.title}>
                <TileGrid label="More">
                  {(info?.discs || 0) > 1 && <Tile id="disc" icon="disc" label="Change disc" sub={`Disc ${(info?.disc || 0) + 1} of ${info?.discs}`} onClick={() => setPanel('discs')} />}
                  <Tile id="undo" icon="reset" label="Undo load" onClick={() => act('undo').then(() => { say('Undone'); setPanel(null) }, (e) => say(e?.message || 'There’s nothing to undo'))} />
                  <Tile id="pause" icon="pause" label={frozen ? 'Unpause' : 'Pause'} pressed={frozen} onClick={() => { setFrozen((x) => !x); setPanel(null) }} />
                  <Tile id="slow" icon="slow" label={slow ? 'Normal speed' : 'Slow motion'} pressed={slow} onClick={() => { setPanel(null); window.setTimeout(() => setSlow((x) => !x), 50) }} />
                  {canClip && <Tile id="clip" icon="record" label={recording ? 'Stop the clip' : 'Record a clip'} sub="Up to a minute" pressed={recording} onClick={() => { toggleClip(); setPanel(null) }} />}
                  <Tile id="start" icon="play" label="Press Start" onClick={() => press(START)} />
                  <Tile id="select" icon="more" label="Press Select" onClick={() => press(SELECT)} />
                  {!tv && <Tile id="edit" icon="touch" label="Arrange touch controls" onClick={() => setPanel('touch')} />}
                  <Tile id="connection" icon="connection" label="Connection" onClick={() => setPanel('connection')} />
                  <Tile id="full" icon="full" label="Full screen" onClick={fullscreen} />
                  <Tile id="reset" icon="reset" label="Reset the game" onClick={() => { const s = sessionRef.current; if (s) void host.action(s.id, 'reset').then(() => { say('Reset'); setPanel(null) }) }} />
                  {!safe && <Tile id="defaults" icon="settings" label="Restart with default settings" onClick={() => leaveThen(() => host.relaunch({ resume: true, safeMode: true }))} />}
                  {safe && <Tile id="mine" icon="settings" label="Restart with my settings" onClick={() => leaveThen(() => host.relaunch({ resume: true, safeMode: false }))} />}
                  <Tile id="back" icon="back" label="Back" onClick={() => setPanel('menu')} />
                </TileGrid>
              </MenuFrame>
            )}
            {(panel === 'save' || panel === 'load') && (
              <>
                <h2>Save states</h2>
                <p className="wp-muted wp-sheet-sub">{panel === 'save' ? 'Choose a slot to save to' : 'Choose a save to load'}</p>
                <SlotsPanel host={host} schema={schema} itemId={host.itemId} kind={panel} confirm={values['states.confirm'] !== false} core={info?.core} say={say}
                  act={async (n) => { const s = sessionRef.current; if (s) await host.action(s.id, panel, n) }} onDone={(closed) => setPanel(closed ? null : 'menu')} />
                <Back onClick={up} />
              </>
            )}
            {panel && panel in GROUP_TITLES && schema && settings.view && (
              <>
                <h2>{GROUP_TITLES[panel as SettingsGroup]}</h2>
                {safe && <p className="wp-caution" role="note">Safe mode: changes here are kept for later, but this game is running with defaults.</p>}
                <SettingsPanel schema={schema} api={settings} view={settings.view} groups={[panel as string]} level={level} levels={levels} onLevel={setLevel} consoleName={host.consoleName} host={host} say={say}
                  presets={panel === 'display' ? [...schema.presets, ...settings.view.resolved.presets] : undefined} />
                <Back onClick={up} />
              </>
            )}
            {panel === 'core' && settings.view && schema && (
              <>
                <h2>Core options</h2>
                <CorePanel host={host} itemId={host.itemId} api={settings} view={settings.view} level={level} levels={levels} onLevel={setLevel} consoleName={host.consoleName} say={say} onJson={() => setPanel('json')}
                  onRestart={() => leaveThen(() => host.relaunch({ resume: false, safeMode: safe }))} />
                <Back onClick={up} />
              </>
            )}
            {panel === 'json' && settings.view && (
              <>
                <h2>Custom settings file</h2>
                <JsonEditor host={host} api={settings} view={settings.view} scope={{ item: host.itemId }} level={level} levels={levels} onLevel={setLevel} consoleName={host.consoleName} title={host.title} say={say} itemId={host.itemId} onDirty={noteJsonDirty} />
                <Back onClick={() => { if (mayLeavePanel()) up() }} />
              </>
            )}
            {panel === 'cheats' && (
              <>
                <h2>Cheats</h2>
                <CheatsPanel host={host} itemId={host.itemId} title={host.title} say={say} />
                <Back onClick={up} />
              </>
            )}
            {panel === 'patches' && (
              <>
                <h2>Patches</h2>
                <PatchesPanel host={host} itemId={host.itemId} say={say} onRestart={() => leaveThen(() => host.relaunch({ resume: true, safeMode: false }))} />
                <Back onClick={up} />
              </>
            )}
            {panel === 'discs' && (
              <>
                <h2>Change disc</h2>
                <nav className="wp-menu-list" aria-label="Discs">
                  {Array.from({ length: info?.discs || 0 }, (_, n) => (
                    <button key={n} type="button" className={`wp-btn${n === (info?.disc || 0) ? ' primary' : ''}`} onClick={() => {
                      const s = sessionRef.current
                      if (s) void host.action(s.id, 'disc', n).then(() => { say(`Disc ${n + 1}`); setPanel(null) }, (e: any) => say(e?.message || 'That didn’t work'))
                    }}>Disc {n + 1}{n === (info?.disc || 0) ? ' (in the drive)' : ''}</button>
                  ))}
                </nav>
                <Back onClick={up} />
              </>
            )}
            {panel === 'connection' && (
              <>
                <h2>Connection</h2>
                <fieldset className="wp-radios">
                  <legend>How the game reaches this browser</legend>
                  {(['auto', 'webrtc', 'webrtc-direct', 'ws-h264', 'ws-jpeg'] as Mode[]).map((m) => (
                    <label key={m}><input type="radio" name="wp-mode" checked={mode === m} onChange={() => setMode(m)} /> {m === 'auto' ? 'Automatic (recommended)' : TRANSPORT_LABELS[m as TransportKind]}</label>
                  ))}
                </fieldset>
                <p className="wp-muted wp-small">Now using: {transport ? TRANSPORT_LABELS[transport.kind] : 'nothing yet'}</p>
                <label className="wp-check"><input type="checkbox" checked={values['osd.on'] === true} onChange={(e) => settings.set('person', 'osd.on', e.target.checked)} /> Show connection numbers while playing</label>
                <Back onClick={up} />
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// The same screen away from a game, for setting things up in peace. It reads controllers
// but sends them nowhere.
export function ControllersPage({ profiles, controls, controllers, consoles, consoleId, onConsole, saveControl, onClose }: {
  profiles: ProfileHost; controls: (consoleId: string, device: string) => Promise<ControlsData>; controllers: () => Promise<ControllerData>; consoles: { id: string; short: string; name: string }[]
  consoleId: string; onConsole: (id: string) => void; saveControl: (consoleId: string, s: SaveControl) => Promise<void>; onClose: () => void
}) {
  const [data, setData] = useState<ControlsData | null>(null)
  const [known, setKnown] = useState<ControllerData | null>(null)
  const [error, setError] = useState('')
  const [touch, setTouch] = useState(false)
  const orientation = useOrientation()
  const fetchData = useCallback(() => controls(consoleId, deviceId()), [controls, consoleId])
  useEffect(() => {
    let stale = false
    Promise.all([fetchData(), controllers().catch(() => ({ layouts: {}, pads: [] }))]).then(([c, k]) => { if (!stale) { setKnown(k); setData(c) } }).catch((e) => setError(e?.message || 'This couldn’t be loaded'))
    return () => { stale = true }
  }, [fetchData, controllers])
  const input = useMemo(() => (data && known ? new InputManager(data, known) : null), [data?.console, known]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!input) return
    input.setEnabled(false) // no keys taken, and nothing sent: this is only for reading pads
    input.start()
    return () => input.stop()
  }, [input])
  const refresh = useCallback(async () => {
    const fresh = await fetchData()
    setData(fresh)
    input?.setControls(fresh)
  }, [fetchData, input])
  // Escape, and the many names of Back on a TV remote.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (!['Escape', 'GoBack', 'BrowserBack', 'Back', 'XF86Back'].includes(e.key)) return
      e.preventDefault()
      // In the touch editor, the same as its Cancel.
      if (touch) setTouch(false)
      else onClose()
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [touch, onClose])
  if (error) return <div className="wp-page"><div className="wp-error" role="alert"><p>{error}</p><button type="button" className="wp-btn primary" onClick={onClose}>Back</button></div></div>
  if (!data || !input) return <div className="wp-page"><div className="wp-status" role="status"><span className="wp-spinner" aria-hidden="true" />Loading…</div></div>
  const current = consoles.find((c) => c.id === consoleId)
  return (
    <div className="wp-page">
      {touch
        ? <TouchEditor data={data} orientation={orientation} onClose={() => setTouch(false)} onSave={async (l) => { await saveControl(consoleId, { kind: 'touch', key: deviceId(), orientation, data: l }); await refresh() }} />
        : <ControllerStudio key={data.console} host={profiles} data={data} known={known} input={input} consoleName={current?.short || consoleId} refresh={refresh} onClose={onClose} onTouch={onTV() ? undefined : () => setTouch(true)}
            consolePicker={(
              <ConsoleChips consoles={consoles} chosen={consoleId} onChoose={onConsole} />
            )} />}
    </div>
  )
}

const panelTitle = (p: Panel, title: string) => ({ menu: title, settings: 'Settings', more: 'More', save: 'Save states', load: 'Save states', controllers: 'Controllers', connection: 'Connection', touch: 'Touch controls', discs: 'Change disc', core: 'Core options', cheats: 'Cheats', patches: 'Patches', json: 'Custom settings file', ...GROUP_TITLES } as Record<string, string>)[p as string]

// Every sheet over the game keeps its ways out in view, however far it scrolls: Back a level, and
// × straight back to the game. Esc, a controller's B, Back and a tap outside do the same.
function SheetBar({ onUp, onClose }: { onUp?: () => void; onClose: () => void }) {
  return (
    <div className="wp-sheet-bar">
      {onUp && <button type="button" data-bar className="wp-btn wp-sheet-up" onClick={onUp}><svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path fill="currentColor" d="M15.41 7.41L14 6l-6 6 6 6 1.41-1.41L10.83 12z" /></svg>Back</button>}
      <span className="wp-grow" />
      <button type="button" data-bar className="wp-btn wp-sheet-close" onClick={onClose} aria-label="Back to the game" title="Back to the game (Esc)">
        <span className="wp-sheet-close-text" aria-hidden="true">Back to the game</span>
        <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" /></svg>
      </button>
    </div>
  )
}

const Back = ({ onClick }: { onClick: () => void }) => <div className="wp-row"><span className="wp-grow" /><button type="button" className="wp-btn" onClick={onClick}>Back</button></div>
