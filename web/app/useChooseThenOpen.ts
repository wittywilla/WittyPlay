// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useRef } from 'react'
import { pressTracker } from './pointerChoose'

/** useChooseThenOpen is pointerChoose's pressTracker kept for a component's life, across the render the press's focus causes. */
export function useChooseThenOpen() {
  const t = useRef<ReturnType<typeof pressTracker> | null>(null)
  if (!t.current) t.current = pressTracker()
  return t.current
}
