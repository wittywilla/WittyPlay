// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// A standalone emulator's game: what the app hands the player, and the parts of the player with
// no browser in them, so they can be tested on their own (tests/emulator.test.ts). The emulator
// runs in a container or a sandbox on the server (docs/DOCKER-BACKEND.md) and is streamed here
// as a core's game is, or shown through its own desktop.

import type { ControlsData } from './consoles'
import type { ControllerData } from './mapping'
import type { Hello, StreamInfo } from './transport'
import type { Explained } from './words'

export type EmulatorBackend = 'docker+webrtc' | 'docker+kasmvnc' | 'local+webrtc'

// What starting a game with an emulator answers (POST /api/v1/games/{id}/play/emulator).
export type EmulatorSession = {
  id: string; emulator: string; name: string; backend: EmulatorBackend | string
  connect?: string; kasmUrl?: string; stop: string; pause?: string; resume?: string
  width: number; height: number; fps: number; players: number; notices?: string[]
}

// What connecting answers: the stream as it is, and the server's side of the WebRTC offer.
export type Connected = { stream: StreamInfo; sdp: string }

// The player's way to the server. A call that fails rejects with an Error; one the server
// refused may carry its `status` and `code`, which is how a game that has ended is told apart.
export interface EmulatorHost {
  connect(hello: Hello, offer: string): Promise<Connected>
  stop(): Promise<void>
  pause(): Promise<void>
  resume(): Promise<void>
  // The console's controller, as for a core's game. Without it the usual layout is used.
  controls?(device: string): Promise<ControlsData>
  controllers?(): Promise<ControllerData>
  // A server address made ready for an <iframe>, where the app has to add anything to it.
  mediaUrl?(path: string): string
  report?(level: 'error' | 'warn', message: string): void
}

// How a session reaches the screen: as a stream, or through the emulator's own desktop.
export type Showing = 'stream' | 'desktop' | 'none'
export function showingOf(s: Pick<EmulatorSession, 'backend' | 'connect' | 'kasmUrl'>): Showing {
  if (s.backend === 'docker+kasmvnc') return s.kasmUrl ? 'desktop' : 'none'
  if (s.backend === 'docker+webrtc' || s.backend === 'local+webrtc') return s.connect ? 'stream' : 'none'
  // A backend from after this player was written: go by what it gave.
  return s.connect ? 'stream' : s.kasmUrl ? 'desktop' : 'none'
}

// Pause is offered only where the session has both halves of it.
export const canPause = (s: Pick<EmulatorSession, 'pause' | 'resume'>) => !!s.pause && !!s.resume

export const playersOf = (s: Pick<EmulatorSession, 'players'>) => Math.max(1, Math.min(4, Math.floor(Number(s.players)) || 1))

// The game isn't on the server any more: connecting answered 404, or said so by its code.
export function isGone(e: unknown): boolean {
  const x = e as { status?: unknown; code?: unknown; data?: { code?: unknown } } | null
  return !!x && (x.status === 404 || x.code === 'no-session' || x.data?.code === 'no-session')
}

// ---- where the stream stands ----

export type Phase = 'connecting' | 'playing' | 'reconnecting' | 'failed' | 'ended'
// attempt goes up with every try, so the player knows to start one. retried: the one try a lost
// connection gets by itself has been used.
export type StreamState = { phase: Phase; attempt: number; retried: boolean; message: string }
export type StreamEvent =
  | { type: 'connected' }
  | { type: 'lost' } // the connection went while playing
  | { type: 'error'; gone: boolean; message: string } // a try at connecting failed
  | { type: 'retry' } // someone pressed Retry
  | { type: 'ended'; message?: string } // the server said the game is over

export const firstState: StreamState = { phase: 'connecting', attempt: 1, retried: false, message: '' }

export function nextState(s: StreamState, e: StreamEvent): StreamState {
  if (s.phase === 'ended') return s
  switch (e.type) {
    case 'connected':
      return s.phase === 'connecting' || s.phase === 'reconnecting' ? { ...s, phase: 'playing', retried: false, message: '' } : s
    case 'lost':
      if (s.phase !== 'playing') return s
      return { phase: 'reconnecting', attempt: s.attempt + 1, retried: true, message: '' }
    case 'error':
      if (e.gone) return { ...s, phase: 'ended', message: '' }
      if (s.phase !== 'connecting' && s.phase !== 'reconnecting') return s
      return { ...s, phase: 'failed', message: e.message }
    case 'retry':
      return s.phase === 'failed' ? { phase: 'connecting', attempt: s.attempt + 1, retried: false, message: '' } : s
    case 'ended':
      return { ...s, phase: 'ended', message: e.message || '' }
  }
}

// problemOf is what to say when there's no game to show, and whether trying again can help.
export function problemOf(phase: Phase | 'none', message: string, name: string): Explained | null {
  if (phase === 'none') return { title: 'This game can’t be shown here', body: `The server started ${name} in a way this player doesn’t know. A newer WittyPlay may.` }
  if (phase === 'ended') return { title: 'This game has ended', body: message || `${name} has stopped on the server. It can be started again from the game’s page.` }
  if (phase === 'failed') {
    return { title: 'The game is running, but its picture can’t reach this device', body: `${message ? message + ' ' : ''}This is usually a firewall or a network that blocks the connection. Trying again may get through.`, retry: true }
  }
  return null
}

// ---- a press on the picture ----

export type Box = { left: number; top: number; width: number; height: number }

// pictureBox is where a picture of picW by picH sits in the element showing it, whole and
// centred, with bars on two sides when the shapes differ (object-fit: contain).
export function pictureBox(el: Box, picW: number, picH: number): Box {
  if (!(picW > 0) || !(picH > 0) || !(el.width > 0) || !(el.height > 0)) return el
  const scale = Math.min(el.width / picW, el.height / picH)
  const width = picW * scale, height = picH * scale
  return { left: el.left + (el.width - width) / 2, top: el.top + (el.height - height) / 2, width, height }
}

// pointOnPicture is a place on the page as fractions of the picture, 0 to 1 each way, which is
// what the server takes for a pointer. inside is false on the bars around it; x and y are then
// the nearest place on the picture's edge, for a drag that leaves it.
export function pointOnPicture(el: Box, picW: number, picH: number, clientX: number, clientY: number): { x: number; y: number; inside: boolean } {
  const box = pictureBox(el, picW, picH)
  if (!(box.width > 0) || !(box.height > 0)) return { x: 0, y: 0, inside: false }
  const x = (clientX - box.left) / box.width, y = (clientY - box.top) / box.height
  const inside = x >= 0 && x <= 1 && y >= 0 && y <= 1
  return { x: Math.max(0, Math.min(1, x)), y: Math.max(0, Math.min(1, y)), inside }
}

// ---- the emulator's desktop ----

// The same server's own pages only: a session's desktop address is a path on this server, and
// anything else is not put in a frame.
export function desktopSrc(kasmUrl: string | undefined, mediaUrl?: (path: string) => string): string {
  const path = (kasmUrl || '').trim()
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('\\')) return ''
  return mediaUrl ? mediaUrl(path) : path
}

// ---- controls ----

// The usual controller, for when the app has none of a console's own to give: the layout the
// server gives a console it knows nothing more about (controls.go, DefaultGamepad and
// DefaultKeyboard), with both sticks.
export function usualControls(): ControlsData {
  const labels = ['B', 'Y', 'Select', 'Start', 'Up', 'Down', 'Left', 'Right', 'A', 'X', 'L', 'R', 'L2', 'R2', 'L3', 'R3',
    'Left stick left', 'Left stick right', 'Left stick up', 'Left stick down', 'Right stick left', 'Right stick right', 'Right stick up', 'Right stick down']
  const group = (id: number) => (id >= 16 ? 'stick' : id >= 4 && id <= 7 ? 'dpad' : id === 2 || id === 3 ? 'meta' : id >= 10 ? 'shoulder' : 'face') as ControlsData['targets'][number]['group']
  return {
    console: '',
    pad: { art: '', sticks: true },
    controls: [],
    targets: labels.map((label, id) => ({ id, label, group: group(id) })),
    defaults: {
      gamepad: {
        0: { b: 0 }, 8: { b: 1 }, 1: { b: 2 }, 9: { b: 3 }, 10: { b: 4 }, 11: { b: 5 }, 12: { b: 6 }, 13: { b: 7 }, 2: { b: 8 }, 3: { b: 9 }, 14: { b: 10 }, 15: { b: 11 },
        4: { b: 12 }, 5: { b: 13 }, 6: { b: 14 }, 7: { b: 15 },
        16: { a: 0, d: -1 }, 17: { a: 0, d: 1 }, 18: { a: 1, d: -1 }, 19: { a: 1, d: 1 }, 20: { a: 2, d: -1 }, 21: { a: 2, d: 1 }, 22: { a: 3, d: -1 }, 23: { a: 3, d: 1 },
      },
      keyboard: {
        4: 'ArrowUp', 5: 'ArrowDown', 6: 'ArrowLeft', 7: 'ArrowRight', 0: 'KeyZ', 8: 'KeyX', 1: 'KeyA', 9: 'KeyS', 10: 'KeyQ', 11: 'KeyW', 12: 'KeyE', 13: 'KeyR',
        3: 'Enter', 2: 'ShiftRight', 18: 'KeyI', 19: 'KeyK', 16: 'KeyJ', 17: 'KeyL',
      },
      remote: { 4: 'ArrowUp', 5: 'ArrowDown', 6: 'ArrowLeft', 7: 'ArrowRight', 8: 'Enter', 0: 'MediaPlayPause', 2: 'MediaRewind', 3: 'MediaFastForward' },
      touch: { landscape: {}, portrait: {} },
      options: {},
    },
    saved: { gamepad: {}, keyboard: null, touch: {} },
    game: 0,
    profiles: [],
    background: { version: 0, fit: 'cover', x: 50, y: 50, dim: 0 },
  }
}
