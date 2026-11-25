// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// "A pointer only points", for every WittyPlay grid and list with a chosen card (Consoles, All games and a console's games),
// the same rule as the server's rows (its web/src/ui/pointerChoose.ts, which draws Recent and the other SDUI rows):
//
// - Arrows, a remote and a pad move the focus, and the focus chooses.
// - A pointer passing over a card only shows a hover style; it never chooses, or crossing cards on the way to the details'
//   buttons would change the details and move the layout under the pointer.
// - A click on a card that isn't the chosen one chooses it.
// - A click on the chosen card, a double-click, or Enter (a click with no mouse behind it: detail 0) opens it.
//
// Whether the card pressed was already the chosen one is read as the press begins, before the focus it brings chooses it.

/** clickOpens: whether a click opens the card, rather than only choosing it. */
export function clickOpens(detail: number, wasChosen: boolean): boolean {
  return detail === 0 || detail > 1 || wasChosen
}

/** pressTracker keeps whether the card pressed was the chosen one, from the press to its click. */
export function pressTracker() {
  let wasChosen = false
  return {
    /** For onPointerDown: chosen is whether this card is the chosen one now. */
    press(chosen: boolean) { wasChosen = chosen },
    /** For onClick: whether this click opens. */
    opens(detail: number) { const o = clickOpens(detail, wasChosen); wasChosen = false; return o },
  }
}

