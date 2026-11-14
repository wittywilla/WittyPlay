// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Invites from friends (the module's api/invites.go), as the status bar and the game page use them.
// No browser in here.

// An invite as GET /games/online/invites answers it.
export type InviteView = { id: string; from: number; fromName: string; to: number; sid: string; itemId: number; title: string; console?: string; sentAt: number; expiresAt: number }

// Where the invites are listed: WittyPlay Online's Together tab.
export const PLAY_TOGETHER = '/page/wittyplay-online?tab=together'

// inviteWords is what an invite says: "sam invites you to play Star Tide".
export const inviteWords = (i: Pick<InviteView, 'fromName' | 'title'>) => `${i.fromName || 'A friend'} invites you to play ${i.title || 'a game'}`

// newInvites is the words for each invite not seen before, oldest first, noting them as seen. An
// invite that's gone is forgotten, so one sent again is new.
export function newInvites(list: InviteView[], seen: Set<string>): string[] {
  const now = new Set(list.map((i) => i.id))
  for (const id of [...seen]) if (!now.has(id)) seen.delete(id)
  const out: string[] = []
  for (const i of [...list].sort((a, b) => a.sentAt - b.sentAt)) {
    if (seen.has(i.id)) continue
    seen.add(i.id)
    out.push(inviteWords(i))
  }
  return out
}

// joinedGame is what the game page's address asks of the player: to join a game as a seat (?join=
// its session), to accept an invite and then join (?invite= its ID), or neither (a game of its own).
export function joinedGame(params: URLSearchParams): { join?: string; invite?: string } {
  const ok = (v: string | null) => (v && /^[A-Za-z0-9_-]{1,64}$/.test(v) ? v : undefined)
  const invite = ok(params.get('invite'))
  if (invite) return { invite }
  const join = ok(params.get('join'))
  return join ? { join } : {}
}
