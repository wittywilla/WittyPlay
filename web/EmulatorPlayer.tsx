// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useCallback, useEffect, useId, useMemo, useReducer, useRef, useState } from 'react'
import { canPause, desktopSrc, firstState, isGone, nextState, playersOf, pointOnPicture, problemOf, showingOf, usualControls, type EmulatorHost, type EmulatorSession } from './emulator'
import { deviceId, InputManager } from './input'
import { screenNow } from './refresh'
import { ProblemScreen } from './Screens'
import { make, type Hello, type PlayEvent, type Signal, type Transport } from './transport'
import './play.css'
import './emulator.css'

// The player for a standalone emulator's game: its picture and sound, the controllers, and a
// slim bar to pause, stop or leave. There are no save states, cheats or core options here, as
// the emulator keeps its own. A stream arrives as a core's game does (transport.ts, input.ts);
// an emulator that can't be streamed that way is shown through its own desktop instead.

const BAR_MS = 3000 // how long the bar stays after the pointer last moved
const NOTICE_MS = 12000

type Props = { host: EmulatorHost; session: EmulatorSession; title: string; onExit: () => void }

export function EmulatorPlayer({ host, session, title, onExit }: Props) {
  const showing = showingOf(session)
  const players = playersOf(session)
  const root = useRef<HTMLDivElement>(null)
  const pic = useRef<HTMLDivElement>(null)
  const bar = useRef<HTMLDivElement>(null)
  const [state, dispatch] = useReducer(nextState, firstState)
  const [transport, setTransport] = useState<Transport | null>(null)
  const transportRef = useRef<Transport | null>(null)
  const [needsTap, setNeedsTap] = useState(false)
  const [stalled, setStalled] = useState(false)
  const [paused, setPaused] = useState(false)
  const pausedRef = useRef(false)
  pausedRef.current = paused
  const lostWhilePaused = useRef(false)
  const [busy, setBusy] = useState(false)
  const [asking, setAsking] = useState(false)
  const [full, setFull] = useState(false)
  const [barFocus, setBarFocus] = useState(false)
  const [barPeek, setBarPeek] = useState(true)
  const [notices, setNotices] = useState<string[]>(() => (session.notices || []).filter(Boolean))
  const [toast, setToast] = useState('')
  const closing = useRef(false)
  const askId = useId()

  const say = useCallback((text: string) => setToast(text), [])
  useEffect(() => {
    if (!toast) return
    const t = window.setTimeout(() => setToast(''), 3500)
    return () => window.clearTimeout(t)
  }, [toast])
  // What the server had to say about this start is said once, and goes by itself.
  useEffect(() => {
    if (!notices.length) return
    const t = window.setTimeout(() => setNotices([]), NOTICE_MS)
    return () => window.clearTimeout(t)
  }, [notices])

  // ---- controllers ----

  const input = useMemo(() => new InputManager(usualControls()), [])
  useEffect(() => {
    if (showing !== 'stream') return
    let live = true
    // The console's own controller and the list of known pads are a help, not a need.
    host.controls?.(deviceId()).then((c) => { if (live && c) input.setControls(c) }).catch(() => {})
    // An emulator takes as many players as it said; anyone beyond that presses nothing.
    input.filter = (port, mask) => (port < players ? mask : 0)
    let held = ''
    input.onChange = () => {
      const ports = input.ports().filter((p) => p < players)
      if (ports.join() !== held) {
        held = ports.join()
        transportRef.current?.control?.({ type: 'seats', ports })
      }
    }
    input.onMenu = () => openBar()
    input.start()
    return () => { live = false; input.stop() }
  }, [host, input, players, showing]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- the stream ----

  const onEvent = useCallback((e: PlayEvent) => {
    if (e.type === 'message' && e.text) say(e.text)
    else if (e.type === 'ended' || e.type === 'shutdown') {
      closing.current = true
      dispatch({ type: 'ended', message: e.text })
    }
  }, [say])

  useEffect(() => {
    if (showing !== 'stream' || (state.phase !== 'connecting' && state.phase !== 'reconnecting')) return
    const again = state.phase === 'reconnecting'
    let stale = false, taken = false
    const signal: Signal = {
      // Version 1 of the stream isn't offered for an emulator; only stream and hello are used.
      answer: () => Promise.reject(new Error('this game has only one way to connect')),
      wsUrl: () => '',
      stream: (hello: Hello, sdp: string) => host.connect(hello, sdp),
      hello: (): Hello => ({ v: 2, app: 'WittyPlay web', device: deviceId(), seats: input.ports().filter((p) => p < players), display: screenNow(), latency: 'auto' }),
    }
    const t = make('webrtc', signal)
    t.onEvent = onEvent
    t.connect().then(() => {
      if (stale || closing.current) { t.close(); return }
      taken = true
      // After a lost connection the last picture stays up until there's a new one to show.
      transportRef.current?.close()
      transportRef.current = t
      t.onLost = (why) => {
        if (closing.current || transportRef.current !== t) return
        host.report?.('warn', `The emulator's stream was lost (${why})`)
        setStalled(false)
        // A paused emulator sends nothing, which looks the same as a stream that has gone. It's
        // looked at again when the game carries on.
        if (pausedRef.current) { lostWhilePaused.current = true; return }
        dispatch({ type: 'lost' })
      }
      t.onStall = (on) => { if (transportRef.current === t) setStalled(on) }
      input.setSender((msg) => t.send(msg))
      setStalled(false)
      setTransport(t)
      setNeedsTap(t.needsUnmute())
      dispatch({ type: 'connected' })
    }, (e) => {
      t.close()
      if (stale || closing.current) return
      const gone = isGone(e)
      const message = e?.message || String(e)
      host.report?.(gone ? 'warn' : 'error', gone ? 'The emulator was no longer running on the server' : `The emulator's stream could not connect: ${message}`)
      if (again || gone) {
        transportRef.current?.close()
        transportRef.current = null
        setTransport(null)
      }
      dispatch({ type: 'error', gone, message })
    })
    return () => { stale = true; if (!taken) t.close() }
  }, [state.attempt]) // eslint-disable-line react-hooks/exhaustive-deps

  // The picture goes on the page whole, with bars beside it where the shapes differ.
  useEffect(() => {
    const el = transport?.element, box = pic.current
    if (!el || !box) return
    el.classList.add('wp-emu-media')
    box.replaceChildren(el)
    return () => { el.remove() }
  }, [transport])

  useEffect(() => () => {
    closing.current = true
    transportRef.current?.close()
    transportRef.current = null
  }, [])

  const playing = state.phase === 'playing'
  const over = state.phase === 'failed' || state.phase === 'ended' || showing === 'none'
  useEffect(() => { input.setEnabled(playing && !paused && !asking && !barFocus) }, [input, playing, paused, asking, barFocus])

  // ---- the bar ----

  const hideTimer = useRef(0)
  const peek = useCallback(() => {
    setBarPeek(true)
    window.clearTimeout(hideTimer.current)
    hideTimer.current = window.setTimeout(() => setBarPeek(false), BAR_MS)
  }, [])
  useEffect(() => { peek(); return () => window.clearTimeout(hideTimer.current) }, [peek])
  const openBar = useCallback(() => { bar.current?.querySelector<HTMLElement>('button:not(:disabled)')?.focus() }, [])
  const closeBar = useCallback(() => { root.current?.focus({ preventScroll: true }) }, [])
  // A desktop takes the pointer and the keys for itself, so its bar stays where it is.
  const barOn = showing !== 'stream' || !playing || paused || barFocus || barPeek || asking

  useEffect(() => { root.current?.focus({ preventScroll: true }) }, [])
  useEffect(() => {
    const changed = () => setFull(!!document.fullscreenElement)
    document.addEventListener('fullscreenchange', changed)
    return () => document.removeEventListener('fullscreenchange', changed)
  }, [])
  const fullscreen = () => {
    const el = root.current as (HTMLDivElement & { webkitRequestFullscreen?: () => void }) | null
    if (document.fullscreenElement) void document.exitFullscreen()
    else void (el?.requestFullscreen?.() || el?.webkitRequestFullscreen?.())
  }

  // Esc leaves full screen first, then whatever is open, and with nothing open it opens the bar.
  const esc = useRef(() => {})
  esc.current = () => {
    if (asking) { setAsking(false); closeBar() }
    else if (document.fullscreenElement) void document.exitFullscreen()
    else if (barFocus) closeBar()
    else openBar()
  }
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || input.isBound(e.code)) return
      e.preventDefault()
      esc.current()
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [input])

  // ---- what the bar does ----

  const togglePause = async () => {
    if (busy) return
    setBusy(true)
    try {
      if (paused) {
        await host.resume()
        setPaused(false)
        if (lostWhilePaused.current) { lostWhilePaused.current = false; dispatch({ type: 'lost' }) }
      } else {
        await host.pause()
        setPaused(true)
      }
    } catch (e: any) {
      if (isGone(e)) { closing.current = true; dispatch({ type: 'ended' }) } else say(e?.message || 'That didn’t work')
    } finally { setBusy(false) }
  }
  const stop = async () => {
    if (busy) return
    setBusy(true)
    try {
      await host.stop()
    } catch (e: any) {
      // Already gone is as good as stopped. Anything else is said, and the game stays up.
      if (!isGone(e)) { say(e?.message || 'The game couldn’t be stopped'); setBusy(false); setAsking(false); return }
    }
    closing.current = true
    transportRef.current?.close()
    onExit()
  }
  // A press with a mouse or a finger gives the keys back to the game; one made from the
  // keyboard leaves the focus where it is, for the next.
  const pressed = (e: React.MouseEvent, f: () => void) => { f(); if (e.detail > 0) closeBar() }
  const tap = () => { transport?.unmute(); setNeedsTap(false) }

  // ---- a press on the picture ----

  const down = useRef(false)
  const point = (e: React.PointerEvent, phase: 'down' | 'move' | 'up') => {
    const el = transport?.element
    if (!el) return
    const v = el as HTMLVideoElement
    const p = pointOnPicture(el.getBoundingClientRect(), v.videoWidth || session.width, v.videoHeight || session.height, e.clientX, e.clientY)
    if (phase === 'down' && !p.inside) return
    if (phase === 'down') down.current = true
    else if (!down.current) return
    if (phase === 'up') down.current = false
    input.setPointer(p.x, p.y, phase !== 'up')
  }
  const stageDown = (e: React.PointerEvent) => {
    closeBar()
    if (!playing || paused) return
    e.preventDefault()
    try { e.currentTarget.setPointerCapture(e.pointerId) } catch { /* fine */ }
    point(e, 'down')
  }

  const problem = problemOf(showing === 'none' ? 'none' : state.phase, state.message, session.name)
  const status = over || paused || showing !== 'stream' ? '' : state.phase === 'connecting' ? 'Connecting…' : state.phase === 'reconnecting' ? 'Reconnecting…' : stalled ? 'Waiting for the picture…' : ''
  const src = showing === 'desktop' ? desktopSrc(session.kasmUrl, host.mediaUrl) : ''

  return (
    <div ref={root} tabIndex={-1} className={`wp-root wp-emu wp-emu-${showing}${barOn ? ' wp-emu-bar-on' : ''}${playing && !paused ? ' wp-playing' : ''}`} onPointerMove={peek} onPointerDown={peek}>
      {!over && (
        <div ref={bar} className="wp-emu-bar" role="toolbar" aria-label="Game controls"
          onFocus={() => setBarFocus(true)} onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setBarFocus(false) }}>
          <button type="button" className="wp-btn" onClick={() => onExit()}><span aria-hidden="true">×</span> Back</button>
          <div className="wp-emu-names">
            <strong>{title}</strong>
            <span>{session.name}</span>
          </div>
          {canPause(session) && <button type="button" className="wp-btn" disabled={busy || (showing === 'stream' && !transport)} aria-pressed={paused} onClick={(e) => pressed(e, () => void togglePause())}>{paused ? 'Resume' : 'Pause'}</button>}
          <button type="button" className="wp-btn" aria-pressed={full} onClick={(e) => pressed(e, fullscreen)}>{full ? 'Leave full screen' : 'Full screen'}</button>
          <button type="button" className="wp-btn danger" disabled={busy} onClick={() => setAsking(true)}>Stop</button>
        </div>
      )}

      {showing === 'desktop' && !over && (
        <>
          <p className="wp-emu-note">{session.name} is shown through its own desktop. Click inside it to use the keyboard and mouse.</p>
          {src
            ? <iframe className="wp-emu-frame" title={`${title}, in ${session.name}`} src={src} sandbox="allow-scripts allow-pointer-lock" allow="fullscreen; gamepad; autoplay" />
            : <div className="wp-error" role="alert"><p>The server gave no address for this emulator’s desktop.</p><button type="button" className="wp-btn primary" onClick={() => onExit()}>Back</button></div>}
          {paused && <div className="wp-status" role="status">Paused</div>}
        </>
      )}

      {showing === 'stream' && (
        <div className="wp-video-area wp-emu-stage" onPointerDown={stageDown} onPointerMove={(e) => point(e, 'move')} onPointerUp={(e) => point(e, 'up')} onPointerCancel={(e) => point(e, 'up')}>
          <div ref={pic} className="wp-emu-pic" />
        </div>
      )}

      {notices.length > 0 && !over && (
        <div className="wp-emu-notices" role="status">
          <ul>{notices.map((n) => <li key={n}>{n}</li>)}</ul>
          <button type="button" className="wp-btn" onClick={(e) => pressed(e, () => setNotices([]))}>Dismiss</button>
        </div>
      )}

      {status && <div className="wp-status" role="status"><span className="wp-spinner" aria-hidden="true" />{status}</div>}
      {showing === 'stream' && paused && !over && (
        <div className="wp-error wp-emu-paused" role="status">
          <p>Paused</p>
          <button type="button" className="wp-btn primary" disabled={busy} onClick={(e) => pressed(e, () => void togglePause())}>Resume</button>
        </div>
      )}
      {needsTap && playing && !paused && <button type="button" className="wp-tap" onClick={(e) => pressed(e, tap)}>Tap to turn the sound on</button>}

      {problem && (
        <ProblemScreen title={`${title} · ${session.name}`} problem={problem} onBack={() => onExit()}
          onRetry={problem.retry ? () => dispatch({ type: 'retry' }) : undefined} />
      )}

      {asking && (
        <div className="wp-emu-ask-wrap">
          <div className="wp-emu-ask" role="alertdialog" aria-modal="true" aria-labelledby={askId + 't'} aria-describedby={askId + 'd'}
            onKeyDown={(e) => {
              if (e.key !== 'Tab') return
              // Two buttons, and the focus stays between them.
              const buttons = [...e.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled)')]
              const at = buttons.indexOf(document.activeElement as HTMLElement)
              e.preventDefault()
              buttons[(at + (e.shiftKey ? buttons.length - 1 : 1)) % buttons.length]?.focus()
            }}>
            <h2 id={askId + 't'}>Stop this game?</h2>
            <p id={askId + 'd'}>{session.name} closes, and anything the game hasn’t saved itself is lost.</p>
            <div className="wp-row wp-emu-ask-actions">
              <button type="button" className="wp-btn" autoFocus disabled={busy} onClick={() => { setAsking(false); closeBar() }}>Keep playing</button>
              <button type="button" className="wp-btn primary" disabled={busy} onClick={() => void stop()}>{busy ? 'Stopping…' : 'Stop the game'}</button>
            </div>
          </div>
        </div>
      )}

      <div className="wp-toast" role="status" aria-live="polite">{toast}</div>
    </div>
  )
}
