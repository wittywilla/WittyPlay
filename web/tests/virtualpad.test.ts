// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, describe, test } from 'node:test'
import type { ControlsData } from '../consoles'

// A virtual gamepad plays an NES game through the browser's Gamepad API: navigator.getGamepads
// hands the web player a standard pad, its buttons and stick are pressed, and what the player
// sends the game is checked, state by state, against testdata/virtual-pad.json. The game runner's
// side (host/virtualpad_test.go) plays those same states into a game and checks it took them, so
// between the two the whole way from a pad to a game is covered with no server. Run with: npm test

type Step = { say: string; pad: { buttons?: number[]; axes?: number[] }; sends: string; buttons: string[]; frames: number }
const fixture = JSON.parse(readFileSync(new URL('../../testdata/virtual-pad.json', import.meta.url), 'utf8')) as { controls: Omit<ControlsData, 'saved' | 'game' | 'profiles' | 'background'>; steps: Step[] }

// The RetroPad's buttons, by the numbers a core reads (RETRO_DEVICE_ID_JOYPAD_*).
const RETRO: Record<string, number> = { b: 0, y: 1, select: 2, start: 3, up: 4, down: 5, left: 6, right: 7, a: 8, x: 9, l: 10, r: 11, l2: 12, r2: 13, l3: 14, r3: 15 }

// What a game runner reads from a state (ipc.ParseInput): the player, then the buttons held.
function asTheGameReadsIt(msg: Uint8Array) {
  const v = new DataView(msg.buffer, msg.byteOffset, msg.byteLength)
  const mask = v.getUint16(1, true)
  return { port: msg[0], held: Object.keys(RETRO).filter((n) => mask & (1 << RETRO[n])) }
}
const hex = (b: Uint8Array) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')

// A standard-mapping pad as a browser describes one (the Gamepad API's Gamepad and GamepadButton).
function virtualPad() {
  return {
    id: 'Virtual pad (STANDARD GAMEPAD Vendor: 045e Product: 028e)', index: 0, mapping: 'standard', connected: true, timestamp: 1,
    buttons: Array.from({ length: 17 }, () => ({ pressed: false, touched: false, value: 0 })), axes: [0, 0, 0, 0], vibrationActuator: null,
  }
}

describe('a virtual gamepad playing an NES game', () => {
  let InputManager: typeof import('../input').InputManager
  let pads: ReturnType<typeof virtualPad>[] = []
  let frames: (() => void)[] = []
  const events = new Map<string, Set<(e: unknown) => void>>()
  const fire = (name: string, e: unknown = {}) => { for (const f of [...(events.get(name) || [])]) f(e) }
  const frame = () => { const f = frames; frames = []; for (const cb of f) cb() }
  const was: Record<string, PropertyDescriptor | undefined> = {}
  const stubbed = ['navigator', 'window', 'document', 'addEventListener', 'removeEventListener', 'requestAnimationFrame', 'cancelAnimationFrame', 'matchMedia', 'localStorage']

  before(async () => {
    const g = globalThis as any
    for (const k of stubbed) was[k] = Object.getOwnPropertyDescriptor(g, k)
    const on = (prefix: string) => (n: string, f: any) => { if (!events.has(prefix + n)) events.set(prefix + n, new Set()); events.get(prefix + n)!.add(f) }
    const off = (prefix: string) => (n: string, f: any) => events.get(prefix + n)?.delete(f)
    // The browser's Gamepad API, with the virtual pad plugged in.
    Object.defineProperty(g, 'navigator', { configurable: true, value: { userAgent: 'test', getGamepads: () => pads } })
    g.window = g
    g.document = { hidden: false, addEventListener: on('doc:'), removeEventListener: off('doc:') }
    g.addEventListener = on('')
    g.removeEventListener = off('')
    g.requestAnimationFrame = (cb: () => void) => { frames.push(cb); return frames.length }
    g.cancelAnimationFrame = () => { frames = [] }
    g.matchMedia = () => ({ matches: false })
    g.localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} }
    InputManager = (await import('../input')).InputManager
  })
  after(() => {
    const g = globalThis as any
    for (const k of stubbed) { const d = was[k]; if (d) Object.defineProperty(g, k, d); else delete g[k] }
  })

  // The controls the server sends for the NES (GET /consoles/nes/controls), for someone who has
  // changed nothing. The fixture's part is checked against the Go tables in host/virtualpad_test.go.
  const controls = (): ControlsData => ({
    ...fixture.controls,
    defaults: { ...fixture.controls.defaults, touch: { landscape: {}, portrait: {} } },
    saved: { gamepad: {}, keyboard: null, touch: {} }, game: 0, profiles: [], background: { version: 0, fit: 'cover', x: 50, y: 50, dim: 50 },
  })

  test('every press reaches the game as the state the game runner reads', () => {
    const sent: Uint8Array[] = []
    const m = new InputManager(controls())
    m.setSender((msg) => sent.push(new Uint8Array(msg)))
    m.start()
    try {
      const pad = virtualPad()
      pads = [pad]
      fire('gamepadconnected', { gamepad: pad })
      assert.deepEqual(m.listPads().map((p) => [p.name, p.port, p.standard]), [['Virtual pad', 0, true]], 'the pad is player 1, read as a standard pad')

      for (const step of fixture.steps) {
        pad.buttons.forEach((b, i) => { const on = !!step.pad.buttons?.includes(i); b.pressed = on; b.touched = on; b.value = on ? 1 : 0 })
        pad.axes = step.pad.axes ? [...step.pad.axes] : [0, 0, 0, 0]
        pad.timestamp++
        const before = sent.length
        frame()
        // Until something is pressed nothing need be sent: the game starts with nothing held.
        const now = [...sent].reverse().find((s) => s[0] === 0) ?? new Uint8Array(13)
        // A change goes out on the frame it happens, not on a later heartbeat.
        if (step.buttons.length) assert.ok(sent.length > before, `${step.say}: sent at once`)
        assert.deepEqual(asTheGameReadsIt(now), { port: 0, held: step.buttons }, step.say)
        assert.equal(hex(now), step.sends, `${step.say}: the bytes the game runner is played in host/virtualpad_test.go`)
      }
    } finally {
      m.stop()
      pads = []
    }
  })

  test('Start held down is one press: the state is not sent again each frame', () => {
    const sent: Uint8Array[] = []
    const m = new InputManager(controls())
    m.setSender((msg) => sent.push(new Uint8Array(msg)))
    m.start()
    try {
      const pad = virtualPad()
      pads = [pad]
      fire('gamepadconnected', { gamepad: pad })
      pad.buttons[9] = { pressed: true, touched: true, value: 1 }
      pad.timestamp++
      frame()
      const starts = () => sent.filter((s) => asTheGameReadsIt(s).held.includes('start')).length
      const first = starts()
      assert.equal(first, 1)
      for (let i = 0; i < 30; i++) { pad.timestamp++; frame() }
      assert.equal(starts(), first, 'held, not pressed again')
    } finally {
      m.stop()
      pads = []
    }
  })
})
