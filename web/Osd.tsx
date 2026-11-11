// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useMemo, useState } from 'react'
import { adviceFor, LatencyMeter, type LatencySummary } from './latency'
import type { SessionInfo } from './Player'
import type { SettingsHost } from './settings'
import type { Stats, Transport } from './transport'

// The numbers on screen. Off by default. They ask the server about once a second, and
// only while they're showing. While they're on, every frame says where its time went, and
// every press is followed to the screen.

type ServerStats = { gameFps: number; sentFps: number; skipped: number; encodedFps: number; encodeMs: number; kbps: number; droppedFrames: number; width: number; height: number; core: string; encoder?: string; speed: number; rewindBytes?: number; rewindFrames?: number }

const round = (n: number) => Math.round(n * 10) / 10
const ms = (n?: number) => (n == null || !Number.isFinite(n) ? '–' : n < 10 ? round(n).toFixed(1) : String(Math.round(n)))

export function Osd({ host, session, transport, values, stampInput }: { host: SettingsHost; session: SessionInfo | null; transport: Transport | null; values: Record<string, unknown>; stampInput?: (next: (() => number) | null) => void }) {
  const [client, setClient] = useState<Stats>({})
  const [server, setServer] = useState<ServerStats | null>(null)
  const [path, setPath] = useState<LatencySummary | null>(null)
  const meter = useMemo(() => new LatencyMeter(), [transport]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!transport) return
    transport.measure(meter)
    stampInput?.(() => meter.press())
    return () => { transport.measure(null); stampInput?.(null) }
  }, [transport, meter, stampInput])

  useEffect(() => {
    if (!session) return
    let stop = false
    const tick = () => {
      transport?.stats().then((s) => { if (!stop) setClient(s) }).catch(() => {})
      host.request<ServerStats>('GET', `/play/${session.id}/stats`).then((s) => { if (!stop) setServer(s) }).catch(() => {})
      setPath(meter.summary())
    }
    tick()
    const iv = setInterval(tick, 1000)
    return () => { stop = true; clearInterval(iv) }
  }, [host, session?.id, transport, meter]) // eslint-disable-line react-hooks/exhaustive-deps

  const on = (k: string) => values['osd.' + k] === true
  const lines: { text: string; strong?: boolean }[] = []
  const add = (text: string, strong = false) => lines.push({ text, strong })
  if (on('fps') && client.fps != null) add(`${client.fps} fps shown`)
  if (on('gameFps') && server) add(`${Math.round(server.gameFps)} fps game` + (server.skipped ? `, ${server.skipped} skipped` : ''))
  if (on('stages') && path) {
    if (path.press != null) add(`${ms(path.press)} ms press to screen`, true)
    add(`${ms(path.total)} ms finished frame to screen`, path.press == null)
    add(`game ${ms(path.core)}  encode ${ms(path.encode)}  send ${ms(path.toNetwork)}`)
    add(`network ${ms(path.network)}  decode ${ms(path.decode)}  display ${ms(path.display)}`)
  }
  if (on('latency') && client.rttMs != null) add(`${client.rttMs} ms round trip` + (client.jitterMs != null ? `, ${client.jitterMs} ms jitter` : ''))
  if (on('encode') && server && !on('stages')) add(`${round(server.encodeMs)} ms encode` + (client.decodeMs != null ? `, ${client.decodeMs} ms decode` : ''))
  if (on('bitrate')) { const k = client.kbps ?? server?.kbps; if (k != null) add(k >= 1000 ? `${round(k / 1000)} Mbit/s` : `${Math.round(k)} kbit/s`) }
  if (on('dropped')) {
    const parts = []
    if (server) parts.push(`${server.droppedFrames} at the server`)
    if (client.dropped != null) parts.push(`${client.dropped} here`)
    if (client.lost) parts.push(`${client.lost} packets lost`)
    if (parts.length) add(`Dropped: ${parts.join(', ')}`)
  }
  if (on('resolution') && server && server.width) add(`${server.width}×${server.height}`)
  if (on('core')) {
    const name = server?.core || session?.core
    if (name) add(name)
    const via = [transport?.kind, server?.encoder, client.codec].filter(Boolean).join(', ')
    if (via) add(via)
  }
  if (server?.rewindFrames) add(`Rewind: ${server.rewindFrames} steps, ${Math.ceil((server.rewindBytes || 0) / 1048576)} MB`)
  const advice = path && on('stages') ? adviceFor(path, { fps: session?.fps || 60, gameFps: server?.gameFps, rttMs: client.rttMs, jitterMs: client.jitterMs, lost: client.lost }) : ''
  const corner = String(values['osd.corner'] || 'tl')
  const size = String(values['osd.size'] || 'normal')
  return (
    <div className={`wp-osd wp-osd-${corner} wp-osd-${size}`} aria-hidden="true">
      {lines.map((l, i) => <span key={i} className={l.strong ? 'wp-osd-main' : undefined}>{l.text}</span>)}
      {lines.length === 0 && <span>Measuring…</span>}
      {advice && <span className="wp-osd-advice">{advice}</span>}
    </div>
  )
}
