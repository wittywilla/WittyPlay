# Changelog

What changed in WittyPlay, newest first. What changed before it had a repository of its own is in the
[WittyMediaServer changelog](https://github.com/wittywilla/WittyMediaServer/blob/master/CHANGELOG.md).

## 0.7.19

### What's fixed

- **Settings pages on the server look as the server draws them** (#121). WittyPlay's bundle carried an old copy of the
  server's settings stylesheet, and once WittyPlay was installed it restyled every Settings page, core ones too: each
  page's title and lead sat off to one side of what was under them, and the settings menu slid under the top bar. The
  copy is gone: WittyPlay's settings pages use the server's own, and only WittyPlay's console photos switch is styled
  here. A test (`web/hostcss.test.mjs`) fails on any rule of this module's for the server's Settings frame.
- **Settings, Controllers keeps its heading below the top bar** as the page scrolls, not under it (#121).
- **Games play in Docker.** On a NAS or any server in Docker with its usual network, a game started but never showed anything: the player sat on "Connecting to this device…" for good. Three things went wrong. WebRTC could only offer the container's private address and a port nobody had published, so the browser could never reach it. The player waited for that connection to start playing before it started its timer, so it never gave up and never tried anything else. And the WebSocket it would have tried next was refused by WittyPlay itself, everywhere: the server passes it on with its own address, which never matched the page's. Now the WebSocket is let through (a page on another site still isn't), the player gives up on WebRTC as soon as the browser says it can't get through, or after 12 seconds at most, and on Docker's own network the game goes straight over the server's own port. For WebRTC's lower latency in Docker, publish a UDP port and set it and the server's address in Settings, Games (the server's docs/DOCKER.md says how), or use the host's network.
- **Nintendo 64 homebrew that can't be drawn here says so, instead of a black screen.** Games built with libdragon need a graphics card with Vulkan; on a server without one (most NAS boxes) they started anyway, showed black, and said why only in a note that was gone in a few seconds. They now don't start, and the player says "This game needs a graphics card with Vulkan on the server to show a picture." (or "…a graphics card on the server…" where there's none), with Back. ParaLLEl N64 isn't counted as failing for it, and plays commercial games as before.

### Developer notes

- `stream.behindBridge`: in a container (`WMS_DOCKER=1`, `/.dockerenv` or `/run/.containerenv`) that sees no `docker0`. There, `Modes` leaves WebRTC out unless `gamingPublicIp` and `gamingUdpPort` are both set, and the log says so once at start.
- `ServeStream` accepts any Origin (`InsecureSkipVerify`): the server refuses a cross-origin upgrade before passing it on (`passToModule`), and the helper refuses `Sec-Fetch-Site: cross-site`/`same-site` and `Origin: null` itself.
- `stream.NoPictureError`: `startOn` refuses a libdragon game with no Vulkan card; `POST /games/{id}/play` answers 409 with `needsGpu`, like a core that needs a card, and doesn't call `CoreStartFailed`.
- web/transport.ts: `video.play()` isn't awaited before the first-picture timer; `connectionFails` ends the WebRTC try when the connection or ICE state is `failed`.
- Tests: stream/docker_test.go (a WebSocket through the server's proxy, modes behind a bridge). The server's scripts/test/docker-games.sh plays two real cores in its image in CI.

## 0.7.18

### What's new

- **The console photos switches are easy to reach on a TV.** In setup's Games step and WittyPlay's Settings, each switch is now its whole row: one target as tall as a remote or a finger needs, and a screen reader says its name.
- **The graphics card check no longer fails on Linux under WSL.** It answered, then the game runner crashed on its way out on about half the checks (a graphics driver thread racing the runner's tidy-up), and the server threw the answer away, so it never knew it had a card. The runner now leaves straight after answering, and an answer given in full stands however the runner ended. With no card at all the check says so, with the reason, and never crashes.
- **N64 homebrew no longer crashes where the graphics card can't use Vulkan.** Libdragon games draw only with parallel-RDP, which needs Vulkan. On a card without it (WSL, or a driver with only the processor's Vulkan) ParaLLEl N64 fell back to an OpenGL plugin, and the 240p Test Suite stopped on its own error screen a minute in. There they now get the processor's settings, and the start screen says "This game needs a graphics card with Vulkan on the server to show a picture."
- **Nintendo 64 homebrew shows a picture on a server with a graphics card.** Games built with libdragon (most new homebrew) were always drawn by ParaLLEl N64's software renderer, which in this core draws them black. With a card they now use parallel-RDP, which draws them correctly; with no card they still can't be shown. Commercial games, on Mupen64Plus-Next, were never affected. Starting one says so on its start screen: "This game needs a graphics card on the server to show a picture."
- **On a phone, tapping a game in All games opens it.** The details panel meant only for wider screens was drawn under the games instead, with its Play button behind the dock, where a tap opened Together.
- **A game with no cover picture can be played on a phone.** Its page drew the stand-in cover over the whole page, Play included.

### Developer notes

- `SwitchRow` (web/app/pages/settings/SwitchRow.tsx): a `role="switch"` button with its words inside and an `aria-label`, at least 48px tall. It replaces the server's bare `Switch` (26px, named only through `aria-labelledby`), which setup's TV walk counted as a nameless button under 48px. Test: web/tests/switchrow.test.ts.

## 0.7.17

_Never published on its own: it came out in 0.7.18._

### What's new

- **The graphics card check no longer fails on Linux under WSL.** It answered, then the game runner crashed on its way out on about half the checks (a graphics driver thread racing the runner's tidy-up), and the server threw the answer away, so it never knew it had a card. The runner now leaves straight after answering, and an answer given in full stands however the runner ended. With no card at all the check says so, with the reason, and never crashes.

### Developer notes

- `wittyplay-game-runner -probe-gpu` exits right after its answer and doesn't destroy the OpenGL context first: under WSL that took the crash rate from 9 in 20 checks to 0 in 20, and 0 in 10 in a container with no card. `stream.ProbeGPU` keeps a full answer from a runner that ended badly (`answeredAnyway`), and logs it. `TestProbeGPUAnswersCleanly` runs the real check five times through the real sandbox on whatever the machine has (CI's have no card).

## 0.7.16

_Never published on its own: it came out in 0.7.18._

### What's new

- **N64 homebrew no longer crashes where the graphics card can't use Vulkan.** Libdragon games draw only with parallel-RDP, which needs Vulkan. On a card without it (WSL, or a driver with only the processor's Vulkan) ParaLLEl N64 fell back to an OpenGL plugin, and the 240p Test Suite stopped on its own error screen a minute in. There they now get the processor's settings, and the start screen says "This game needs a graphics card with Vulkan on the server to show a picture."

### Developer notes

- The game is offered Vulkan (`-vulkan`) only when the graphics card check named a Vulkan card (`vulkanCard` in `api/gpu.go`). Under WSL that check fails (the runner's `-probe-gpu` ends on a segmentation fault), so nothing there is offered Vulkan for these games. Same core version on Windows and Linux (ParaLLEl N64 1.0 e854c84): the difference was Vulkan, not the build.

## 0.7.15

_Never published on its own: it came out in 0.7.18._

### What's new

- **Nintendo 64 homebrew shows a picture on a server with a graphics card.** Games built with libdragon (most new homebrew) were always drawn by ParaLLEl N64's software renderer, which in this core draws them black. With a card they now use parallel-RDP, which draws them correctly; with no card they still can't be shown. Commercial games, on Mupen64Plus-Next, were never affected. Starting one says so on its start screen: "This game needs a graphics card on the server to show a picture."
- **On a phone, tapping a game in All games opens it.** The details panel meant only for wider screens was drawn under the games instead, with its Play button behind the dock, where a tap opened Together.
- **A game with no cover picture can be played on a phone.** Its page drew the stand-in cover over the whole page, Play included.

## 0.7.14

### What's new

- **A Games library waiting for its first scan says so.** While another library is scanned ahead of it, it reads "Waiting to be scanned", then "Scanning…" while its turn runs, and the games appear when it ends, instead of "No games found in this library yet".
- **The controller layout named "Kyoto layout"** no longer adds "(A on the right)"; its note still says A on the right confirms (#105).
- **On a phone, a controller row fits on one line** (name, key, Change and Put back), and the first game in a row starts at the page edge, not cut off (#110).

### Developer notes

- `PlayLibraryView` asks `GET /api/libraries/{id}/scan` once and every 3 s while a scan waits or runs (WittyMediaServer #553).

## 0.7.13

- **Games start on a phone away from home.** An app that queued a button press or a keyframe request while its connection was still opening sent it before the hello, and the game refused the connection ("the first message must be the hello, as text") and never showed a picture. Binary messages before the hello are now skipped.
- **The note for a server with no graphics card is short** enough to read on a phone's start screen.
- **Return To Server says so on a phone.** On a narrow screen the button was only an arrow; it now reads "Return" beside it. Wider screens keep the whole name.

## 0.7.12

- **A big games library's first scan no longer crawls.** While a scan described the games it found, it kept reading the game database again for almost every game, so a first scan of a large library could take hours. It now keeps a few consoles' lists in memory.
- **A TV remote is told to press Back** for the game menu while a game loads, instead of Esc.
- **Button hints follow TV mode:** with the server's TV mode on, hints show the remote's OK and Back instead of a keyboard's keys, and the choice of buttons to show offers the remote instead of the keyboard.
- **Release dates are whole or left out.** A date a source gave half-way (such as "2001-1") is shown as the year, so games sort by date correctly.
- **Fewer questions the first time you open Games.** It asks which buttons to show, then how to get back to the app, and you're at your games. How games are laid out and what choosing one does start as they are; change them any time in Settings.
- **Choosing your buttons takes one press.** Enter on a choice picks it and moves on; with a controller, picking one moves straight to Continue.

## Developer notes for 0.7.12

- `gamemeta.Libretro` keeps up to `libdbKeep` (3) consoles' lists, most recently used first, instead of one. A scan's `describeLocal` and the worker's `localBatch` asked about different consoles, so each lookup threw out the other's list and parsed a multi-MB RDB again (about 12 s a game on a 2,293-game library's first scan). Measured on the same machine and a new library of the same NES and N64 folders (447 games): describing took 2 min 38 s before and 53 s after; the whole scan 2 min 47 s and 1 min 4 s. A list with files not downloaded yet is kept too, for offline (scan) lookups only and for `partialFor` (one minute); a lookup that may download reads it again. `reads` counts list reads for the new tests.
- `web/app/shell/playPaths.ts`: in TV mode the `keyboard` choice is drawn as the remote (`PLAY_REMOTE`: OK and Back, nothing for X and Y), labelled "Remote", and `exitWords` says Back. `web/Screens.tsx`: the loading tip has a `remote` case; `web/prompts.tsx`: `isTV()` also counts the server's TV mode (`html.tv-mode`), per docs/TV-MODE.md.
- `cleanDate` keeps only whole parts (month 01-12, day 01-31); `FuzzGameParsers` checks its output's shape, and `TestCleanDateWholeParts` the cases.
- `sandbox/docker` tests: `specUser` is atomic; `TestLifecycleConcurrencyLimit` raced on it under `-race`.
- The recent-games source (`sduiAsk.recent`) caps the offset and limit before adding them: a huge pair wrapped round to a negative SQL `LIMIT`, which SQLite reads as no limit.

## 0.7.11

_Never published on its own: it came out in 0.7.12._

- **Fewer questions the first time you open Games.** It asks which buttons to show, then how to get back to the app, and you're at your games. How games are laid out and what choosing one does start as they are; change them any time in Settings.
- **Choosing your buttons takes one press.** Enter on a choice picks it and moves on; with a controller, picking one moves straight to Continue.

## Developer notes for 0.7.11

- Fuzz tests for .wittyplay files and players' input messages.
- `ExitTip` (`web/app/shell/PlayChrome.tsx`) has two steps, `pads` and `exit`; the `layout` and `open` steps are gone, and the defaults of `usePlayLayout` and `usePlayOpen` stand until changed in Settings (#94).
- Enter on a `PadsChooser` tile inside the tip chooses it and goes to `exit`; a click (a pad's A) chooses it and focuses Continue.

## 0.7.10

The first WittyPlay update since 0.7.2, with everything from the versions in between.

**Looks and feel**

- **Fits a computer's screen.** In a desktop or laptop browser and the desktop app, games are drawn at a normal size: six or seven to a row on a large window, with the chosen game's details beside them. Small, medium and large change the size everywhere. TV mode keeps its big, from-the-sofa sizes.
- **A mouse only points.** Hovering over a game no longer chooses it, so the details stay put while you move to their buttons. Click to choose, click again (or double-click, or press Enter) to open. A remote, a gamepad and the arrow keys work as before.
- **The game menu fits the window,** with no scroll bar on a computer, phone or TV.
- **A details panel beside the games** on All games, a console's games and search, and All games follows the Grid and List switch.
- **Console photos without their white box,** fixed photos for the Wii, Switch and Switch 2, and a console's name follows the version you pick (the NES or the Famicom, the Genesis or the Mega Drive).
- **Narrower windows read better,** and the highlight around a chosen game no longer spills over its neighbours.

**Playing**

- **Smoother games on a graphics card.** PlayStation, PlayStation 2, GameCube and Wii games draw with Vulkan where the card supports it, and frames no longer wait for the card every time. Games run ahead of background work on Windows.
- **GameCube and Wii at full speed** on Windows and Linux, and they play on a Linux server with no graphics card too.
- **Nintendo 64: ParaLLEl-RDP can be chosen** in a core's options. It isn't the default, because it costs more processor time.
- **Performance profiles:** one starting point for every emulator, chosen in the first-run step and in Settings, Games.
- **The Wii Remote's pointer works from a browser:** the mouse, or a finger, is where the remote points, and a click presses A.
- **Fixed:** some Nintendo 64 cores crashed at random on Windows, or when starting with Vulkan.
- **Fixed:** the core picker keeps your place while a core downloads, and the game shows the new core at once.

**Your games' files**

- **A game's own `.wittyplay`** can carry everything WittyPlay knows about it: details, several pictures of each kind, controls, cheats and how it plays. It is checked against the game file it was made for, so a file for another region or revision is flagged rather than applied.
- **An editor for a game's `.wittyplay`,** for administrators on a computer (from a game's "Fix this game, files and advanced").
- **Bring in what other tools left behind** (game lists, `.nfo` files, box art in media folders, remaps) or write WittyPlay's details back out for them.
- **A games library being scanned says so.** Consoles appear as the scan sorts games into them ("Finding your games: 1,204 found so far"), instead of a page saying there's no games library.
- **First-run setup sees your games folders.** Its Games step lists each games library with its consoles and how many games, instead of saying there's no games folder.
- **On a television,** console settings stay, and file and folder screens tell you to use a computer.

## Developer notes for 0.7.10

The same release in technical detail (not shown in What's new).

- **PlayStation games draw with Vulkan (#84).** SwanStation and Beetle PSX HW now take their automatic renderers, which follow the context the runner prefers: Vulkan where the card can make a device, OpenGL where it can't. Final Fantasy VII's opening on an RTX 3070, in pairs, Vulkan against OpenGL: SwanStation's 95th percentile 3.5 to 4.4 ms against 12.8 to 17.4; Beetle PSX HW's 10.4 to 13.0 against 24.9 to 53.1, on 19 to 34% of a thread against 46 to 75%. Pictures are right through the card's NV12 conversion.
- **Nintendo 64: ParaLLEl-RDP can be chosen.** Mupen64Plus-Next and ParaLLEl N64 are offered Vulkan, which GLideN64, their default, leaves alone; choosing ParaLLEl-RDP in a core's options now plays (the console's own drawing, on the card), with the internal resolution setting as its upscaling. It isn't the default: with its low-level RSP, Banjo-Kazooie took 50 to 67% of a thread against GLideN64's 14%.
- PSP stays on OpenGL: PPSSPP's Vulkan renderer measured slower (95th percentile 9.3 to 10.3 ms against 7.1 to 7.6). The 3DS couldn't be compared here.
- **Fixed: cores that run the game on a stack of their own died at random on Windows** ("morestack on g0"; Mupen64Plus-Next with ParaLLEl-RDP). They call back from that stack, which Go allows only on a thread it didn't create, so on Windows the core now runs on one, as on Linux.
- **Fixed: Mupen64Plus-Next crashed starting Vulkan.** The runner handed `create_device` no feature list; it now hands empty lists and features, as RetroArch does. And what the runner passes a core by address is kept off the stack (`go:uintptrescapes`, as purego does), since a core calling back during the call can move the stack.

- **Several pictures of each kind in a `.wittyplay`, and its version said out loud (#85).** Each artwork role in a `.wittyplay` holds one picture or a list of up to 32, the first shown: box art from several regions, several fan arts. A role with one picture is still written as a plain name. Gathering picks up numbered extras (`media/box2dfront/Game-2.png`, `Game-boxart-2.png`) after the first; writing out puts them back as `Game-02.png`... and never over a file already there. The spec gains a *Versions* section: a file from a newer version of the format is now read for what WittyPlay knows rather than refused, keeps the rest when saved, and is flagged (`newer: true`, and a note in the editor); a writer never lowers a file's version.
- **The `.wittyplay` editor adds more pictures of a kind, takes any one out, and shows another first.** It notes when a file follows a newer version of the format.
- **WittyPlay fits a computer's screen (#91).** In a desktop or laptop browser and the desktop app (not TV mode), the console UI is drawn at arm's length: normal-size type, header and notice, six or seven games a row on a 1440–2000px window instead of three, a small dock on a band of its own, and the chosen game's details whole and scrolling on their own. Small, medium and large change the games' size everywhere (5, 7 or 9 a row at 1440), and the chosen game is no longer drawn smaller than the rest in a grid. On a phone the details are a short band over the games. TV mode keeps its ten-foot sizes.
- **A pointer only points (#91).** On Consoles, All games and (with WittyMediaServer 0.8.1-alpha) Recent and the other rows, hovering no longer changes what's chosen, so the details stay put on the way to their buttons: a click chooses, a second click, a double-click or Enter opens. Arrows, a remote and a pad still move the choice.
- **Fixed: choosing a core (#91).** While a core downloads and after, the focus stays on it in the list (it was lost when Install and use went away, leaving a remote or a pad with nothing to move); the game's page and Consoles show the new core at once, not only once the picker closes; a second press can't start a second install; closing the picker puts the focus on Play.
- **The game menu fits the window (#91).** Its tiles share the height the header and key hints leave, so there's no scroll bar at any desktop, laptop, phone or TV size; only a window under about 600px tall scrolls, as a last resort.
- **A games library being scanned says so (#88).** Consoles show as the scan sorts games into them ("Finding games: 120 so far"), not only once the scan ends, and Consoles, Recent, Classics, a library's page and TV mode say "Finding your games: 1,204 found so far in Games" instead of that there's no games library. The scan's progress names the console it's sorting, and with WittyMediaServer 0.8.1-alpha each later step shows in its own words rather than sitting at "12,709 of 12,709 files".
- **Setup's Games step reads the server's games libraries (#95).** It listed only the libraries added during this run of setup, so a library added before it, or on a re-run, read as "no games folder". It now reads `GET /libraries` and each games library's counts from `GET /games/consoles?library=<id>`, shows WittyPlay's sorting progress while `/games/scanning` lists the library, then re-reads the counts every 5 s while the server still marks it `scanning`, and offers "Add another folder".

## 0.7.9

_Never published on its own: it came out in 0.7.10._

- **Core fixes: the runner corrects what a core gets, on this machine, as it runs (#76).** A core looks up every Vulkan function it uses through the runner, so the runner can hand it a corrected one (`libretro/vk_fixes.go`, listed in `corefixes.go`): nothing in a core's file is changed and no core is built here. On unless **Settings, Games, Advanced, Core fixes** is off (`gamingCoreFixes`), which lists each fix; off, every core gets what it would from any other frontend. `GET /gaming/core-fixes` lists them.
- **The first: GameCube and Wii draw with Vulkan.** Dolphin builds its stand-in swapchain at the size its glue asks for, then holds it to the surface's smallest and largest sizes. The runner's surface on Windows is a hidden window, which Windows makes 120 by 0 pixels, so every picture was 0 pixels high and every frame black (found by the runner's new `-vk-trace`, which logs what a core asks of Vulkan). The fix reports that surface as taking any size, as a server's headless surface does, and Dolphin draws at its real size. Dolphin is now offered Vulkan where the card can make a device, with core fixes on (the `VulkanWithFixes` trait); with them off, or with no card, it draws with OpenGL as before. On an RTX 3070 with the machine busy with other work, measured in pairs, Vulkan against OpenGL: Cars in a race, at native resolution, about as fast in the middle (5.9 against 4.8 ms, 5.8 against 8.4) and steadier at the slow end (the 95th percentile 11.6 against 13.4 ms, 12.0 against 17.6), the card 15% busy against 21%, and, as Dolphin on Vulkan hands over only new pictures, 30 encoded a second against 60 repeated; Mario Kart Wii at 3x (1920x1584): the 95th percentile 31 against 37 ms and 12.8 against 23.8, stutters 5 against 7 and 1 against 3, the runner on 44 to 55% of a thread against 39 to 44%. Its 10-bit pictures go through the card's NV12 conversion correctly.
- `wittyplay-probe frames -nv12` has frames converted on the card, as the server does.
- **On a television, WittyPlay's console settings stay and its file and folder screens point to a computer (WittyMediaServer #469).** Game libraries, custom cores and importing game pictures show the server's card (use the WittyMediaClient app on a computer, or a web browser) in TV mode; controllers, mapping, game and console settings, cores and the store are unchanged. Needs a server with the web host feature `tvLevels`; on an older one nothing changes.

## 0.7.8

_Never published on its own: it came out in 0.7.10._

- **A game's own `.wittyplay`, to keep beside it or to share (#75).** `.wittyplay` is now version 2, and a game's own file can carry everything WittyPlay knows or is told about the game. As an archive (a ZIP with `manifest.json` and its pictures) it holds:
  - **details:** title, sort title, title in its own script, year and date, overview, genres, developer, publisher, series, players, rating, age ratings, regions, languages and other catalogues' ids;
  - **pictures:** box front, back and spine, disc or cartridge, logo, marquee, banner, title screen, background, video still, icon and screenshots;
  - **which dump it was made for:** the file's name, size, CRC32, MD5 and SHA-1, so a file for another region or revision is flagged rather than applied;
  - **how it plays:** core, core options, shader, aspect ratio, video standard and starting disc;
  - **controls, cheats, notes on its saves,** who made the file, and translations keyed by language tag, as a `.wittyinfo` has.

  It is read as strictly as a `.wittyinfo`: limits on size, member count, packing and names; pictures must decode as what their names say; nothing is ever fetched or run; unknown fields are kept; and every problem is reported at once, with where it is. Version 1 files read as before, and a file with nothing new is still written as version 1. The spec and JSON Schema are in `docs/specs/` (CC0). `langtag` is the server's language-tag package.
- **Controls in `.wittyplay`.** For every game, per console or for one game, a file can give:
  - per-player button maps for gamepads, keyboards and mice, and the device in each port;
  - remaps and turbo buttons;
  - hotkeys: save and load state, slots, fast-forward, rewind, screenshot, menu and more, with a hold-to-enable button;
  - turbo rate and mode, stick dead zone and sensitivity, trigger threshold and rumble.

  A game's controls page now uses a file's player 1 maps as its defaults (`fromFile` has everything else). A person's own saved controls still come first.
- **Scans read a game's `.wittyplay`.** Its look always applies. When the file was made for this dump, or can't tell, its details and box art stand over an NFO and the sources, and its core becomes the game's core if none was chosen. A file made for another dump is flagged and not used. What each scan found is kept (`wittyplay_sidecars`).
- **Conversion both ways** (`gamefiles`): `gamelist.xml` entries, `.nfo` files, pictures in `media/` folders or named after the game, `.rmp` remaps and `.cfg` input overrides are gathered into `.wittyplay` files, and `.wittyplay` files are written back out to them. Writing out keeps every other entry, element and line, and replaces an `.nfo` or a picture only when asked. For the server's first-run setup and for administrators:
  - `GET /games/wittyplay/convert`: counts.
  - `POST /games/wittyplay/convert`: starts a conversion, `{direction: gather|writeout, source: files|library, overwrite, library}`.
  - `GET /games/wittyplay/convert/status`: progress.
  - `DELETE /games/wittyplay/convert`: stops it.

  One conversion runs at a time, game by game.
- **An editor for a game's `.wittyplay`** (`/wittyplay/game/{id}/file`, from a game's *Fix this game, files and advanced*), for administrators on a desktop or in a browser. It edits details, pictures, play choices, controls for each of eight players, hotkeys, cheats and save notes. Download shares the file; Import takes a shared one (one made for another dump is refused unless confirmed); Write out writes the standard files. In TV mode the page says that editing is on the desktop.

## 0.7.7

_Never published on its own: it came out in 0.7.10._

- **Performance profiles: one standard for what every emulator starts with (#78).** WittyPlay's first-run step (on every app and in the web wizard's Games step) and Settings, Games ask how strong the server is: **Very light** (Raspberry Pi 4 or 5, Intel N100, Celeron or Atom mini PCs, dual-core laptops, no graphics card), **Light** (four cores with built-in graphics, such as a Core i5-3470 or i5-7200U with Intel UHD 620/630 or Radeon Vega 8), **Balanced** (six cores and a mid-range card, such as a Ryzen 5 3600 or Core i5-8400 with a GTX 1650 or RX 570) or **High quality** (eight or more cores and a strong card, such as a Ryzen 7 5800X or Core i7-12700 with an RTX 3060 or RX 6700 XT). The profile sets each core's internal resolution (native on the light ones, a step above each console's usual on High quality), run-ahead, rewind memory, fast-forward limit, picture quality and how many games play at once. It only fills in what an administrator hasn't set by hand, and one console's core is changed on top of it in My game settings (Everyone, that console). With none chosen, games start as Balanced, as before; the step suggests one from the processor threads and graphics card it finds, and says why (`GET /gaming/profiles`, `PUT /gaming/profiles/{id}`, `gamingProfile`).
- Settings, Games no longer saves a number box that was only left, so leaving one doesn't pin the profile's value.

## 0.7.6

_Never published on its own: it came out in 0.7.10._

- **Frames come back from the graphics card as NV12, converted there (#74).** A frame drawn with Vulkan goes through a compute shader (`libretro/shaders/nv12.comp`, embedded as SPIR-V), one drawn with OpenGL through a fragment shader, into NV12, BT.709, limited range, which is what the encoders take: a third of the bytes of BGRA come back, cross the pipes and go into FFmpeg, which then has nothing to convert (its filter is `null`). VP8 and OpenH264 get yuv420p, VP8 in BT.601, as before. Screenshots and the JPEG stream some viewers fall back on convert it back on the processor. On by default; **Settings, Games, Advanced, Convert frames on the graphics card** turns it off (`gamingGpuFrames`), and it's off while frame stamps are drawn for measuring. OpenGL contexts older than 3.0 (ES 3) keep sending BGRA. Gran Turismo 4 at 4x internal resolution (2560x1792) on an RTX 3070: 60 frames a second against 45, the picture at the server in 1.6 ms against 5.0, the server side on 21% of a thread against 39%; Banjo-Kazooie at 1280x960: the server side 6% against 14%, encoding 7.4 ms against 8.9.
- A frame from a graphics card goes through a new `VideoReady` handler, so one handed over while the next frame is running isn't taken for that frame's and dropped.

## 0.7.5

_Never published on its own: it came out in 0.7.10._

- **PlayStation 2 games draw with Vulkan where the graphics card can (#72).** The runner can now offer a core a Vulkan context (`RETRO_HW_CONTEXT_VULKAN`), through the system's Vulkan loader, with no window: an instance, a surface on a hidden window (Windows) or a headless one (Linux) for cores that want one, a device made by the runner or by the core through its context negotiation interface (v1 and v2), and libretro's Vulkan interface. Each frame is copied into memory the processor reads, converted to B8G8R8A8 on the card first where it's in another format, and handed over without waiting, as OpenGL's frames are since 0.7.4. Cores whose traits ask for it (`Vulkan`, the runner's `-vulkan`) are offered it only where a real card can make a device, and `GET_PREFERRED_HW_RENDER` says so; anywhere else they have OpenGL, as before, and with no card at all the processor draws them. LRPS2's renderer is now "Auto", which follows that. Gran Turismo 4 in a race, from a saved state, on an RTX 3070: the core's time on a frame 11.4 to 12.8 ms against 15 to 17 on OpenGL, its 95th percentile 18 to 20 ms against 23 to 60, stutters 0 against 7 to 67, the card 41% busy against 63%, the runner 1.4 to 1.5 threads against 1.8 to 1.9.
- Dolphin stays on OpenGL: through this interface its Vulkan backend ran but handed over black frames (its own presentation, not the copy, which the tests check with its 10-bit format), and on OpenGL with fast memory GameCube already plays at full speed on a third of a thread.
- The graphics card check names the Vulkan card cores would get (`vulkan` in the GPU status), and `wittyplay-probe -vulkan` offers Vulkan to any core, to measure it.
## 0.7.4

_Never published on its own: it came out in 0.7.10._

- **Games drawn on a graphics card no longer wait for it every frame (#68).** A frame's copy back from the card was asked for and then waited on straight after the core's frame, which held the game thread (Dolphin's video thread) for all of the card's drawing, and on a busy machine for as long again to be woken. Now two pixel buffers take turns: the frame goes on when the card has finished it, checked without waiting, or in the time before the next frame is due; waits look again every 0.2 ms rather than spin a processor thread in the driver; and frames nobody will see (skipped, or run ahead) aren't read back at all. Banjo-Kazooie went from 25 to 60 frames a second on a machine kept at 93 to 100% by other work, its frames from 8.5 ms (70 at worst) to 2.3 ms (4.3). On an idle machine every game already kept its speed, and the runner now uses far less of the processor: PSP 66% of a thread to 16%, PlayStation 2 35% to 16%, Nintendo 64 40% to 13%.
- **Games come before background work on Windows.** Demanding games' runners ask for above-normal priority (they had normal), and WittyPlay's own process, which carries every frame to the encoder and the network, goes above normal while a game is playing and back afterwards (the server starts modules below normal). Game encoders start above normal. Browsers raise themselves above normal, and a game sharing the processor equally with them, a transcode or a scan ran at a fraction of its speed while using a fraction of a thread.
- **GameCube and Wii at full speed: Dolphin's fast memory is on for Windows and Linux** (`traits_fastmem.go`), with shaders compiled in the background (uber shaders drawing meanwhile), as #65 also sets for Windows: fast memory is the core's own default, which the runner had turned off everywhere. Cars (GameCube) in a race, from a saved state, on an i7-12700H: 25 frames a second of 60 before, 28 with the read-back alone, 60 with fast memory, the emulation thread from a whole core to a third of one. In WSL: 35 to 60. macOS stays as it was, untested.
- **GameCube and Wii play on a Linux server with no graphics card**, drawn through the processor's OpenGL (Mesa's llvmpipe) at the console's own resolution: Dolphin's software renderer is only in its debug builds, so before this they couldn't play at all there. Only Dolphin, and only where the server has no card it can use, gets the runner's new `-processor-gl`; every other core still refuses processor-drawn OpenGL, which is far too slow for them (Mupen64Plus-Next's OpenGL took 5.7 threads, angrylion about one). In an Ubuntu container with no card on an i7-12700H, through the sandboxed runner: Cars at 60 frames a second on 1.1 threads, Mario Kart Wii at 60 on 3.5. Such a runner gets a graphics driver's sandbox limits and memory, which llvmpipe needs too, and the Linux runner takes llvmpipe only after every real card, integrated ones included. Settings says so, with the speed measured.
- **The graphics card check says more,** and the log says what was found: the runner's `-probe-gpu` adds the driver's maker, its OpenGL version and, where NVIDIA's or AMD's driver says, the card's memory (`vendor`, `version` and `vramMb` beside `name` in the server's GPU status); WittyPlay logs the card, the processor threads, and the hardware and processor encoders it will use.
- `wittyplay-probe sweep` measures what a game costs: the runner's and the server side's processor time, the whole machine's, an NVIDIA card's load and its encoder's, the core's time per frame (median and 95th percentile, read-back included), the picture's way to the server and the encode time; and it keeps the run's label in its results. `-no-card` starts a game as a server with no graphics card would.

## 0.7.3

_Never published on its own: it came out in 0.7.10._

- **All games follows the layout switch** (#59): Grid lays each console's games out as a grid and List as a list, as on Recent. A console's games (from Consoles or All) and a search have the size and layout switches too, with Grid and List. Each console's shelf runs to the edge of the screen, so its pictures slide off the edge instead of being cut inside the page's margin.
- **A details panel beside the games** (#59) on All games, a console's games and a search, as on Recent's grid and list: the game pointed at or moved to, with its cover, logo, Play and its page, and what's known of it. A switch beside the layout buttons hides it, per profile; it's on to begin with, and not drawn under 760 pixels wide.
- **Highlights stay off the next game** (#59): the chosen game in Recent's grid no longer grows over the games beside and under it, and every ring around a chosen game, console, picture or button is thinner, inside the gap to the next one (the grid's gaps are a little wider). The server's focus-grow (25% bigger) no longer applies in a grid or list. The Dock's chosen button grows less. A game's logo in the details panel is centred, and a details panel scrolls its last buttons clear of the Dock. On Consoles, the console panel no longer touches the tab row's switches above it. The chosen tab's bar is one clean bar: the app's own bottom border on tabs, curved by WittyPlay's rounded links, drew a hook on top of it.
- **Narrower windows read better** (#59): a game's details panel keeps a tall cover (a PS2 case) inside its box instead of spilling over the title and buttons; between 760 and 1,199 pixels wide the details panel is narrower and the grid's games smaller, so two or more fit beside it; the status bar's backing is solid enough that what scrolls under it doesn't show through.
- **Console photos without their white box** (#61): a photo's plain background (most are shot on white) is taken away, once, when it's first shown, and kept beside it (`gamelogos.CutOut`: it fills inward from the edge through the edge's colour only, with a soft edge and the background's colour taken out of it, and leaves a photo whose edge isn't one plain colour as it is). On to begin with; Settings, Console photos has **Remove plain backgrounds** to turn it off. The credit says "background removed".
- **Background removal that leaves light plastic alone** (#61): the fill goes only through pixels nearly exactly the backdrop's colour, so a Famicom's cream or an SNES's grey is never made see-through; pockets of backdrop the edge can't reach go too, inside a cord's loop or its arch over a controller (any not tiny, as flat as a studio's backdrop: a white label or button has shading of its own); light specks left by JPEG noise go; and each edge pixel's opacity is read from its colour against the console's own colour just inside it, so dark controllers have no white fringe. Photos already cut are cut again.
- **Fixed photos for the Wii, Switch and Switch 2** (#61): the Wii's photo on Wikidata is a white console on white, which no cut-out can tell apart, and the Switches had none to find. They now have one free photo each (the Wii's and Switch 2's already see-through).
- **A console's name follows its version** (#61): choosing the Famicom calls the NES the Family Computer (Famicom) on the Consoles tab, in a library's rows and on game cards, and so on for the other versions; **Call it Famicom everywhere**, under the photo, keeps its usual names when turned off. Until a version is chosen nothing is renamed. A console's other short names come with the console list (`also`), so a page finds it by any of them.
- **A console's version, for its photo** (#61): the NES or the Famicom, the SNES or the Super Famicom, the Genesis or the Mega Drive (Europe or Japan), the PC Engine or the TurboGrafx-16, each its own free photo on Commons. The first is shown to begin with; an administrator chooses under the photo on the Consoles tab (`PUT /console-photos/{id}/variant`), and that version's photo is fetched instead.
- **Smoother games on Windows** (#64): the game runner opts out of Windows' power throttling for itself, so Windows 11 doesn't treat it as background work (efficiency cores, a coarse timer) for having no window; Dolphin compiles shaders while drawing, with its uber shaders standing in (`dolphin_shader_compilation_mode` 2), instead of stopping each frame that shows something new.
- **The Wii Remote's pointer works from a browser** (#63): over a Wii game the mouse (or a finger) is where the Wii Remote points, and a click presses A. Dolphin's core reads the browser's pointer only in its "Mouse controls pointer" mode (`dolphin_ir_mode` 2: the core's values are codes), now always set; a layout marked `aim` sends where the pointer is as it moves, not only while pressed, and its `aimPress` button is held while it is.
- Tests: the emulator tests check Unix file modes only where there are any, so they pass on Windows (#48).

## 0.7.2

- **A virtual gamepad plays a game in the tests.** `web/tests/virtualpad.test.ts` stubs the browser's Gamepad API (`navigator.getGamepads`) with a standard pad, presses Start, Select, A and the left stick through the web player's input, and checks each state it sends; `host/virtualpad_test.go` plays those same states into the game runner with the test core and checks the game took them (Start counted, the LED and rumble, the square moving). Both read `testdata/virtual-pad.json`, whose NES controls are checked against the console table, so CI covers the way from a pad to a game with no server.
- **Friends: invites, and batteries.** Someone playing a game invites a friend by name from Together (`POST /games/online/friends/{user}/invite`; Invite, then Invited with Cancel the invite, on the friend's card); the friend sees it under Invites, and in the status bar, with Accept (the game's page with `?invite=`, which joins as a seat) and Decline. An invite lasts ten minutes or until the game ends. A friend whose room is open has Join on their card, and someone who joined a room has Play (`/game/{id}?join={session}`, which the web player now plays as a seat). New commands `wittyplay.friend.invite`, `wittyplay.invite.accept`, `wittyplay.invite.decline`, `wittyplay.invite.cancel`, and source `wittyplay.invites`. The status bar and the game menu show each controller's battery where the browser gives one, and this device's (`navigator.getBattery`), as a level in words; one running low is shown over the game and said once, politely.
- **Refresh game details, in one click.** An administrator refreshes a whole games library (its page's Refresh game details, `POST`/`GET /libraries/{id}/details/refresh`) or one game (its page, `POST /games/{id}/metadata/refresh`): every source is asked again from the start, so titles in their own script and in English, box art, logos and details come back fresh, and every picture is downloaded again. A match picked by hand and fields people typed stay. It runs as the host task "Finding out about games", and the library's button says how far it has got. The item editor's Refresh and a Full rescan (`scan.describe` with `refresh`) do the same. On every app: the SDUI source `wittyplay.refresh` (a row on `wittyplay-library` and, inserted, on a game's `detail` page) with the commands `wittyplay.library.refresh` and `wittyplay.game.refresh`.
- **Console photos from Wikimedia are off until an administrator opts in.** They used to follow the brand-logo agreement. Now they have a switch of their own, beside a notice saying what is fetched (each console's name to Wikidata, one photo from Wikimedia Commons), from where, and the licences (Creative Commons or public domain, with author and licence shown): in WittyPlay's Settings (Console photos) and its first-run step, on the web and in the SDUI pages `wittyplay-settings` and `wittyplay-setup` (source `wittyplay.consolePhotos`, commands `wittyplay.consolePhotos.on` and `.off`, `GET`/`PUT /games/console-photos`). Nothing is fetched while it's off, and turning it off deletes the photos kept. The first-run step's version is now 2, so it shows again.
- Finding out about games and looking for console logos show in the server's activity panel again. Their reports named the task by its title ("Finding out about games"), which the host's tasks route refuses, so neither ever showed; they're `game-details` and `brand-logos` now, with the title sent beside the name.

## 0.7.1

- The first published release on module API 5, with the WittyModule 0.5.4 workflows. The tag v0.7.0 was made with workflows that couldn't build it and was never published.
- `npm test` skips a web test that builds a screen from the app's own parts (`specs.test.ts`) when there is no WittyMediaServer checkout, and says so, so the module's own CI can run before the server it is for is published; beside a checkout every test runs.

## 0.7.0

- Built on WittyModule 0.5.4 and module API 5; its workflows use WittyModule 0.5.4.
- Repair: WittyPlay answers the server's `doctor.Check` with its own checks (`capabilities.doctor`): a Mac's missing Screen Recording or Accessibility permission for the native host, and a backend that can't run. `docker.Summary`, which the server used to call by name, is gone.
- **Settings, Consoles & cores keeps its settings in WittyPlay.** Which version of a game comes first, picture quality, games at once, the limits on fast-forward, rewind and run-ahead, the runner, the room for unpacked games and the network were written to the server's `/settings`, which had stopped keeping them, so a change was lost. They are WittyPlay's own now (`wittyplay_config`), at `GET`/`PUT /gaming/settings` (administrators): every one as text by name, checked all together before any is kept, `""` unsetting one.
- **Scans say how far they've got, and a full rescan reads every file again.** While WittyPlay works on a scan call (a batch of files, the last call that puts them together into games, a batch to describe) it reports its progress as the host task `scan-<library>`, which the server shows on the scan and takes as a sign the scan isn't stuck: only when the work moves, at most once a second, one report at a time. When the server's host libraries say a library's scan is a full rescan (`scan: {mode: "full", since}`), every file is worked out again from the start, once for each run, so a full rescan stopped by a restart carries on from where it got to; what people said about files stays.
- **The first-run step on every app.** An administrator's Home says "Finish setting up WittyPlay" until the step is done: the page `wittyplay-setup` (Cores & emulators, What this server plays, WittyPlay options, then Done or Do this later, the commands `wittyplay.setup.done` and `.later`, `POST /setup/games`). The browser's setup wizard keeps its own step for game titles, pictures and metadata keys.
- **All games on a games library's page lists its games**, with genre, "score at least" and order (title, added, rating, oldest release): `wittyplay.library` with `every` and `filterable`.

- **The library and Cores & emulators on the Apple TV and Android TV apps** (and the phone apps), as SDUI pages they draw themselves: `wittyplay-library` (every games library's page: Continue playing, a shelf for each console, All games with a filter bar; source `wittyplay.library`), `wittyplay-store` and `wittyplay-store-console` (for administrators: each console's core as a choice, its cores to install with the licence accepted in the row's words, update or remove, a download's progress at the row's end, its BIOS files; the standalone emulators to install, update, stop or remove; sources `wittyplay.storeConsoles`, `wittyplay.storeEmulators`) and `wittyplay-settings`, which the dock's Settings opens. WittyPlay Online gains Consoles and All games in the apps. The rows run WittyPlay's own commands (`commands` in `sdui.json`: `wittyplay.core.install`, `.core.remove`, `.console.core`, `.emulator.install`, `.emulator.cancel`, `.emulator.remove`) on its existing routes. Custom cores, which need a password and a file, stay in the browser, and a row says so.

- **WittyPlay owns the whole game API** (module API 5). Starting and playing games, saves and battery files, seats and joining with a code, rooms and screenshots, a person's game settings, cheats, patches and bezels, friends and Kids profiles' friends, the games home screen and a game's check, consoles and modes, cores (installs, custom cores and custom consoles, which core a console or game uses), BIOS files, GPU and platform checks, standalone emulators with Docker and KasmVNC: all answered by WittyPlay's `api` package at `/api/v1/modules/wittyplay/…` (the server's old paths, under the module's prefix), kept in WittyPlay's own database and folder, starting empty. The server checks sign-in, the game, screen time and, for custom cores, the person's password (`confirm`) before passing a request on. Each game is a host session, so the server still counts screen time and time played and ends a game when its sign-in ends or the time is up (`session.stop`). The engine runs in-process: no server call goes through the facade for these any more, and `rpc.Routes` is gone (`rpc.Start` runs Docker's clean-up). New packages: `gamearchive` (archives, unpacked cache; reads .rar with nwaples/rardecode, BSD-2-Clause) and `appcontrol` (Windows Smart App Control). A game's details, matching, review and pictures are still the server's until its scan hands games over. Needs WittyMediaServer 0.9.0-alpha and WittyModule 0.5.0.

- **One way to stream a game.** The stream's first version is gone: `stream.Session.Answer` (`POST /play/{sid}/webrtc`), `Session.ServeWS` and the module's `GET /ws/{sid}` route. Every client connects with a hello, through `Session.Connect` (`POST /play/{sid}/stream`) or `Session.ServeStream` (`GET /play/{sid}/stream`); the WebSocket modes ws-h264 and ws-jpeg are that WebSocket asking for H.264 or JPEG. The player's host gives `stream` and `streamUrl` only, and wittyplay-probe always connects this way (its `-v2` flag is gone).

- **Controller layouts, controller profiles and the controller screen's picture are WittyPlay's** (module API 5, the first of its APIs to move out of the server). WittyPlay answers `/api/consoles/{console}/controls`, `/api/game-controllers`, `/api/game-profiles…` and `/api/game-background…` itself (`api/`), at its own addresses (`/api/modules/wittyplay/consoles/{console}/controls`, `/game-controllers`, `/game-profiles…`, `/game-background…`), which the server passes to it once it has checked who is asking. They're kept in WittyPlay's own database (`store/`, `wittyplay.db` in its own folder), starting empty; an account that goes takes its data with it. Needs WittyModule 0.5.0.

- **Cores & emulators, one store inside WittyPlay.** WittyPlay → Settings → Cores & emulators holds consoles and their cores (installs, updates, per-console and custom cores, BIOS files, region order, streaming), what each console plays here, and standalone emulators, as tabs of one page; the addresses each had (`/wittyplay/settings/consoles`, `support`, `emulators`) open their tab. The server's Settings shows one row, Games & emulators, which opens it (`launch`), and its own addresses for these go there too. The core and emulator machinery is unchanged.

- **WittyPlay 0.5.0: every WittyPlay screen is in WittyPlay (module API 5).** WittyPlay's frame (status bar and dock), its pages (consoles, libraries, game pages, settings, game pictures), the game page, the controllers screen, its four sections of the server's Settings, its first-run step and its TV screens come with the module now, not with WittyMediaServer, so they update from the module store. They look and work exactly as before: the same code, drawn with the server's own components (`@wms/app/…`), checked page by page against 0.4.2 on the same server. `integration.json` says what WittyPlay adds to the web app; `register` in `web/app/register.tsx` hands it over. The bundle is built with WittyModule's `kit/web/build-bundle.mjs` (this repository's own copy is gone). Needs a server of module API 5; with an older WittyPlay, a server draws its own copy as before. Games' APIs and data are still the server's (they move in 0.6 and 0.7).

- **WittyPlay 0.4.2: the game menu and the launch screen look like the games pages.** Deep indigo, a mint pill for what to do next, round chips for the rest, big bold titles, cards on a faint white and the same focus (a dark ring, then a white one) as the server's game page, sized to fill 4:3, 16:10, 16:9 and 21:9 screens. The menu's tiles carry their icon in a round chip, and Resume shows the button that closes the menu (B, Esc or Back). Save and Load open as a side sheet, **Save states**, like the game page's: big screenshots with their slot number, when each was made in words ("12 minutes ago", the exact time on hover), a cue (Load, Save here, Save over, Replace it?) and round Download and Delete buttons. The launch screen puts the console in a mint chip, the steps on a card and the unpacking as a percentage, and what went wrong reads the same way. Every tile, panel, key and way out is as before; reduced motion and high-contrast modes are respected.

- **WittyPlay 0.4.1:** The menu entry is called Games, and the dock's Settings opens WittyPlay's whole Settings.

- **Logging the server can read.** The helper writes one slog stream to stderr, at the level and with the topics chosen in Settings, WittyLog (`WITTYMODULE_LOG`, `WITTYMODULE_LOG_TOPICS`; `capabilities.logTopics` lists `core`, `runner`, `capture`, `firmware` and `settings`). Games carry `cat=games`, standalone emulators `cat=emulators`, and each line about a game its `session`. New lines: a game session starting, being stopped and ending (how long it ran, why), viewers coming and going, a relay starting, an emulator starting and stopping on Docker, the local sandbox or this machine (its window, its encoder, bitrate changes, a kill when it won't stop), firmware found by name, settings saved by name, and an emulator added by hand (how it was recognised) or not recognised (why). Installs, uninstalls, licence acceptance, hash mismatches, unverified installs and own programs saved are kept whatever the level (`audit=yes`). A Mac missing Screen Recording or Accessibility says so as `macOS permission missing`, with `permission` and a `fix`. A crashed emulator's last words are `stderr`, with key material hidden; download errors never repeat an address's login or query. The half-minute streaming line is quiet unless a game falls behind. `docker.Summary` tells the server how standalone emulators stand, with no paths or addresses.

- **Module API 4, with its own server-driven UI** (`sdui.json`, written from `ui.UI` and held to it by a test). A row on the Games page and a page of its own, **What this server plays**: every console, whether it plays here and how (a core, or a standalone emulator), answered by the helper (`wittyplay.consoles`). A section for the page editor, the Games accent colour, and settings under Settings, Modules, Games: the row for everyone, and each profile's "only consoles that play here". Needs WittyModule with module API 4.
- **GameCube, Wii, Wii U, PlayStation 3, Xbox and Switch are consoles of their own** (`gc`, `wii`, `wiiu`, `ps3`, `xbox`, `switch`), with their files, folder names, controllers and firmware. GameCube and Wii are offered Dolphin's libretro core (published for Windows, Linux and both kinds of Mac), with its support files from the same build server, though on a Mac it stalls after its first frame (docs/PLATFORMS-AND-PLAN.md), and standalone Dolphin; the rest name the standalone emulators that play them. The server recognises each from its files' own bytes and moves `custom-gamecube`-style consoles over by itself.
- **Standalone emulators on the server's own desktop** (`docker.runtime: host`, the default on macOS and Windows; `sandbox/host`): on a Mac, sandbox-exec, ScreenCaptureKit and key presses sent to that process alone; on Windows, a job object at low integrity, Desktop Duplication, WASAPI loopback and SendInput. Same relay, same WebRTC, same bitrate steps; hardware encoders where the machine has them. A Mac's server needs Screen Recording and Accessibility permission, and the page says which is missing. See [docs/STANDALONE-EMULATORS.md](docs/STANDALONE-EMULATORS.md); it hasn't played a game yet, for want of those permissions here and of a Windows machine.
- **Current versions, for every system:** PCSX2 2.8.2, Dolphin 2609b, PPSSPP 1.20.4, melonDS 1.1, Flycast 2.7, xemu 0.8.139, Cemu 2.6, RPCS3 0.0.43-20582, each pinned and hashed for macOS and Windows as well as Linux (melonDS for FreeBSD too). `.7z`, `.tar.xz` and Mac disk images are opened under the zip's rules.
- **The Switch and the Switch 2, by emulators added by hand.** Ryujinx (Switch) and Lichii (Switch 2 and Switch) are in the list with everything but a download: their command lines on each system, every setting from their own configuration, their controllers, where their keys and firmware go. The owner installs one from its own project and adds it in Settings, Standalone emulators: found where people install it or in the module's drop folder, or by path; checked to be that emulator (a Mac app's bundle identifier, a program's name and the files beside it) with its version noted. `emulators/manual.json` does the same for a server with no screen or in a container. Keys and firmware are copied from the owner's BIOS folder into each person's emulator folder, never fetched. The Switch 2 is a console of its own (`switch2`), known by its folder or library.
- **Docker hosts for a server in a container:** `docker.hostPaths` turns the server's folders into the Docker host's, so a standalone emulator's container finds the game, the saves and the emulator when WittyMediaServer itself runs in Docker.
- **Every player's controller works without setting anything up** (`emulators/inputmap`): their keys are written into each emulator's own controller settings, in its own notation on each system, for up to four players where the emulator allows.
- **Every emulator setting**: schemas generated from each emulator's own configuration (`cmd/wittyplay-emuschema`, `emulators/schemas`), keys found in a person's own file offered too, written into INI, TOML, YAML, XML or JSON files without disturbing the rest (`emulators/configfile`).
- **What plays where**: [docs/PLATFORMS-AND-PLAN.md](docs/PLATFORMS-AND-PLAN.md).

- **Standalone emulators, optional and off by default.** An administrator can turn them on in the server's Settings, Standalone emulators. PCSX2, PPSSPP, Flycast, melonDS and others in `emulators/manifest.json` are downloaded from their own projects' releases, each run one per game in a hardened Docker container or, with `docker.runtime` set to `local`, a bubblewrap sandbox, and streamed over WebRTC. A game goes to one only when someone chooses **Play with …** on its page; libretro cores are untouched. No emulator, game, BIOS or key is bundled. See [docs/DOCKER-BACKEND.md](docs/DOCKER-BACKEND.md), which ends with **what hasn't been run**: this has tests against a stand-in for Docker, and has not yet played a game on a real Docker host.
  - **Starting a game is the server's to ask** (`docker.Launch`), with a file it has checked the account may play. A browser never names a file. WittyPlay refuses in a sentence when the emulator isn't installed, the machine is too small for it (a Raspberry Pi 3 is told so for every one), firmware is missing, or the limit on emulators at once is reached.
  - **Containers** run as uid 1000 with a read-only root, every capability dropped, `no-new-privileges`, default seccomp, pids, memory and CPU limits, an image pinned by digest (or by its id, for one built on the machine), one game mounted read-only, and no mounts but the four WittyPlay makes. `Check` refuses anything weaker before it reaches Docker, and tests hold each property in place. A daemon's socket is never mounted, whatever its name.
  - **Reaching Docker is reaching the machine**, when its daemon runs as root. The docs and the settings page say so, Diagnostics says whether the daemon is rootless, and rootless Docker, Podman and the local sandbox are the ways round it.
  - **Downloads** are pinned to a version, a size and a SHA-256 (checked on 2026-11-11), carry on where they stopped, need the licence named in the request, run as background jobs with progress and can be cancelled, and are chosen for the machine containers run on rather than the server's own. PPSSPP 1.20.4, RPCS3 and melonDS for arm64 were added. An install recorded as unverified is never run. The base image's own inputs (both `FROM` images and KasmVNC's package) are pinned too.
  - **Settings for each emulator, as data.** `GET /docker/emulators/{id}/settings` gives a schema (kind, label, help, limits, choices, who may set it), the administrator's, the person's and the game's values, and what they come to. They're checked on the way in and written into the emulator's own files, through `os.Root`, before each start. Stream size, frame rate, encoder, bitrate and each player's keys are settings of every emulator.
  - **Sessions:** one game each, pause and resume, a list with stop for administrators, an idle limit, a longest time, stopped after two minutes with nobody connected, an `ended` event saying why, and what a crashed emulator said last in the log. Containers a crash left behind are removed at startup.
  - **The stream:** the bitrate steps down when a browser reports lasting loss and back up after a clean stretch; four players' pads become keys, each player's their own, with a key map an administrator can change. Still no analogue, no rumble, and the emulator's own settings have to know the keys.
  - **The desktop fallback (KasmVNC) is boxed in.** Its page comes out of the container, so it's served with a sandbox policy that keeps it off the server's origin, shown in a frame without `allow-same-origin`, and reached by a per-session ticket in its address, since such a frame carries no sign-in.
  - Emulators name the library's own console ids (`ps1`, `dc`), and one for a console WittyPlay doesn't know (GameCube, Wii U …) plays a custom console named for it (`custom-gamecube`).
  - `docker/images/`: a base image (Debian slim, Xvfb, KasmVNC, PulseAudio, FFmpeg, xdotool, the input agent), an example Flycast image, and how to build and pin them. Not published yet.

- **Every menu over a game is easy to leave, on a phone and a computer.** The game menu, its settings, core options, saves, discs and connection panels keep **Back** and **× Back to the game** pinned at the top as they scroll, and the controller screen keeps **Done** in view. A phone's Back gesture, the browser's Back and a mouse's back button now close a menu a level at a time instead of leaving the game, and Esc, a controller's B and a TV remote's Back do the same. The touch-controls editor leaves the same ways, as its Cancel, and keeps Cancel on screen with its options hidden. Once everything's closed the game carries on, with the keyboard and controllers its own again. Settings opened for one console (from its page) start on that console's games.

- **Nine more cores.** QuickNES (NES, the lightest), today's bsnes (Super Nintendo, the most accurate), Gearboy (Game Boy), VBA-M and gpSP (Game Boy Advance; gpSP has a BIOS of its own built in), Beetle PCE and Beetle SuperGrafx (PC Engine), Beetle Lynx (Lynx) and RACE (Neo Geo Pocket). Each is free to use commercially, by its licence in libretro's core info, and comes from libretro's build server like the others. All nine installed from it on macOS on Apple silicon and opened in the sandboxed runner, and bsnes played 16 retail Super Nintendo games, DSP-1, Super FX and SA-1 games among them, with no firmware files. Beetle Lynx won't start without the Lynx's boot ROM, so its traits say so, and a game gives way to Handy when there isn't one.
- **Core options says which core plays the game, with Change core.** An app that gives the player `chooseCore` (the server's website does, for administrators) gets a line at the top of Core options; its picker answers restart, later or none, and on restart the game starts again on the new core, without resuming, as a state from another core won't load in it.
- **An install says how far it has got.** `cores.Manager.InstallProgress` gives the bytes received and the download's size while `Install` runs, from another call to the module, so a page can show a progress bar.
- **Firefox: "Waiting for the picture…" no longer stays up over a game that's playing.** The player took a count of pictures shown that Firefox keeps at 0 for a WebRTC stream, so it said the picture had stopped 1.5 s in, and never that it had started again. It now counts with `requestVideoFrameCallback`, and only says the picture has stopped when the connection's own stats say nothing new is being decoded either. Seen at phone and desktop sizes; the WebSocket ways of connecting were never affected.
- **WittyPlay 0.3.6:** a Mac's graphics card draws PSP, PlayStation and PlayStation 2 games, at full speed on a fraction of a processor core, and their picture now reaches the stream.

- **Games drawn on a graphics card can use a Mac's own.** The runner makes an OpenGL context with CGL, with no window, on the Mac's graphics processor (on Apple silicon, OpenGL runs through Metal). macOS stops at OpenGL 4.1 core and has no OpenGL ES, so a core asking for more is told "no usable graphics card" at once. The GPU check names the card ("Apple M3 Pro"), and the hardware frame test passes on one. Played through a test server with the owner's own games on an Apple M3 Pro: PPSSPP (God of War: Ghost of Sparta), SwanStation and Beetle PSX HW (Final Fantasy VII) and Play! (Final Fantasy X, Gran Turismo 4) draw on the card at full speed, PSP on a quarter of a core where its software renderer took three to five. GLideN64, ParaLLEl N64's OpenGL plugins and LRPS2's OpenGL renderer don't draw correctly on a Mac, and the server keeps those on the processor. A frame a Mac's OpenGL won't map (after PPSSPP's OpenGL renderer has run) is copied out of the pixel buffer instead, so its picture reaches the stream.
- **PlayStation games start on a Mac.** They stopped at "the game took too long to load". PCSX ReARMed asks to be traced by its parent on every Apple system but tvOS, for iOS's sake, and a traced process stops at every signal until its parent lets it go on. The Go runtime signals its own threads several times a second to preempt goroutines, so the runner froze within a second of loading. A runner on a Mac now starts with `GODEBUG=asyncpreemptoff=1`, and FF7 runs at full speed.
- **No graphics context is promised where none can be made.** On macOS and FreeBSD the runner said yes to a core asking for OpenGL, then failed once the game had loaded, and some cores crashed. It now says no at once, so PPSSPP and LRPS2 use their own software renderers, and other cores stop with "no usable graphics card" instead.
- **A game that won't load on a Mac says why.** The runner can't read a C log call's arguments on an Apple arm64 Mac, so a core's log line there is only its format, and "the core could not load this game: %s" was all anyone was told. A line like that is no longer taken for the reason, and what the core puts on the screen while the game loads now is: Flycast's "Invalid CDI file …", for one.
- **Version 2 of the stream: one way in for every device.** A hello and an offer through one route, answered with what the device gets; the same messages on WebRTC's data channels and on a WebSocket. The server's `docs/GAMING-STREAM.md` is the protocol. With it:
  - **Seats:** each device holds its own players. Another device's presses for them are ignored, rumble goes only to the device holding that player, and a player let go has every button released. A phone can join with `"watch": false`, as a controller with no picture or sound.
  - **The WebSocket fallback keeps up with a slow network.** Only two frames may wait; past that one is dropped, a keyframe is fetched, and the bitrate comes down, from how late the client says frames arrive. On a 3 Mbit/s link with 40 ms of delay, press to picture went from 2 to 2.8 s to about 60 ms at the median, as over WebRTC. Its sound is Opus where the browser or app can decode it: about 100 kbit/s instead of 1 to 1.5 Mbit/s of PCM.
  - **Games keep to the screen.** A device says its refresh rate, and a game within 1% of it (a 60.0988 Hz NES game on a 60 Hz screen) runs at the screen's rate, with its sound resampled to match, so no frame is dropped every ten seconds.
  - **Away from home starts lower**, at half the bitrate, and goes up within seconds when the network carries it.
  - Stats once a second for a device that asks, and refusals and endings with a code to act on.
  - **Everything a full controller carries.** Motion reaches cores through libretro's sensor interface (on macOS and Linux,
    x86-64 and ARM64), and the stream says which players' motion a game wants. Several fingers on a touch screen at once.
    The game's lights come back as `led` events, through libretro's LED interface. Rumble names its motor, strong or weak,
    and says how long it lasts; it's renewed while it does, so a dropped connection can't leave a pad shaking, and a
    paused game's motors stop. Up to 8 players, for cores with a multitap.
- **`wittyplay-probe` measures the WebSocket modes through a network too:** `-impair` now applies to TCP, with loss as TCP recovers from it, and jitter on UDP keeps packets in order, as one path does. `-v2` connects as the player does with version 2.
- **PlayStation 2 `.bin` files on Play!:** Play! crashes when handed a PS2 disc kept as a `.bin` without opening a `.cue` sheet. WittyPlay now steps aside to LRPS2 when available, or refuses cleanly naming LRPS2 and `.cue` sheets in `wittyplay-check`, `wittyplay-probe`, and streaming sessions ([#31](https://github.com/wittywilla/WittyPlay/issues/31)).
- **WittyPlay 0.3.5:** steadier streaming on a busy machine, and `wittyplay-probe` builds on macOS and FreeBSD, so CI is green again. There it reads a process's memory from `ps`, and has no count of open files.
- **"The connection dropped" no longer comes up for a picture that's only late.** The player started a new connection whenever the picture stopped for 4 s, which on a busy machine was every few minutes, though the connection was fine. It now waits while the server's own watchdogs bring the picture back, says it's waiting, and starts again only when the connection has really gone. A reconnection that takes under 1.5 s says nothing. Over 19 minutes on a busy machine: 7 drops before, none after ([#16](https://github.com/wittywilla/WittyPlay/issues/16)).
- **A busy machine no longer makes the picture worse, and stop.** The server took a round trip that was slow for a moment for a full network, cut the bitrate, and started the encoder again, which stops the picture briefly. Round trips now have to stay slow for four reports in a row, and one over a second doesn't count; loss counts as before. Over 19 minutes on a busy machine: 22 bitrate changes and 45 encoder starts before, 6 and 8 after ([#21](https://github.com/wittywilla/WittyPlay/issues/21)).
- **No black flash on a reconnection.** The new picture goes in underneath, and the old one stays up until the new one is on the screen. Reconnecting every 10 s for 4 minutes: 12 black flashes before, none after ([#17](https://github.com/wittywilla/WittyPlay/issues/17)).
- **A paused game costs less.** With a menu open, its still picture is sent again four times a second rather than twenty, each one encoded again ([#22](https://github.com/wittywilla/WittyPlay/issues/22)).
- The web bundle is built with vite 8, as the server's app is, and esbuild is a development dependency of its own for the tests and the probe page ([#29](https://github.com/wittywilla/WittyPlay/issues/29)).
- **The picture no longer stops for a keyframe, a new bitrate or someone joining.** Each of those needs another encoder. The one in use now carries on until the new one's first frame, where the picture used to stand still while FFmpeg started: 160 to 930 ms here ([#32](https://github.com/wittywilla/WittyPlay/issues/32)).

- **WittyPlay 0.3.4:** 0.3.3 was tagged and never released: a sandbox test failed on GitHub's runners, which don't allow a private mount. Its fallback was always there.

- **WittyPlay 0.3.3:** Homebrew built with libdragon plays on the Nintendo 64: it goes to ParaLLEl N64 with the settings it needs, and without that core the message says so.

- **Cores install from the store again on Linux.** A module's sandbox refuses chmod, and installing a core used it, so every core stopped with "operation not permitted".
- **melonDS starts on a current Linux.** glibc 2.41 and later refuse a library asking for an executable stack, as melonDS's does by an accident of how it's built. It now loads from a private copy without that request, and no core ever gets an executable stack. It also needed room for the address space it reserves, and shared memory, or it crashed while loading.
- **Play! starts on Linux.** It stopped at once for want of a home folder. It has its save folder as one, and its memory cards are kept there.
- **SwanStation and PPSSPP start on Linux under the sandbox.** They need shared memory, as melonDS and LRPS2 do. The PSP had no way to start under the sandbox at all.
- **Shared memory is private.** A core that needs `/dev/shm` now gets an empty one of its own: nothing else can reach it and nothing is left behind. Where the system doesn't allow that, it has the system's, as LRPS2 did before.
- **PlayStation and Dreamcast discs on Linux:** a `.cue`, `.gdi` or `.m3u` naming `GAME.BIN` beside `Game.bin` didn't open on most cores, as Linux is particular about capitals. Such a game gets a folder of links under the names its sheet uses, and the library is left alone.
- **Dreamcast memory cards now last.** Flycast kept them in the system folder, which a running game can't write to. They're in the game's save folder.
- `wittyplay-probe` has `sweep`, which plays a game through the real stream and says whether time passed; `test-cia` and `test-rom`, which write small programs from nothing but hand-assembled instructions, for consoles with no game to hand; and `cia-info`, which prints what a 3DS package's own bytes say about whether it's encrypted.
- The patches panel links to the guide in Help. The message about an encrypted 3DS file no longer suggests where else a game might come from.
- **PlayStation 2 runs under the sandbox on Linux.** Its core crashed on loading, as it needs `/dev/shm` to map its recompiled code. It's asked for by name, for that core only. It keeps its shader cache beside the save between games, where it used to be rebuilt every time.
- **On a Mac, the sandbox let no game start.** It refused every program, including the runner it was about to start, and its rules named paths through links, which match nothing. Found by reading: this hasn't been run on a Mac.
- **FreeBSD:** a game with no graphics card can't start programs or open a socket. That isn't a sandbox, and custom cores stay refused.
- **Security:** a core fetched by address can't reach shared address space, or a private address wrapped in IPv6 for a gateway to unwrap.

- **Latency:** from a finished frame to the screen went from 65 to 235 ms to 9 to 22 ms, and from a press to the screen from 75 to 270 ms to
  11 to 31 ms, or about 50 on PlayStation 2, which takes most of a frame to run. Those are medians with the server and browser on one machine;
  a wired network adds about 1 ms, Wi-Fi more. The browser no longer holds frames back, sound can't hold the picture back, FFmpeg's queues can't fill,
  and a new press starts the next frame at once. Pads are read every 4 ms. Frames land just before the display wants them where a browser shows it helps.
- **Run-ahead** is on by default for one frame on the older consoles, which takes a whole frame off a game's own reaction. It's a setting.
- **The picture:** PlayStation 2 is native size by default, which ends the bands and grid in Gran Turismo 4. Colours are right on every console:
  red was coming out orange. Pixel art is sharp from the first frame, and pictures of 640 wide or more are scaled smoothly.
- **Speed:** PlayStation 2 goes from about 40 frames a second to 45 to 60 in a race.
- **Poor networks:** bitrate follows the connection, keyframes can't flood a slow link, and a browser's request for a keyframe is answered.
- **Reliability:** a stuck or dead encoder, a dropped network and a stalled picture all come back by themselves, with the last picture up
  until there's a new one. A dropped connection used to step down to a worse way of connecting for the rest of the game.
- A game that's gone from the server, because the server or the module restarted or this device was away too long, starts again from its last save,
  in place of a frozen picture for a minute and then a message about the network. If that happens twice in two minutes, it says so instead.
- Battery saves reach the disk within 5 seconds of changing, from 30, so a machine that dies loses less.
- SameBoy starts: it gives its sound at the Game Boy chip's own rate, over 2 MHz, which is now averaged down.
- **On-screen numbers** say where the delay goes, stage by stage, from a press to the screen, and what's holding things back when something is.
- **Speed or steadiness**, under Speed and rewind, with a good automatic choice.
- **WebRTC, drawn directly**, under Connection: for those who want to try it. It can tear.
- `wittyplay-probe` measures all of it on real frames, in a real browser, and can run through an impaired network, for hours, or killing each process in turn.
- WittyPlay is now a service of its own, with nothing compiled into a server: `wittyplay-module` holds the games being played and answers a server's calls, and `wittyplay-host` is now called `wittyplay-game-runner`.
- The display preview needs no inline style, so the page policy can leave it out.
- **Security:** a core can't remove a file's access list or other extended attributes, or use `userfaultfd`.
- **Security:** one game takes at most 8 viewers and 4 connecting at once. Past that the oldest goes, so someone allowed to play can't run the machine out of sockets and memory.
- The controller screen and the game settings screen are redrawn to match the host app: tabs along a line, chips to choose from,
  cards, switches and sliders in the app's own shapes, button prompts along the bottom, and 44px targets on a phone. Choosing a
  console on the settings screen is a row of chips (`ConsoleChips`) rather than a drop-down list.
- A short press on a pad isn't lost if the browser holds its frames back: pads are also read on a timer whenever a frame is late.
- Whether a 3DS file is encrypted now goes by its bytes as well as its package's mark: `NCCH` where it belongs, and the NoCrypto flag. A package marked encrypted whose program is readable plays.
- A game made only for the New 3DS, as its header says, tells Azahar to be a New 3DS (`citra_is_new_3ds`).
- `.bcci` and `.bcxi`, Azahar's newer compressed forms, count as 3DS files.
- A new game menu, with large tiles for save, load, rewind, fast forward, screenshots, controllers, settings and quit. It works the
  same by finger, keyboard, remote or controller, and each screen in it goes back one level.
- A loading screen that shows what's happening, and error screens that say what to do about it.
- Touch controls redrawn for each console, with shapes drawn rather than typed. One tap hides them, and they step aside when a
  controller is connected. The editor can resize every control at once, and tuck its options away to see the layout.
- Button prompts that match the controller in use (`web/prompts.tsx`).
- A save state can be chosen when a game starts (`startSlot`).
- WittyPlay is now a project of its own, developed and released from this repository.
