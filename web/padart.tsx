// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useRef } from 'react'
import { PhysGlyph, StickGlyph } from './padglyph'
import type { PadFamily } from './words'

// The controller in someone's hands, with what each of its buttons does in this game written
// beside it, the way a console's own controller settings show it. Drawn for this project: a
// generic two-grip pad, with no maker's logo and nothing traced from a real product. The
// callouts are real <button>s, so a remote, a keyboard and a screen reader reach them; the
// drawing lights up as the controller is used, straight from the browser's gamepad, without
// drawing the page again.

const W = 1100, H = 500
const S = 0.8, TX = 550 - 320 * S, TY = 40 // where the 640 by 400 drawing sits
const at = (x: number, y: number) => ({ x: TX + x * S, y: TY + y * S })

const BODY = 'M150 92 H490 Q562 92 592 172 L626 306 Q640 366 592 370 Q552 374 522 340 L482 312 H158 L118 340 Q88 374 48 370 Q0 366 14 306 L48 172 Q78 92 150 92 Z'

type Part = { i: number; x: number; y: number; w?: number; h?: number; shape: 'round' | 'pill' | 'rect' | 'arrow' }
// Where each standard button is on the drawing, in its 640 by 400 space.
const PARTS: Part[] = [
  { i: 0, x: 490, y: 206, shape: 'round' }, { i: 1, x: 532, y: 164, shape: 'round' }, { i: 2, x: 448, y: 164, shape: 'round' }, { i: 3, x: 490, y: 122, shape: 'round' },
  { i: 4, x: 118, y: 66, w: 92, h: 26, shape: 'pill' }, { i: 5, x: 522, y: 66, w: 92, h: 26, shape: 'pill' },
  { i: 6, x: 118, y: 30, w: 92, h: 30, shape: 'pill' }, { i: 7, x: 522, y: 30, w: 92, h: 30, shape: 'pill' },
  { i: 8, x: 226, y: 118, w: 34, h: 20, shape: 'pill' }, { i: 9, x: 414, y: 118, w: 34, h: 20, shape: 'pill' },
  { i: 12, x: 150, y: 128, shape: 'arrow' }, { i: 13, x: 150, y: 200, shape: 'arrow' }, { i: 14, x: 114, y: 164, shape: 'arrow' }, { i: 15, x: 186, y: 164, shape: 'arrow' },
  { i: 16, x: 320, y: 214, w: 26, h: 26, shape: 'round' }, { i: 17, x: 320, y: 140, w: 116, h: 60, shape: 'rect' },
]
const STICKS = [{ right: false, x: 236, y: 262 }, { right: true, x: 404, y: 262 }]

export type Callout = { key: string; side: 'l' | 'r' | 'b'; row: number; phys?: number; stick?: 0 | 1; target: { x: number; y: number } }

// The callouts, top to bottom on each side, in the order the buttons sit on the pad.
function callouts(extra: boolean): Callout[] {
  const part = (i: number) => { const p = PARTS.find((q) => q.i === i)!; return at(p.x, p.y) }
  const stick = (n: 0 | 1) => at(STICKS[n].x, STICKS[n].y)
  const left: Omit<Callout, 'side' | 'row'>[] = [
    { key: 'p6', phys: 6, target: part(6) }, { key: 'p4', phys: 4, target: part(4) }, { key: 'p12', phys: 12, target: part(12) }, { key: 'p14', phys: 14, target: part(14) },
    { key: 'p15', phys: 15, target: part(15) }, { key: 'p13', phys: 13, target: part(13) }, { key: 'p8', phys: 8, target: part(8) }, { key: 's0', stick: 0, target: stick(0) }, { key: 'p10', phys: 10, target: stick(0) },
  ]
  const right: Omit<Callout, 'side' | 'row'>[] = [
    { key: 'p7', phys: 7, target: part(7) }, { key: 'p5', phys: 5, target: part(5) }, { key: 'p3', phys: 3, target: part(3) }, { key: 'p1', phys: 1, target: part(1) },
    { key: 'p2', phys: 2, target: part(2) }, { key: 'p0', phys: 0, target: part(0) }, { key: 'p9', phys: 9, target: part(9) }, { key: 's1', stick: 1, target: stick(1) }, { key: 'p11', phys: 11, target: stick(1) },
  ]
  const bottom: Omit<Callout, 'side' | 'row'>[] = [{ key: 'p16', phys: 16, target: part(16) }]
  if (extra) bottom.unshift({ key: 'p17', phys: 17, target: part(17) })
  return [...left.map((c, row) => ({ ...c, side: 'l' as const, row })), ...right.map((c, row) => ({ ...c, side: 'r' as const, row })), ...bottom.map((c, row) => ({ ...c, side: 'b' as const, row }))]
}

const ROW0 = 24, ROW = 48, COL = 280, BOTTOM_Y = 468
const boxOf = (c: Callout, nBottom: number) => {
  if (c.side === 'b') {
    const w = 250, gap = 20, total = nBottom * w + (nBottom - 1) * gap
    return { x: W / 2 - total / 2 + c.row * (w + gap), y: BOTTOM_Y - 18, w, h: 36 }
  }
  return { x: c.side === 'l' ? 0 : W - COL, y: ROW0 + c.row * ROW - 18, w: COL, h: 36 }
}

export type PadPictureProps = {
  family: PadFamily
  extraButton: boolean // the pad has a 17th button: a touchpad or a capture button
  device: number | null // the browser's index for the pad to light up from, if there is one
  describe: (c: Callout) => { text: string; changed?: boolean; none?: boolean }
  onPick: (c: Callout) => void
  picking: string | null // the callout whose choice is open
}

export function PadPicture({ family, extraButton, device, describe, onPick, picking }: PadPictureProps) {
  const list = callouts(extraButton)
  const nBottom = list.filter((c) => c.side === 'b').length
  const parts = useRef(new Map<number, SVGElement>())
  const nubs = useRef<(SVGCircleElement | null)[]>([null, null])
  const pressed = useRef<string | null>(null) // a callout chosen on its press, whose click then comes too

  // Lighting up, sixty times a second, without React.
  useEffect(() => {
    if (device === null) return
    let raf = 0
    const lit = new Set<number>()
    const loop = () => {
      raf = requestAnimationFrame(loop)
      let p: Gamepad | null = null
      try { p = navigator.getGamepads?.()[device] ?? null } catch { /* blocked */ }
      if (!p) return
      for (const [i, el] of parts.current) {
        const b = p.buttons[i]
        const on = !!b && (b.pressed || b.value > 0.5)
        if (on !== lit.has(i)) { if (on) { lit.add(i); el.setAttribute('data-on', '') } else { lit.delete(i); el.removeAttribute('data-on') } }
      }
      STICKS.forEach((s, n) => {
        const nub = nubs.current[n]
        if (!nub) return
        const x = p!.axes[n * 2] ?? 0, y = p!.axes[n * 2 + 1] ?? 0
        const c = at(s.x + x * 14, s.y + y * 14)
        nub.setAttribute('cx', String(c.x)); nub.setAttribute('cy', String(c.y))
      })
    }
    raf = requestAnimationFrame(loop)
    return () => { cancelAnimationFrame(raf); for (const [, el] of parts.current) el.removeAttribute('data-on') }
  }, [device])

  const keep = (i: number) => (el: SVGElement | null) => { if (el) parts.current.set(i, el); else parts.current.delete(i) }
  return (
    <div className="wpp" style={{ aspectRatio: `${W} / ${H}` }}>
      <svg viewBox={`0 0 ${W} ${H}`} aria-hidden="true" focusable="false" className="wpp-svg">
        {list.map((c) => {
          const b = boxOf(c, nBottom)
          const from = c.side === 'l' ? { x: b.x + b.w, y: b.y + b.h / 2 } : c.side === 'r' ? { x: b.x, y: b.y + b.h / 2 } : { x: b.x + b.w / 2, y: b.y }
          const elbow = c.side === 'b' ? { x: from.x, y: from.y - 14 } : { x: from.x + (c.side === 'l' ? 18 : -18), y: from.y }
          return <polyline key={c.key} className={'wpp-lead' + (picking === c.key ? ' on' : '')} points={`${from.x},${from.y} ${elbow.x},${elbow.y} ${c.target.x},${c.target.y}`} />
        })}
        <g transform={`translate(${TX} ${TY}) scale(${S})`}>
          <path className="wpp-body" d={BODY} />
          <path className="wpp-well" d="M131 126h38v19h19v38h-19v19h-38v-19h-19v-38h19z" />
          {STICKS.map((s) => <circle key={String(s.right)} className="wpp-well" cx={s.x} cy={s.y} r={30} />)}
          {PARTS.map((p) => {
            if (p.i === 17 && !extraButton) return null
            const w = p.w ?? 36, h = p.h ?? 36
            if (p.shape === 'arrow') {
              const d = { 12: `M${p.x} ${p.y - 13}l13 16h-26z`, 13: `M${p.x} ${p.y + 13}l-13 -16h26z`, 14: `M${p.x - 13} ${p.y}l16 -13v26z`, 15: `M${p.x + 13} ${p.y}l-16 13v-26z` }[p.i]
              return <path key={p.i} ref={keep(p.i)} className="wpp-part" d={d} />
            }
            if (p.shape === 'round') return <circle key={p.i} ref={keep(p.i)} className="wpp-part" cx={p.x} cy={p.y} r={w / 2} />
            return <rect key={p.i} ref={keep(p.i)} className="wpp-part" x={p.x - w / 2} y={p.y - h / 2} width={w} height={h} rx={p.shape === 'pill' ? h / 2 : 10} />
          })}
          {STICKS.map((s, n) => <circle key={'cap' + n} className="wpp-part wpp-cap" cx={s.x} cy={s.y} r={19} ref={keep(10 + n)} />)}
        </g>
        {STICKS.map((s, n) => { const c = at(s.x, s.y); return <circle key={'nub' + n} ref={(el) => { nubs.current[n] = el }} className="wpp-nub" cx={c.x} cy={c.y} r={5} /> })}
      </svg>
      {/* The face buttons' own glyphs, laid over the drawing. */}
      {PARTS.filter((p) => p.shape === 'round' && p.i < 4).map((p) => {
        const c = at(p.x, p.y)
        return <span key={p.i} className="wpp-face" aria-hidden="true" style={{ left: `${(c.x / W) * 100}%`, top: `${(c.y / H) * 100}%` }}><PhysGlyph i={p.i} family={family} size="sm" /></span>
      })}
      {list.map((c) => {
        const b = boxOf(c, nBottom)
        const d = describe(c)
        const glyph = c.stick !== undefined ? <StickGlyph right={c.stick === 1} size="sm" /> : <PhysGlyph i={c.phys!} family={family} size="sm" />
        return (
          <button key={c.key} type="button" className={`wpp-call wpp-${c.side}` + (d.none ? ' none' : '') + (d.changed ? ' changed' : '') + (picking === c.key ? ' on' : '')}
            style={{ left: `${(b.x / W) * 100}%`, top: `${(b.y / H) * 100}%`, width: `${(b.w / W) * 100}%`, height: `${(b.h / H) * 100}%` }}
            // Chosen on the press, not the click: the first mouse press after a controller was used
            // makes the app's text smaller again, the drawing moves under the pointer, and the
            // click would land on whatever is there by the time the button comes up.
            onPointerDown={(e) => { if (e.button !== 0) return; e.preventDefault(); pressed.current = c.key; setTimeout(() => { if (pressed.current === c.key) pressed.current = null }, 800); onPick(c) }}
            onClick={() => { if (pressed.current === c.key) { pressed.current = null; return } onPick(c) }} aria-haspopup={c.stick === undefined ? 'dialog' : undefined}>
            {glyph}<span className="wpp-what">{d.text}</span>
          </button>
        )
      })}
    </div>
  )
}
