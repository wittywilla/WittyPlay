// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useRef, useState } from 'react'
import { api } from '@wms/app/api'

// The games libraries being scanned now, and what each has found so far (GET /games/scanning). On a big combined
// folder WittyPlay sorts files for minutes before the server keeps any game: meanwhile a page with no games says
// they're being found, and how many, never that there's no games library.

export type GamesFinding = {
  library: number
  name: string
  found: number // games in the files sorted so far
  consoles: Record<string, number> // of those, how many for each console
  files: number
  total: number
}

// Asked again every 2 seconds while a scan goes on, and every 15 while none does (one may start).
const BUSY_MS = 2000
const IDLE_MS = 15000

// useGamesScan: the scans going on now, [] when none. onEnd runs when the last one ends, so the page can read what
// the server now holds.
export function useGamesScan(onEnd?: () => void): GamesFinding[] {
  const [list, setList] = useState<GamesFinding[]>([])
  const end = useRef(onEnd)
  end.current = onEnd
  useEffect(() => {
    let live = true
    let timer = 0
    let was = false
    const ask = async () => {
      let busy = was
      try {
        const r = await api.get<{ scanning: GamesFinding[] }>('/modules/wittyplay/games/scanning')
        if (!live) return
        const now = r.scanning || []
        busy = now.length > 0
        setList(now)
        if (was && !busy) end.current?.()
      } catch { /* an older WittyPlay, or the server away: nothing to show */ }
      if (!live) return
      was = busy
      timer = window.setTimeout(ask, busy ? BUSY_MS : IDLE_MS)
    }
    void ask()
    return () => { live = false; window.clearTimeout(timer) }
  }, [])
  return list
}

const count = (n: number) => n.toLocaleString()

// findingWords: "Finding your games: 1,204 found so far in Games".
export function findingWords(f: GamesFinding): string {
  return f.found > 0 ? `Finding your games: ${count(f.found)} found so far in ${f.name}` : `Finding your games in ${f.name}…`
}

// findingDetail: how far the sorting has got, "Sorting files: 4,851 of 12,709".
export function findingDetail(f: GamesFinding): string {
  return f.total > 0 ? `Sorting files: ${count(f.files)} of ${count(f.total)}. Each console shows as its first games are sorted.` : 'Looking through its files.'
}

// foundFor: what the scans going on have found for each console.
export function foundFor(list: GamesFinding[]): Map<string, number> {
  const out = new Map<string, number>()
  for (const f of list) for (const [c, n] of Object.entries(f.consoles || {})) out.set(c, (out.get(c) || 0) + n)
  return out
}
