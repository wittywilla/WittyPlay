// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// Refresh game details, for an administrator: one click that looks every game in a games library, or one game, up again from the
// start (POST /libraries/{id}/details/refresh, POST /games/{id}/metadata/refresh): titles in their own script and in English, box
// art, logos and details. What people typed or chose stays. The work goes on in the background, as the server's task "Finding out
// about games"; a library's button says how far it has got. The apps run the same commands (wittyplay.library.refresh and
// wittyplay.game.refresh) from the rows of the SDUI source wittyplay.refresh, with the same words.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '@wms/app/api'
import { IconRefresh } from '@wms/app/icons'
import { toast } from '@wms/app/state'
import { ask } from '@wms/app/ui/index'

export type RefreshState = { games: number; waiting: number; later: number; pictures: number; running: boolean; native: number; english: number; logos: number; since: number }

// refreshWords is the state in a few words, as the apps' row says it (api/gamerefresh.go refreshWords).
export function refreshWords(s: RefreshState): string {
  if (s.waiting > 0) return `Looking up ${(s.games - s.waiting - s.later).toLocaleString()} of ${s.games.toLocaleString()}`
  if (s.pictures > 0) return `Downloading pictures: ${s.pictures.toLocaleString()} to go`
  if (s.later > 0) return `${s.later.toLocaleString()} waiting for a source`
  return 'Up to date'
}

const confirmWords = 'It runs in the background, and a big library takes a while. Titles in their own script and in English, box art, logos and details are fetched again. What people typed or chose stays.'

// The library's button, with how far a refresh has got beside it while one goes on.
export function RefreshLibraryDetails({ libraryId, className = 'btn ghost' }: { libraryId: number; className?: string }) {
  const [state, setState] = useState<RefreshState | null>(null)
  const [busy, setBusy] = useState(false)
  const timer = useRef(0)
  const read = useCallback(async () => {
    window.clearTimeout(timer.current)
    try {
      const s = await api.get<RefreshState>(`/modules/wittyplay/libraries/${libraryId}/details/refresh`)
      setState(s)
      if (s.running && s.since) timer.current = window.setTimeout(() => { void read() }, 3000)
    } catch { setState(null) }
  }, [libraryId])
  useEffect(() => { void read(); return () => window.clearTimeout(timer.current) }, [read])
  const start = async () => {
    if (!await ask({ title: 'Refresh every game’s details?', body: confirmWords, confirm: 'Refresh game details' })) return
    setBusy(true)
    try {
      const r = await api.post<{ games: number }>(`/modules/wittyplay/libraries/${libraryId}/details/refresh`)
      toast(`Looking up ${r.games.toLocaleString()} ${r.games === 1 ? 'game' : 'games'} again`)
      await read()
    } catch (e: any) { toast(e.message) } finally { setBusy(false) }
  }
  if (state && state.games === 0) return null
  const going = !!state?.since && state.running
  return (
    <>
      <button type="button" className={className} onClick={() => void start()} disabled={busy} aria-describedby={going ? `wpl-refresh-${libraryId}` : undefined}>
        <IconRefresh /> Refresh game details
      </button>
      {going && <span id={`wpl-refresh-${libraryId}`} className="muted" role="status">{refreshWords(state!)}</span>}
    </>
  )
}

// One game's button: the same refresh, for that game.
export function RefreshGameDetails({ id, onChanged, className = 'btn sm ghost' }: { id: number; onChanged?: () => void; className?: string }) {
  const [busy, setBusy] = useState(false)
  const start = async () => {
    setBusy(true)
    try { await api.post(`/modules/wittyplay/games/${id}/metadata/refresh`); toast('Looking this game up again'); onChanged?.() } catch (e: any) { toast(e.message) } finally { setBusy(false) }
  }
  return <button type="button" className={className} onClick={() => void start()} disabled={busy}><IconRefresh /> Refresh game details</button>
}
