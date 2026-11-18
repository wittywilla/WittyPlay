// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import type { PadFamily } from './words'

// A controller's own button, as its family prints it, by the browser's standard gamepad
// number: 0 to 3 the face buttons from the bottom one round, 4 to 7 the shoulders and
// triggers, 8 and 9 the small middle buttons, 10 and 11 the stick clicks, 12 to 15 the D-pad,
// 16 the home button and 17 a pad's extra one (a touchpad's click, a capture button). The
// shapes are plain geometry drawn here, with no maker's marks.

export const PHYS_COUNT = 18

const NAMES: Record<PadFamily, string[]> = {
  playstation: ['Cross', 'Circle', 'Square', 'Triangle', 'L1', 'R1', 'L2', 'R2', 'Create', 'Options', 'L3', 'R3', 'Up', 'Down', 'Left', 'Right', 'Home', 'Touchpad'],
  xbox: ['A', 'B', 'X', 'Y', 'LB', 'RB', 'LT', 'RT', 'View', 'Menu', 'Left stick click', 'Right stick click', 'Up', 'Down', 'Left', 'Right', 'Home', 'Share'],
  nintendo: ['B', 'A', 'Y', 'X', 'L', 'R', 'ZL', 'ZR', 'Minus', 'Plus', 'Left stick click', 'Right stick click', 'Up', 'Down', 'Left', 'Right', 'Home', 'Capture'],
  generic: ['Bottom button', 'Right button', 'Left button', 'Top button', 'Left bumper', 'Right bumper', 'Left trigger', 'Right trigger', 'Select', 'Start', 'Left stick click', 'Right stick click', 'Up', 'Down', 'Left', 'Right', 'Home', 'Extra button'],
}

const SHORT: Record<PadFamily, string[]> = {
  playstation: ['', '', '', '', 'L1', 'R1', 'L2', 'R2', '', '', 'L3', 'R3', '', '', '', '', '', ''],
  xbox: ['A', 'B', 'X', 'Y', 'LB', 'RB', 'LT', 'RT', '', '', 'LS', 'RS', '', '', '', '', '', ''],
  nintendo: ['B', 'A', 'Y', 'X', 'L', 'R', 'ZL', 'ZR', '−', '+', 'LS', 'RS', '', '', '', '', '', ''],
  generic: ['A', 'B', 'X', 'Y', 'LB', 'RB', 'LT', 'RT', '', '', 'LS', 'RS', '', '', '', '', '', ''],
}

// The name of a controller's button, as its family calls it: "Cross", "LB", "Plus".
export const physName = (family: PadFamily, i: number) => NAMES[family][i] ?? `Button ${i}`

// A stick's direction, and what a D-pad reported as one axis calls its directions.
export const STICK_DIR = [['Left stick left', 'Left stick right'], ['Left stick up', 'Left stick down'], ['Right stick left', 'Right stick right'], ['Right stick up', 'Right stick down']]

const line = { fill: 'none', stroke: 'currentColor', strokeWidth: 2.4, strokeLinecap: 'round', strokeLinejoin: 'round' } as const

function Icon({ i, family }: { i: number; family: PadFamily }) {
  if (family === 'playstation') {
    if (i === 0) return <path {...line} d="M7 7l10 10M17 7L7 17" />
    if (i === 1) return <circle {...line} cx="12" cy="12" r="5.6" />
    if (i === 2) return <rect {...line} x="7" y="7" width="10" height="10" rx=".8" />
    if (i === 3) return <path {...line} d="M12 6.2l6 10.3H6z" />
    if (i === 17) return <rect {...line} x="4" y="7" width="16" height="10" rx="2.5" />
  }
  if (i >= 12 && i <= 15) {
    const d = { 12: 'M12 6l6 9H6z', 13: 'M12 18l-6-9h12z', 14: 'M6 12l9-6v12z', 15: 'M18 12l-9 6V6z' }[i]
    return <path fill="currentColor" d={d} />
  }
  if (i === 16) return <path {...line} d="M5.5 11.5L12 6l6.5 5.5M7.5 10v7.5h9V10" />
  if (i === 8 && family !== 'nintendo') return <path {...line} strokeWidth={1.8} d="M5.5 7h9v7h-9zM9.5 14v3h9v-7h-4" />
  if (i === 9 && family !== 'nintendo') return <path fill="currentColor" d="M6 8h12v1.8H6zm0 3.1h12v1.8H6zm0 3.1h12V16H6z" />
  if (i === 17) return <circle {...line} cx="12" cy="12" r="4.5" />
  return null
}

// One button of the controller in someone's hands, drawn small enough to sit in a line of text.
export function PhysGlyph({ i, family, size = 'md' }: { i: number; family: PadFamily; size?: 'sm' | 'md' | 'lg' }) {
  const text = SHORT[family][i]
  const name = physName(family, i)
  const icon = text ? null : <Icon i={i} family={family} />
  const wide = !!text && text.length > 1
  return (
    <span className={`wsg wsg-${size}` + (wide ? ' wsg-pill' : ' wsg-round') + (i >= 4 && i <= 7 ? ' wsg-shoulder' : '')} role="img" aria-label={name} title={name}>
      {icon ? <svg viewBox="0 0 24 24" aria-hidden="true">{icon}</svg> : text}
    </span>
  )
}

// A stick, for a line of text.
export function StickGlyph({ right, size = 'md' }: { right: boolean; size?: 'sm' | 'md' | 'lg' }) {
  const name = right ? 'Right stick' : 'Left stick'
  return (
    <span className={`wsg wsg-${size} wsg-round`} role="img" aria-label={name} title={name}>
      <svg viewBox="0 0 24 24" aria-hidden="true"><circle {...line} cx="12" cy="12" r="6.5" /><text x="12" y="12.5" textAnchor="middle" dominantBaseline="central" fill="currentColor" fontSize="8" fontWeight="700">{right ? 'R' : 'L'}</text></svg>
    </span>
  )
}

// A keyboard key.
export function KeyGlyph({ code, size = 'md' }: { code?: string; size?: 'sm' | 'md' | 'lg' }) {
  const text = !code ? '—' : code.replace(/^Key/, '').replace(/^Digit/, '').replace(/^Arrow(\w+)/, (_, d) => ({ Up: '↑', Down: '↓', Left: '←', Right: '→' } as Record<string, string>)[d] || d).replace(/^(Shift|Control|Alt|Meta)(Left|Right)$/, '$2 $1').replace(/^Numpad/, 'Num ')
  return <kbd className={`wsg wsg-${size} wsg-key`}>{text}</kbd>
}
