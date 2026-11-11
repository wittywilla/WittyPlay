// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// Leaving what opens over a game: the game menu and its panels, the controller screen and the
// touch editor. Each is left the same ways, on a phone or a computer: its × or Back, Esc, a
// controller's B or Back, a phone's or browser's Back, and a tap outside a sheet. Then the game
// carries on as it was.
//
// This is the part with no page in it: one entry in the browser's history while anything is
// open, so Back closes a menu instead of leaving the game. The page that holds the player keeps
// its own entries; this one carries the state that was there, with a mark of its own.

export type HistoryLike = { readonly state: unknown; pushState(data: unknown, unused: string): void; back(): void }

const MARK = 'wpMenu'
const isOurs = (state: unknown) => !!state && typeof state === 'object' && (state as Record<string, unknown>)[MARK] === true

export class BackEntry {
  private history: HistoryLike
  private onBack: () => boolean
  private wait: number
  private pushed = false
  private skips = 0
  private then: (() => void) | null = null
  private timer: ReturnType<typeof setTimeout> | undefined

  // onBack runs when someone goes Back while something is open. It answers true when something
  // is still open after it (it went up a level, or kept unsaved text), so the next Back is ours too.
  constructor(history: HistoryLike, onBack: () => boolean, wait = 400) {
    this.history = history
    this.onBack = onBack
    this.wait = wait
  }

  // Something opened over the game. Going a level deeper doesn't add another entry.
  opened() {
    if (this.pushed) return
    const state = this.history.state && typeof this.history.state === 'object' ? this.history.state : {}
    this.history.pushState({ ...state, [MARK]: true }, '')
    this.pushed = true
  }

  // Everything over the game closed another way (its ×, Esc, Resume): the entry comes back out,
  // so Back leaves the game as it did before anything opened.
  closed() {
    if (!this.pushed) return
    this.pushed = false
    if (isOurs(this.history.state)) {
      this.skips++
      this.history.back()
    }
  }

  // The browser went back (popstate).
  popped() {
    if (this.then) { this.finish(); return }
    if (this.skips > 0) { this.skips--; return }
    if (!this.pushed) return
    // Something the page put over the menu (a sheet with an entry of its own) went: the menu is
    // still where it was.
    if (isOurs(this.history.state)) return
    this.pushed = false
    if (this.onBack()) this.opened()
  }

  // Before leaving the player. The entry goes first, or the page being left would stay in the
  // history under the next one, and Back from there would start the game again.
  release(then: () => void) {
    const mine = this.pushed && isOurs(this.history.state)
    this.pushed = false
    if (!mine) { then(); return }
    this.then = then
    this.history.back()
    // A browser that never says it went back still leaves.
    this.timer = setTimeout(() => this.finish(), this.wait)
  }

  get open() { return this.pushed }

  private finish() {
    clearTimeout(this.timer)
    const f = this.then
    this.then = null
    f?.()
  }
}
