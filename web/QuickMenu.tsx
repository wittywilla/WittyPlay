// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import type { ReactNode } from 'react'
import { PadGlyph, PromptBar, useInputKind, type PromptButton } from './prompts'

// The game menu: everything someone might want while playing, as large tiles that work the
// same under a thumb, a mouse, a keyboard, a remote or a controller. The game stays paused
// and in view behind it. What's used in the middle of a game comes first; what's used
// once in a while is one level down.

const P: Record<string, string> = {
  play: 'M8 5v14l11-7z',
  save: 'M5 3h11l5 5v13H3V3zm7 15a3 3 0 100-6 3 3 0 000 6zM6 5v4h9V5z',
  load: 'M12 3v9.2l3.6-3.6L17 10l-5 5-5-5 1.4-1.4L11 12.2V3zM4 14h2v5h12v-5h2v7H4z',
  quickSave: 'M13 2L5 13h6l-1 9 9-12h-6z',
  quickLoad: 'M13 3a9 9 0 11-8.5 12h2.2A7 7 0 1013 5a7 7 0 00-5.3 2.4L10 10H3V3l3.2 3.2A9 9 0 0113 3zm-1 5h2v5l4 2.3-1 1.7-5-3z',
  rewind: 'M11 18V6l-8.5 6zm.5-6l8.5 6V6z',
  ff: 'M4 18l8.5-6L4 6zm9-12v12l8.5-6z',
  camera: 'M9 3L7.2 5H4a2 2 0 00-2 2v12a2 2 0 002 2h16a2 2 0 002-2V7a2 2 0 00-2-2h-3.2L15 3zm3 15a5 5 0 110-10 5 5 0 010 10z',
  record: 'M12 5a7 7 0 100 14 7 7 0 000-14z',
  pad: 'M7 7h10a5 5 0 014.9 6l-.8 4a2.5 2.5 0 01-4.3 1.2L14.5 16h-5l-2.3 2.2A2.5 2.5 0 012.9 17l-.8-4A5 5 0 017 7zm1 2.5V11H6.5v2H8v1.5h2V13h1.5v-2H10V9.5zm8.5.5a1.2 1.2 0 100 2.4 1.2 1.2 0 000-2.4zm-2 2.5a1.2 1.2 0 100 2.4 1.2 1.2 0 000-2.4z',
  touch: 'M9 11.2V6a2.5 2.5 0 015 0v5.2a4.5 4.5 0 10-5 0zM12 2a4 4 0 00-4 4v.3A4.5 4.5 0 0112 2zm-.8 4a.8.8 0 011.6 0v5.5l5.2 2.2a2 2 0 011.2 2l-.5 4.3a2 2 0 01-2 1.7h-4.6a2 2 0 01-1.6-.8l-3.8-5 1.2-1.2 3.3 1.8z',
  settings: 'M19.4 13a7.9 7.9 0 000-2l2.1-1.6-2-3.5-2.5 1a8 8 0 00-1.7-1L15 3H9l-.3 2.9a8 8 0 00-1.7 1l-2.5-1-2 3.5L4.6 11a7.9 7.9 0 000 2l-2.1 1.6 2 3.5 2.5-1a8 8 0 001.7 1L9 21h6l.3-2.9a8 8 0 001.7-1l2.5 1 2-3.5zM12 15.5a3.5 3.5 0 110-7 3.5 3.5 0 010 7z',
  more: 'M5 10a2 2 0 100 4 2 2 0 000-4zm7 0a2 2 0 100 4 2 2 0 000-4zm7 0a2 2 0 100 4 2 2 0 000-4z',
  quit: 'M10 3h9a2 2 0 012 2v14a2 2 0 01-2 2h-9v-2h9V5h-9zm1.5 4.5L16 12l-4.5 4.5-1.4-1.4 2.1-2.1H3v-2h9.2l-2.1-2.1z',
  picture: 'M3 4h18a1 1 0 011 1v14a1 1 0 01-1 1H3a1 1 0 01-1-1V5a1 1 0 011-1zm1 2v12h16V6zm2 10l3.5-4.5 2.5 3 3.5-4.5 2.5 6z',
  sound: 'M3 9v6h4l5 5V4L7 9zm13.5 3A4.5 4.5 0 0014 8v8a4.5 4.5 0 002.5-4z',
  numbers: 'M4 20h3v-8H4zm6.5 0h3V4h-3zM17 20h3v-5h-3z',
  speed: 'M12 4a9 9 0 00-7.8 13.5h15.6A9 9 0 0012 4zm0 9.5a1.8 1.8 0 01-1.3-3L17 7l-3.5 6.2a1.8 1.8 0 01-1.5.3z',
  saves: 'M4 4h16v4H4zm0 6h16v4H4zm0 6h16v4H4z',
  keys: 'M3 6h18a1 1 0 011 1v10a1 1 0 01-1 1H3a1 1 0 01-1-1V7a1 1 0 011-1zm1 3v2h2V9zm4 0v2h2V9zm4 0v2h2V9zm4 0v2h2V9zM7 14v2h10v-2z',
  core: 'M7 2h2v3h2V2h2v3h2V2h2v3h2v2h3v2h-3v2h3v2h-3v2h3v2h-3v2h-2v3h-2v-3h-2v3h-2v-3H9v3H7v-3H5v-2H2v-2h3v-2H2V9h3V7H2V5h3V3zm0 5v10h10V7z',
  cheat: 'M12 2l9 4v6c0 5-3.8 9.4-9 10-5.2-.6-9-5-9-10V6zm-1 12.2l6.2-6.2-1.4-1.4L11 11.4 8.6 9 7.2 10.4z',
  patch: 'M4 4h7v4h2V4h7v7h-4v2h4v7h-7v-4h-2v4H4v-7h4v-2H4z',
  file: 'M6 2h9l5 5v15H4V2zm8 2v4h4zM7 12v2h10v-2zm0 4v2h10v-2z',
  disc: 'M12 2a10 10 0 100 20 10 10 0 000-20zm0 13a3 3 0 110-6 3 3 0 010 6z',
  connection: 'M12 18a2 2 0 100 4 2 2 0 000-4zM5.6 13.6l1.8 1.8a6.5 6.5 0 019.2 0l1.8-1.8a9 9 0 00-12.8 0zM2 10l1.8 1.8a11.6 11.6 0 0116.4 0L22 10A14.1 14.1 0 002 10z',
  full: 'M4 4h6v2H6v4H4zm10 0h6v6h-2V6h-4zM4 14h2v4h4v2H4zm14 0h2v6h-6v-2h4z',
  reset: 'M12 4a8 8 0 11-7.9 9.3h2A6 6 0 1012 6a6 6 0 00-4.4 1.9L10 10H4V4l2.2 2.2A8 8 0 0112 4z',
  back: 'M20 11H7.8l5.6-5.6L12 4l-8 8 8 8 1.4-1.4L7.8 13H20z',
  pause: 'M6 19h4V5H6zm8-14v14h4V5z',
  slow: 'M12 2a10 10 0 100 20 10 10 0 000-20zm1 5v5.4l4 2.4-1 1.6-5-3V7z',
}

export const MenuIcon = ({ name }: { name: keyof typeof P | string }) => <svg viewBox="0 0 24 24" width="24" height="24" aria-hidden="true"><path fill="currentColor" d={P[name] || P.more} /></svg>

export type TileProps = { icon: string; label: string; sub?: string; onClick?: () => void; primary?: boolean; danger?: boolean; pressed?: boolean; disabled?: boolean; id: string; wide?: boolean; glyph?: PromptButton }

// The button that does the same as a tile, as what's in someone's hands prints it. Nothing for a mouse or a finger.
const KEY: Partial<Record<PromptButton, string>> = { back: 'Esc', confirm: 'Enter' }
const REMOTE_KEY: Partial<Record<PromptButton, string>> = { back: 'Back', confirm: 'OK' }
function TileGlyph({ button }: { button: PromptButton }) {
  const input = useInputKind()
  if (input.kind === 'pointer') return null
  const cap = (input.kind === 'remote' ? REMOTE_KEY : KEY)[button]
  if (input.kind !== 'pad' && !cap) return null
  return <span className="wp-tile-glyph" aria-hidden="true">{input.kind === 'pad' ? <PadGlyph button={button} family={input.family} /> : <kbd className="wp-glyph key">{cap}</kbd>}</span>
}

export function Tile({ icon, label, sub, onClick, primary, danger, pressed, disabled, id, wide, glyph }: TileProps) {
  return (
    <button type="button" className={'wp-tile' + (primary ? ' primary' : '') + (danger ? ' danger' : '') + (wide ? ' wide' : '')} data-tile={id} aria-pressed={pressed} disabled={disabled} onClick={onClick}>
      <span className="wp-tile-ic"><MenuIcon name={icon} /></span>
      <span className="wp-tile-text"><b>{label}</b>{sub && <small>{sub}</small>}</span>
      {glyph && <TileGlyph button={glyph} />}
    </button>
  )
}

export function MenuFrame({ title, sub, cover, children, foot }: { title: string; sub?: string; cover?: string; children: ReactNode; foot?: ReactNode }) {
  return (
    <>
      <header className="wp-qm-head">
        {cover && <img src={cover} alt="" className="wp-qm-cover" />}
        <div className="wp-qm-title">{sub && <p className="wp-qm-eyebrow">{sub}</p>}<h2>{title}</h2></div>
      </header>
      {children}
      {foot}
      <PromptBar className="wp-qm-prompts" items={[{ button: 'dpad', label: 'Move' }, { button: 'confirm', label: 'Select' }, { button: 'back', label: 'Back' }]} />
    </>
  )
}

export const TileGrid = ({ label, children }: { label: string; children: ReactNode }) => <nav className="wp-tiles" aria-label={label}>{children}</nav>
