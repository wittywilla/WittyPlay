// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
//
// A game's own .wittyplay (/wittyplay/game/:id/file), for administrators on a desktop or in a
// browser: its details, pictures, how it plays, its controls, cheats and notes on its saves, saved
// beside the game. Download shares it; Import takes one someone shared (one made for another dump
// of the game is refused unless you say so); Write out puts it in the standard files other game
// programs read. The server checks everything and lists every problem with where it is. In TV mode
// the page only says editing is on the desktop: a remote is no way to type a game's overview.
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, mediaUrl, uploadFile, type Item } from '@wms/app/api'
import { LoadError, Spinner } from '@wms/app/components/common'
import { IconDownload, IconInfo, IconTrash } from '@wms/app/icons'
import { toast, useApp } from '@wms/app/state'
import { useTvMode } from '@wms/app/tvmode/mode'
import { playGamePath } from '../shell/playPaths'
import './playgamefile.css'

type Problem = { where: string; what: string }
type Hotkey = { keyboard?: string; gamepad?: string }
type Player = { player: number; device?: string; keyboard?: Record<string, string>; gamepad?: Record<string, string>; mouse?: Record<string, string>; remap?: Record<string, string>; turbo?: string[] }
type ControlSet = { players?: Player[]; hotkeys?: { enable?: Hotkey; actions?: Record<string, Hotkey> }; turbo?: { rate?: number; mode?: string }; analog?: { deadzone?: number; sensitivity?: number }; rumble?: boolean }
type Cheat = { id?: string; label: string; code: string; enabled?: boolean }
type Details = {
  title?: string; sortTitle?: string; originalTitle?: string; console?: string; year?: number; releaseDate?: string; overview?: string
  genres?: string[]; developer?: string; publisher?: string; series?: string; regions?: string[]; rating?: number
  players?: { min?: number; max?: number; coop?: boolean; versus?: boolean; online?: boolean }
}
type Doc = {
  wittyplay: number; language?: string
  game?: { details?: Details; identity?: Record<string, unknown>; artwork?: Record<string, string>; screenshots?: string[]
    play?: { core?: string; shader?: string; aspectRatio?: string; videoStandard?: string; disc?: number }
    cheats?: Cheat[]; saves?: { kind?: string; memoryCard?: string; notes?: string }; notes?: string }
  controls?: { default?: ControlSet; consoles?: Record<string, ControlSet> }
  [k: string]: unknown
}
type View = {
  exists: boolean; name: string; draft?: boolean; verdict?: string; console: string
  state?: { problem?: string; verdict?: string }; doc: Doc; pictures: Record<string, string>
  limits: { artworkRoles: string[]; targets: string[]; hotkeyActions: string[]; devices: string[]; maxImage: number }
}

const ROLE_NAMES: Record<string, string> = {
  boxfront: 'Box front', boxback: 'Box back', boxspine: 'Box spine', boxfull: 'Whole box', media: 'Disc or cartridge', logo: 'Logo',
  marquee: 'Marquee', banner: 'Banner', titlescreen: 'Title screen', fanart: 'Background', snapshot: 'Video still', icon: 'Icon',
}
const ACTION_NAMES: Record<string, string> = {
  menu: 'Menu', pause: 'Pause', reset: 'Reset', exit: 'Quit the game', saveState: 'Save state', loadState: 'Load state', nextSlot: 'Next slot',
  previousSlot: 'Previous slot', fastForward: 'Fast-forward (toggle)', fastForwardHold: 'Fast-forward (hold)', slowMotion: 'Slow motion',
  rewind: 'Rewind', frameAdvance: 'Frame advance', screenshot: 'Screenshot', recordVideo: 'Record video', fullscreen: 'Full screen',
  mute: 'Mute', volumeUp: 'Volume up', volumeDown: 'Volume down', nextDisc: 'Next disc', ejectDisc: 'Eject disc', nextShader: 'Next shader',
  cheats: 'Cheats on or off', netplayChat: 'Online chat',
}
const list = (s: string) => s.split(',').map((x) => x.trim()).filter(Boolean)
const num = (s: string) => (s.trim() === '' || Number.isNaN(Number(s)) ? undefined : Number(s))

function readAsDataURL(f: File): Promise<string> {
  return new Promise((ok, bad) => { const r = new FileReader(); r.onload = () => ok(String(r.result)); r.onerror = () => bad(r.error); r.readAsDataURL(f) })
}

// A field: its label, the control, and the server's problem with it if there is one.
function Field({ label, where, problems, children, wide }: { label: string; where: string; problems: Problem[]; children: ReactNode; wide?: boolean }) {
  const mine = problems.filter((p) => p.where === where || p.where.startsWith(where + '.') || p.where.startsWith(where + '['))
  return (
    <label className={'wgf-field' + (wide ? ' wide' : '') + (mine.length ? ' bad' : '')}>
      <span>{label}</span>
      {children}
      {mine.map((p) => <em key={p.where + p.what} role="alert">{p.what}</em>)}
    </label>
  )
}

// A key's code name, by pressing it.
function KeyInput({ value, onChange, label }: { value?: string; onChange: (v: string) => void; label: string }) {
  const [listening, setListening] = useState(false)
  return (
    <span className="wgf-key">
      <input aria-label={label} value={value || ''} placeholder="—" onChange={(e) => onChange(e.target.value.trim())}
        onKeyDown={(e) => { if (!listening) return; e.preventDefault(); const mods = (['Control', 'Shift', 'Alt', 'Meta'] as const).filter((m) => e.getModifierState(m) && !e.code.startsWith(m)); onChange([...mods, e.code].join('+')); setListening(false) }}
        onBlur={() => setListening(false)} className={listening ? 'listening' : ''} />
      <button type="button" className="btn sm ghost" onClick={(e) => { setListening(true); (e.currentTarget.previousElementSibling as HTMLInputElement)?.focus() }}>{listening ? 'Press a key…' : 'Set'}</button>
    </span>
  )
}

export default function PlayGameFile() {
  const { id } = useParams()
  const tv = useTvMode()
  const { user } = useApp()
  const [it, setIt] = useState<Item | null>(null)
  const [view, setView] = useState<View | null>(null)
  const [doc, setDoc] = useState<Doc | null>(null)
  const [failed, setFailed] = useState('')
  const [problems, setProblems] = useState<Problem[]>([])
  const [images, setImages] = useState<Record<string, string>>({}) // role -> new picture, as a data address
  const [shots, setShots] = useState<string[]>([]) // new screenshots
  const [saving, setSaving] = useState(false)
  const [player, setPlayer] = useState(1)
  const importRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      setIt(await api.get<Item>(`/items/${id}`))
      const v = await api.get<View>(`/modules/wittyplay/games/${id}/wittyplay`)
      setView(v); setDoc(v.doc); setImages({}); setShots([]); setProblems([]); setFailed('')
    } catch (e: any) {
      if (e?.data?.problems) { setProblems(e.data.problems); if (e.data.view) { setView(e.data.view); setDoc({ wittyplay: 2 }) } }
      setFailed(e?.message || 'This game’s file can’t be opened')
    }
  }, [id])
  useEffect(() => { if (!tv && user?.isAdmin) void load() }, [load, tv, user?.isAdmin])

  const g = doc?.game || {}
  const d = g.details || {}
  const setGame = (patch: Partial<NonNullable<Doc['game']>>) => setDoc((x) => ({ ...(x || { wittyplay: 2 }), game: { ...(x?.game || {}), ...patch } }))
  const setDetails = (patch: Partial<Details>) => setGame({ details: { ...d, ...patch } })
  const set = doc?.controls?.default || {}
  const setControls = (patch: Partial<ControlSet>) => setDoc((x) => ({ ...(x || { wittyplay: 2 }), controls: { ...(x?.controls || {}), default: { ...(x?.controls?.default || {}), ...patch } } }))
  const p: Player = useMemo(() => (set.players || []).find((x) => x.player === player) || { player }, [set.players, player])
  const setPlayerPatch = (patch: Partial<Player>) => {
    const others = (set.players || []).filter((x) => x.player !== player)
    setControls({ players: [...others, { ...p, ...patch }].sort((a, b) => a.player - b.player) })
  }
  const setMap = (kind: 'keyboard' | 'gamepad', target: string, v: string) => {
    const m = { ...(p[kind] || {}) }
    if (v) m[target] = v; else delete m[target]
    setPlayerPatch({ [kind]: Object.keys(m).length ? m : undefined })
  }
  const setHotkey = (action: string, patch: Hotkey) => {
    const actions = { ...(set.hotkeys?.actions || {}) }
    const next = { ...(actions[action] || {}), ...patch }
    if (!next.keyboard) delete next.keyboard
    if (!next.gamepad) delete next.gamepad
    if (Object.keys(next).length) actions[action] = next; else delete actions[action]
    setControls({ hotkeys: { ...(set.hotkeys || {}), actions } })
  }

  const save = async () => {
    if (!doc) return
    setSaving(true); setProblems([])
    try {
      const r = await api.put<{ saved: string; verdict: string }>(`/modules/wittyplay/games/${id}/wittyplay`, { doc, images, screenshots: shots })
      toast(`Saved ${r.saved}. The game’s details and pictures change at the next scan of its library.`)
      void load()
    } catch (e: any) {
      setProblems(e?.data?.problems || [{ where: '', what: e.message }])
      toast('Not saved: ' + e.message)
    } finally { setSaving(false) }
  }
  const importFile = async (f: File, force = false) => {
    try {
      await uploadFile(`/modules/wittyplay/games/${id}/wittyplay/import${force ? '?force=1' : ''}`, f)
      toast('Imported.'); void load()
    } catch (e: any) {
      if (e?.status === 409 && !force && window.confirm(e.message + '\n\nUse it anyway?')) return importFile(f, true)
      setProblems(e?.data?.problems || [{ where: '', what: e.message }])
    }
  }
  const writeOut = async () => {
    try {
      const r = await api.post<{ files?: string[]; kept?: string[] }>(`/modules/wittyplay/games/${id}/wittyplay/writeout`, { overwrite: false })
      toast(`Written: ${(r.files || []).join(', ') || 'nothing new'}${r.kept?.length ? `. Kept as they were: ${r.kept.join(', ')}` : ''}`)
    } catch (e: any) { toast(e.message) }
  }

  if (tv) {
    return (
      <div className="wgf wgf-tv">
        <h1>{it?.title || 'This game'}: its .wittyplay file</h1>
        <p className="wgf-note"><IconInfo size={18} /> Editing a game’s .wittyplay file is limited to the desktop: open this game in the desktop app or in a web browser on a computer. Everything it says still works here.</p>
        <Link className="btn" to={playGamePath(Number(id))}>Back to the game</Link>
      </div>
    )
  }
  if (!user?.isAdmin) return <div className="wgf"><p className="wgf-note">Only an administrator can edit a game’s .wittyplay file.</p></div>
  if (!view || !doc) return failed ? <LoadError what={failed} onRetry={() => { void load() }} /> : <Spinner />

  const pic = (role: string) => images[role] || (g.artwork?.[role] ? view.pictures[g.artwork[role]] : '')
  const targets = view.limits.targets
  return (
    <div className="wgf">
      <header className="wgf-head">
        <div>
          <Link to={playGamePath(Number(id))} className="wgf-back">← {it?.title}</Link>
          <h1>{view.name}</h1>
          <p className="muted">
            {view.draft ? 'Not saved yet: this draft is what was found beside the game and what WittyPlay knows of it.' : 'Saved beside the game. Scans read it.'}
            {view.verdict === 'same' && ' Made for this very file.'}
            {view.verdict === 'other' && ' It was made for another dump of this game, so it isn’t used.'}
          </p>
          {view.state?.problem && <p className="wgf-problem" role="alert">{view.state.problem}</p>}
        </div>
        <div className="wgf-actions">
          <button type="button" className="btn primary" disabled={saving} onClick={() => void save()}>{saving ? 'Saving…' : 'Save'}</button>
          <a className="btn" href={mediaUrl(`/api/modules/wittyplay/games/${id}/wittyplay/download`)} download><IconDownload size={16} /> Download to share</a>
          <button type="button" className="btn" onClick={() => importRef.current?.click()}>Import…</button>
          <input ref={importRef} type="file" accept=".wittyplay" hidden onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void importFile(f) }} />
          {view.exists && <button type="button" className="btn" onClick={() => void writeOut()} title="gamelist.xml, the .nfo, media/ pictures, .rmp and .cfg">Write out to standard files</button>}
        </div>
      </header>

      {problems.length > 0 && (
        <div className="wgf-problems" role="alert">
          <b>{problems.length === 1 ? 'One thing to fix' : `${problems.length} things to fix`}</b>
          <ul>{problems.map((x) => <li key={x.where + x.what}><code>{x.where || 'file'}</code> {x.what}</li>)}</ul>
        </div>
      )}

      <section className="wgf-section">
        <h2>Details</h2>
        <div className="wgf-grid">
          <Field label="Title" where="game.details.title" problems={problems}><input value={d.title || ''} onChange={(e) => setDetails({ title: e.target.value })} /></Field>
          <Field label="Sort title" where="game.details.sortTitle" problems={problems}><input value={d.sortTitle || ''} onChange={(e) => setDetails({ sortTitle: e.target.value })} /></Field>
          <Field label="Title in its own script" where="game.details.originalTitle" problems={problems}><input value={d.originalTitle || ''} onChange={(e) => setDetails({ originalTitle: e.target.value })} /></Field>
          <Field label="Year" where="game.details.year" problems={problems}><input inputMode="numeric" value={d.year ?? ''} onChange={(e) => setDetails({ year: num(e.target.value) })} /></Field>
          <Field label="Release date" where="game.details.releaseDate" problems={problems}><input placeholder="1994-11-21" value={d.releaseDate || ''} onChange={(e) => setDetails({ releaseDate: e.target.value || undefined })} /></Field>
          <Field label="Developer" where="game.details.developer" problems={problems}><input value={d.developer || ''} onChange={(e) => setDetails({ developer: e.target.value })} /></Field>
          <Field label="Publisher" where="game.details.publisher" problems={problems}><input value={d.publisher || ''} onChange={(e) => setDetails({ publisher: e.target.value })} /></Field>
          <Field label="Series" where="game.details.series" problems={problems}><input value={d.series || ''} onChange={(e) => setDetails({ series: e.target.value })} /></Field>
          <Field label="Genres (commas between)" where="game.details.genres" problems={problems}><input value={(d.genres || []).join(', ')} onChange={(e) => setDetails({ genres: list(e.target.value) })} /></Field>
          <Field label="Regions (commas between)" where="game.details.regions" problems={problems}><input value={(d.regions || []).join(', ')} onChange={(e) => setDetails({ regions: list(e.target.value) })} /></Field>
          <Field label="Rating out of 10" where="game.details.rating" problems={problems}><input inputMode="decimal" value={d.rating ?? ''} onChange={(e) => setDetails({ rating: num(e.target.value) })} /></Field>
          <Field label="Players" where="game.details.players" problems={problems}>
            <span className="wgf-pair">
              <input aria-label="Fewest players" inputMode="numeric" value={d.players?.min ?? ''} onChange={(e) => setDetails({ players: { ...d.players, min: num(e.target.value) } })} />
              <span>to</span>
              <input aria-label="Most players" inputMode="numeric" value={d.players?.max ?? ''} onChange={(e) => setDetails({ players: { ...d.players, max: num(e.target.value) } })} />
            </span>
          </Field>
          <Field label="Overview" where="game.details.overview" problems={problems} wide><textarea rows={5} value={d.overview || ''} onChange={(e) => setDetails({ overview: e.target.value })} /></Field>
        </div>
      </section>

      <section className="wgf-section">
        <h2>Pictures</h2>
        <div className="wgf-pics">
          {view.limits.artworkRoles.map((role) => (
            <Field key={role} label={ROLE_NAMES[role] || role} where={'game.artwork.' + role} problems={problems}>
              <span className="wgf-pic">
                {pic(role) ? <img src={pic(role)} alt="" /> : <span className="wgf-pic-empty">None</span>}
                <input type="file" accept="image/png,image/jpeg,image/webp" aria-label={`Choose the ${ROLE_NAMES[role] || role}`}
                  onChange={async (e) => { const f = e.target.files?.[0]; if (f) { if (f.size > view.limits.maxImage) { toast('That picture is too large.'); return } const u = await readAsDataURL(f); setImages((m) => ({ ...m, [role]: u })) } }} />
                {pic(role) && <button type="button" className="btn sm ghost" aria-label="Remove" onClick={() => { setImages((m) => { const n = { ...m }; delete n[role]; return n }); const a = { ...(g.artwork || {}) }; delete a[role]; setGame({ artwork: a }) }}><IconTrash size={14} /></button>}
              </span>
            </Field>
          ))}
        </div>
        <h3>Screenshots</h3>
        <div className="wgf-shots">
          {(g.screenshots || []).map((s) => (
            <span key={s} className="wgf-pic"><img src={view.pictures[s]} alt="" />
              <button type="button" className="btn sm ghost" aria-label="Remove" onClick={() => setGame({ screenshots: (g.screenshots || []).filter((x) => x !== s) })}><IconTrash size={14} /></button></span>
          ))}
          {shots.map((s, i) => (
            <span key={'new' + i} className="wgf-pic"><img src={s} alt="" />
              <button type="button" className="btn sm ghost" aria-label="Remove" onClick={() => setShots((l) => l.filter((_, j) => j !== i))}><IconTrash size={14} /></button></span>
          ))}
          <label className="btn sm">Add screenshots…<input type="file" hidden multiple accept="image/png,image/jpeg,image/webp"
            onChange={async (e) => { const fs = Array.from(e.target.files || []); e.target.value = ''; const urls = await Promise.all(fs.map(readAsDataURL)); setShots((l) => [...l, ...urls]) }} /></label>
        </div>
      </section>

      <section className="wgf-section">
        <h2>How it plays</h2>
        <div className="wgf-grid">
          <Field label="Core" where="game.play.core" problems={problems}><input value={g.play?.core || ''} onChange={(e) => setGame({ play: { ...g.play, core: e.target.value || undefined } })} /></Field>
          <Field label="Shader preset" where="game.play.shader" problems={problems}><input value={g.play?.shader || ''} onChange={(e) => setGame({ play: { ...g.play, shader: e.target.value || undefined } })} /></Field>
          <Field label="Aspect ratio" where="game.play.aspectRatio" problems={problems}><input placeholder="4:3, 8:7, core, stretch" value={g.play?.aspectRatio || ''} onChange={(e) => setGame({ play: { ...g.play, aspectRatio: e.target.value || undefined } })} /></Field>
          <Field label="Video" where="game.play.videoStandard" problems={problems}>
            <select value={g.play?.videoStandard || ''} onChange={(e) => setGame({ play: { ...g.play, videoStandard: e.target.value || undefined } })}>
              <option value="">As the core decides</option><option value="ntsc">NTSC (60 Hz)</option><option value="pal">PAL (50 Hz)</option>
            </select>
          </Field>
        </div>
      </section>

      <section className="wgf-section">
        <h2>Controls</h2>
        <p className="muted">These are the game’s defaults. Anyone’s own saved controls still come first.</p>
        <div className="wgf-tabs" role="tablist">
          {[1, 2, 3, 4, 5, 6, 7, 8].map((n) => <button key={n} type="button" role="tab" aria-selected={player === n} className={'btn sm' + (player === n ? ' primary' : '')} onClick={() => setPlayer(n)}>Player {n}</button>)}
        </div>
        <Field label="Plugged in" where={`controls.default.players`} problems={problems}>
          <select value={p.device || ''} onChange={(e) => setPlayerPatch({ device: e.target.value || undefined })}>
            <option value="">As the console decides</option>
            {view.limits.devices.map((x) => <option key={x} value={x}>{x}</option>)}
          </select>
        </Field>
        <table className="wgf-map">
          <thead><tr><th>Button</th><th>Key</th><th>Gamepad</th><th>Turbo</th></tr></thead>
          <tbody>
            {targets.map((t) => (
              <tr key={t}>
                <th scope="row">{t}</th>
                <td><KeyInput label={`Key for ${t}`} value={p.keyboard?.[t]} onChange={(v) => setMap('keyboard', t, v)} /></td>
                <td><input aria-label={`Gamepad for ${t}`} placeholder="button:0" value={p.gamepad?.[t] || ''} onChange={(e) => setMap('gamepad', t, e.target.value.trim())} /></td>
                <td><input type="checkbox" aria-label={`Turbo on ${t}`} checked={(p.turbo || []).includes(t)} onChange={(e) => setPlayerPatch({ turbo: e.target.checked ? [...(p.turbo || []), t] : (p.turbo || []).filter((x) => x !== t) })} /></td>
              </tr>
            ))}
          </tbody>
        </table>
        <h3>Hotkeys</h3>
        <table className="wgf-map">
          <thead><tr><th>Action</th><th>Key</th><th>Gamepad (buttons together)</th></tr></thead>
          <tbody>
            {view.limits.hotkeyActions.map((a) => (
              <tr key={a}>
                <th scope="row">{ACTION_NAMES[a] || a}</th>
                <td><KeyInput label={`Key for ${ACTION_NAMES[a] || a}`} value={set.hotkeys?.actions?.[a]?.keyboard} onChange={(v) => setHotkey(a, { keyboard: v })} /></td>
                <td><input aria-label={`Gamepad for ${ACTION_NAMES[a] || a}`} placeholder="button:8+button:5" value={set.hotkeys?.actions?.[a]?.gamepad || ''} onChange={(e) => setHotkey(a, { gamepad: e.target.value.trim() })} /></td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="wgf-grid">
          <Field label="Turbo presses a second" where="controls.default.turbo.rate" problems={problems}><input inputMode="decimal" value={set.turbo?.rate ?? ''} onChange={(e) => setControls({ turbo: { ...set.turbo, rate: num(e.target.value) } })} /></Field>
          <Field label="Turbo" where="controls.default.turbo.mode" problems={problems}>
            <select value={set.turbo?.mode || ''} onChange={(e) => setControls({ turbo: { ...set.turbo, mode: e.target.value || undefined } })}><option value="">While held</option><option value="toggle">On until pressed again</option></select>
          </Field>
          <Field label="Stick dead zone (0 to 0.9)" where="controls.default.analog.deadzone" problems={problems}><input inputMode="decimal" value={set.analog?.deadzone ?? ''} onChange={(e) => setControls({ analog: { ...set.analog, deadzone: num(e.target.value) } })} /></Field>
          <Field label="Stick sensitivity (0.1 to 5)" where="controls.default.analog.sensitivity" problems={problems}><input inputMode="decimal" value={set.analog?.sensitivity ?? ''} onChange={(e) => setControls({ analog: { ...set.analog, sensitivity: num(e.target.value) } })} /></Field>
        </div>
      </section>

      <section className="wgf-section">
        <h2>Cheats</h2>
        {(g.cheats || []).map((c, i) => (
          <div key={c.id || i} className="wgf-cheat">
            <Field label="Name" where={`game.cheats[${i}].label`} problems={problems}><input value={c.label} onChange={(e) => setGame({ cheats: (g.cheats || []).map((x, j) => (j === i ? { ...x, label: e.target.value } : x)) })} /></Field>
            <Field label="Code" where={`game.cheats[${i}].code`} problems={problems}><input value={c.code} onChange={(e) => setGame({ cheats: (g.cheats || []).map((x, j) => (j === i ? { ...x, code: e.target.value } : x)) })} /></Field>
            <label className="wgf-check"><input type="checkbox" checked={!!c.enabled} onChange={(e) => setGame({ cheats: (g.cheats || []).map((x, j) => (j === i ? { ...x, enabled: e.target.checked } : x)) })} /> On</label>
            <button type="button" className="btn sm ghost" aria-label="Remove" onClick={() => setGame({ cheats: (g.cheats || []).filter((_, j) => j !== i) })}><IconTrash size={14} /></button>
          </div>
        ))}
        <button type="button" className="btn sm" onClick={() => setGame({ cheats: [...(g.cheats || []), { label: '', code: '' }] })}>Add a cheat</button>
      </section>

      <section className="wgf-section">
        <h2>Saves and notes</h2>
        <div className="wgf-grid">
          <Field label="How it saves" where="game.saves.kind" problems={problems}>
            <select value={g.saves?.kind || ''} onChange={(e) => setGame({ saves: { ...g.saves, kind: e.target.value || undefined } })}>
              <option value="">Not said</option><option value="battery">On the cartridge</option><option value="memorycard">Memory card</option><option value="password">Passwords</option><option value="none">It doesn’t</option>
            </select>
          </Field>
          <Field label="Memory card" where="game.saves.memoryCard" problems={problems}>
            <select value={g.saves?.memoryCard || ''} onChange={(e) => setGame({ saves: { ...g.saves, memoryCard: e.target.value || undefined } })}>
              <option value="">Not said</option><option value="shared">Shared with other games</option><option value="game">One of its own</option>
            </select>
          </Field>
          <Field label="About its saves" where="game.saves.notes" problems={problems} wide><textarea rows={2} value={g.saves?.notes || ''} onChange={(e) => setGame({ saves: { ...g.saves, notes: e.target.value } })} /></Field>
          <Field label="Notes" where="game.notes" problems={problems} wide><textarea rows={3} value={g.notes || ''} onChange={(e) => setGame({ notes: e.target.value })} /></Field>
          <Field label="Language of this text" where="language" problems={problems}><input placeholder="en" value={doc.language || ''} onChange={(e) => setDoc({ ...doc, language: e.target.value || undefined })} /></Field>
        </div>
      </section>
    </div>
  )
}
