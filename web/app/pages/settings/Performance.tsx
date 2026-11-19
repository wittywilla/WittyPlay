// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// The performance profile: one standard for what every emulator starts with, chosen for how strong this server is
// (GET/PUT /gaming/profiles). Settings set by hand still win, and one console's core is changed on top of it in My game
// settings (Everyone, that console). In the setup wizard's Games step (as cards) and WittyPlay's Consoles & cores settings.
import { useEffect, useId, useState } from 'react'
import { api } from '@wms/app/api'
import { useApp } from '@wms/app/state'
import { Link } from 'react-router-dom'
import { ChoiceCards } from '@wms/app/pages/setup/parts'

type Profile = { id: string; label: string; for: string; examples: string; does: string; settings: Record<string, string> }
export type ProfilesState = { chosen: string; inUse: string; recommended: string; why: string; profiles: Profile[] }

const path = '/modules/wittyplay/gaming/profiles'

export function PerformanceSetting({ cards = false, onChange }: { cards?: boolean; onChange?: (s: ProfilesState) => void }) {
  const { user } = useApp()
  const id = useId()
  const [s, setS] = useState<ProfilesState | null>(null)
  const [err, setErr] = useState('')
  const [said, setSaid] = useState('')
  useEffect(() => { api.get<ProfilesState>(path).then((r) => { setS(r); onChange?.(r) }).catch((e) => setErr(e.message)) }, [])
  if (!user?.isAdmin) return null
  const choose = async (p: string) => {
    setErr(''); setSaid('')
    try {
      const now = await api.put<ProfilesState>(`${path}/${p}`)
      setS(now)
      onChange?.(now)
      setSaid(`${now.profiles.find((x) => x.id === now.inUse)?.label ?? 'It'} is what every emulator starts with now.`)
    } catch (e: any) { setErr(e.message) }
  }
  const rec = s?.profiles.find((p) => p.id === s.recommended)
  const value = s ? (s.chosen || s.inUse) : ''
  const Heading = cards ? 'h2' : 'h3'
  return (
    <section className="wplay-perf" aria-labelledby={id}>
      <Heading id={id} className={cards ? 'oobe-sub' : undefined}>How strong is this server?</Heading>
      <p className="muted oobe-hint">Every emulator starts from this one standard: its resolution, run-ahead, rewind, picture quality and how many games play at once. Set it first. To change one console’s core on top of it, use <Link to="/settings/games">My game settings</Link> afterwards: choose <b>Everyone</b> and that console. Anything set by hand below still wins.</p>
      {s && rec && <p className="muted oobe-hint">Suggested for this server: <b>{rec.label}</b>. {s.why}</p>}
      {s && (cards
        ? <ChoiceCards label="Performance profile" value={value} onChange={(v) => void choose(v)} small
            cards={s.profiles.map((p) => ({ value: p.id, title: p.label + (p.id === s.recommended ? ' (suggested)' : ''), text: `${p.for}, such as ${p.examples}. ${p.does}` }))} />
        : <div className="set-form gaming-form">
            <label>Performance profile
              <select className="select" value={value} onChange={(e) => void choose(e.target.value)}>
                {s.profiles.map((p) => <option key={p.id} value={p.id}>{p.label}{p.id === s.recommended ? ' (suggested)' : ''}: {p.for.toLowerCase()}</option>)}
              </select>
              {(() => { const p = s.profiles.find((x) => x.id === value); return p && <small className="muted">Such as {p.examples}. {p.does}</small> })()}
            </label>
          </div>)}
      {said && <p role="status">{said}</p>}
      {err && <p className="error" role="alert">{err}</p>}
    </section>
  )
}
