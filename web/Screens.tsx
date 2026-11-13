// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
import { useEffect, useState } from 'react'
import { PadGlyph, useInputKind } from './prompts'
import type { Explained } from './words'

// What's on screen when there's no game yet: what's happening, or what went wrong and what
// can be done about it. Never just a blank wait.

export type LoadStage = 'prepare' | 'start' | 'connect' | 'picture'
const STAGES: { id: LoadStage; label: string }[] = [
  { id: 'prepare', label: 'Getting the game ready' },
  { id: 'start', label: 'Starting the console' },
  { id: 'connect', label: 'Connecting to this device' },
  { id: 'picture', label: 'Waiting for the first picture' },
]

export function LoadingScreen({ title, consoleName, cover, stage, status, percent, onCancel }: { title: string; consoleName: string; cover?: string; stage: LoadStage; status: string; percent?: number; onCancel: () => void }) {
  const input = useInputKind()
  const [seconds, setSeconds] = useState(0)
  useEffect(() => { const t = window.setInterval(() => setSeconds((s) => s + 1), 1000); return () => window.clearInterval(t) }, [])
  const at = STAGES.findIndex((s) => s.id === stage)
  const hint = seconds > 45 ? 'This is taking longer than usual. A big game on a busy server can take a few minutes the first time.' : seconds > 12 ? 'Big games take longer the first time. They’re quicker after that.' : ''
  return (
    <div className="wp-load" role="status" aria-live="polite" aria-label={`${title}: ${status}`}>
      {cover && <img className="wp-load-cover" src={cover} alt="" />}
      <div className="wp-load-body">
        <p className="wp-load-console">{consoleName}</p>
        <h1>{title}</h1>
        <ol className="wp-load-steps">
          {STAGES.map((s, i) => (
            <li key={s.id} className={i < at ? 'done' : i === at ? 'now' : ''} aria-current={i === at ? 'step' : undefined}>
              <span className="wp-load-mark" aria-hidden="true">
                {i < at ? <svg viewBox="0 0 24 24"><path fill="currentColor" d="M9.5 16.6L5 12l-1.4 1.4 5.9 6L21 7.9l-1.4-1.4z" /></svg> : i === at ? <span className="wp-spinner" /> : null}
              </span>
              <span>{i === at ? status || s.label : s.label}</span>
            </li>
          ))}
        </ol>
        {percent != null && (
          <div className="wp-load-progress">
            <div className="wp-load-bar" role="progressbar" aria-label="Unpacking" aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent}><i style={{ transform: `scaleX(${percent / 100})` }} /></div>
            <span className="wp-load-pct" aria-hidden="true">{Math.round(percent)}%</span>
          </div>
        )}
        {hint && <p className="wp-muted wp-small">{hint}</p>}
        <div className="wp-load-foot">
          <button type="button" className="wp-btn" onClick={onCancel}>Cancel</button>
          <p className="wp-load-tip">
            {input.kind === 'pad'
              ? <>For the game menu, press <PadGlyph button="start" family={input.family} /> and <PadGlyph button="select" family={input.family} /> together</>
              : input.kind === 'pointer' ? <>For the game menu, use the button in the corner</>
              : <>For the game menu, press <kbd className="wp-glyph key">Esc</kbd></>}
          </p>
        </div>
      </div>
    </div>
  )
}

export function ProblemScreen({ title, cover, problem, onBack, onRetry, onDefaults, onSettings }: { title: string; cover?: string; problem: Explained; onBack: () => void; onRetry?: () => void; onDefaults?: () => void; onSettings?: () => void }) {
  return (
    <div className="wp-load wp-problem" role="alertdialog" aria-modal="true" aria-labelledby="wp-problem-t" aria-describedby="wp-problem-d">
      {cover && <img className="wp-load-cover" src={cover} alt="" />}
      <div className="wp-load-body">
        <p className="wp-load-console">{title}</p>
        <h1 id="wp-problem-t">{problem.title}</h1>
        <p id="wp-problem-d" className="wp-problem-text">{problem.body}</p>
        <div className="wp-row wp-wrap">
          {problem.fix === 'settings' && onSettings && <button type="button" className="wp-btn primary" autoFocus onClick={onSettings}>Open Consoles &amp; cores</button>}
          {problem.retry && onRetry && <button type="button" className={'wp-btn' + (problem.fix ? '' : ' primary')} autoFocus={!problem.fix} onClick={onRetry}>Try again</button>}
          {problem.defaults && onDefaults && <button type="button" className="wp-btn" onClick={onDefaults}>Start with default settings</button>}
          <button type="button" className={'wp-btn' + (!problem.retry && !problem.fix ? ' primary' : '')} autoFocus={!problem.retry && !problem.fix} onClick={onBack}>Back</button>
        </div>
      </div>
    </div>
  )
}
