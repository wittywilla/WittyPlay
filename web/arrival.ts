// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// How late frames are arriving: each frame's arrival against its timestamp, less the quickest
// seen lately. A queue growing anywhere on the way shows up here before anything is lost.
export class ArrivalDelay {
  private best = Infinity
  private bestAt = 0
  private worst = 0
  note(sentMs: number, arrivedMs: number) {
    const d = arrivedMs - sentMs
    // The best is forgotten after a while, so a clock that drifts can't build up.
    if (d < this.best || arrivedMs - this.bestAt > 10000) { this.best = d; this.bestAt = arrivedMs }
    this.worst = Math.max(this.worst, d - this.best)
  }
  take() { const w = this.worst; this.worst = 0; return w }
}
