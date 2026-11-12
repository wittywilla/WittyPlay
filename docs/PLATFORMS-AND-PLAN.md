# What plays where, and the plan

An audit of WittyPlay and the games side of WittyMediaServer, written in November 2026 for the 0.8.0-alpha server, and the plan that followed from it. Each part says what works on each server platform, what doesn't, and why. "Verified" means it was run; everything else is built and tested only, and says so.

The short version, for the owner:

- **Libretro cores** (NES to PS2, PSP, Dreamcast, 3DS, and now GameCube and Wii) run in WittyPlay's own sandboxed game runner on every server platform that libretro publishes cores for: Windows, Linux and macOS. FreeBSD has the runner but no published cores.
- **Standalone emulators** (PCSX2, Dolphin, PPSSPP, melonDS, Flycast, xemu, Cemu, RPCS3) can now run on the server itself on macOS and Windows, not only in Docker or a Linux sandbox. They're captured, encoded and streamed over the same WebRTC path a core's game uses, with the player's controllers written into each emulator's own settings.
- **Every console the previous work added is a real console** in the library: GameCube, Wii, Wii U, PlayStation 3, Xbox and Switch, recognised from their files' own bytes, with metadata, art and a Play button.
- **The Switch and the Switch 2** are library consoles, played by Ryujinx and Lichii, which the owner installs and adds by hand: WittyPlay never downloads them, their keys or their firmware. Read "The Switch and the Switch 2" below.

## The pieces

| Piece | Where | What it is |
|---|---|---|
| Console table | `consoles.go`, `controls.go`, `traits.go` | Every console, its files, folder names, cores, controller and firmware |
| Game runner | `cmd/wittyplay-game-runner`, `host`, `libretro` | Runs one libretro core per game, in a sandbox, over the pipe protocol |
| Streaming | `stream` | Sessions, the encoder, WebRTC and the WebSocket fallback, the relay for emulators |
| Cores | `cores` | Installing cores from libretro's build server |
| Emulator list | `emulators/manifest.json`, `emulators` | Standalone emulators: pinned, hashed downloads per platform, settings, firmware |
| Settings | `emulators/configfile`, `emulators/schemas`, `cmd/wittyplay-emuschema` | Every option of each emulator, from its own source, written into its own files |
| Controllers | `emulators/inputmap` | Each player's keys in each emulator's own notation, per system |
| Where emulators run | `sandbox/docker`, `sandbox/local`, `sandbox/host` | A container; Linux's bubblewrap; this machine's own desktop |
| Server side | WittyMediaServer `internal/gameid`, `internal/gamemeta`, `internal/library/games_*`, `internal/server/api_games*`, `games_standalone.go`, `games_adopt.go` | Recognising files, metadata, the library, play routes |
| Web | WittyMediaServer `web/src/pages/games`, `web/src/pages/settings/Emulators.tsx`; WittyPlay `web/` | The games pages, settings, the player |

## Per platform

### Libretro cores, in the game runner

| | macOS | Windows | Linux | FreeBSD | Docker |
|---|---|---|---|---|---|
| Runner | Verified (Apple M3 Pro) | Built; earlier versions run on Windows | Verified on x64 | Built | The server's container image carries it |
| Sandbox | sandbox-exec profile: no network, no programs, writes only saves | Job object, low integrity | Landlock, seccomp, namespaces | Resource limits only | As Linux |
| Cores to install | libretro's build server | libretro's build server | libretro's build server (x64, arm64, arm32) | None published: system packages or ones you place | As Linux |
| Graphics-card consoles | OpenGL 4.1 through CGL; N64 and LRPS2 drawn by the processor (see README) | OpenGL; Vulkan cores fall back | OpenGL with a card passed through | Processor only | With `/dev/dri` or `--gpus all` |
| GameCube and Wii (Dolphin core) | Verified with real discs: plays on the card, Wii at 55 to 60 frames a second (below) | Published; not run | Published (x64); not run | Not published | As Linux |

### Standalone emulators

| | macOS | Windows | Linux | FreeBSD | Docker |
|---|---|---|---|---|---|
| Where it runs | On the Mac's desktop (`sandbox/host`) | On the PC's desktop (`sandbox/host`) | bubblewrap with a display of its own (`sandbox/local`) | A display of its own, limits only (`sandbox/local`) | A hardened container (`sandbox/docker`) |
| Sandbox | sandbox-exec: no network, no other programs, writes only its own folder | Job object (memory, processes, CPU, killed with the module), low integrity: writes only its own folder | Every namespace unshared, no network, a systemd scope's limits | Resource limits; no sandbox (FreeBSD's jails need root) | Read-only root, no capabilities, seccomp, limits |
| Picture | ScreenCaptureKit, the emulator's window | Desktop Duplication (FFmpeg `ddagrab`) of where its window is | Xvfb, FFmpeg `x11grab` | As Linux | As Linux |
| Sound | ScreenCaptureKit, that app's sound | WASAPI loopback: the whole machine's | PulseAudio, the sandbox's own | As Linux | As Linux |
| Input | Key presses sent to that one process (`CGEventPostToPid`) | `SendInput`, with its window brought to the front | `xdotool` on its own display | As Linux | As Linux |
| What it needs | Screen & System Audio Recording and Accessibility permission for the server, given by the Mac's owner; macOS 13 or later | A signed-in desktop (not a service); an FFmpeg with `ddagrab` | bubblewrap, systemd-run, Xvfb, xdotool, PulseAudio, FFmpeg | Xvfb, xdotool, PulseAudio, FFmpeg from packages | Docker or Podman |
| Downloads | PCSX2 (Intel, via Rosetta), Dolphin, PPSSPP, melonDS, Flycast, xemu, Cemu (Intel, via Rosetta), RPCS3 | All eight, x64; Dolphin, PPSSPP, melonDS, xemu, RPCS3 also arm64 | PCSX2, PPSSPP, melonDS, Flycast, xemu, Cemu, RPCS3 (Dolphin publishes only a Flatpak) | melonDS's own build; the rest from packages, as your own programs | As Linux |
| Verified | Built, unit-tested, the sandbox profile run for real; see "What hasn't been run" | Built for Windows only | Unit-tested; never run | Built only | Never run |

### Why each "no"

- **FreeBSD has no published cores** because libretro's build server builds none. Cores from the system's packages (`pkg install libretro-*`) are found and used.
- **Dolphin has no Linux download** because the project publishes only a Flatpak, which isn't an archive WittyPlay can unpack. On Linux, use the Dolphin libretro core (GameCube and Wii play in the runner), an image with Dolphin in it, or Dolphin from your packages as your own program.
- **PCSX2 and Cemu on Apple silicon** run through Rosetta 2: neither project builds for arm64 Macs.
- **An emulator on a Mac's desktop needs two permissions** that macOS asks its owner for and no program can give itself: Screen & System Audio Recording, to capture the window, and Accessibility, to send it keys. Without them the settings page and every game say exactly that.
- **An emulator on Windows needs a desktop.** A Windows service runs in session 0, where windows can't be seen or captured. Run the server signed in, as the tray app does.
- **FreeBSD has no sandbox WittyPlay can use** without root. It's offered only as "limits only", and the settings page says so.
- **Analogue sticks and rumble in standalone emulators:** input reaches them as key presses, as no system offers a virtual gamepad to an unprivileged program without a driver. Sticks become their four directions; there is no rumble back. Libretro cores have both.

### The Dolphin core on a Mac, as run

Run on 2026-11-11 on an Apple M3 Pro, with the owner's own GameCube and Wii discs through the server: both played on the M3 Pro's OpenGL, with sound and keyboard input, and save states. A Wii game ran at 55 to 60 frames a second against the console's 59.94, so its sound can crackle where it dips; Dolphin's fast memory, the usual cure, faults on purpose and stays off in the runner. Dolphin's on-screen warnings (about the Mac's OpenGL) are turned off, as they sat over the player's picture. The Wii plugs in a Wii Remote with a Nunchuk, as its controller layout is drawn.

An earlier run with Swiss (GameCube homebrew, through `wittyplay-probe frames`) drew one frame and then crawled, its CPU thread spending its time in purego's callbacks from a thread Go didn't start. That hasn't been seen with real discs; if it comes back, look in `libretro/libretro.go` (the callbacks) and `host/`.

### The 3DS on a Mac

Azahar asks for Vulkan on a Mac, which the runner can't offer, so there the server has it draw with its own software renderer (`citra_graphics_api` Software), which kept 60 frames a second, rather than give way to Panda3DS. A 3DS file decrypted by a tool that left its NoCrypto flag unset counts as decrypted (its extended header reads plainly), and plays from a copy of its program with the flag set, as GodMode9 sets it: Azahar goes by the flag.

## The consoles

| Console | Id | Plays on | Files recognised by their bytes |
|---|---|---|---|
| GameCube | `gc` | Dolphin core; Dolphin | Disc images, Dolphin's GCZ, CISO, WIA and RVZ, `.dol` and `.elf` homebrew |
| Wii | `wii` | Dolphin core; Dolphin | As GameCube, plus WBFS and WAD |
| Wii U | `wiiu` | Cemu | `.wud` and `.wux` (need your keys), `.rpx`, `.wua` |
| PlayStation 3 | `ps3` | RPCS3 | Discs with `PS3_DISC.SFB`, folder dumps by that file, `.pkg` |
| Xbox | `xbox` | xemu | XISO and full disc images, `.xbe` |
| Switch | `switch` | Ryujinx or Lichii, added by hand | NSP, NSZ, XCI, XCZ (need your keys), NRO homebrew |
| Switch 2 | `switch2` | Lichii, added by hand | The same files: a Switch 2 game is known by its folder or library |

Each has a controller layout, firmware it may need, platform ids for every metadata source the server uses (IGDB, TheGamesDB, ScreenScraper, RAWG, Wikidata, libretro's database where it has one), and a page that says what's missing before anyone presses Play. Custom consoles made for these before (`custom-gamecube` and so on) are moved to the real ones automatically.

**Files that need your console's keys** (Switch, Wii U discs, some PS3 dumps) are listed as games, never decrypted by WittyPlay or the server: only an emulator, with keys you dumped from your own console, opens them.

## Settings, every one

Each emulator's settings form is generated from the emulator's own configuration: its source at the pinned version declares every option, its type, its default and its choices, and `cmd/wittyplay-emuschema` turns that into a schema (`emulators/schemas`). On top of that, any key found in a person's own settings file that the schema doesn't know is offered too, so a newer emulator's options still appear. Anything that names a folder, a network address, an updater or telemetry is never offered.

Values layer: default, then an administrator's for everyone, then a person's own, then theirs for one game, and are written into the emulator's own files (INI, TOML, YAML, XML or JSON) before each start, without disturbing anything else in them, through `os.Root`.

## Controllers

`emulators/inputmap` writes the keys each player's buttons press into each emulator's own controller settings, in its own notation on each system: PCSX2 (4 players), Dolphin's GameCube pads and Wii Remotes with Nunchuks (4), Flycast (4), RPCS3 (2), PPSSPP, melonDS, xemu and Cemu (1 each, the most their keyboard mapping allows). Players beyond that need a real controller on the server.

## The Switch and the Switch 2

Ryujinx (Switch) and Lichii (Switch 2 and Switch) are integrated like every other standalone emulator, on every system each publishes a build for, and are never downloaded: the owner installs them from their own projects and adds them in Settings, Standalone emulators (or in `manual.json`, for a server with no screen or in a container), and WittyPlay checks each is what it says and notes its version. Keys and firmware are the owner's, put in the BIOS folder; WittyPlay copies them where each emulator looks and never fetches them. Ryujinx plays no Switch 2 games; Lichii plays some, by its authors' account very experimentally. The owner is responsible for having the right to the keys, firmware and games. Everything, with where to get each and the legal note: [STANDALONE-EMULATORS.md, The Switch and the Switch 2](STANDALONE-EMULATORS.md#the-switch-and-the-switch-2).

## Engine work in this round

- Standalone emulators on macOS and Windows, with their own capture, input and sandbox.
- Bitrate: an emulator's encoder steps its bitrate down under loss and back up, as before, on every runtime; on this machine it restarts the encoder with a keyframe rather than freezing.
- Hardware encoders for emulators on this machine: VideoToolbox on a Mac; NVENC, Quick Sync or AMF on Windows.
- Delay: the encoder, its rate buffer and the relay are as the container's; the budget and how to measure it are in [STANDALONE-EMULATORS.md](STANDALONE-EMULATORS.md#delay).

## What hasn't been run

See [STANDALONE-EMULATORS.md](STANDALONE-EMULATORS.md#what-hasnt-been-run) for the full list, and the test plans for Windows, Linux and Docker.
