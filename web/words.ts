// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// What a controller is called, and what went wrong in words a person can act on. No browser
// objects in here, so these can be tested on their own (logic.test.mjs).

export type PadFamily = 'xbox' | 'playstation' | 'nintendo' | 'generic'

// Which family a controller belongs to, from how the browser describes it.
export function familyOf(id: string): PadFamily {
  const s = id.toLowerCase()
  if (/nintendo|pro controller|joy-?con|vendor: 057e|^057e-/.test(s)) return 'nintendo'
  if (/playstation|dualshock|dualsense|sony|vendor: 054c|^054c-/.test(s)) return 'playstation'
  if (/xbox|xinput|microsoft|vendor: 045e|^045e-/.test(s)) return 'xbox'
  return 'generic'
}

export type ProblemData = { needCore?: boolean; otherCore?: boolean; needBios?: boolean; encrypted?: boolean; file?: string; rig?: boolean; connect?: boolean; gone?: boolean; status?: number }

export type Explained = { title: string; body: string; fix?: 'settings'; retry?: boolean; defaults?: boolean }

// explain turns what went wrong into what a person can do about it. It goes by what the
// server said it was, then by the words of the few messages that have no flag.
export function explain(message: string, d: ProblemData | null | undefined, isAdmin: boolean, consoleName: string): Explained {
  if (d?.needCore) {
    return isAdmin
      ? { title: `Nothing to play ${consoleName} games with yet`, body: 'This server needs a core for this console: a free program that plays its games. It takes one click to install in Settings.', fix: 'settings' }
      : { title: `This server can’t play ${consoleName} games yet`, body: 'It needs a core for this console, which whoever runs this server can install in Settings.' }
  }
  if (d?.otherCore) return { title: 'This game needs a different core', body: message, fix: isAdmin ? 'settings' : undefined }
  if (d?.needBios) return { title: `${consoleName} games need a BIOS file`, body: message, fix: isAdmin ? 'settings' : undefined }
  if (d?.encrypted) return { title: 'This game file is encrypted', body: message + ' Only a copy that isn’t encrypted can be played. The file on the server is untouched.' }
  if (d?.connect) return { title: 'The game started, but its picture can’t reach this device', body: `${message} This is usually a firewall or a network that blocks the connection. Trying again may pick another way through.`, retry: true }
  if (d?.gone) return { title: 'This game keeps stopping', body: `${message} Starting it again goes back to its last save.`, retry: true, defaults: true }
  const m = message.toLowerCase()
  if (m.includes('graphics card')) return { title: 'This game needs a graphics card', body: message, fix: isAdmin ? 'settings' : undefined }
  if (m.includes('file is missing')) return { title: 'This game’s file has gone', body: 'It was moved or deleted after the library was last scanned. Put it back, or scan the library again.' }
  if (d?.status === 503 || m.includes('at once') || m.includes('busy') || m.includes('too many')) return { title: 'The server is busy', body: `${message} Wait for another game to finish, then try again.`, retry: true }
  if (d?.status === 0 || m.includes('can’t reach') || m.includes("can't reach")) return { title: 'Can’t reach the server', body: message, retry: true }
  return { title: 'This game didn’t start', body: message || 'The server didn’t say why.', retry: true, defaults: true }
}
