// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors
//
// WittyPlay's screens in WittyMediaServer's web app (module API 5; integration.json says what
// it claims). The app calls register(host) once, when one of these is first needed, and draws
// what it hands back in place of its own copy: the WittyPlay frame and pages, the game page,
// game cards' art and links, its Settings sections, its first-run step and its TV screens.
import { lazy, Suspense, useEffect, useState, type ReactNode } from 'react'
import { Route, Routes, useLocation } from 'react-router-dom'
import { PageRoutes } from '@wms/app/App'
import { Spinner, PageSkeleton } from '@wms/app/components/common'
import { shapeFor } from '@wms/app/ui/Surface'
import { PageHead } from '@wms/app/pages/settings/ui'
import { PlayFrame } from './shell/PlayChrome'
import { MainGames } from './shell/MainGames'
import { GamePage, ControllersScreen } from './pages/Games'
import { GameTitlesSetting } from './pages/games/GameTitlesSetting'
import { GameSettingsPage } from './pages/games/consoles'
import GamingSettings from './pages/settings/Gaming'
import { Games as GamesSetup } from './pages/setup/Games'

const PlaySettings = lazy(() => import('./pages/PlaySettings'))
const PlayConsoles = lazy(() => import('./pages/PlayConsoles'))
const PlayConsoleSettings = lazy(() => import('./pages/PlayConsoles').then((m) => ({ default: m.PlayConsoleSettings })))
const PlayLibrary = lazy(() => import('./pages/PlayConsoles').then((m) => ({ default: m.PlayLibrary })))
const PlayGame = lazy(() => import('./pages/PlayGame'))
const EmulatorsSettings = lazy(() => import('./pages/settings/Emulators'))
const GamesSupport = lazy(() => import('./pages/settings/GamesSupport'))
const TvGamesLibrary = lazy(() => import('./tvmode/Games').then((m) => ({ default: m.GamesLibraryScreen })))
const TvGame = lazy(() => import('./tvmode/Games').then((m) => ({ default: m.GameScreen })))

function RouteSkeleton() {
  return <PageSkeleton shape={shapeFor(useLocation().pathname)} />
}

// WittyPlay's frame around its own pages, and around the app's pages it takes in (its SDUI pages,
// /games), exactly as the app's own copy drew it.
function Frame() {
  return (
    <PlayFrame>
      <Suspense fallback={<RouteSkeleton />}>
        <Routes>
          <Route path="/wittyplay/consoles" element={<Suspense fallback={<Spinner />}><PlayConsoles /></Suspense>} />
          <Route path="/wittyplay/consoles/:id" element={<Suspense fallback={<Spinner />}><PlayConsoleSettings /></Suspense>} />
          <Route path="/wittyplay/library" element={<Suspense fallback={<Spinner />}><PlayLibrary /></Suspense>} />
          <Route path="/wittyplay/library/:id" element={<Suspense fallback={<Spinner />}><PlayLibrary /></Suspense>} />
          <Route path="/wittyplay/game/:id" element={<Suspense fallback={<Spinner />}><PlayGame /></Suspense>} />
          <Route path="/wittyplay/settings" element={<Suspense fallback={<Spinner />}><PlaySettings /></Suspense>} />
          <Route path="/wittyplay/settings/module/:module" element={<Suspense fallback={<Spinner />}><PlaySettings /></Suspense>} />
          <Route path="/wittyplay/settings/:section/*" element={<Suspense fallback={<Spinner />}><PlaySettings /></Suspense>} />
          <Route path="*" element={<PageRoutes />} />
        </Routes>
      </Suspense>
    </PlayFrame>
  )
}

// Inside the Apple and Android apps (their web view marks <html data-app="wittymediaclient">), a game
// is played by the app's own player: touch controls, gamepads and the stream are the app's. Play,
// Resume and a save's slot all lead to /game/<id>?resume=1 (or slot=, safe=, emulator=), and the
// app catches a page load of that address; a move inside this web app isn't one, so here it becomes
// one, and the web player never starts.
function InAppHandoff({ children }: { children: ReactNode }) {
  // An app that didn't catch it lands here again at once: then it gets the web player, not a page
  // that loads forever. The same game started again later is handed over as before.
  const [handOff] = useState(() => {
    if (typeof document === 'undefined' || document.documentElement.dataset.app !== 'wittymediaclient') return false
    try {
      const key = 'wittyplay.handoff'
      const [href, at] = (sessionStorage.getItem(key) || '').split('\n')
      if (href === window.location.href && Date.now() - Number(at) < 5000) return false
      sessionStorage.setItem(key, window.location.href + '\n' + Date.now())
    } catch {
      return false
    }
    return true
  })
  useEffect(() => {
    if (handOff) window.location.replace(window.location.href)
  }, [handOff])
  return handOff ? <Spinner /> : <>{children}</>
}

function Settings({ children }: { children: ReactNode }) {
  return <Suspense fallback={null}>{children}</Suspense>
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function register(_host: unknown): any {
  return {
    routes: {
      '/wittyplay/*': Frame,
      '/page/wittyplay': Frame,
      '/page/wittyplay-*': Frame,
      '/games': Frame,
      '/game/:id': () => <InAppHandoff><Suspense fallback={<Spinner />}><GamePage /></Suspense></InAppHandoff>,
      '/controllers': () => <Suspense fallback={<Spinner />}><ControllersScreen /></Suspense>,
      '/games/library/:id': () => <Suspense fallback={<Spinner />}><TvGamesLibrary /></Suspense>,
      '/games/game/:id': () => <Suspense fallback={<Spinner />}><TvGame /></Suspense>,
    },
    cards: {
      game: { Provider: MainGames },
    },
    settings: {
      gaming: () => <GamingSettings />,
      'games-support': () => <Settings><GamesSupport /></Settings>,
      emulators: () => <Settings><EmulatorsSettings /></Settings>,
      games: () => <><PageHead title="My game settings" desc="How your games look, sound and behave. Set something for every game, for one console or, from a game’s own page, for one game. It follows you to every device." /><GameTitlesSetting /><GameSettingsPage /></>,
    },
    setup: {
      games: GamesSetup,
    },
  }
}
