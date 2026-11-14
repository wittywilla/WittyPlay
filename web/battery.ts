// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Batteries: the controllers' and this device's, as the status bar and the game menu show them, and
// the notice when one runs low. No browser in here; BatteryChips.tsx reads them.
//
// A browser tells a page about its own battery through navigator.getBattery() (Chrome, Edge and
// Android browsers; not Safari or Firefox). The Gamepad API has no battery in its standard: a pad's
// battery is read only where a browser adds one (pad.battery, with level 0 to 1 and charging), and
// is otherwise not shown at all rather than guessed. The Apple and Android apps read both from the
// system and draw them the same way.

export type BatteryKind = 'device' | 'controller'

export type BatteryReading = {
  id: string // "device", or the pad's slot: "pad-0"
  kind: BatteryKind
  name: string // "P1" for a controller, "This device" for the device
  detail?: string // the controller's own name, for a screen reader
  level: number // 0 to 1
  charging: boolean
}

// Low: at or under a fifth, and not charging. It counts as low again only once it has been back
// over LOW_CLEAR or on charge, so a level that wobbles about the line isn't announced over and over.
export const LOW = 0.2
export const LOW_CLEAR = 0.25

export const percent = (level: number) => Math.round(Math.min(1, Math.max(0, level)) * 100)

export const isLow = (r: Pick<BatteryReading, 'level' | 'charging'>) => !r.charging && r.level <= LOW + 1e-9

// What a chip shows: short, the same everywhere. "P1 72%", "Battery 64%".
export function batteryText(r: BatteryReading): string {
  return `${r.kind === 'device' ? 'Battery' : r.name} ${percent(r.level)}%`
}

// What a screen reader says for it: "Controller P1, Xbox Wireless Controller: battery 72%, charging".
export function batteryLabel(r: BatteryReading): string {
  const who = r.kind === 'device' ? 'This device' : `Controller ${r.name}${r.detail ? `, ${r.detail}` : ''}`
  return `${who}: battery ${percent(r.level)}%${r.charging ? ', charging' : isLow(r) ? ', low' : ''}`
}

// The notice for a battery that ran low, said once.
export function lowNotice(r: BatteryReading): string {
  const n = percent(r.level)
  return r.kind === 'device'
    ? `This device’s battery is low: ${n}%. Plug it in soon.`
    : `Controller ${r.name}’s battery is low: ${n}%. Charge it or change its batteries soon.`
}

type PadLike = { index?: number; id?: string; connected?: boolean; battery?: unknown } | null | undefined

// padBattery is a pad's battery where the browser gives one, or null. Only a level from 0 to 1 is
// believed; anything else isn't a battery this page can trust.
export function padBattery(pad: PadLike): { level: number; charging: boolean } | null {
  const b = pad?.battery as { level?: unknown; charging?: unknown } | null | undefined
  if (!b || typeof b !== 'object') return null
  const level = b.level
  if (typeof level !== 'number' || !Number.isFinite(level) || level < 0 || level > 1) return null
  return { level, charging: b.charging === true }
}

// padName is a pad's own name without the browser's vendor and product numbers.
export const padName = (id: string) => id.replace(/\s*\(.*$/, '').trim() || 'Controller'

// padReadings is each connected pad with a battery the browser gives, named by its player: P1 for
// the first connected, and so on, as the status bar numbers them.
export function padReadings(pads: ArrayLike<PadLike>): BatteryReading[] {
  const out: BatteryReading[] = []
  let n = 0
  for (let i = 0; i < pads.length; i++) {
    const p = pads[i]
    if (!p || p.connected === false) continue
    n++
    const b = padBattery(p)
    if (b) out.push({ id: `pad-${p.index ?? i}`, kind: 'controller', name: `P${n}`, detail: padName(p.id || ''), ...b })
  }
  return out
}

type DeviceBattery = { level: number; charging: boolean; chargingTime?: number; dischargingTime?: number }

// deviceReading is this device's battery, or null for one with none to speak of. A computer with no
// battery answers full, charging and charged at once; so does one full on its charger, which there
// is no need to show either.
export function deviceReading(b: DeviceBattery | null | undefined): BatteryReading | null {
  if (!b || typeof b.level !== 'number' || !Number.isFinite(b.level)) return null
  if (b.charging && b.level >= 1 && (b.chargingTime ?? 0) === 0) return null
  return { id: 'device', kind: 'device', name: 'This device', level: Math.min(1, Math.max(0, b.level)), charging: !!b.charging }
}

// LowBatteryNotes says which batteries just ran low: each once, until it has been charged again.
export class LowBatteryNotes {
  private said = new Set<string>()

  // check takes every reading now and answers the notices to give, if any.
  check(readings: BatteryReading[]): string[] {
    const out: string[] = []
    const now = new Set(readings.map((r) => r.id))
    for (const id of [...this.said]) if (!now.has(id)) this.said.delete(id) // gone: a new one starts afresh
    for (const r of readings) {
      if (isLow(r)) {
        if (!this.said.has(r.id)) { this.said.add(r.id); out.push(lowNotice(r)) }
      } else if (r.charging || r.level >= LOW_CLEAR) {
        this.said.delete(r.id)
      }
    }
    return out
  }
}
