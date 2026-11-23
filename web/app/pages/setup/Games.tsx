// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useState } from 'react'
import { api, type Library } from '@wms/app/api'
import type { GameConsole, GameSource } from '../../gameTypes'
import { GameKeyFields, saveGameKeys } from '../GameMeta'
import { IconCheck } from '@wms/app/icons'
import { Button } from '@wms/app/ui/index'
import { ChoiceCards, LaterNote, StepActions, StepHead, type StepProps } from '@wms/app/pages/setup/parts'
import { GameTitleSample } from '../games/GameName'
import { GAME_TITLE_CHOICES } from '../games/titles'
import { useGameTitlePref } from '../games/useGameTitles'
import { GAMES_LINE } from '@wms/app/pages/setup/plan'
import { MainArtChooser, PictureQuickChooser } from '../../shell/GamePictureArt'
import { cleanLook, type Look } from '../../shell/gamePictures'
import { ConsolePhotosSetting } from '../settings/ConsolePhotos'
import { PerformanceSetting } from '../settings/Performance'
import { findingWords, useGamesScan, type GamesFinding } from '../games/scanning'

// A games library already on the server, and how many of its games each console has.
// scanning: the server is still looking through it, so the counts grow.
type HeldLibrary = { id: number; name: string; scanning: boolean; consoles: { short: string; n: number }[] }

const count = (n: number) => n.toLocaleString()

// heldWords: "Games: 2,210 games for 14 consoles (NES 412, SNES 380, Game Boy 301 and 11 more)", with "so far" while it's
// still being looked through.
function heldWords(l: HeldLibrary): string {
  const games = l.consoles.reduce((t, c) => t + c.n, 0)
  if (games === 0) return l.scanning ? `${l.name}: being looked through now` : `${l.name}: no games found in it yet`
  const top = l.consoles.slice(0, 3).map((c) => `${c.short} ${count(c.n)}`).join(', ')
  const more = l.consoles.length > 3 ? ` and ${l.consoles.length - 3} more` : ''
  const consoles = l.consoles.length === 1 ? '1 console' : `${l.consoles.length} consoles`
  const so = l.scanning ? ' so far' : ''
  return `${l.name}: ${count(games)} ${games === 1 ? 'game' : 'games'}${so} for ${consoles} (${top}${more})`
}

// While a library is being looked through, its counts are read again this often: the server keeps adding games
// for minutes after WittyPlay has sorted the files.
const SCANNING_MS = 5000

// The server's games libraries (added at the Libraries step, or before this setup), with their consoles' counts.
function useHeldLibraries(added: Library[]): [HeldLibrary[] | null, () => void] {
  const [held, setHeld] = useState<HeldLibrary[] | null>(null)
  const [ask, setAsk] = useState(0)
  const addedIds = added.filter((l) => l.type === 'games').map((l) => l.id).join(',')
  useEffect(() => {
    let live = true
    let timer = 0
    const read = async () => {
      let libs: Library[]
      try { libs = (await api.get<Library[]>('/libraries')).filter((l) => l.type === 'games') } catch { libs = added.filter((l) => l.type === 'games') }
      const out = await Promise.all(libs.map(async (l) => {
        let consoles: { short: string; n: number }[] = []
        try {
          const list = await api.get<GameConsole[]>(`/modules/wittyplay/games/consoles?library=${l.id}`)
          consoles = list.filter((c) => (c.count || 0) > 0).map((c) => ({ short: c.short, n: c.count || 0 })).sort((a, b) => b.n - a.n)
        } catch { /* the counts can wait; the library is still shown */ }
        return { id: l.id, name: l.name, scanning: !!l.scanning, consoles }
      }))
      if (!live) return
      setHeld(out)
      if (out.some((l) => l.scanning)) timer = window.setTimeout(read, SCANNING_MS)
    }
    void read()
    return () => { live = false; window.clearTimeout(timer) }
  }, [addedIds, ask]) // eslint-disable-line react-hooks/exhaustive-deps
  return [held, () => setAsk((n) => n + 1)]
}

// Belongs to WittyPlay, so it's only here when WittyPlay is.
export function Games({ info, next, back, added, goTo }: StepProps & { added: Library[]; goTo: (id: 'media') => void }) {
  const [sources, setSources] = useState<GameSource[]>([])
  const [keys, setKeys] = useState<Record<string, string>>({})
  const [open, setOpen] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [titles, setTitles] = useGameTitlePref()
  useEffect(() => { api.get<{ sources: GameSource[] }>('/modules/wittyplay/gamemeta/sources').then((r) => setSources(r.sources.filter((s) => s.fields.length > 0))).catch(() => {}) }, [])
  const [held, reread] = useHeldLibraries(added)
  const scans = useGamesScan(reread) // read the counts again when a scan ends
  const scanOf = (id: number): GamesFinding | undefined => scans.find((f) => f.library === id)
  // How games are pictured in WittyPlay, for everyone on the server (each person can choose their own later).
  const [look, setLook] = useState<Look>({ picture: 'case' })
  const save = async () => {
    setBusy(true)
    const body = Object.fromEntries(Object.entries(keys).filter(([, v]) => v.trim()).map(([k, v]) => [k, v.trim()]))
    try { await saveGameKeys(body) } catch { /* keys can be added later */ }
    try {
      const now = await api.get<{ server?: Record<string, unknown> }>('/modules/wittyplay/games/pictures')
      await api.put('/modules/wittyplay/games/pictures', { ...(now.server || {}), wittyplay: 1, default: cleanLook(look) })
    } catch { /* it can be chosen later, in WittyPlay's Settings */ }
    setBusy(false)
    next()
  }
  return (
    <section>
      <StepHead title="Your games" lead={GAMES_LINE} />
      {held === null ? null : held.length > 0
        ? (
          <>
            <ul className="oobe-games-libs" style={{ listStyle: 'none', padding: 0, margin: '0 0 var(--sp-3)', display: 'grid', gap: 'var(--sp-2)' }}>
              {held.map((l) => {
                const f = scanOf(l.id)
                return <li key={l.id} className="oobe-added"><IconCheck size={16} /> {f ? findingWords(f) : heldWords(l)}</li>
              })}
            </ul>
            <p className="muted oobe-hint">Games in other folders? <Button size="sm" onClick={() => goTo('media')}>Add another folder</Button></p>
          </>
        )
        : <p className="muted oobe-hint">You haven’t added a games folder yet. <Button size="sm" onClick={() => goTo('media')}>Add one</Button></p>}
      <p className="muted oobe-hint su-gap">It works out each game’s console and name by itself, and checks each file against a list of known games. Nothing is bundled or downloaded, so the games are your own. Files with copy protection are listed with a plain reason, and aren’t played.</p>
      <h2 className="oobe-sub">Game titles</h2>
      <p className="muted oobe-hint">Many games were made in Japan and are listed by their name in Latin letters. Show their names that way, in their own script, or by their English release. This is for your profile; everyone can choose their own in Settings.</p>
      <ChoiceCards label="Game titles" value={titles} onChange={setTitles} small
        cards={GAME_TITLE_CHOICES.map((c) => ({ value: c.id, title: c.label, text: c.note, pic: <GameTitleSample pref={c.id} /> }))} />
      <h2 className="oobe-sub">How your games look</h2>
      <p className="muted oobe-hint">Box art comes in every shape. Show it whole, filling its tile, or each game in a case or cartridge like the ones its console used. Anyone can change this for themselves later, for every game, a console or one game, in WittyPlay’s Settings.</p>
      <PictureQuickChooser value={look} onChange={setLook} />
      <h2 className="oobe-sub">Games on Home</h2>
      <p className="muted oobe-hint">On Home and in the rest of the app, show games the same way, or just their box art. Choosing one always opens it in WittyPlay. This is for your profile; everyone can choose their own in WittyPlay’s Settings.</p>
      <MainArtChooser heading={false} />
      <PerformanceSetting cards />
      <ConsolePhotosSetting />
      {sources.length > 0 && (
        <>
          <h2 className="oobe-sub">More details, if you want them</h2>
          <p className="muted oobe-hint">Games already get names, pictures and descriptions from free sources. These optional sources add critic scores, age ratings and more box art. Each gives you a free key: a code that lets your server ask it for things. Only names and file fingerprints are looked up, never your files.</p>
          <div className="oobe-providers">
            {sources.map((s) => {
              const shown = open === s.id
              return (
                <div key={s.id} className="oobe-provider">
                  <button type="button" className="op-head" onClick={() => setOpen(shown ? null : s.id)} aria-expanded={shown}>
                    <b>{s.name}</b><span className="pill">{s.cost}</span>
                    <span className="op-add">{shown ? 'Hide' : 'Add a key'}</span>
                  </button>
                  <div className="muted op-note">{s.why}</div>
                  {shown && <GameKeyFields source={s} values={keys} onChange={(k, v) => setKeys((c) => ({ ...c, [k]: v }))} />}
                </div>
              )
            })}
          </div>
        </>
      )}
      <LaterNote info={info} />
      <StepActions back={back}><Button variant="primary" size="big" busy={busy} onClick={save}>Continue</Button></StepActions>
    </section>
  )
}
