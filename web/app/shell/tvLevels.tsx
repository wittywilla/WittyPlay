// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import type { ComponentType, ReactNode } from 'react'
import { useLocation } from 'react-router-dom'

// How much of a screen a television offers (WittyMediaServer's docs/TV-MODE.md). The server keeps the table and the
// card that says to use the WittyMediaClient app on a computer, or a web browser; it hands them to modules as
// host.tv (web host feature tvLevels 1). Console settings, controllers, mapping and the store stay a TV's own: only
// what needs typing, files or folders is sent to a computer. A server without tvLevels shows everything, as before.

type Level = 'full' | 'view' | 'desktopOnly'
type TvHost = {
  has?: (feature: string, v?: number) => boolean
  useTvMode?: () => boolean
  tv?: {
    screenFor: (path: string) => { level: Level; title: string; summary?: string } | null
    OnAComputer: ComponentType<{ level: Level; title: string; path: string; summary?: string; children?: ReactNode }>
    TvPart: ComponentType<{ id: string; children: ReactNode }>
  }
}

let host: TvHost | null = null
export function rememberTvHost(h: unknown) { host = h as TvHost }

const tvOf = () => (host?.has?.('tvLevels') ? host.tv : undefined)
const noTv = () => false

// TvPart: a part of a screen a TV may not offer (custom cores, importing a file).
export function TvPart({ id, children }: { id: string; children: ReactNode }) {
  const Part = tvOf()?.TvPart
  return Part ? <Part id={id}>{children}</Part> : <>{children}</>
}

// TvScreen: a whole screen of WittyPlay's own frame, by its address in the server's table.
export function TvScreen({ path, children }: { path: string; children: ReactNode }) {
  const useTv = host?.useTvMode || noTv
  const on = useTv()
  const loc = useLocation()
  const tv = tvOf()
  const screen = on && tv ? tv.screenFor(path) : null
  if (!tv || !screen || screen.level === 'full') return <>{children}</>
  const Card = tv.OnAComputer
  return <Card level={screen.level} title={screen.title} path={loc.pathname} summary={screen.summary}>{children}</Card>
}
