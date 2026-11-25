// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// WittyPlay's Consoles tab: a grid of the consoles this server has games for, each a button. The one with the focus (or the pointer, or
// the one chosen) is shown off in a panel at the side: its name and mark, its era and years, how many games the library has for it,
// the core it plays on and whether it is ready (a BIOS file missing, an emulator to install), with Browse games and Settings. On a
// phone the panel is a sheet from the bottom, opened by choosing a console. A console's Settings page hosts the app's own parts for
// it: its core and BIOS files (Consoles and cores), its game settings, its controls, and the libraries. The data is the app's own
// /api/modules/wittyplay/games/consoles (pages/games/consoles.tsx getConsoles).
import { lazy, Suspense, useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, mediaUrl } from '@wms/app/api'
import type { GameConsole } from '../gameTypes'
import { ConsoleMark } from './games/ConsoleMark'
import { IconCheck, IconGamepad, IconInfo, IconSettings } from '@wms/app/icons'
import { useApp } from '@wms/app/state'
import { Spinner } from '@wms/app/ui/Surface'
import { isBackKey } from '@wms/app/ui/spatial'
import { useMedia } from '@wms/app/shell/useNav'
import { LayoutSwitch, usePlayTabs } from '../shell/playPads'
import { arrangeTabs } from '../sdui/playModel'
import { libraryFor, PLAY_CONSOLES, PLAY_LIBRARY, PLAY_TABS, tabRoute, yearsWords } from '../shell/playPaths'
import { getConsoles } from './games/consoles'
import { findingDetail, findingWords, foundFor, useGamesScan } from './games/scanning'
import PlayLibraryView from './PlayLibraryView'
import { TileSizeSwitch } from '../shell/GamePictureArt'
import { ERA_NAMES } from './games/Shelves'
import { GameSettingsPage } from './games/consoles'
import GamingSettings from './settings/Gaming'
import './games/games-ui.css'
import { useChooseThenOpen } from '../useChooseThenOpen'

const ControllersScreen = lazy(() => import('./Games').then((m) => ({ default: m.ControllersScreen })))

// A console's picture of its hardware, from Wikimedia Commons by way of the server, which fetches and keeps it (GET
// /api/modules/wittyplay/console-photos/{id}); none until an administrator opts in to console photos (Settings, Console photos), when Wikidata lookups are off, or when Commons has no free one.
// A console sold in another shape elsewhere (an NES and a Famicom) has versions; variant is the one shown. changed says what
// WittyPlay did to the photo (its plain background taken away), for the credit.
type PhotoVariant = { id: string; label: string; name?: string; short?: string }
type ConsolePhoto = { imageUrl: string; credit?: string; license?: string; licenseUrl?: string; pageUrl?: string; source?: string; changed?: string; variant?: string; variants?: PhotoVariant[]; keepNames?: boolean }
// A console's names changed (another version chosen, or its usual names kept): the pages that list consoles ask again.
const namesListeners = new Set<() => void>()
const namesChanged = () => { void getConsoles(true).finally(() => namesListeners.forEach((l) => l())) }
function useNamesChanged(f: () => void) { useEffect(() => { namesListeners.add(f); return () => { namesListeners.delete(f) } }, [f]) }
const photos = new Map<string, Promise<ConsolePhoto | null>>()
const photoListeners = new Set<() => void>()
// A new answer for a console (another version chosen): every picture and credit of it shows the new one.
function putPhoto(id: string, p: ConsolePhoto | null) {
  photos.set(id, Promise.resolve(p))
  photoListeners.forEach((l) => l())
}
function useConsolePhoto(id: string): ConsolePhoto | null {
  const [p, setP] = useState<ConsolePhoto | null>(null)
  const [n, setN] = useState(0)
  useEffect(() => { const l = () => setN((x) => x + 1); photoListeners.add(l); return () => { photoListeners.delete(l) } }, [])
  useEffect(() => {
    let on = true
    if (!photos.has(id)) { setP(null); photos.set(id, api.get<ConsolePhoto>('/modules/wittyplay/console-photos/' + encodeURIComponent(id)).catch(() => null)) }
    photos.get(id)!.then((x) => { if (on) setP(x) })
    return () => { on = false }
  }, [id, n])
  return p
}

// The console's picture, else its mark (a logo, or its name drawn).
function ConsoleArt({ c, size }: { c: GameConsole; size: 'md' | 'lg' }) {
  const photo = useConsolePhoto(c.id)
  const [broken, setBroken] = useState(false)
  if (photo && !broken) return <img className="wplay-console-photo" src={mediaUrl(photo.imageUrl)} alt="" decoding="async" loading="lazy" onError={() => setBroken(true)} />
  return <ConsoleMark console={c} size={size} on="dark" />
}

// Who took the picture, its licence and where it is: shown under it, as its licence asks.
function PhotoCredit({ id }: { id: string }) {
  const photo = useConsolePhoto(id)
  if (!photo) return null
  return (
    <p className="wplay-photo-credit">Photo: {photo.credit || 'unknown author'}{photo.license ? <>, {photo.licenseUrl ? <a href={photo.licenseUrl} target="_blank" rel="noopener noreferrer">{photo.license}</a> : photo.license}</> : null}, {photo.pageUrl ? <a href={photo.pageUrl} target="_blank" rel="noopener noreferrer">Wikimedia Commons</a> : 'Wikimedia Commons'}{photo.changed ? `, ${photo.changed}` : ''}</p>
  )
}

// Which version of the console the photo shows (an NES or a Famicom), for an administrator to choose, when it has versions.
function PhotoVersions({ id }: { id: string }) {
  const { user } = useApp()
  const photo = useConsolePhoto(id)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const label = useId()
  if (!user?.isAdmin || !photo?.variants || photo.variants.length < 2) return null
  const choose = async (v: string) => {
    if (v === photo.variant) return
    setBusy(v); setErr('')
    try { putPhoto(id, await api.put<ConsolePhoto>('/modules/wittyplay/console-photos/' + encodeURIComponent(id) + '/variant', { variant: v })); namesChanged() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }
  const keep = async (kept: boolean) => {
    setBusy('names'); setErr('')
    try { putPhoto(id, await api.put<ConsolePhoto>('/modules/wittyplay/console-photos/' + encodeURIComponent(id) + '/variant', { keepNames: kept })); namesChanged() } catch (e: any) { setErr(e.message) } finally { setBusy('') }
  }
  const shown = photo.variants.find((v) => v.id === photo.variant)
  return (
    <div className="wplay-photo-versions">
      <span id={label}>Photo of</span>
      <div className="wplay-layout-switch" role="group" aria-labelledby={label} data-nav-group>
        {photo.variants.map((v) => <button key={v.id} type="button" aria-pressed={v.id === photo.variant} disabled={!!busy} onClick={() => void choose(v.id)}>{busy === v.id ? '…' : v.label}</button>)}
      </div>
      {shown?.short && (
        <label className="wplay-photo-names">
          <input type="checkbox" checked={!photo.keepNames} disabled={!!busy} onChange={(e) => void keep(!e.target.checked)} />
          <span>Call it {shown.short} everywhere<span className="muted"> · Its name on the Consoles tab, the library and game cards follows the photo. Off keeps its usual name.</span></span>
        </label>
      )}
      {err && <p className="error" role="alert">{err}</p>}
    </div>
  )
}

// sorting: no game of it is listed yet, but a scan going on has sorted this many into it (totalGames), so it shows now.
type ShownConsole = GameConsole & { sorting?: boolean }
const gamesOf = (c: GameConsole) => c.totalGames ?? c.count ?? 0
const gamesWords = (n: number) => `${n.toLocaleString()} ${n === 1 ? 'game' : 'games'}`
const countWords = (c: ShownConsole) => c.sorting ? `${gamesOf(c).toLocaleString()} found so far` : gamesWords(gamesOf(c))

// The home's tab row, here with Consoles chosen: the same words and places as the server's page, and the layout switch at its end.
export function PlayTabsRow({ current }: { current: string }) {
  const [pref] = usePlayTabs()
  const tabs = arrangeTabs(PLAY_TABS.map((t) => ({ ...t, value: t.id })), pref.order, pref.hidden, current)
  return (
    <div className="wplay-tabs-row" data-sdui-head>
      <nav className="tabs" aria-label="Views">
        {tabs.map((t) => <Link key={t.id} to={tabRoute(t.id)} className={t.id === current ? 'active' : ''} aria-current={t.id === current ? 'page' : undefined}>{t.label}</Link>)}
      </nav>
      <div className="wplay-switches"><TileSizeSwitch /><LayoutSwitch /></div>
    </div>
  )
}

// The core (or emulator) a console plays on now, and whether it can.
function coreLine(c: GameConsole): { core: string; state: 'ready' | 'setup' | 'not-here'; note: string } {
  const emulator = c.standalone ? c.standaloneEmulators?.find((e) => e.id === c.standalone) : undefined
  const core = emulator ? `${emulator.name}${emulator.version ? ' ' + emulator.version : ''} (standalone)` : (c.installed.find((x) => x.id === (c.defaultCore || c.playsOn)) || c.installed[0])?.name || ''
  if (c.ready) return { core: core || 'Ready', state: 'ready', note: c.software ? 'Drawn by this server’s processor: no graphics card games can use.' : c.notice || '' }
  if (c.reason === 'gpu' || c.reason === 'unavailable') return { core: core || 'None', state: 'not-here', note: c.message || c.unavailable || 'This server can’t play these games.' }
  const note = c.biosMissing ? `Needs your own BIOS file: ${c.biosMissing}.` : c.reason === 'emulator' ? 'Needs its standalone emulator installed.' : c.message || 'Needs a core installed.'
  return { core: core || 'No core yet', state: 'setup', note }
}

function ConsolePanel({ c, sheet, onClose }: { c: ShownConsole; sheet: boolean; onClose: () => void }) {
  const { libraries } = useApp()
  const head = useId()
  const lib = libraryFor(libraries, c.id)
  const how = coreLine(c)
  const n = gamesOf(c)
  const facts = [c.era ? ERA_NAMES[c.era] : '', yearsWords(c.launched, c.ended), c.media === 'disc' ? 'Discs' : c.media === 'cart' ? 'Cartridges' : c.media === 'pocket' ? 'Handheld' : '',
    c.capabilities?.input.ports ? (c.capabilities.input.ports > 1 ? `1 to ${c.capabilities.input.ports} players` : '1 player') : ''].filter(Boolean)
  const close = useRef<HTMLButtonElement | null>(null)
  useEffect(() => { if (sheet) close.current?.focus() }, [sheet, c.id])
  const keys = (e: KeyboardEvent<HTMLElement>) => { if (sheet && (e.key === 'Escape' || isBackKey(e.nativeEvent))) { e.preventDefault(); e.stopPropagation(); onClose() } }
  return (
    <aside key={c.id} className={'wplay-console-panel' + (sheet ? ' sheet' : '')} aria-labelledby={head} role={sheet ? 'dialog' : 'region'} aria-modal={sheet || undefined} onKeyDown={keys} aria-live={sheet ? undefined : 'polite'}>
      {sheet && <button ref={close} type="button" className="wplay-console-close" onClick={onClose}>Close</button>}
      <div className="wplay-console-mark"><ConsoleArt c={c} size="lg" /></div>
      <PhotoCredit id={c.id} />
      <PhotoVersions id={c.id} />
      <h2 id={head}>{c.name}</h2>
      {facts.length > 0 && <p className="wplay-console-facts">{facts.join(' · ')}</p>}
      <dl className="wplay-console-stats">
        <div><dt>In your library</dt><dd>{countWords(c)}</dd></div>
        <div><dt>Plays on</dt><dd>{how.core}</dd></div>
        <div><dt>Ready</dt><dd className={'state ' + how.state}>{how.state === 'ready' ? <><IconCheck size={18} /> Plays here</> : how.state === 'setup' ? <><IconInfo size={18} /> Once set up</> : <><IconInfo size={18} /> Not on this server</>}</dd></div>
      </dl>
      {how.note && <p className="wplay-console-note">{how.note}</p>}
      <div className="wplay-console-actions">
        {lib && n > 0 && !c.sorting && <Link className="btn primary" to={`${PLAY_LIBRARY}/${lib.id}?console=${encodeURIComponent(c.id)}`}><IconGamepad /> Browse games</Link>}
        <Link className="btn" to={`${PLAY_CONSOLES}/${encodeURIComponent(c.id)}?console=${encodeURIComponent(c.id)}`}><IconSettings /> Settings</Link>
      </div>
    </aside>
  )
}

export default function PlayConsoles() {
  const [list, setList] = useState<GameConsole[] | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => { getConsoles().then(setList).catch(() => setFailed(true)) }, [])
  useNamesChanged(useCallback(() => { getConsoles().then(setList).catch(() => {}) }, []))
  // A games library being scanned: its consoles show as the scan sorts games into them, and when it ends the page
  // reads what the server now holds.
  const finding = useGamesScan(useCallback(() => { getConsoles(true).then(setList).catch(() => {}) }, []))
  const sorted = useMemo(() => foundFor(finding), [finding])
  const phone = useMedia('(max-width: 759px)')
  const shown = useMemo<ShownConsole[]>(() => {
    const all: ShownConsole[] = (list || []).map((c) => gamesOf(c) > 0 || !sorted.get(c.id) ? c : { ...c, totalGames: sorted.get(c.id), sorting: true })
    const had = all.filter((c) => gamesOf(c) > 0).sort((a, b) => gamesOf(b) - gamesOf(a) || a.short.localeCompare(b.short))
    return had.length || finding.length ? had : all.filter((c) => c.ready)
  }, [list, sorted, finding.length])
  const [at, setAt] = useState('')
  const [sheet, setSheet] = useState(false)
  const current = shown.find((c) => c.id === at) || shown[0]
  const head = useId()
  const pick = (id: string, open: boolean) => { setAt(id); if (open && phone) setSheet(true) }
  // The chosen console is the one the focus is on (arrows, a remote or a pad), or the one last clicked: the pointer only points, so
  // the panel stays on the console chosen while the pointer crosses others on its way to Browse games. A second click on the chosen
  // console, a double-click, or Enter on it opens its games.
  const { libraries } = useApp()
  const nav = useNavigate()
  const press = useChooseThenOpen()
  const browse = (c: ShownConsole) => {
    const lib = libraryFor(libraries, c.id)
    if (lib && gamesOf(c) > 0 && !c.sorting) nav(`${PLAY_LIBRARY}/${lib.id}?console=${encodeURIComponent(c.id)}`)
  }
  return (
    <div className={'wplay-consoles' + (phone ? ' phone' : '')}>
      <h1 className="sr-only">Consoles</h1>
      <PlayTabsRow current="consoles" />
      {failed ? <p className="wplay-empty">The consoles couldn’t be read. Try again in a moment.</p> : !list ? <Spinner /> : !shown.length ? (
        finding.length ? (
          <div className="wplay-empty" role="status">{finding.map((f) => <p key={f.library}>{findingWords(f)}<br /><small>{findingDetail(f)}</small></p>)}</div>
        ) : <p className="wplay-empty">No console has games here yet. An administrator adds a games library in Settings.</p>
      ) : (
        <div className="wplay-consoles-body">
          <section aria-labelledby={head}>
            {finding.length > 0 && <p className="wplay-finding" role="status">{finding.map((f) => findingWords(f)).join(' · ')}</p>}
            <h2 id={head} className="wplay-consoles-h">{shown.some((c) => gamesOf(c) > 0) ? 'Consoles with games' : 'Consoles that play here'}</h2>
            <ul className="wplay-console-grid" data-nav-group>
              {shown.map((c) => (
                <li key={c.id}>
                  <button type="button" className={'wplay-console' + (current?.id === c.id ? ' on' : '')} aria-pressed={current?.id === c.id}
                    onFocus={() => !phone && setAt(c.id)} onPointerDown={() => press.press(current?.id === c.id)}
                    onClick={(e) => { if (press.opens(e.detail) && !phone && current?.id === c.id) browse(c); else pick(c.id, true) }}>
                    <span className="wplay-console-art"><ConsoleArt c={c} size="md" /></span>
                    <b>{c.short}</b>
                    <span className="wplay-console-count">{countWords(c)}</span>
                    <i className={'wplay-console-dot ' + coreLine(c).state} aria-hidden="true" />
                  </button>
                </li>
              ))}
            </ul>
          </section>
          {current && (!phone || sheet) && <ConsolePanel c={current} sheet={phone} onClose={() => setSheet(false)} />}
          {phone && sheet && <div className="wplay-sheet-scrim" onClick={() => setSheet(false)} aria-hidden="true" />}
        </div>
      )}
    </div>
  )
}

// One console's settings: its core and BIOS files (administrators), its game settings and its controls, and the libraries.
export function PlayConsoleSettings() {
  const { id = '' } = useParams()
  const [q, setQ] = useSearchParams()
  const { user } = useApp()
  const [c, setC] = useState<GameConsole | null>(null)
  useEffect(() => { getConsoles().then((l) => setC(l.find((x) => x.id === id) || null)).catch(() => {}) }, [id])
  useNamesChanged(useCallback(() => { getConsoles().then((l) => setC(l.find((x) => x.id === id) || null)).catch(() => {}) }, [id]))
  const parts = [
    ...(user?.isAdmin ? [{ id: 'core', label: 'Core and BIOS' }] : []),
    { id: 'games', label: 'Game settings' },
    { id: 'controls', label: 'Controls' },
    ...(user?.isAdmin ? [{ id: 'library', label: 'Library' }] : []),
  ]
  const part = parts.find((p) => p.id === q.get('part'))?.id || parts[0].id
  const go = (p: string) => { const next = new URLSearchParams(q); next.set('part', p); next.set('console', id); setQ(next, { replace: true }) }
  const name = c?.short || id.toUpperCase()
  return (
    <div className="wplay-settings-page">
      <Link className="wplay-settings-back" to={PLAY_CONSOLES}>Consoles</Link>
      <header className="wplay-console-head">{c && <ConsoleMark console={c} size="md" on="dark" />}<h1>{name} settings</h1></header>
      <div className="wplay-parts" role="tablist" aria-label={`${name} settings`}>
        {parts.map((p) => <button key={p.id} type="button" role="tab" aria-selected={p.id === part} className={p.id === part ? 'active' : ''} onClick={() => go(p.id)}>{p.label}</button>)}
      </div>
      <div role="tabpanel" className="wplay-part">
        <Suspense fallback={<Spinner />}>
          {part === 'core' && <GamingSettings only={id} />}
          {part === 'games' && <GameSettingsPage />}
          {part === 'controls' && <ControllersScreen />}
          {part === 'library' && (
            <p className="wplay-console-note">Which folders {name} games come from, a library’s console and how files are named are in <Link to="/wittyplay/settings/libraries">Settings, Libraries</Link>.</p>
          )}
        </Suspense>
      </div>
    </div>
  )
}

// A games library in WittyPlay's frame: the app's own (every game, by console, with search and filters).
export function PlayLibrary() {
  const { id } = useParams()
  const { libraries } = useApp()
  const games = libraries.filter((l) => l.type === 'games')
  const lib = (id ? games.find((l) => String(l.id) === id) : games[0])
  if (!lib) return <p className="wplay-empty">No games library here yet. An administrator adds one in Settings.</p>
  return <Suspense fallback={<Spinner />}><PlayLibraryView key={lib.id} lib={lib} /></Suspense>
}
