// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// Console photos from Wikimedia: an administrator's opt-in, off until turned on, with the notice saying what is fetched, from where
// and under which licences (GET/PUT /games/console-photos; the words are WittyPlay's, ui.ConsolePhotoNotice, the same the apps show
// on WittyPlay's Settings and first-run step). Turning it off deletes every photo downloaded. In WittyPlay's Settings and the setup
// wizard's Games step.
import { useEffect, useId, useState } from 'react'
import { api } from '@wms/app/api'
import { useApp } from '@wms/app/state'
import { ask, Notice, Switch } from '@wms/app/ui/index'
import './settings.css'

type ConsolePhotosState = { enabled: boolean; notice: string; wikidata: boolean }

export function ConsolePhotosSetting({ heading = 'h2' }: { heading?: 'h2' | 'h3' }) {
  const { user } = useApp()
  const id = useId()
  const [s, setS] = useState<ConsolePhotosState | null>(null)
  const [busy, setBusy] = useState(false)
  const [said, setSaid] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => { api.get<ConsolePhotosState>('/modules/wittyplay/games/console-photos').then(setS).catch((e) => setErr(e.message)) }, [])
  if (!user?.isAdmin) return null
  const put = async (enabled: boolean) => {
    if (!enabled && !await ask({ title: 'Turn off console photos?', body: 'Every console photo downloaded to this server is deleted. You can turn them on again later.', confirm: 'Turn off', danger: true })) return
    setBusy(true); setErr(''); setSaid('')
    try {
      setS(await api.put<ConsolePhotosState>('/modules/wittyplay/games/console-photos', { enabled }))
      setSaid(enabled ? 'Console photos are on. Each is downloaded the first time the Consoles page shows it.' : 'Console photos are off, and the ones downloaded are deleted.')
    } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  const H = heading
  return (
    <section className="wplay-photos" id="console-photos" aria-labelledby={id}>
      <H id={id}>Console photos from Wikimedia</H>
      {s && <Notice tone="info" title="What this fetches, from where, and the licence">{s.notice}</Notice>}
      <div className="wplay-photos-switch">
        <span id={id + 's'}>Download console photos from Wikimedia</span>
        <Switch checked={!!s?.enabled} disabled={busy || !s} labelledBy={id + 's'} onChange={(on) => void put(on)} />
      </div>
      {s?.enabled && !s.wikidata && <p className="muted">Console photos are found through Wikidata, which is off in Settings, Game metadata, so none are fetched until it’s on again.</p>}
      {said && <p role="status">{said}</p>}
      {err && <p className="error" role="alert">{err}</p>}
    </section>
  )
}

export default function ConsolePhotos() {
  return <ConsolePhotosSetting />
}
