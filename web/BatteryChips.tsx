// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useRef, useState } from 'react'
import { batteryLabel, batteryText, deviceReading, isLow, LowBatteryNotes, padReadings, percent, type BatteryReading } from './battery'

// The batteries on screen (battery.ts says where they come from): chips with their level as text,
// and a notice, said politely and once, when one runs low. The status bar and the game menu show the
// same chips; the notice is given once wherever it's first seen.

type BatteryManagerLike = EventTarget & { level: number; charging: boolean; chargingTime: number; dischargingTime: number }

const notes = new LowBatteryNotes()

// How often the pads are read again: the Gamepad API sends no event when a level changes.
const PAD_POLL_MS = 15000

export function useBatteries(): BatteryReading[] {
  const [device, setDevice] = useState<BatteryReading | null>(null)
  const [pads, setPads] = useState<BatteryReading[]>([])
  useEffect(() => {
    const nav = navigator as Navigator & { getBattery?: () => Promise<BatteryManagerLike> }
    if (typeof nav.getBattery !== 'function') return
    let gone = false
    let bm: BatteryManagerLike | null = null
    const read = () => { if (!gone && bm) setDevice(deviceReading(bm)) }
    nav.getBattery().then((b) => {
      bm = b
      read()
      for (const e of ['levelchange', 'chargingchange', 'chargingtimechange']) b.addEventListener(e, read)
    }).catch(() => {})
    return () => {
      gone = true
      if (bm) for (const e of ['levelchange', 'chargingchange', 'chargingtimechange']) bm.removeEventListener(e, read)
    }
  }, [])
  useEffect(() => {
    if (typeof navigator.getGamepads !== 'function') return
    const read = () => {
      let list: ArrayLike<Gamepad | null> = []
      try { list = navigator.getGamepads() } catch { /* not allowed here */ }
      const next = padReadings(list)
      setPads((old) => (JSON.stringify(old) === JSON.stringify(next) ? old : next))
    }
    read()
    const iv = window.setInterval(read, PAD_POLL_MS)
    window.addEventListener('gamepadconnected', read)
    window.addEventListener('gamepaddisconnected', read)
    return () => { window.clearInterval(iv); window.removeEventListener('gamepadconnected', read); window.removeEventListener('gamepaddisconnected', read) }
  }, [])
  return device ? [...pads, device] : pads
}

// useLowBatteryNotice gives each notice to say (a polite live region's text) as a battery runs low.
export function useLowBatteryNotice(readings: BatteryReading[], say: (text: string) => void) {
  const sayRef = useRef(say)
  sayRef.current = say
  useEffect(() => {
    const said = notes.check(readings)
    if (said.length) sayRef.current(said.join(' '))
  }, [readings])
}

export function BatteryIcon({ reading }: { reading: BatteryReading }) {
  const w = Math.max(1, Math.round(percent(reading.level) / 100 * 14))
  return (
    <svg className="wp-batt-ic" viewBox="0 0 22 12" width="22" height="12" aria-hidden="true" focusable="false">
      <rect x="0.75" y="0.75" width="18" height="10.5" rx="2" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <rect x="19.5" y="3.5" width="2" height="5" rx="1" fill="currentColor" />
      <rect x="2.75" y="2.75" width={w} height="6.5" rx="1" fill="currentColor" />
      {reading.charging && <path d="M10.5 1.5 7 6.5h3l-1 4 4-5.5h-3z" fill="var(--wp-batt-bolt, #000)" stroke="currentColor" strokeWidth=".6" />}
    </svg>
  )
}

// BatteryChips: each battery as an icon and its level in words, the low ones marked in words too.
export function BatteryChips({ readings, className = '' }: { readings: BatteryReading[]; className?: string }) {
  if (readings.length === 0) return null
  return (
    <ul className={'wp-batts ' + className} aria-label="Batteries">
      {readings.map((r) => (
        <li key={r.id} className={'wp-batt' + (isLow(r) ? ' low' : '') + (r.charging ? ' charging' : '')} title={batteryLabel(r)}>
          <BatteryIcon reading={r} />
          <span aria-hidden="true">{batteryText(r)}{isLow(r) ? ' · Low' : ''}</span>
          <span className="wp-sr">{batteryLabel(r)}</span>
        </li>
      ))}
    </ul>
  )
}
