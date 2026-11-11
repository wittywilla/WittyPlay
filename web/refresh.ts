// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// The screen's refresh rate, from animation frames, for version 2's hello: the server keeps a
// game to the screen when the two are close, so a 60.0988 Hz game on a 60 Hz screen never
// drops a frame. A browser doesn't say its rate, so it's measured once, in the background.

const COMMON = [50, 59.94, 60, 72, 75, 90, 100, 119.88, 120, 144, 165, 240]
const FRAMES = 90

let measured = 0

// measureRefresh times FRAMES animation frames and calls done with the rate, once.
export function measureRefresh(done: (hz: number) => void) {
  if (measured) { done(measured); return }
  if (typeof requestAnimationFrame === 'undefined') return
  const gaps: number[] = []
  let last = 0
  const tick = (t: number) => {
    if (last) gaps.push(t - last)
    last = t
    if (gaps.length < FRAMES) { requestAnimationFrame(tick); return }
    const hz = rateOf(gaps)
    if (hz) { measured = hz; done(hz) }
  }
  requestAnimationFrame(tick)
}

// rateOf is the rate a list of frame gaps says, snapped to the nearest usual one within 0.3%,
// or 0 when the gaps are too uneven to say (a busy or hidden page).
export function rateOf(gaps: number[]): number {
  const sorted = [...gaps].sort((a, b) => a - b)
  const mid = sorted[Math.floor(sorted.length / 2)]
  if (!mid || mid <= 0) return 0
  const near = sorted.filter((g) => Math.abs(g - mid) < mid * 0.1)
  if (near.length < sorted.length / 2) return 0
  const hz = 1000 / (near.reduce((a, b) => a + b, 0) / near.length)
  // The nearest usual rate, if it's close: 59.94 and 60 are only 0.1% apart.
  const nearest = COMMON.reduce((a, b) => (Math.abs(hz / b - 1) < Math.abs(hz / a - 1) ? b : a))
  if (Math.abs(hz / nearest - 1) < 0.003) return nearest
  return Math.round(hz * 100) / 100
}

// screenNow is what the hello says about the screen.
export function screenNow(): { hz?: number; width?: number; height?: number } {
  const dpr = typeof devicePixelRatio === 'number' ? devicePixelRatio : 1
  const s = typeof screen !== 'undefined' ? screen : null
  return { hz: measured || undefined, width: s ? Math.round(s.width * dpr) : undefined, height: s ? Math.round(s.height * dpr) : undefined }
}
