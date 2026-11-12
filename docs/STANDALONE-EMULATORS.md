# Standalone emulators, wherever the server runs

A standalone emulator is a whole program (PCSX2, Dolphin, RPCS3 and the rest), not a libretro core. WittyPlay runs it for one person's game, boxes it in, captures its picture and sound, streams them over the same WebRTC connection a core's game uses, and turns the player's controller into key presses for it. This page covers where they run, their settings and controllers, the legal position, and what hasn't been run. Running them in Docker is in [DOCKER-BACKEND.md](DOCKER-BACKEND.md); the platform-by-platform audit is in [PLATFORMS-AND-PLAN.md](PLATFORMS-AND-PLAN.md).

- **Off by default.** Nothing runs until an administrator turns it on in **Settings, Standalone emulators**.
- **Nothing is bundled.** Each emulator is downloaded, only when an administrator asks, from its own project, pinned to a version, a size and a SHA-256, with its licence shown first. No BIOS, firmware, keys or games ever come with WittyPlay or are downloaded by it.
- **Libretro cores are untouched.** GameCube and Wii play on Dolphin's libretro core in the normal runner; a standalone emulator is for when you choose **Play with …**, or for a console no core plays (Xbox, PS3, Wii U).

## Where they run

**Settings, Standalone emulators, Where they run** (`docker.runtime`):

| Choice | Systems | What it is |
|---|---|---|
| `host`, This Mac / This PC | macOS, Windows (the default there) | The emulator runs on the server's own desktop, boxed in by the system's own sandbox, and its window is captured |
| `local`, This machine's own sandbox | Linux; FreeBSD with limits only | A virtual display of the emulator's own (Xvfb), bubblewrap on Linux, captured by FFmpeg |
| `docker`, Containers | Any system with Docker or Podman (the default on Linux) | A hardened container with its own display: [DOCKER-BACKEND.md](DOCKER-BACKEND.md) |

### On a Mac

The emulator runs under `sandbox-exec` with a profile that allows no network at all, no program but the emulator's own, and writing only to that person's folder for that emulator and the account's cache folder (where Metal keeps compiled shaders). Its home and temporary folders are inside its own folder, so what it keeps lands there. ScreenCaptureKit captures that one window and that one application's sound, at the stream's size, and `CGEventPostToPid` sends key presses to that process only: nothing reaches whatever else is on the screen.

macOS asks its owner, and no program can answer for them, before one program records another's window or sends it keys. Before the first game, in **System Settings, Privacy & Security**:

1. **Screen & System Audio Recording:** turn it on for the program that runs the server (WittyMediaServer, or Terminal if you start it there). Start the server again.
2. **Accessibility:** turn it on for the same program.

Until both are given, **Settings, Standalone emulators** says which is missing, and no game starts. The server must run in a signed-in session (the app, or a LaunchAgent), not as a LaunchDaemon, which has no screen. The window shows on the Mac's screen while it plays; leaving it covered is fine, minimising it isn't.

PCSX2 and Cemu publish Intel builds only; an Apple silicon Mac runs them through Rosetta 2, which macOS offers to install the first time. Encoding can use the Mac's media engine (**stream.encoder**, VideoToolbox).

### On Windows

The emulator runs in a job object, which holds it to the memory, process and processor limits in Settings and ends it, and anything it started, with the module. It runs at low integrity: it can read what the server can, and write only where it's been allowed, which is its own folder (labelled for it the first time). It gets its own `APPDATA`, `LOCALAPPDATA`, `TEMP` and profile folder inside its own folder.

FFmpeg's `ddagrab` (Desktop Duplication) captures the part of the first screen where its window is; WittyPlay moves the window to the screen's top left first. WASAPI's loopback captures what the machine plays: on a machine that plays other sound at the same time, that's heard too. `SendInput` sends key presses, and Windows sends those only to the window in front, so WittyPlay brings the emulator's window to the front first.

It needs a signed-in desktop: a Windows service runs where windows can't be seen or captured, and Settings says so. It also needs an FFmpeg with `ddagrab`, which the server's own has. Encoding can use NVENC, Quick Sync or AMF (**stream.encoder**). Builds that keep their settings beside their own program (RPCS3, Cemu, melonDS and Flycast's Windows builds) are copied once into each person's own folder, so each person has their own and the installed one stays untouched.

### On Linux and FreeBSD

As [DOCKER-BACKEND.md](DOCKER-BACKEND.md) describes for the local sandbox: Xvfb, PulseAudio, FFmpeg and `xdotool` inside bubblewrap, with no network. On FreeBSD there's no bubblewrap and no other sandbox an unprivileged program can use, so an emulator there has only resource limits, and the settings page says so before it's turned on. FreeBSD's emulators come from its packages (`pkg install pcsx2 ppsspp dolphin-emu rpcs3 xemu flycast`, `games/melonds`), added as your own programs.

## Your own emulator programs

**Settings, Standalone emulators, Your own emulator programs** adds an emulator already installed on this server: its name, the consoles it plays, the program's full path, its command line (`{game}`, `{config}`, `{bios}`, `{settings}` as in the list), and which emulator in the list it's a build of, if any, so that one's settings and controller set-up apply. WittyPlay checks the file is a program and runs it in the same sandbox as any other, on this machine or in the local sandbox, never in a container. It downloads nothing and checks no hash for it: it's yours. Up to 32.

This is how a FreeBSD server uses its packages, how a Linux server uses Dolphin's Flatpak, and the only way a Switch game plays.

## The Switch

WittyPlay offers no Switch emulator. Nintendo sued Yuzu's makers in 2024 (they settled and stopped), Ryujinx stopped later that year, and in February 2026 Nintendo had GitHub block Eden, Citron, MeloNX and their forks under the DMCA, as tools for getting round the Switch's protection (`github/dmca`, `2026/02/2026-02-12-nintendo.md`). A project that downloads or recommends one stands where they stood. The Switch is still a console in the library, with its games recognised, described and pictured, and an administrator can add a Switch emulator they installed themselves as their own program. WittyPlay never downloads one, links to one, or handles keys or firmware; whether running one is lawful where you are is yours to judge.

## The emulators

Downloads are from each project's own releases (Dolphin's from dl.dolphin-emu.org), pinned on 2026-11-11 to:

| Emulator | Consoles | Version | macOS | Windows | Linux | Licence |
|---|---|---|---|---|---|---|
| PCSX2 | PS2 | 2.8.2 | Intel (Rosetta) | x64 | x64 | GPL-3.0-or-later |
| Dolphin | GameCube, Wii | 2609b | Universal | x64, arm64 | Flatpak only: use the core, or your own | GPL-2.0-or-later |
| PPSSPP | PSP | 1.20.4 | Universal | x64, arm64 | x64, arm64 | GPL-2.0-or-later |
| melonDS | DS | 1.1 | Universal | x64, arm64 | x64, arm64; FreeBSD x64 | GPL-3.0-or-later |
| Flycast | Dreamcast | 2.7 | Universal | x64 | x64 | GPL-2.0-only |
| xemu | Xbox | 0.8.139 | Universal | x64, arm64 | x64, arm64 | GPL-2.0-or-later |
| Cemu | Wii U | 2.6 | Intel (Rosetta) | x64 | x64 | MPL-2.0 |
| RPCS3 | PS3 | 0.0.43-20582 | Intel, Apple silicon | x64, arm64 | x64, arm64 | GPL-2.0-only |
| DuckStation | PlayStation | — | never offered: CC BY-NC-ND forbids commercial use | | | |

`.7z`, `.tar.xz`, `.zip`, AppImages and Mac disk images are opened under the same rules: nothing lands outside the emulator's folder, nothing is written through a link, and the whole is held to 4 GiB and 100,000 files. A disk image is attached read-only, never shown in the Finder, and detached once copied.

Firmware each looks for, in the server's BIOS folder (never downloaded): PCSX2 a PS2 BIOS; xemu the Xbox boot ROM, BIOS and hard disk image; RPCS3 the PS3 system software (`PS3UPDAT.PUP`), which RPCS3 installs the first time; Cemu your games' disc keys (`keys.txt`) for `.wud` and `.wux`. A game's page lists what's found and what isn't.

## Settings

Every emulator has every one of its options in **Emulator settings**: on a game's page, for yourself or that game, and in Settings for everyone. They come from the emulator's own configuration:

1. `cmd/wittyplay-emuschema` reads the emulator's source at the pinned version, where each option is declared with its file, section, type, default and choices (Dolphin's `Config/*Settings.cpp`, PCSX2's `Pcsx2Config.cpp`, PPSSPP's `Config.cpp`, melonDS's `Config.cpp`, Flycast's `option.h`, xemu's `config_spec.yml`, Cemu's `CemuConfig`, RPCS3's `system_config.h`), and writes `emulators/schemas/<id>.json`.
2. When someone opens the form, keys found in their own settings file that the schema doesn't know are offered too, so a newer emulator's options appear.
3. Anything that names a folder, a file, a network address, an updater, achievements or telemetry is never offered: those are written by WittyPlay, or left alone.

The form groups settings by the section they're in, with a search and a switch for advanced ones. Values layer: the emulator's default, an administrator's for everyone, a person's own, theirs for one game. Which of them a person may change for themselves is part of each setting's description: graphics, speed and sound are each person's; the rest are an administrator's. Before each start they're written into the emulator's own files, INI, TOML, YAML, XML or JSON, changing only those keys and keeping everything else, through `os.Root`, so a link the emulator left in its folder can't turn a write into one elsewhere.

## Controllers

Each player's controller becomes key presses: four players, each with keys of their own (`keys.p1` to `keys.p4`). Before each start WittyPlay writes those same keys into the emulator's own controller settings, in its own notation on the system it runs on, so every pad works with nothing set up by hand:

| Emulator | Players | Where |
|---|---|---|
| PCSX2 | 4 (3 and 4 need its multitap turned on) | `[Pad1]`…`[Pad4]` in `PCSX2.ini` |
| Dolphin | 4 GameCube pads; 4 Wii Remotes held sideways with a Nunchuk | `GCPadNew.ini`, `WiimoteNew.ini` |
| Flycast | 4 | `mappings/SDL_Keyboard.cfg`, `emu.cfg` |
| RPCS3 | 2 | `input_configs/global/Default.yml` |
| PPSSPP, melonDS, xemu, Cemu | 1 | their own files |

Sticks become their four directions; there is no rumble back to the player, and no motion. That's the limit of key presses: no system lets an unprivileged program make a virtual gamepad without a driver.

## Delay

How the picture gets out on this machine: the window is captured (ScreenCaptureKit, or Desktop Duplication), handed to FFmpeg at a steady rate (each new frame as it comes, the last one again when nothing changed), encoded with no B-frames, no lookahead and a rate buffer a quarter of a second deep, and sent as RTP to WittyPlay's relay on loopback, which passes the packets as they are to the browser. Sound goes in 10 ms pieces, never more than a quarter of a second behind; silence fills in when the emulator plays nothing.

| Step | Typical |
|---|---|
| Input: browser to WittyPlay to the emulator | 2 to 5 ms |
| The emulator's own input delay | 1 to 3 frames: 17 to 50 ms |
| Capture, and waiting for the next frame | up to 17 ms |
| Encoding | 3 to 8 ms (x264 ultrafast), 2 to 5 ms (a hardware encoder) |
| The browser's jitter buffer, decoding, the display | 25 to 45 ms |

**Not measured yet**: see [DOCKER-BACKEND.md](DOCKER-BACKEND.md#delay) for how.

**Bitrate** starts at the person's `stream.kbps` and follows loss: down 30% after loss has stayed above 8% for three reports in a row, back up after most of a minute without, never twice in 20 seconds. On this machine a change restarts the encoder, which starts with a keyframe.

## Test plan

What to run by hand, on the systems this Mac couldn't. Each step says what to see.

### Windows (signed in, not as a service)

1. Build the module for Windows (`go run github.com/wittywilla/wittymodule/cmd/wittymodule pack -targets windows/amd64 -out release .`) and install it on a server running on Windows 10 or 11.
2. **Settings, Standalone emulators**: turn them on. **Where they run** says This PC; **On this computer's desktop** says Available. Run the server as a service instead: it says the service has no desktop.
3. Install PPSSPP. The licence shows first; the progress bar fills; cancelling half-way and installing again carries on.
4. Play a PSP homebrew game (any open-licence `.iso` or `.pbp`). Within a few seconds: the picture in the browser, the sound, and the D-pad and buttons working without touching PPSSPP's settings. The emulator's window is at the top left of the first screen.
5. In Task Manager, Details: the emulator's process has Integrity level Low (add the column). Try writing outside its folder from it (its own file dialogs): refused.
6. Pause and resume from the player; stop it from Settings, Running now. Close the browser tab: it stops after two minutes.
7. Install RPCS3. Play a PS3 homebrew `.self` or a dump you own with `PS3UPDAT.PUP` in the BIOS folder. Check `docker\saves\<user>\rpcs3\app` is a copy of RPCS3 of that person's own.
8. Install Dolphin and play the GameCube homebrew Swiss (`swiss_r*.iso`, GPL-2.0, from emukidid/swiss-gc). Two controllers on one browser: both pads move in Swiss's menu.
9. Change **stream.encoder** to NVENC (or Quick Sync, or AMF, for the card you have) and play again; the module's log shows `h264_nvenc`.
10. Add packet loss with clumsy (`clumsy --filter "udp" --drop on --drop-chance 15`) and watch the bitrate step down in the module's log and back up after.

### Linux, and Windows Subsystem for Linux

1. In WSL (Ubuntu 24.04, with WSLg), install `bubblewrap systemd xvfb x11-utils xdotool pulseaudio pulseaudio-utils ffmpeg`. Check `systemctl --user` works (WSL needs `systemd=true` in `/etc/wsl.conf`).
2. **Where they run**: This machine's own sandbox. Status says Available, or names exactly what's missing.
3. Install Flycast and play a Dreamcast homebrew (`wittyplay-probe test-rom -console dc -o test.elf` makes one): a picture that changes colour, sound silent, no network from inside (`ip addr` from its terminal shows only `lo`).
4. Repeat step 4 and 8 of the Windows plan with PPSSPP and Dolphin's libretro core.
5. Docker in WSL: follow [DOCKER-BACKEND.md](DOCKER-BACKEND.md#testing-on-a-real-docker-host).

### Docker

[DOCKER-BACKEND.md, Testing on a real Docker host](DOCKER-BACKEND.md#testing-on-a-real-docker-host), with this change: GameCube and Wii now use `gc` and `wii`, not custom consoles.

### macOS

1. Give the server Screen & System Audio Recording and Accessibility (above). **On this computer's desktop** says Available.
2. Install PPSSPP and play a PSP homebrew: the window opens on the Mac's screen and the browser shows it. Covering the window with another keeps the picture; minimising it stops it.
3. `ps -o pid,command -ax | grep sandbox-exec` shows the emulator under its profile.
4. Install Dolphin and play Swiss; then Neverball's GameCube port (GPL, `neverball-…-ogc.zip` from the same releases) with two controllers.

## What hasn't been run

Written plainly, so nobody takes a passing test for a played game.

- **No game has been played in a standalone emulator on this Mac.** This Mac's server process has neither of the two permissions above, and they're the owner's to give. The capture, the encoder, the key presses and the window lookup are built and compiled; the sandbox profile has been run for real (a program in it writes to its own folder and nowhere else) and the settings and controller files are tested against each emulator's own notation.
- **Windows has only been compiled for.** The job object, low integrity, the window lookup, Desktop Duplication through FFmpeg, WASAPI's loopback and SendInput have never run. The WASAPI calls are made through the interfaces' tables by hand; a mistake there shows as no sound, with the reason in the module's log, and the picture still plays.
- **Each emulator's command line and settings folder on each system** come from its documentation and source, not from running it: `{config}` folders for PPSSPP, melonDS, Flycast, Cemu and RPCS3 on a Mac follow `HOME`, which those programs were read to honour.
- **Controllers:** on a Mac, Dolphin reads the keyboard's state from the system rather than from the events sent to it, so it may not see key presses sent only to it; PPSSPP can't tell the keypad's Enter from Return on Windows; X11's keypad needs Num Lock on.
- **FreeBSD** has been compiled for, not run.
- **Delay** has a budget, not a measurement.
