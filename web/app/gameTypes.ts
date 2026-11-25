// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// What WittyPlay's own routes answer (module API 5): game details, the review of a games library,
// metadata sources, consoles and cores. Each is called at /modules/wittyplay/<path> through the
// app's api. The app's item types (Item, Library) stay the app's.
import type { CoreSupport } from './pages/games/platforms'

// What's known about a game beyond what its item holds (GET /modules/wittyplay/games/{id}/metadata).
export type GameScore = { source: string; kind: 'critic' | 'user' | string; value: number; votes?: number; url?: string }
export type GameDetailsInfo = {
  itemId: number; status: 'pending' | 'done' | string; locked: boolean; matchSource: string; matchMethod: string; confidence: number
  tags: string; released: string; developer: string; publisher: string; franchise: string; players: number
  scores: GameScore[]; ages: { system: string; value: string; age: number | null }[]; art: { kind: string; url: string }[]
  credits: { source: string; name: string; site: string; text: string; license: string; url?: string }[]
  crcs?: string; serial?: string
  // How its console was worked out, why it can't play if it can't, and its discs.
  detectedBy?: string; detectSure?: number; detectWhy?: string; problem?: string; problemText?: string; entry?: string
  discs?: { disc: number; name: string }[]
}
// Files in a games library that a scan wasn't sure about (GET /modules/wittyplay/libraries/{id}/games/review).
export type GameReviewItem = { id: number; path: string; name: string; size: number; kind: string; reason: string; suggest: { console: string; name: string; short: string }[]; itemId?: number }
export type GameReview = { items: GameReviewItem[]; counts: Record<string, number>; total: number }
export type GameImport = {
  scanning: boolean; scan: { phase: string; current: string; found: number; done: number } | null; lookup: GameMetaStatus
  review: Record<string, number>; games: Record<string, number>
}
export type GameVersion = { id?: number; entry?: string; label: string; current?: boolean; altered?: boolean }
export type GameVersions = { versions: GameVersion[]; inside?: GameVersion[]; discs?: { disc: number; name: string }[] }
export type GameCandidate = { source: string; id: string; title: string; year?: number; console?: string; poster?: string; score: number }
export type GameFacet = { value: string; count: number }
export type GameFacets = { genres: GameFacet[]; decades: GameFacet[]; players: GameFacet[]; rated: number; series: GameFacet[]; letters: GameFacet[] }
export type GameMetaStatus = {
  running: boolean; phase: string; total: number; pending: number; matched: number; byChecksum: number; pictures: number; withPoster: number; withDetails: number
  consoles: Record<string, { total: number; matched: number; byChecksum: number }>; paused: { source: string; until: number; why: string }[]; keysSet: boolean
}
export type GameSource = {
  id: string; name: string; site: string; why: string; gives: string[]; fields: { setting: string; label: string; hint?: string }[]; keyUrl?: string; cost: string; steps?: string[]
  limit: string; sends: string; credit: string; license: string; unavailable?: string; on: boolean; ready: boolean; saved: string[]; used?: number; allowance?: number; period?: string
}
export type GameCore = { id: string; name: string; license: string; source?: string; sha256?: string; nonCommercial?: boolean; licenseSource?: string; version?: string; unapproved?: boolean }
export type GameConsole = {
  id: string; name: string; short: string; also?: string[]; extensions: string[]; cores: GameCore[]; bios?: { file: string; for: string; required: boolean }[]
  unavailable?: string; installed: GameCore[]; ready: boolean; count?: number; needsGpu?: boolean
  // When it isn't ready: why (gpu means this server can't draw its games), in words for people.
  reason?: 'unavailable' | 'core' | 'gpu' | 'emulator'; message?: string; gpuMissing?: string
  // Standalone emulators set up to play it here, and the one Play starts when no core plays it.
  standaloneEmulators?: { id: string; name: string; version: string; ready: boolean; why?: string }[]; standalone?: string
  totalGames?: number
  // The games library its games open in (the one kept for it, else the one with the most of them): see libraryFor.
  library?: number
  // Ready, with the processor drawing its games: no graphics card here. notice is the speed caveat.
  software?: boolean; notice?: string
  // Why the processor draws (no-card: none here; card-fault: its core draws wrongly on this system's card),
  // the core Play starts its games on unless a game has its own, and the BIOS file that core lacks.
  softwareWhy?: 'no-card' | 'card-fault'; playsOn?: string; biosMissing?: string
  // Where each core on offer runs, how it draws there, and here: see pages/games/platforms.tsx.
  coreSupport?: Record<string, CoreSupport>
  // What each core on offer is good at beside the console's others (docs/GAMING-API.md, Choosing a core).
  coreAbout?: Record<string, { strength?: 'accuracy' | 'speed' | 'balanced'; about: string }>
  customCores?: GameCore[]; defaultCore?: string; shared?: string[]; padLike?: string; logoName?: string
  pad: { face: { id: number; label: string }[]; shoulder?: { id: number; label: string }[]; meta: { id: number; label: string }[]; sticks?: boolean; pointer?: boolean; art?: string }
  // Its era and years, how its games come, and what it can do (docs/GAMING-API.md, Consoles).
  era?: 'first' | '8-bit' | '16-bit' | 'early-3d' | 'disc' | 'pocket'; launched?: number; ended?: number; media?: 'cart' | 'pocket' | 'disc'
  capabilities?: GameCapabilities
  // How its games stand on a shelf: its case and its cartridge or disc, as shapes to draw around a cover.
  shelf?: { case: GameShelfShape; cart: GameShelfShape }
}
export type GameShelfShape = { kind: string; aspect: number; height: number }
export type GameCapabilities = {
  screens: { count: number; touch?: number; layouts?: string[]; options?: Record<string, { layout: string; values: Record<string, [string, string]>; swap?: string; swapValues?: [string, string] }> }
  input: { ports: number; multitap?: number; sticks?: number; analogTriggers?: boolean; touch?: boolean; lightGun?: boolean; mouse?: boolean; motion?: boolean; keyboard?: boolean; mic?: boolean }
  media: { discs?: boolean; memoryCards?: number; bios: 'none' | 'optional' | 'required'; region?: Record<string, string> }
  play: { rumble?: boolean; rewindByDefault?: boolean }
  picture: { aspect: string; integer?: boolean; look: string }
}
// What this account is playing now, and where (GET /modules/wittyplay/games/playing).
export type GamePlaying = { id: string; itemId: number; title: string; console: string; device?: string; startedAt?: number; seats?: { userId: number; name: string; seat: number }[] }
