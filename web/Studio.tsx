// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ControllerPicture } from './art'
import type { Background, ControlsData, Target } from './consoles'
import { MAX_PLAYERS, onTV, padSupport, type InputManager, type PadInfo } from './input'
import {
  conflicts, describeKey, describeSource, effectiveKeys, effectiveMap, effectiveOptions, findPress, keyConflicts, noteReleases, rawFromDefaults, resolveProfile, snapshot, standingOf, toStandard,
  type Binding, type ControllerData, type KeyMap, type PadMap, type Profile, type ProfileOptions,
} from './mapping'
import { PadPicture, type Callout } from './padart'
import { KeyGlyph, PhysGlyph, StickGlyph, physName } from './padglyph'
import { PromptBar } from './prompts'
import { familyOf, type PadFamily } from './words'
import './studio.css'

// The controller screen. Along the top, the controllers that are here; down the side, what
// can be set for the one chosen. Buttons are shown as the controller in someone's hands prints
// them, with what each does in this game beside it. Everything works with a D-pad and OK: real
// buttons in a sensible order, steppers instead of sliders, and nothing that needs pointing.

export type ImportResult = { imported: Profile[]; rejected: { index: number; name: string; error: string }[]; notes: string[] }

// What the screen needs from the app. Everything goes through documented routes.
export interface ProfileHost {
  list(): Promise<Profile[]>
  create(p: Partial<Profile>): Promise<Profile>
  update(id: number, p: Profile): Promise<Profile>
  remove(id: number): Promise<void>
  copy(id: number): Promise<Profile>
  setActive(id: number, on: boolean): Promise<Profile>
  exportFile(ids?: number[]): Promise<string>
  importFile(text: string): Promise<ImportResult>
  setBackground(b: Background): Promise<Background>
  uploadBackground(f: File): Promise<Background>
  removeBackground(): Promise<Background>
  backgroundUrl(version: number): string
}

export type PortChoice = { port: number; types: { id: number; name: string }[]; current: number }

type Props = {
  host: ProfileHost
  data: ControlsData
  known: ControllerData | null
  input: InputManager
  consoleName: string
  gameId?: number
  gameTitle?: string
  consolePicker?: React.ReactNode
  devices?: PortChoice[]
  setDevice?: (port: number, id: number) => Promise<void>
  refresh: () => Promise<void> // profiles or the background changed
  onTouch?: () => void
  onClose: () => void
}

type Section = 'buttons' | 'sticks' | 'pointer' | 'vibration' | 'players' | 'profiles' | 'background'
type Device = number | 'keyboard'

const LISTEN_MS = 7000
const MAX_FILE = 10 << 20
const MAX_IMPORT = 256 << 10
const GROUPS: [Target['group'], string][] = [['face', 'Buttons'], ['dpad', 'D-pad'], ['shoulder', 'Shoulders and triggers'], ['stick', 'Stick'], ['meta', 'System']]

// While this is held, a pad's buttons mean nothing to the app's own navigation
// (the server's web/src/tv.ts). Let go a moment late, so that what was just pressed isn't also OK.
let tvHolds = 0
function holdTvNavigation(on: boolean) {
  tvHolds += on ? 1 : -1
  if (on) document.documentElement.classList.add('wp-capture')
  else setTimeout(() => { if (tvHolds === 0) document.documentElement.classList.remove('wp-capture') }, 120)
}

const pct = (v: number) => `${Math.round(v * 100)}%`
const cap = (t: string) => t && t[0].toUpperCase() + t.slice(1)

// ---- small controls ----

// A value changed by pressing + and −, which a remote can do and a slider often can't.
function Stepper({ label, hint, value, text, onChange, min, max, step }: { label: string; hint?: string; value: number; text?: string; onChange: (v: number) => void; min: number; max: number; step: number }) {
  const set = (v: number) => onChange(Math.round(Math.min(max, Math.max(min, v)) / step) * step)
  return (
    <div className="wps-stepper" role="group" aria-label={label}>
      <span className="wp-grow">{label}{hint && <small className="wp-muted wsc-hint">{hint}</small>}</span>
      <button type="button" className="wp-btn" disabled={value <= min} aria-label={`Less ${label.toLowerCase()}`} onClick={() => set(value - step)}>−</button>
      <output className="wps-value">{text ?? value}</output>
      <button type="button" className="wp-btn" disabled={value >= max} aria-label={`More ${label.toLowerCase()}`} onClick={() => set(value + step)}>+</button>
    </div>
  )
}

function Toggle({ label, on, onChange, hint }: { label: string; on: boolean; onChange: (v: boolean) => void; hint?: string }) {
  return (
    <button type="button" className={'wps-toggle' + (on ? ' on' : '')} role="switch" aria-checked={on} onClick={() => onChange(!on)}>
      <span className="wp-grow">{label}{hint && <small className="wp-muted"> {hint}</small>}</span>
      <span className="wp-switch" aria-hidden="true" />
    </button>
  )
}

// One of a few values, side by side.
function Segmented({ label, value, choices, onChange }: { label: string; value: string; choices: [string, string][]; onChange: (v: string) => void }) {
  return (
    <div className="wsc-seg-row">
      <span className="wsc-seg-label">{label}</span>
      <div className="wsc-seg" role="radiogroup" aria-label={label}>
        {choices.map(([v, text]) => <button key={v} type="button" role="radio" aria-checked={v === value} className={v === value ? 'on' : ''} onClick={() => onChange(v)}>{text}</button>)}
      </div>
    </div>
  )
}

function Card({ title, children, actions, className }: { title?: string; children: React.ReactNode; actions?: React.ReactNode; className?: string }) {
  return (
    <section className={'wsc-card' + (className ? ' ' + className : '')}>
      {(title || actions) && <header className="wsc-card-head">{title && <h3>{title}</h3>}{actions}</header>}
      {children}
    </section>
  )
}

const noPadWords = (): string => {
  const s = padSupport()
  if (s === 'insecure') return 'No controller can be seen: this page is on plain http, and most browsers only show gamepads to secure pages. Open this server over https, or at localhost on this computer. Until then the keyboard and touch controls work.'
  if (s === 'blocked') return 'This browser is set not to share controllers with this page. Check its site settings.'
  if (s === 'none') return 'This browser has no way to read gamepads. Try a current version of Chrome, Edge, Firefox or Safari.'
  return 'No controller yet. Turn one on or plug it in, then press any button on it: browsers only show a controller once it has been pressed.'
}

// A drawing of a kind of controller, for its card: a pad, or a keyboard.
function DeviceIcon({ kind }: { kind: 'pad' | 'keyboard' | 'touch' }) {
  const s = { fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinejoin: 'round', strokeLinecap: 'round' } as const
  if (kind === 'keyboard') return <svg viewBox="0 0 32 24" aria-hidden="true"><rect {...s} x="2" y="5" width="28" height="15" rx="2.5" /><path {...s} d="M7 10h1M11 10h1M15 10h1M19 10h1M23 10h1M9 15h14" /></svg>
  if (kind === 'touch') return <svg viewBox="0 0 32 24" aria-hidden="true"><rect {...s} x="9" y="2" width="14" height="20" rx="2.5" /><circle cx="16" cy="18.5" r="1" fill="currentColor" /></svg>
  return <svg viewBox="0 0 32 24" aria-hidden="true"><path {...s} d="M9 5h14q5 0 6.5 6l1.3 6q.7 4-2.6 4-2 0-3.6-2.3L23 17H9l-1.6 1.7Q5.8 21 3.8 21 .5 21 1.2 17l1.3-6Q4 5 9 5z" /><path {...s} d="M8 10v4M6 12h4" /><circle cx="23" cy="10.5" r="1" fill="currentColor" /><circle cx="25.5" cy="13" r="1" fill="currentColor" /></svg>
}

// ---- the screen ----

export function ControllerStudio(props: Props) {
  const { host, data, input, onClose, refresh, onTouch } = props
  const root = useRef<HTMLDivElement>(null)
  const [note, setNote] = useState('')
  const say = useCallback((t: string) => setNote(cap(t)), [])
  const [bg, setBg] = useState<Background>(data.background)
  useEffect(() => setBg(data.background), [data.background])

  // What's connected, kept up to date while this screen is open: some browsers fire no events.
  const [pads, setPads] = useState<PadInfo[]>(() => input.listPads())
  useEffect(() => {
    const on = () => setPads(input.listPads())
    const prev = input.onChange
    input.onChange = () => { prev?.(); on() }
    const iv = setInterval(on, 1000)
    return () => { input.onChange = prev; clearInterval(iv) }
  }, [input])
  const [device, setDevice] = useState<Device>(() => input.listPads()[0]?.index ?? 'keyboard')
  useEffect(() => { if (typeof device === 'number' && !pads.some((p) => p.index === device)) setDevice(pads[0]?.index ?? 'keyboard') }, [pads, device])
  // A controller turned on while the keyboard is showing is what someone came here for.
  const seen = useRef(pads.length)
  useEffect(() => { if (pads.length > seen.current && device === 'keyboard') setDevice(pads[pads.length - 1].index); seen.current = pads.length }, [pads.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const ed = useEditor(props, device, pads, say)
  const isKeys = device === 'keyboard'
  const sections: [Section, string][] = [
    ['buttons', 'Buttons'],
    ...(!isKeys ? [['sticks', 'Sticks and triggers'] as [Section, string]] : []),
    ...(!isKeys && data.pad.aim ? [['pointer', 'Pointer'] as [Section, string]] : []),
    ...(!isKeys ? [['vibration', 'Vibration'] as [Section, string]] : []),
    ['players', 'Players'], ['profiles', 'Profiles'], ['background', 'Background'],
  ]
  const [section, setSection] = useState<Section>('buttons')
  useEffect(() => { if (!sections.some(([s]) => s === section)) setSection('buttons') }) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { root.current?.querySelector<HTMLElement>('.wsc-device[aria-pressed=true]')?.focus({ preventScroll: true }) }, [])

  return (
    <div ref={root} className="wps wsc" role="dialog" aria-label="Controllers">
      <header className="wps-head">
        <h1 className="wps-title">Controllers <span className="wp-muted">· {props.gameTitle || props.consoleName}</span></h1>
        <button type="button" className="wp-btn primary" onClick={onClose}>Done</button>
      </header>

      <nav className="wsc-devices" aria-label="Which controller">
        {pads.map((p) => (
          <button key={p.index + p.id} type="button" className="wsc-device" aria-pressed={device === p.index} onClick={() => setDevice(p.index)}>
            <DeviceIcon kind="pad" />
            <span className="wsc-device-text"><b>{p.name}</b><small>Player {p.port + 1}{p.profile ? ` · ${p.profile.name}` : ''}</small></span>
          </button>
        ))}
        <button type="button" className="wsc-device" aria-pressed={isKeys} onClick={() => setDevice('keyboard')}>
          <DeviceIcon kind="keyboard" /><span className="wsc-device-text"><b>Keyboard</b><small>Player {input.keyboardPlayer + 1}</small></span>
        </button>
        {onTouch && (
          <button type="button" className="wsc-device" onClick={onTouch}>
            <DeviceIcon kind="touch" /><span className="wsc-device-text"><b>Touch controls</b><small>Edit on screen…</small></span>
          </button>
        )}
      </nav>
      {pads.length === 0 && <p className="wsc-nopad" role="status">{noPadWords()}</p>}
      {props.consolePicker}

      <div className="wsc-body">
        <nav className="wsc-nav" role="tablist" aria-label="What to set" aria-orientation="vertical">
          {sections.map(([id, name]) => <button key={id} type="button" role="tab" aria-selected={section === id} className="wsc-nav-item" onClick={() => setSection(id)}>{name}</button>)}
        </nav>
        <main className="wsc-main">
          <div className="wps-note" role="status" aria-live="polite">{note}</div>
          {['buttons', 'sticks', 'pointer', 'vibration'].includes(section) && <ProfileBar ed={ed} props={props} say={say} goProfiles={() => setSection('profiles')} />}
          {section === 'buttons' && <ButtonsSection ed={ed} props={props} say={say} goSticks={() => setSection(data.pad.aim && ed.options.pointerStick !== 'off' ? 'pointer' : 'sticks')} />}
          {section === 'sticks' && <SticksSection ed={ed} props={props} say={say} />}
          {section === 'pointer' && <PointerSection ed={ed} />}
          {section === 'vibration' && <VibrationSection ed={ed} props={props} />}
          {section === 'players' && <PlayersSection props={props} pads={pads} say={say} />}
          {section === 'profiles' && <ProfilesSection ed={ed} props={props} say={say} />}
          {section === 'background' && <BackgroundSection host={host} bg={bg} setBg={setBg} refresh={refresh} say={say} data={data} />}
        </main>
      </div>
      <PromptBar className="wps-prompts" items={[{ button: 'dpad', label: 'Move' }, { button: 'confirm', label: 'Choose' }, { button: 'back', label: 'Done' }]} />
    </div>
  )
}

// ---- what's being edited: one controller's profile, shared by every section ----

type Editor = ReturnType<typeof useEditor>

function useEditor(props: Props, device: Device, pads: PadInfo[], say: (t: string) => void) {
  const { host, data, known, input, consoleName, gameId, refresh } = props
  const targets = data.targets
  const targetIds = useMemo(() => new Set(targets.map((t) => t.id)), [targets])
  const [profiles, setProfiles] = useState<Profile[] | null>(null)
  const [selected, setSelected] = useState<number | null>(null) // null is the built-in standard
  const pad = typeof device === 'number' ? pads.find((p) => p.index === device) || null : null
  const isKeys = device === 'keyboard'
  const consoleId = data.console
  const family: PadFamily = pad ? familyOf(pad.id) : 'generic'

  const load = useCallback(() => host.list().then(setProfiles).catch((e) => say(e?.message || 'Profiles couldn’t be loaded')), [host, say])
  useEffect(() => { void load() }, [load])

  const model = isKeys ? 'keyboard' : pad?.model || ''
  const raw = typeof device === 'number' ? (() => { try { return navigator.getGamepads?.()[device] ?? null } catch { return null } })() : null
  const standing = useMemo(() => (raw ? standingOf(raw, known, navigator.userAgent) : null), [device, known, pads]) // eslint-disable-line react-hooks/exhaustive-deps
  const rawPad = standing?.standing === 'unknown'
  const extraButton = !!raw && raw.buttons.length > 17
  const fitting = useMemo(() => (profiles || []).filter((p) => p.kind === (isKeys ? 'keyboard' : 'gamepad') && (!p.controller || p.controller === model) && (!p.console || p.console === consoleId) && (!p.game || p.game === gameId) && (isKeys || !!p.raw === rawPad)),
    [profiles, isKeys, model, consoleId, gameId, rawPad])
  const inUse = useMemo(() => resolveProfile(fitting, isKeys ? 'keyboard' : 'gamepad', model, consoleId, gameId || 0, rawPad), [fitting, isKeys, model, consoleId, gameId, rawPad])
  useEffect(() => { setSelected(inUse?.id ?? null) }, [inUse?.id, device]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { if (profiles && selected !== null && !fitting.some((p) => p.id === selected)) setSelected(inUse?.id ?? null) }, [fitting]) // eslint-disable-line react-hooks/exhaustive-deps
  const profile = fitting.find((p) => p.id === selected) || null

  const defaults = useMemo<PadMap>(() => (rawPad && raw ? rawFromDefaults(data.defaults.gamepad, raw) : data.defaults.gamepad), [data.defaults.gamepad, rawPad, device, pads.length]) // eslint-disable-line react-hooks/exhaustive-deps
  const map = useMemo<PadMap>(() => (rawPad && !profile ? defaults : effectiveMap(data.defaults.gamepad, targetIds, profile)), [data.defaults.gamepad, targetIds, profile, rawPad, defaults])
  const keys = useMemo<KeyMap>(() => effectiveKeys(data.defaults.keyboard, targetIds, profile), [data.defaults.keyboard, targetIds, profile])
  const clashes = useMemo(() => (isKeys ? keyConflicts(keys) : conflicts(map)), [isKeys, keys, map])
  const options = effectiveOptions(data.defaults.options, profile)
  const labelOf = useCallback((id: number) => targets.find((t) => t.id === id)?.label || String(id), [targets])

  // Changes are kept as they're made, and tried on the controller straight away. A queue keeps
  // them in order however fast they come.
  const queue = useRef<Promise<unknown>>(Promise.resolve())
  const timer = useRef(0)
  const pending = useRef<Profile | null>(null)
  const flushSave = useCallback(() => {
    const p = pending.current
    if (!p) return
    pending.current = null
    queue.current = queue.current.then(() => host.update(p.id, p)).then((saved) => {
      setProfiles((list) => (list || []).map((x) => (x.id === saved.id && pending.current?.id !== saved.id ? saved : x)))
      return refresh()
    }).catch((e) => say(e?.message || 'That couldn’t be saved'))
  }, [host, refresh, say])
  useEffect(() => () => { clearTimeout(timer.current); flushSave() }, [flushSave])

  const fresh = useCallback((): Partial<Profile> => ({
    name: `${isKeys ? 'Keyboard' : pad?.name || 'Controller'} · ${consoleName}`.slice(0, 60), kind: isKeys ? 'keyboard' : 'gamepad', controller: isKeys ? 'keyboard' : model, controllerName: pad?.name,
    console: consoleId, raw: !isKeys && rawPad, active: true, map: !isKeys && rawPad ? defaults : {}, keys: {}, options: {},
  }), [isKeys, pad, consoleName, model, consoleId, rawPad, defaults])

  // A press can land while the first change is still making the profile. It waits, and changes
  // that profile, rather than making another. The first change to the standard layout makes a
  // profile of it, so that a person gets what they asked for with nothing else to do first.
  const creating = useRef<Promise<Profile | null> | null>(null)
  const change = useCallback(async (edit: (p: Profile) => Profile) => {
    let cur = profile
    if (!cur && creating.current) {
      cur = await creating.current
      if (!cur) return
      if (pending.current?.id === cur.id) cur = pending.current
    }
    if (!cur) {
      const making = host.create(edit(fresh() as Profile)).catch((e: any) => { say(e?.message || 'That couldn’t be saved'); return null })
      creating.current = making
      const made = await making
      creating.current = null
      if (!made) return
      setProfiles((l) => [made, ...(l || [])])
      setSelected(made.id)
      if (typeof device === 'number') input.previewProfile(device, made)
      void refresh()
      return
    }
    const next = edit(cur)
    setProfiles((l) => (l || []).map((x) => (x.id === next.id ? next : x)))
    if (typeof device === 'number') input.previewProfile(device, next)
    pending.current = next
    clearTimeout(timer.current)
    timer.current = window.setTimeout(flushSave, 300)
  }, [profile, fresh, host, device, input, refresh, say, flushSave])

  const setBinding = useCallback((id: number, b: Binding | null) => change((p) => ({ ...p, map: { ...(p.map || {}), [id]: b } })), [change])
  const setKey = useCallback((id: number, code: string) => change((p) => {
    const next: KeyMap = {}
    for (const [k, v] of Object.entries(p.keys || {})) if (v !== code) next[Number(k)] = v // one key does one thing
    return { ...p, keys: { ...next, [id]: code } }
  }), [change])
  const setOption = useCallback((patch: Partial<ProfileOptions>) => change((p) => ({ ...p, options: { ...(p.options || {}), ...patch } })), [change])
  // One button back to what it does by default.
  const resetTarget = useCallback((id: number) => change((p) => {
    if (isKeys) { const k = { ...(p.keys || {}) }; delete k[id]; return { ...p, keys: k } }
    const m = { ...(p.map || {}) }
    if (p.raw) m[id] = defaults[id] ?? null
    else delete m[id]
    return { ...p, map: m }
  }), [change, isKeys, defaults])

  const act = useCallback(async (fn: () => Promise<unknown>, done?: string) => {
    try {
      await fn()
      if (done) say(done)
      await load()
      await refresh()
    } catch (e: any) { say(e?.message || 'That didn’t work') }
  }, [load, refresh, say])

  // A binding as the controller in someone's hands shows it.
  const glyphOf = useCallback((b: Binding | null | undefined, size: 'sm' | 'md' = 'md'): React.ReactNode => {
    if (!b) return <span className="wsc-unset">Not set</span>
    if (rawPad) return <span className="wsc-rawsrc">{describeSource(b, 'generic', true)}</span>
    if ('b' in b) return <PhysGlyph i={b.b} family={family} size={size} />
    if ('h' in b) return <PhysGlyph i={[0, 12, 15, 13, 14][b.d]} family={family} size={size} />
    const dir = [['←', '→'], ['↑', '↓'], ['←', '→'], ['↑', '↓']][b.a]?.[b.d > 0 ? 1 : 0]
    if (b.a > 3 || !dir) return <span className="wsc-rawsrc">{describeSource(b, pad?.style || 'generic', false)}</span>
    return <span className="wsc-stickdir"><StickGlyph right={b.a >= 2} size={size} /><b aria-hidden="true">{dir}</b></span>
  }, [rawPad, family, pad?.style])

  return {
    targets, targetIds, profiles, fitting, inUse, profile, selected, setSelected, pad, isKeys, model, family, rawPad, extraButton, defaults, map, keys, clashes, options,
    labelOf, change, setBinding, setKey, setOption, resetTarget, act, load, fresh, glyphOf, device, consoleId,
  }
}

// ---- the profile line ----

type Scope = 'game' | 'console' | 'all'
const scopeOf = (p: Profile | null): Scope => (!p ? 'console' : p.game ? 'game' : p.console ? 'console' : 'all')

function ProfileBar({ ed, props, say, goProfiles }: { ed: Editor; props: Props; say: (t: string) => void; goProfiles: () => void }) {
  const { profile, fitting, inUse } = ed
  const [open, setOpen] = useState(false)
  const [armed, setArmed] = useState(false)
  useEffect(() => { setArmed(false) }, [profile?.id])
  const scopes: [string, string][] = [...(props.gameId ? [['game', 'This game'] as [string, string]] : []), ['console', `All ${props.consoleName} games`], ['all', 'Every console']]
  const setScope = (s: string) => {
    const patch = { game: s === 'game' ? props.gameId : undefined, console: s === 'all' ? undefined : ed.consoleId }
    void ed.change((p) => ({ ...p, ...patch }))
    say(s === 'game' ? `Used for ${props.gameTitle || 'this game'} only` : s === 'console' ? `Used for every ${props.consoleName} game` : 'Used for every console')
  }
  const choose = (p: Profile | null) => {
    setOpen(false)
    if (!p) {
      if (inUse) void ed.act(() => props.host.setActive(inUse.id, false), 'Using the standard layout')
      ed.setSelected(null)
      return
    }
    ed.setSelected(p.id)
    if (!p.active) void ed.act(() => props.host.setActive(p.id, true), `Using “${p.name}”`)
  }
  const reset = () => {
    if (!profile) { say('This is already the standard layout'); return }
    if (!armed) { setArmed(true); say('Press Reset again to put every button back as it was'); return }
    setArmed(false)
    void ed.change((p) => ({ ...p, map: !ed.isKeys && ed.rawPad ? ed.defaults : {}, keys: {}, options: {} }))
    say('Every button is back as it was')
  }
  return (
    <div className="wsc-profilebar">
      <div className="wsc-profile-pick">
        <span className="wsc-seg-label">Profile</span>
        <button type="button" className="wp-btn wsc-profile-btn" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <span>{profile ? profile.name : 'Standard'}</span>{profile && !profile.active && <small className="wsc-badge">Not in use</small>}<span aria-hidden="true" className="wsc-caret">▾</span>
        </button>
        <button type="button" className="wp-btn" onClick={reset}>{armed ? 'Reset, really' : 'Reset'}</button>
      </div>
      {profile && <Segmented label="Used for" value={scopeOf(profile)} choices={scopes} onChange={setScope} />}
      {!profile && <p className="wp-muted wsc-bar-note">The standard layout. Change anything and it’s kept as your own profile for {props.consoleName} games.</p>}
      {open && (
        <div className="wsc-menu" role="menu" aria-label="Profiles">
          <button type="button" role="menuitemradio" aria-checked={!profile} onClick={() => choose(null)}><b>Standard</b><small>As this console’s buttons are usually set</small></button>
          {fitting.map((p) => (
            <button key={p.id} type="button" role="menuitemradio" aria-checked={profile?.id === p.id} onClick={() => choose(p)}>
              <b>{p.name}</b><small>{describeScope(p, props)}{p.active ? '' : ' · not in use'}</small>
            </button>
          ))}
          <button type="button" role="menuitem" className="wsc-menu-more" onClick={() => { setOpen(false); goProfiles() }}>New, copy, rename, import…</button>
        </div>
      )}
    </div>
  )
}

function describeScope(p: Profile, props: Props): string {
  const where = p.game ? (p.game === props.gameId ? props.gameTitle || 'One game' : p.gameTitle || 'One game') : p.console ? `All ${p.console === props.data.console ? props.consoleName : p.console} games` : 'Every console'
  const who = p.kind === 'keyboard' ? 'Keyboard' : p.controller ? p.controllerName || 'One model of controller' : 'Any controller'
  return `${where} · ${who}`
}

// ---- buttons ----

function ButtonsSection({ ed, props, say, goSticks }: { ed: Editor; props: Props; say: (t: string) => void; goSticks: () => void }) {
  const { targets, map, keys, defaults, isKeys, pad, rawPad, family, clashes, options, labelOf } = ed
  const { input, data, known } = props
  const [listening, setListening] = useState<number | null>(null)
  const [walk, setWalk] = useState<number[]>([])
  const [picking, setPicking] = useState<Callout | null>(null)
  const tv = useMemo(onTV, [])
  const setBindingNow = useRef(ed.setBinding)
  setBindingNow.current = ed.setBinding
  const setKeyNow = useRef(ed.setKey)
  setKeyNow.current = ed.setKey
  const device = ed.device

  // ---- listening for a press ----
  useEffect(() => {
    if (listening === null) return
    const what = labelOf(listening)
    say(`Press what you want for ${what}. Esc or waiting a few seconds leaves it as it is.`)
    const next = () => setWalk((w) => { const [n, ...rest] = w; setListening(n ?? null); return rest })
    const deadline = window.setTimeout(() => { say(walk.length ? `${what} left as it was` : 'Nothing was pressed, so nothing changed'); next() }, LISTEN_MS)
    const cancel = (e: KeyboardEvent) => {
      if (['Escape', 'GoBack', 'BrowserBack', 'Back', 'XF86Back'].includes(e.key) || (tv && e.key === 'Backspace')) {
        e.preventDefault(); e.stopPropagation()
        setWalk([]); setListening(null)
        say('Nothing changed')
        return true
      }
      return false
    }
    if (isKeys) {
      const key = (e: KeyboardEvent) => {
        if (cancel(e)) return
        e.preventDefault(); e.stopPropagation()
        const code = e.code || e.key // remotes often send no code
        void setKeyNow.current(listening, code)
        say(`${what} is now ${describeKey(code)}`)
        next()
      }
      input.setCapturing(true)
      window.addEventListener('keydown', key, true)
      return () => { clearTimeout(deadline); input.setCapturing(false); window.removeEventListener('keydown', key, true) }
    }
    if (typeof device !== 'number') return () => clearTimeout(deadline)
    const get = () => { try { return navigator.getGamepads?.()[device] ?? null } catch { return null } }
    const first = get()
    if (!first) { clearTimeout(deadline); say('This controller isn’t connected any more'); setListening(null); setWalk([]); return }
    const base = snapshot(first)
    const esc = (e: KeyboardEvent) => { cancel(e) }
    input.setCapturing(true)
    holdTvNavigation(true)
    window.addEventListener('keydown', esc, true)
    let finished = false
    // On a timer rather than animation frames, which stop when the tab isn't drawing.
    const iv = window.setInterval(() => {
      const p = get()
      if (!p || finished) return
      noteReleases(p, base)
      const got = findPress(p, base)
      if (!got) return
      const st = standingOf(p, known, navigator.userAgent)
      const b = st.standing === 'unknown' || st.standing === 'standard' ? got : toStandard(got, st.layout, p)
      finished = true
      clearInterval(iv)
      clearTimeout(deadline)
      if (!b) { say('That button can’t be used here'); next(); return }
      void setBindingNow.current(listening, b)
      say(`${what} is now ${'b' in b && !rawPad ? physName(family, b.b) : describeSource(b, pad?.style || 'generic', st.standing === 'unknown')}`)
      // Whatever was pressed mustn't also count as OK when this screen gets it back.
      const wait = window.setInterval(() => {
        const q = get()
        if (q && findPress(q, base)) return
        clearInterval(wait)
        next()
      }, 16)
    }, 16)
    return () => {
      clearInterval(iv)
      clearTimeout(deadline)
      window.removeEventListener('keydown', esc, true)
      input.setCapturing(false)
      holdTvNavigation(false)
    }
  }, [listening, device, isKeys]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- rows light up as the game's buttons are pressed ----
  const rows = useRef(new Map<number, HTMLElement>())
  useEffect(() => {
    let raf = 0
    const lit = new Set<number>()
    const paint = (id: number, on: boolean) => {
      if (on === lit.has(id)) return
      const el = rows.current.get(id)
      if (on) { lit.add(id); el?.setAttribute('data-on', '') } else { lit.delete(id); el?.removeAttribute('data-on') }
    }
    if (isKeys) {
      const of = new Map(Object.entries(keys).map(([k, v]) => [v, Number(k)]))
      const on = (e: KeyboardEvent) => { const id = of.get(e.code); if (id !== undefined) paint(id, true) }
      const off = (e: KeyboardEvent) => { const id = of.get(e.code); if (id !== undefined) paint(id, false) }
      window.addEventListener('keydown', on)
      window.addEventListener('keyup', off)
      return () => { window.removeEventListener('keydown', on); window.removeEventListener('keyup', off); for (const id of [...lit]) paint(id, false) }
    }
    if (typeof device !== 'number') return
    const prev = input.onFrame
    input.onFrame = () => {
      prev?.()
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        const r = input.reading(device)
        if (r) for (let id = 0; id < r.held.length; id++) paint(id, !!r.held[id])
      })
    }
    return () => { input.onFrame = prev; cancelAnimationFrame(raf); for (const id of [...lit]) paint(id, false) }
  }, [input, device, isKeys, keys])

  // ---- the controller's own buttons, and what each does ----
  const same = (a: Binding | null | undefined, b: Binding | null | undefined) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null)
  const physOf = (b: Binding | null | undefined): number | null => (!b ? null : 'b' in b ? b.b : 'h' in b ? [-1, 12, 15, 13, 14][b.d] : null)
  const doing = (m: PadMap, i: number) => targets.filter((t) => physOf(m[t.id]) === i).map((t) => t.id)
  const plain = (label: string) => label.replace(/\s+(up|down|left|right)$/i, '')
  const describeCallout = (c: Callout) => {
    if (c.stick !== undefined) {
      const axes = [c.stick * 2, c.stick * 2 + 1]
      const names = new Set<string>()
      for (const t of targets) { const b = map[t.id]; if (b && 'a' in b && axes.includes(b.a)) names.add(plain(t.label)) }
      if (data.pad.aim && (options.pointerStick || 'right') === (c.stick ? 'right' : 'left')) { names.clear(); names.add('Pointer') }
      if (c.stick === 0 && options.stickDpad) names.add('D-pad')
      return { text: names.size ? [...names].join(' · ') : 'Not used', none: !names.size }
    }
    const now = doing(map, c.phys!), was = doing(defaults, c.phys!)
    const changed = now.length !== was.length || now.some((x) => !was.includes(x))
    return { text: now.length ? now.map(labelOf).join(' · ') : 'Not used', none: !now.length, changed }
  }
  // A button of the controller made to do one of the game's: whatever that did before moves to
  // what pressed this one, so nothing is left out.
  const assign = (i: number, t: number | null) => {
    const src: Binding = { b: i }
    const had = doing(map, i)
    setPicking(null)
    if (t === null) {
      void ed.change((p) => { const m = { ...(p.map || {}) }; for (const o of had) m[o] = null; return { ...p, map: m } })
      say(`${physName(family, i)} does nothing in this game now`)
      return
    }
    const prev = map[t] ?? null
    void ed.change((p) => { const m = { ...(p.map || {}), [t]: src }; for (const o of had) if (o !== t) m[o] = prev; return { ...p, map: m } })
    const moved = had.filter((o) => o !== t)
    const prevPhys = physOf(prev)
    say(`${physName(family, i)} is now ${labelOf(t)}` + (moved.length ? `, and ${moved.map(labelOf).join(', ')} moved to ${prevPhys !== null ? physName(family, prevPhys) : 'nothing'}` : ''))
  }

  const showPicture = !isKeys && !!pad && !rawPad
  const turboable = (t: Target) => t.id < 16 && (t.group === 'face' || t.group === 'shoulder')
  const anyTurbo = !!options.turbo?.length
  const toggleTurbo = (id: number) => {
    const on = !!options.turbo?.includes(id)
    void ed.setOption({ turbo: on ? (options.turbo || []).filter((x) => x !== id) : [...(options.turbo || []), id] })
    say(on ? `${labelOf(id)} no longer repeats` : `${labelOf(id)} repeats while held`)
  }

  return (
    <div className="wsc-buttons">
      {showPicture && (
        <Card className="wsc-picture-card">
          <PadPicture family={family} extraButton={ed.extraButton} device={typeof device === 'number' ? device : null} describe={describeCallout} picking={picking?.key ?? null}
            onPick={(c) => { if (c.stick !== undefined) goSticks(); else setPicking(c) }} />
          <p className="wp-muted wsc-caption">Choose a button to change what it does. Press buttons on your controller to see them light up.</p>
        </Card>
      )}
      {!isKeys && !pad && <Card><p className="wps-para">{noPadWords()}</p></Card>}
      {rawPad && <Card><p className="wps-para wps-warn">This browser doesn’t say what this controller’s buttons are, so they may be in the wrong places. Choose “Set them all” to be sure.</p></Card>}

      {picking && picking.phys !== undefined && (
        <PhysPicker i={picking.phys} family={family} targets={targets} current={doing(map, picking.phys)} labelOf={labelOf} onPick={(t) => assign(picking.phys!, t)} onClose={() => setPicking(null)} />
      )}

      <Card title={`${props.consoleName} buttons`} actions={
        <div className="wsc-card-actions">
          <button type="button" className="wp-btn" disabled={(!isKeys && !pad) || listening !== null} onClick={() => { const [n, ...rest] = targets.map((t) => t.id); setWalk(rest); setListening(n) }}>Set them all</button>
          {listening !== null && <button type="button" className="wp-btn" onClick={() => { setWalk([]); setListening(null); say('Nothing changed') }}>Stop</button>}
        </div>
      }>
        <div className="wsc-groups">
          {GROUPS.map(([g, title]) => {
            const list = targets.filter((t) => t.group === g)
            if (!list.length) return null
            return (
              <div key={g} className="wsc-group">
                <h4>{title}</h4>
                <ul className="wsc-rows">
                  {list.map((t) => {
                    const clash = clashes.get(t.id)
                    const changed = isKeys ? !!ed.profile?.keys && t.id in (ed.profile.keys || {}) : !same(map[t.id], defaults[t.id])
                    const turbo = !!options.turbo?.includes(t.id)
                    return (
                      <li key={t.id} ref={(el) => { if (el) rows.current.set(t.id, el); else rows.current.delete(t.id) }} className={'wsc-row' + (listening === t.id ? ' listening' : '')}>
                        <span className="wsc-row-name">{t.label}{changed && <small className="wsc-dot" title="Changed from the standard layout" aria-label="changed" />}</span>
                        <span className="wsc-row-src">
                          {listening === t.id ? <span className="wsc-waiting">Press a {isKeys ? 'key' : 'button'}…</span> : isKeys ? <KeyGlyph code={keys[t.id]} /> : ed.glyphOf(map[t.id])}
                          {clash && <small className="wsc-clash">also {clash.map(labelOf).join(', ')}</small>}
                        </span>
                        <span className="wsc-row-actions">
                          {!isKeys && turboable(t) && <button type="button" className={'wsc-chip' + (turbo ? ' on' : '')} aria-pressed={turbo} onClick={() => toggleTurbo(t.id)} title="Repeats while held">Turbo</button>}
                          <button type="button" className={'wp-btn wps-small' + (listening === t.id ? ' primary' : '')} aria-label={`Change ${t.label}`} disabled={!isKeys && !pad} onClick={() => setListening(listening === t.id ? null : t.id)}>{listening === t.id ? 'Cancel' : 'Change'}</button>
                          <button type="button" className="wp-btn wps-small wsc-icon" aria-label={`Put ${t.label} back`} title="Put it back" disabled={!changed} onClick={() => { void ed.resetTarget(t.id); say(`${t.label} is back as it was`) }}>↺</button>
                        </span>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )
          })}
        </div>
        {anyTurbo && <Stepper label="Turbo speed" hint="How fast turbo buttons repeat" value={options.turboHz ?? 12} text={`${options.turboHz ?? 12} a second`} min={2} max={30} step={2} onChange={(v) => void ed.setOption({ turboHz: v })} />}
      </Card>
    </div>
  )
}

// What one of the controller's buttons should do: the game's buttons, grouped, or nothing.
function PhysPicker({ i, family, targets, current, labelOf, onPick, onClose }: { i: number; family: PadFamily; targets: Target[]; current: number[]; labelOf: (id: number) => string; onPick: (t: number | null) => void; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('[aria-checked=true], button')?.focus()
    const esc = (e: KeyboardEvent) => { if (['Escape', 'GoBack', 'BrowserBack'].includes(e.key)) { e.preventDefault(); e.stopPropagation(); onClose() } }
    window.addEventListener('keydown', esc, true)
    return () => window.removeEventListener('keydown', esc, true)
  }, [i]) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <div className="wsc-scrim" onClick={onClose}>
      <div ref={ref} className="wsc-picker" role="dialog" aria-label={`What ${physName(family, i)} does`} onClick={(e) => e.stopPropagation()}>
        <header className="wsc-picker-head">
          <PhysGlyph i={i} family={family} size="lg" />
          <div><h3>{physName(family, i)}</h3><p className="wp-muted">Now: {current.length ? current.map(labelOf).join(', ') : 'nothing'}</p></div>
          <button type="button" className="wp-btn" onClick={onClose}>Cancel</button>
        </header>
        {GROUPS.map(([g, title]) => {
          const list = targets.filter((t) => t.group === g)
          if (!list.length) return null
          return (
            <div key={g} className="wsc-picker-group">
              <h4>{title}</h4>
              <div className="wsc-picker-grid" role="radiogroup" aria-label={title}>
                {list.map((t) => <button key={t.id} type="button" role="radio" aria-checked={current.includes(t.id)} onClick={() => onPick(t.id)}>{t.label}</button>)}
              </div>
            </div>
          )
        })}
        <div className="wsc-picker-group">
          <div className="wsc-picker-grid"><button type="button" role="radio" aria-checked={!current.length} onClick={() => onPick(null)}>Nothing</button></div>
        </div>
      </div>
    </div>
  )
}

// ---- sticks and triggers ----

function SticksSection({ ed, props, say }: { ed: Editor; props: Props; say: (t: string) => void }) {
  const { options, pad, family } = ed
  const device = ed.device
  const dz = options.deadzone ?? 0.15, thr = options.threshold ?? 0.5
  const stickRefs = useRef<(SVGCircleElement | null)[]>([null, null])
  const trigRefs = useRef<(HTMLDivElement | null)[]>([null, null])
  const [cal, setCal] = useState<null | { rest: number[]; low: number[]; high: number[] }>(null)
  const calRef = useRef(cal)
  calRef.current = cal

  // The sticks and triggers as they are now, drawn sixty times a second without React.
  useEffect(() => {
    if (typeof device !== 'number') return
    let raf = 0
    const loop = () => {
      raf = requestAnimationFrame(loop)
      let p: Gamepad | null = null
      try { p = navigator.getGamepads?.()[device] ?? null } catch { /* blocked */ }
      if (!p) return
      for (let n = 0; n < 2; n++) {
        const el = stickRefs.current[n]
        if (!el) continue
        const x = p.axes[n * 2] ?? 0, y = p.axes[n * 2 + 1] ?? 0
        el.setAttribute('cx', String(50 + x * 40)); el.setAttribute('cy', String(50 + y * 40))
        el.toggleAttribute('data-on', Math.hypot(x, y) > dz)
      }
      for (let n = 0; n < 2; n++) {
        const el = trigRefs.current[n]
        if (!el) continue
        const v = p.buttons[6 + n]?.value ?? 0
        el.style.setProperty('--v', String(v))
        el.toggleAttribute('data-on', v > thr)
      }
      const c = calRef.current
      if (c) setCal({ ...c, low: c.low.map((v, i) => Math.min(v, p!.axes[i] ?? v)), high: c.high.map((v, i) => Math.max(v, p!.axes[i] ?? v)) })
    }
    raf = requestAnimationFrame(loop)
    return () => cancelAnimationFrame(raf)
  }, [device, dz, thr])

  const startCal = () => {
    const p = typeof device === 'number' ? navigator.getGamepads?.()[device] : null
    if (!p) return
    const rest = Array.from(p.axes)
    setCal({ rest, low: [...rest], high: [...rest] })
    say('Move each stick all the way round a few times, then press Finish. Keep your hands off the triggers.')
  }
  const finishCal = () => {
    if (!cal) return
    const out: Record<number, [number, number, number]> = {}
    cal.rest.forEach((c, i) => {
      if (Math.abs(c) > 0.6) return // a trigger or a hat, not a stick
      const low = cal.low[i], high = cal.high[i]
      if (high - c > 0.3 && c - low > 0.3) out[i] = [low, c, high]
    })
    setCal(null)
    if (!Object.keys(out).length) { say('Nothing moved far enough, so nothing changed'); return }
    void ed.setOption({ calibration: out })
    say(`Calibrated ${Object.keys(out).length} axes`)
  }

  if (!pad) return <Card><p className="wps-para">{noPadWords()}</p></Card>
  const r = dz * 40
  return (
    <div className="wsc-stack">
      <Card title="Sticks">
        <div className="wsc-sticks">
          {[0, 1].map((n) => (
            <figure key={n} className="wsc-stickview">
              <svg viewBox="0 0 100 100" aria-hidden="true">
                <circle className="wsc-sv-ring" cx="50" cy="50" r="40" />
                <circle className="wsc-sv-dead" cx="50" cy="50" r={r} />
                <path className="wsc-sv-axis" d="M10 50h80M50 10v80" />
                <circle ref={(el) => { stickRefs.current[n] = el }} className="wsc-sv-dot" cx="50" cy="50" r="5" />
              </svg>
              <figcaption><StickGlyph right={n === 1} size="sm" /> {n ? 'Right stick' : 'Left stick'}</figcaption>
            </figure>
          ))}
        </div>
        <Stepper label="Dead zone" hint="How far a stick moves before it counts: the shaded middle" value={Math.round(dz * 100)} text={pct(dz)} min={0} max={60} step={5} onChange={(v) => void ed.setOption({ deadzone: v / 100 })} />
        <Toggle label="Left stick works the D-pad too" on={options.stickDpad ?? !props.data.pad.sticks} onChange={(on) => void ed.setOption({ stickDpad: on })} />
        <div className="wsc-card-actions">
          {cal ? <button type="button" className="wp-btn primary" onClick={finishCal}>Finish calibrating</button> : <button type="button" className="wp-btn" onClick={startCal}>Calibrate the sticks</button>}
          {options.calibration && !cal && <button type="button" className="wp-btn" onClick={() => { void ed.setOption({ calibration: undefined }); say('Calibration forgotten') }}>Forget calibration</button>}
        </div>
      </Card>
      <Card title="Triggers">
        <div className="wsc-triggers">
          {[0, 1].map((n) => (
            <div key={n} className="wsc-trig">
              <PhysGlyph i={6 + n} family={family} size="sm" />
              <div ref={(el) => { trigRefs.current[n] = el }} className="wsc-trig-bar" style={{ '--thr': String(thr) } as React.CSSProperties}><span /></div>
            </div>
          ))}
        </div>
        <Stepper label="Press point" hint="How far a trigger goes down to count as pressed: the line" value={Math.round(thr * 100)} text={pct(thr)} min={10} max={90} step={5} onChange={(v) => void ed.setOption({ threshold: v / 100 })} />
      </Card>
    </div>
  )
}

// ---- the Wii Remote's pointer ----

function PointerSection({ ed }: { ed: Editor }) {
  const { options } = ed
  const stick = options.pointerStick || 'right'
  const speed = options.pointerSpeed ?? 1
  return (
    <div className="wsc-stack">
      <Card title="Aiming with a stick">
        <p className="wp-muted wps-para">The pointer follows your mouse or finger over the picture. A stick can move it too: the stick then aims and does nothing else.</p>
        <Segmented label="Stick" value={stick} choices={[['right', 'Right stick'], ['left', 'Left stick'], ['off', 'Neither']]} onChange={(v) => void ed.setOption({ pointerStick: v as 'left' | 'right' | 'off' })} />
        {stick !== 'off' && <Stepper label="Speed" hint="Pushed all the way, how fast it crosses the picture" value={Math.round(speed * 100)} text={`${speed.toFixed(2).replace(/\.?0+$/, '')}×`} min={25} max={400} step={25} onChange={(v) => void ed.setOption({ pointerSpeed: v / 100 })} />}
      </Card>
      <Card title="The mouse">
        <p className="wp-muted wps-para">A click is the Wii Remote’s A. To hide the crosshair over the game, turn it off in Settings, Picture, Pointer.</p>
      </Card>
    </div>
  )
}

// ---- vibration ----

function VibrationSection({ ed, props }: { ed: Editor; props: Props }) {
  const { pad, options } = ed
  if (!pad) return <Card><p className="wps-para">{noPadWords()}</p></Card>
  if (!pad.rumble) {
    return <Card title="Vibration"><p className="wps-para">This browser doesn’t let the page make {pad.name} vibrate, so games can’t either. Chrome and Edge on a computer usually can.</p></Card>
  }
  return (
    <Card title="Vibration">
      <Toggle label="Vibrate when the game says to" on={!options.noRumble} onChange={(on) => void ed.setOption({ noRumble: !on })} />
      <div className="wsc-card-actions"><button type="button" className="wp-btn" onClick={() => props.input.testRumble(pad.index)}>Try it</button></div>
    </Card>
  )
}

// ---- players ----

function PlayersSection({ props, pads, say }: { props: Props; pads: PadInfo[]; say: (t: string) => void }) {
  const { input, devices, setDevice } = props
  const [, tick] = useState(0)
  useEffect(() => { const iv = setInterval(() => tick((x) => x + 1), 1000); return () => clearInterval(iv) }, [])
  const players = Array.from({ length: MAX_PLAYERS }, (_, i) => i)
  const seat = (label: string, cur: number, set: (n: number) => void) => (
    <div className="wsc-seg" role="radiogroup" aria-label={`${label}: which player`}>
      {players.map((n) => <button key={n} type="button" role="radio" aria-checked={cur === n} aria-label={`Player ${n + 1}`} className={cur === n ? 'on' : ''} onClick={() => set(n)}>{n + 1}</button>)}
    </div>
  )
  return (
    <div className="wsc-stack">
      <Card title="Who plays as who">
        <p className="wp-muted wps-para">Choosing a player that’s taken swaps the two.</p>
        <ul className="wsc-players">
          {pads.map((p) => (
            <li key={p.index + p.id}>
              <DeviceIcon kind="pad" />
              <div className="wp-grow"><b>{p.name}</b><small className="wp-muted">{p.profile ? `Profile: ${p.profile.name}` : 'Standard layout'}</small></div>
              {seat(p.name, p.port, (n) => input.setPadPort(p.index, n))}
              {p.rumble && <button type="button" className="wp-btn wps-small" onClick={() => input.testRumble(p.index)}>Which one?</button>}
            </li>
          ))}
          {pads.length === 0 && <li className="wp-muted" role="status">{noPadWords()}</li>}
          <li><DeviceIcon kind="keyboard" /><div className="wp-grow"><b>Keyboard</b></div>{seat('Keyboard', input.keyboardPlayer, (n) => input.setKeyboardPort(n))}</li>
          <li><DeviceIcon kind="touch" /><div className="wp-grow"><b>Touch controls</b></div>{seat('Touch controls', input.touchPlayer, (n) => input.setTouchPort(n))}</li>
        </ul>
      </Card>
      {devices && devices.some((d) => d.types.length > 1) && (
        <Card title="What each player has plugged in">
          <p className="wp-muted wps-para">This game’s emulator offers a choice of controller for each player.</p>
          <ul className="wsc-players">
            {devices.filter((d) => d.types.length > 1).map((d) => (
              <li key={d.port}>
                <div className="wp-grow"><b>Player {d.port + 1}</b></div>
                <div className="wsc-seg" role="radiogroup" aria-label={`Player ${d.port + 1}: kind of controller`}>
                  {d.types.map((t) => <button key={t.id} type="button" role="radio" aria-checked={d.current === t.id} className={d.current === t.id ? 'on' : ''}
                    onClick={() => void setDevice?.(d.port, t.id).catch((e) => say(e?.message || 'That didn’t work'))}>{t.name}</button>)}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  )
}

// ---- profiles ----

function ProfilesSection({ ed, props, say }: { ed: Editor; props: Props; say: (t: string) => void }) {
  const { host } = props
  const { profiles, profile, isKeys, pad, model } = ed
  const [renaming, setRenaming] = useState<number | null>(null)
  const [name, setName] = useState('')
  const [armed, setArmed] = useState<number | null>(null)
  const [problems, setProblems] = useState<string[]>([])
  const file = useRef<HTMLInputElement>(null)
  const tv = useMemo(onTV, [])
  const mine = (profiles || []).filter((p) => p.kind === (isKeys ? 'keyboard' : 'gamepad'))

  const exportProfiles = async (ids?: number[]) => {
    try {
      const text = await host.exportFile(ids)
      const a = document.createElement('a')
      a.href = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
      a.download = 'wittyplay-controller-profiles.json'
      a.click()
      setTimeout(() => URL.revokeObjectURL(a.href), 5000)
      say('Saved to your downloads')
    } catch (e: any) { say(e?.message || 'That didn’t work') }
  }
  const importProfiles = async (f: File | undefined) => {
    if (!f) return
    if (f.size > MAX_IMPORT) { say('This file is too large to be a profile file'); return }
    try {
      const r = await host.importFile(await f.text())
      setProblems([...r.notes, ...r.rejected.map((x) => `“${x.name || 'A profile'}” wasn’t added: ${x.error}.`)])
      say(`Added ${r.imported.length} ${r.imported.length === 1 ? 'profile' : 'profiles'}`)
      await ed.load()
      await props.refresh()
    } catch (e: any) { say(e?.message || 'That file couldn’t be read') }
  }
  const save = (p: Profile, patch: Partial<Profile>) => ed.act(() => host.update(p.id, { ...p, ...patch }))

  return (
    <div className="wsc-stack">
      <Card title={isKeys ? 'Keyboard profiles' : 'Controller profiles'} actions={
        <div className="wsc-card-actions">
          <button type="button" className="wp-btn primary" onClick={() => void ed.act(async () => { const c = await host.create(ed.fresh()); ed.setSelected(c.id) }, 'New profile made')}>New profile</button>
          {!tv && <button type="button" className="wp-btn" onClick={() => file.current?.click()}>Import…</button>}
          {!tv && <button type="button" className="wp-btn" disabled={!mine.length} onClick={() => void exportProfiles(mine.map((p) => p.id))}>Export all</button>}
          <input ref={file} type="file" accept=".json,application/json" hidden aria-label="Import a profile file" onChange={(e) => { void importProfiles(e.target.files?.[0]); e.target.value = '' }} />
        </div>
      }>
        <p className="wp-muted wps-para">When more than one could be used, the one for this game wins, then the one for this console, then one for every console.</p>
        {problems.length > 0 && <ul className="wps-problems" role="alert">{problems.map((p, i) => <li key={i}>{p}</li>)}</ul>}
        <ul className="wsc-profiles">
          {mine.map((p) => (
            <li key={p.id} className={profile?.id === p.id ? 'current' : ''}>
              {renaming === p.id ? (
                <form className="wsc-rename" onSubmit={(e) => { e.preventDefault(); const n = name.trim(); if (n) void save(p, { name: n.slice(0, 60) }); setRenaming(null) }}>
                  <input className="wp-select" autoFocus maxLength={60} value={name} onChange={(e) => setName(e.target.value)} aria-label="Name" />
                  <button type="submit" className="wp-btn primary wps-small">Save</button>
                  <button type="button" className="wp-btn wps-small" onClick={() => setRenaming(null)}>Cancel</button>
                </form>
              ) : (
                <div className="wsc-profile-text"><b>{p.name}</b><small className="wp-muted">{describeScope(p, props)}</small></div>
              )}
              <div className="wsc-profile-actions">
                <Toggle label="In use" on={p.active} onChange={(on) => void ed.act(() => host.setActive(p.id, on), on ? `Using “${p.name}”` : 'No longer using it')} />
                {!isKeys && !p.raw && pad && <Toggle label={`Only ${pad.name}`} on={!!p.controller} onChange={(on) => void save(p, { controller: on ? model : '', controllerName: on ? pad.name : undefined })} />}
                <button type="button" className="wp-btn wps-small" onClick={() => { setRenaming(p.id); setName(p.name) }}>Rename</button>
                <button type="button" className="wp-btn wps-small" onClick={() => void ed.act(async () => { const c = await host.copy(p.id); ed.setSelected(c.id) }, 'Copied')}>Copy</button>
                {!tv && <button type="button" className="wp-btn wps-small" onClick={() => void exportProfiles([p.id])}>Export</button>}
                <button type="button" className="wp-btn wps-small" onClick={() => { if (armed !== p.id) { setArmed(p.id); say(`Press Delete again to remove “${p.name}”`); return } setArmed(null); void ed.act(() => host.remove(p.id), 'Deleted') }}>{armed === p.id ? 'Delete, really' : 'Delete'}</button>
              </div>
            </li>
          ))}
          {!mine.length && <li className="wp-muted">No profiles yet. Change any button and one is made for you.</li>}
        </ul>
      </Card>
    </div>
  )
}

// ---- the background ----

function BackgroundSection({ host, bg, setBg, refresh, say, data }: { host: ProfileHost; bg: Background; setBg: (b: Background) => void; refresh: () => Promise<void>; say: (t: string) => void; data: ControlsData }) {
  const file = useRef<HTMLInputElement>(null)
  const hot = useRef(new Map<number, HTMLButtonElement>())
  const [busy, setBusy] = useState(false)
  const timer = useRef(0)
  const tv = useMemo(onTV, [])
  useEffect(() => () => clearTimeout(timer.current), [])
  const change = (patch: Partial<Background>) => {
    const next = { ...bg, ...patch }
    setBg(next)
    clearTimeout(timer.current)
    timer.current = window.setTimeout(() => { host.setBackground(next).then(() => refresh()).catch((e) => say(e?.message || 'That couldn’t be saved')) }, 300)
  }
  const upload = async (f: File | undefined) => {
    if (!f) return
    if (!['image/png', 'image/jpeg', 'image/webp'].includes(f.type)) { say('Use a PNG, JPEG or WebP picture'); return }
    if (f.size > MAX_FILE) { say('The picture must be 10 MB or smaller'); return }
    setBusy(true)
    try {
      setBg(await host.uploadBackground(f))
      await refresh()
      say('Picture added')
    } catch (e: any) { say(e?.message || 'That picture couldn’t be used') } finally { setBusy(false) }
  }
  const remove = async () => {
    try { setBg(await host.removeBackground()); await refresh(); say('Picture removed') } catch (e: any) { say(e?.message || 'That didn’t work') }
  }
  return (
    <div className="wsc-stack">
      <Card title="Your own background">
        <p className="wp-muted wps-para">A picture of your own behind the touch controls and this screen’s examples. It’s kept on the server for your account, and nobody else can see it. PNG, JPEG or WebP, up to 10 MB.</p>
        <div className="wps-stage" inert>
          {bg.version > 0 && (
            <>
              <img className="wps-bg" src={host.backgroundUrl(bg.version)} alt="" style={{ objectFit: bg.fit, objectPosition: `${bg.x}% ${bg.y}%` }} />
              <div className="wps-dim" style={{ opacity: bg.dim / 100 }} />
            </>
          )}
          <ControllerPicture art={data.pad.art} targets={data.targets} active={null} describe={() => 'shown as an example'} refs={hot} onPick={() => {}} />
        </div>
        <div className="wsc-card-actions">
          {!tv && <button type="button" className="wp-btn primary" disabled={busy} onClick={() => file.current?.click()}>{bg.version ? 'Choose another…' : 'Choose a picture…'}</button>}
          {tv && !bg.version && <p className="wp-muted wps-para">Add a picture from a computer or phone, and it follows you here.</p>}
          {bg.version > 0 && <button type="button" className="wp-btn" onClick={remove}>Remove it</button>}
          <input ref={file} type="file" accept="image/png,image/jpeg,image/webp" hidden aria-label="Choose a picture" onChange={(e) => { void upload(e.target.files?.[0]); e.target.value = '' }} />
        </div>
        {bg.version > 0 && (
          <div className="wps-group">
            <Toggle label="Fill the whole area" hint="Otherwise all of it shows" on={bg.fit === 'cover'} onChange={(on) => change({ fit: on ? 'cover' : 'contain' })} />
            <Stepper label="Left to right" value={bg.x} text={`${bg.x}%`} min={0} max={100} step={10} onChange={(x) => change({ x })} />
            <Stepper label="Top to bottom" value={bg.y} text={`${bg.y}%`} min={0} max={100} step={10} onChange={(y) => change({ y })} />
            <Stepper label="Dimming" value={bg.dim} text={`${bg.dim}%`} min={0} max={90} step={10} onChange={(dim) => change({ dim })} />
          </div>
        )}
      </Card>
    </div>
  )
}
