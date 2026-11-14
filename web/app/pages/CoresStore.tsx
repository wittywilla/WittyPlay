// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
// WittyPlay's store for what this server plays: libretro cores and standalone emulators, in one place
// inside WittyPlay's frame (the owner, 2026-11-13: one interface; the server's Settings only opens this).
// Each tab is the screen it always was, so nothing is built twice: consoles and their cores (installs,
// updates, per-console and custom cores, BIOS files, region order, streaming), what each console plays
// here and why not, and standalone emulators (installs, firmware, controllers, every setting).
import { lazy, Suspense } from 'react'
import { useApp } from '@wms/app/state'
import { Tabs } from '@wms/app/ui'
import { Spinner } from '@wms/app/ui/Surface'
import GamingSettings from './settings/Gaming'

const EmulatorsSettings = lazy(() => import('./settings/Emulators'))
const GamesSupport = lazy(() => import('./settings/GamesSupport'))

export const STORE = '/wittyplay/settings/store'
export type StoreTab = 'consoles' | 'support' | 'emulators'

export default function CoresStore({ tab }: { tab: StoreTab }) {
  const { user } = useApp()
  const admin = !!user?.isAdmin
  // What this server plays is for everyone; installing and choosing is the administrator's.
  const items = [
    ...(admin ? [{ value: 'consoles' as StoreTab, label: 'Consoles & cores', to: STORE + '/consoles' }] : []),
    { value: 'support' as StoreTab, label: 'What this server plays', to: STORE + '/support' },
    ...(admin ? [{ value: 'emulators' as StoreTab, label: 'Standalone emulators', to: STORE + '/emulators' }] : []),
  ]
  const at = items.some((i) => i.value === tab) ? tab : items[0].value
  return (
    <div className="wplay-store">
      <h1>Cores &amp; emulators</h1>
      <Tabs value={at} items={items} label="Cores and emulators" panelId="wplay-store-panel" />
      <div id="wplay-store-panel" role="tabpanel">
        <Suspense fallback={<Spinner />}>
          {at === 'consoles' && <GamingSettings />}
          {at === 'support' && <GamesSupport />}
          {at === 'emulators' && <EmulatorsSettings />}
        </Suspense>
      </div>
    </div>
  )
}
