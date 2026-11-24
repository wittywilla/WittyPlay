// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// A switch that is its whole row (styles: .wplay-photos-switch in settings.css).
import { useId } from 'react'

// The whole row is the switch: one target at least 48px tall (a remote's and a finger's), named by its words. A bare switch beside a
// label was 26px and had no name of its own.
export function SwitchRow({ name, more, checked, disabled, onChange }: { name: string; more?: string; checked: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  const id = useId()
  return (
    <button type="button" role="switch" aria-checked={checked} aria-label={name} aria-describedby={more ? id : undefined} disabled={disabled}
      className="wplay-photos-switch" onClick={() => onChange(!checked)}>
      <span>{name}{more && <span className="muted" id={id}> · {more}</span>}</span>
      <span className={'set-switch' + (checked ? ' on' : '')} aria-hidden="true"><i /></span>
    </button>
  )
}
