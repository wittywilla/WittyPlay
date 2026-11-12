# Standalone emulators in containers, and Linux's own sandbox

WittyPlay normally runs libretro cores in its own sandboxed game runner. This is a second, optional way to play: a *standalone* emulator (PCSX2, PPSSPP, Flycast, melonDS and others), downloaded from its own project, run one per game in a hardened Docker container or a bubblewrap sandbox, and streamed to the player over WebRTC. On a Mac or a Windows PC they can also run on the server's own desktop: that, the settings and controllers of every emulator, your own emulator programs and the Switch are in [STANDALONE-EMULATORS.md](STANDALONE-EMULATORS.md).

- **It's off by default.** Nothing here runs, and nothing reaches a Docker daemon, until an administrator turns it on in **Settings, Standalone emulators**.
- **Libretro cores are untouched.** They always run in the process sandbox. A game goes to a standalone emulator only when someone chooses **Play with …** on its page.
- **Nothing is bundled.** No emulator, game, BIOS, firmware or key comes with WittyPlay. Emulators are separate programs under their own licences, shown before an install; firmware is yours to provide.
- **Read "What hasn't been run" at the end.** The code is covered by tests against a stand-in for Docker. It has not yet played a game on a real Docker host.

## How it fits together

```
 Browser                  wittymediaserver                wittyplay-module                    Docker, or bubblewrap
+------------+           +-------------------+   RPC     +---------------------------+       +--------------------------------+
| Play with  |  POST     | may this account  | docker.   | Launch: installed? fits   |  API  | wittyplay-<session>, uid 1000  |
| PCSX2      |---------->| play this game?   | Launch    | this machine? settings -> |------>|  read-only root, no caps,      |
|            | /games/   | finds its file    |---------->| the emulator's own files  |       |  no-new-privileges, seccomp,   |
|            | {id}/play |                   |           | Lifecycle: one each, idle |       |  pids/memory/CPU limits        |
|            | /emulator |                   |           | limit, orphan sweep       |       |                                |
| <video>    |  WebRTC   | passes /docker/…  |   HTTP    | stream.Relay  <-----------+--RTP--|  Xvfb -> FFmpeg (x11grab +     |
| gamepads,  |<----------| through as the    |---------->|  (RTP -> WebRTC, bitrate  |  UDP  |  PulseAudio) -> H.264 + Opus   |
| keyboard   |  H.264 +  | person signed in  |           |   follows loss)           |       |                                |
|            |  Opus     |                   |           | inputagent.Forwarder -----+--TCP->|  wittyplay-input-agent ->      |
| sandboxed  |           | /kasm/{sid}/      |           | KasmProxy (ticket in the  |       |  xdotool keys and mouse        |
| <iframe>   |---------->| {ticket}/… public |---------->|  address + per-session    |-HTTP->|  KasmVNC :6901 (desktop only)  |
| (desktop)  |           | with the ticket   |           |  password)                |       |  the emulator, read-only       |
+------------+           +-------------------+           +---------------------------+       +--------------------------------+
```

| Package | What it does |
| --- | --- |
| `emulators` | The built-in list (`manifest.json`), the installer, and settings as data (`settings.go`). |
| `sandbox/docker` | The `Runtime` interface, the hardened `Build` and `Check`, the `Lifecycle`, the `KasmProxy`, the backend's settings, and a `Fake` for tests. |
| `sandbox/docker/engine` | The only package that imports the Docker Engine SDK. |
| `sandbox/local` | The same `Runtime` without Docker: bubblewrap under a systemd scope. Linux only. |
| `sandbox/docker/inputagent`, `cmd/wittyplay-input-agent` | The agent beside the emulator: input in, bitrate requests, and for the local sandbox the picture and sound out. |
| `stream/relay.go`, `stream/ratelad.go` | RTP into WebRTC, and the bitrate ladder. |
| `rpc/docker.go` | Routes, the launch, diagnostics, installs as background jobs. |
| `docker/images/` | The base image and its entrypoint, an example emulator image, and how to build and pin them. |

## Playing a game

1. The game's page asks the server `GET /api/v1/games/{id}/emulators`. The server checks the account may see the game, and asks WittyPlay (`docker.Offers`) which installed emulators play its console and whether each is ready, with a sentence when it isn't.
2. **Play with PCSX2** posts `POST /api/v1/games/{id}/play/emulator` `{"emulator":"pcsx2"}`. The server checks the account's limits again, finds the game's file in its library (unpacking an archive if need be), and calls WittyPlay's `docker.Launch` with the file, the account and the emulator's name. **A browser never names a file and never reaches the launch itself.**
3. WittyPlay refuses politely, in a sentence the page shows, when: the backend is off, the emulator isn't installed or was turned off, its download was never verified, no image is pinned, the runtime isn't reachable, the machine is too small, firmware is missing, or as many emulators are running as are allowed.
4. Otherwise it writes this person's settings into the emulator's own files, starts the container, and answers with where to connect, pause, resume and stop.
5. The player connects the same way it does to a core's game: a WebRTC offer with `input` and `events` data channels to `…/docker/sessions/{sid}/connect`.

Opening the same game again while it's running rejoins it. Starting a different game ends the one before: one game each. A game nobody has been connected to for two minutes is stopped; so is one with no input for `docker.idleMinutes`, and any after `docker.maxHours`. When a game ends, everyone connected gets `{"type":"ended","reason":…,"text":…}` on `events`.

## Routes

On the module's own service, behind the service token; the server passes them through under `/api/v1/modules/wittyplay` and says who is asking in `X-Witty-User` and `X-Witty-Admin`, which it strips from anything a browser sends. A request without them is refused.

| Method | Path | Who | What |
| --- | --- | --- | --- |
| GET | `/docker/status` | admin | On or off, which runtime, whether it answers, its machine, the local sandbox's availability |
| GET | `/docker/diagnostics` | admin | Everything a game depends on, each as `ok`, `warn`, `fail` or `off` with a sentence |
| GET, PUT | `/docker/settings` | admin | The backend's settings and their schema |
| GET | `/docker/emulators` | admin | The list: licence, requirements, what's installed, whether an update is listed, why it can't be installed or run, the install job, firmware found |
| POST | `/docker/emulators/{id}/install` | admin | Start an install: `{"license": "<the entry's licence>"}`. 202 and the job. Installing over an older version is the update |
| GET, DELETE | `/docker/emulators/{id}/install` | admin | The job's progress; cancel it |
| DELETE | `/docker/emulators/{id}` | admin | Uninstall. Refused while someone is playing with it |
| GET, PUT | `/docker/emulators/{id}/settings` | anyone signed in | That emulator's settings: schema, layers, what they come to. Who may write what is below |
| GET | `/docker/sessions` | anyone signed in | Games running: your own, or everyone's for an administrator |
| POST | `/docker/sessions/{sid}/connect` | the player | WebRTC: `{"hello","offer"}` returns `Connected` |
| POST | `/docker/sessions/{sid}/pause`, `/resume` | the player | Freeze the emulator, and let it go on |
| DELETE | `/docker/sessions/{sid}` | the player or an admin | Stop |
| GET | `/kasm/{sid}/{ticket}/…` | whoever holds the ticket | The desktop, for emulators shown that way |

Two calls only the server makes: `docker.Launch` and `docker.Offers`.

## Settings

### The backend's (admin only)

Listed in `module.json` (`capabilities.dockerSettings`), described by `docker.Schema()`, which `GET /docker/settings` returns so a page draws the form from data.

| Key | Default | What it does |
| --- | --- | --- |
| `docker.enabled` | `false` | The switch |
| `docker.runtime` | `docker` | `docker` (a Docker or Podman daemon) or `local` (bubblewrap on this machine) |
| `docker.network` | `wittyplay` | WittyPlay's own bridge network |
| `docker.internal` | `false` | No way out to the internet for emulators. For a host that reaches container addresses directly (Linux without Docker Desktop) |
| `docker.publishLoopback` | `false` | Publish the agent's port on 127.0.0.1 instead, for Docker Desktop and rootless daemons |
| `docker.maxContainers` | 2 | Emulators at once |
| `docker.memoryMB`, `docker.cpus`, `docker.pids` | 4096, 2, 512 | Limits for each. Never more memory than the machine has |
| `docker.idleMinutes`, `docker.maxHours` | 10, 6 | Stopped after this long without input; never longer than this |
| `docker.gpu` | `{}` | `{"DRI": true}` for `/dev/dri`, `{"Nvidia": true}` for the NVIDIA runtime. Off unless set |
| `docker.backend` | `{}` | Per emulator: `docker+webrtc`, `docker+kasmvnc`, or `process` (not offered) |
| `docker.images` | `{}` | Per emulator: your own image, `repo:tag@sha256:…`, or `sha256:…` for one built on this machine |

### Each emulator's (the config API)

`GET /docker/emulators/{id}/settings[?game=<item>]` answers `{schema, admin, user, game, effective, canAdmin}`. Each field of the schema has a `key`, a `kind` (`toggle`, `number`, `text`, `choice`, `map`), a `label`, `help`, a `default`, limits or choices, and a `scope`:

- `scope: "admin"`: only an administrator sets it, for everyone (the graphics backend, the stream's size, the keys).
- `scope: "user"`: each person may set their own, and again for one game (resolution scale, widescreen, bitrate).

Values layer: the default, then the administrator's, then the person's, then the person's for that game. `PUT` takes `{"scope": "admin"|"user"|"game", "game": <item>, "values": {key: value|null}}`; `null` clears a key. Every value is checked against the schema; a person can't write an admin-only setting or anyone else's layer. They're kept in `docker/emulator-settings.json` in the module's folder.

Every emulator has these, which WittyPlay itself uses:

| Key | Scope | What |
| --- | --- | --- |
| `stream.size` | admin | 480p, 720p (default) or 1080p |
| `stream.fps` | admin | 30 or 60 |
| `stream.encoder` | admin | `x264` (the processor) or `vaapi` (the graphics card, with `docker.gpu` DRI) |
| `stream.kbps` | user | The most bitrate, 800 to 20000, default 6000 |
| `keys.p1` … `keys.p4` | admin | Which X key each controller button presses, for each of four players |

And its own from the list, each saying where its value goes: a key in one of the emulator's INI files, a variable, or words on its command line. They're written before each start into the person's own settings folder for that emulator (`/saves/config` inside). PCSX2, Dolphin, PPSSPP and Flycast have a graphics backend, resolution scale and widescreen; the others have the common settings only, and their own menus for the rest (reachable when shown as a desktop). **The emulators' own key names were written from their documentation and haven't been run.**

Firmware: an emulator's entry names the files it looks for (`ps2-*.bin`, `keys.txt` …). The list and the game's page say which are in the server's BIOS folder; the folder is mounted read-only at `/bios`. A game whose emulator needs firmware isn't started without any.

## The emulators

Every download is pinned to a version, a size and a SHA-256, from the project's own GitHub release. The hashes were checked on 2026-11-11: PCSX2, xemu and Flycast by downloading and hashing, the rest against GitHub's own digest for the asset.

| Emulator | Consoles (library id) | Version | Download | Licence | Needs at least | Notes |
| --- | --- | --- | --- | --- | --- | --- |
| PCSX2 | PlayStation 2 (`ps2`) | 2.8.2 | x86-64 | GPL-3.0-or-later | 4 GB, 4 cores | Needs a PS2 BIOS |
| PPSSPP | PSP (`psp`) | 1.20.4 | x86-64, arm64 | GPL-2.0-or-later | 1 GB, 2 cores | |
| melonDS | Nintendo DS (`nds`) | 1.1 | x86-64, arm64 | GPL-3.0-or-later | 1 GB, 2 cores | |
| Flycast | Dreamcast (`dc`) | 2.7 | x86-64 | GPL-2.0-only | 1 GB, 2 cores | |
| Dolphin | GameCube (`gc`), Wii (`wii`) | 2609b | none: Dolphin publishes only a Flatpak for Linux | GPL-2.0-or-later | 4 GB, 2 cores | Needs an image with Dolphin in it; or play on the Dolphin core |
| xemu | Xbox (`xbox`) | 0.8.139 | x86-64, arm64 | GPL-2.0-or-later | 4 GB, 4 cores | Desktop. Needs Xbox boot ROM, BIOS, disk image |
| Cemu | Wii U (`wiiu`) | 2.6 | x86-64 | MPL-2.0 | 8 GB, 4 cores, a graphics card | Desktop. Needs your keys |
| RPCS3 | PlayStation 3 (`ps3`) | 0.0.43-20582 | x86-64, arm64 | GPL-2.0-only | 8 GB, 6 cores, a graphics card | Desktop. Needs Sony's system software |
| DuckStation | PlayStation (`ps1`) | 0.1-12442 | **never offered**: CC BY-NC-ND forbids commercial use | CC-BY-NC-ND-4.0 | 1 GB, 2 cores | An administrator can build an unmodified image of their own |

The same versions are downloaded for Macs and Windows: [STANDALONE-EMULATORS.md](STANDALONE-EMULATORS.md#the-emulators). No Switch emulator is in the list: see [The Switch](STANDALONE-EMULATORS.md#the-switch).

**Every console here is the library's own.** GameCube, Wii, Wii U, PS3, Xbox and Switch used to need a custom console named `custom-gamecube` and so on; the server now moves those to the real consoles by itself.

**Images.** No image has been published, so every `image.digest` in the list says `TODO`. Until one is, each emulator runs only where an administrator has built the base image and pinned it under `docker.images` (see [docker/images/README.md](../docker/images/README.md)), or with `docker.runtime` set to `local`, which needs no image.

## The security model, in plain words

**What you're trusting.** A standalone emulator is a large program that reads untrusted files. It's treated as something that may be taken over by a bad game file, the way a core is.

**The Docker socket is the machine.** Whatever can talk to a Docker daemon that runs as root can start a container that owns the host. Turning this backend on with such a daemon means WittyMediaServer's own account can do that, and so can anything that takes over the server or this module. This is the largest thing you decide here, and there are three ways to make it smaller:

- **A rootless daemon.** `dockerd-rootless-setuptool.sh install`, then start the server with `DOCKER_HOST=unix:///run/user/<uid>/docker.sock`. The daemon is then only that user. Turn on `docker.publishLoopback`: a rootless daemon's containers aren't reachable by address.
- **Podman.** `systemctl --user enable --now podman.socket`, then `DOCKER_HOST=unix:///run/user/<uid>/podman/podman.sock`. Podman answers Docker's API, without a root daemon. With `docker.publishLoopback` on, as above.
- **No daemon at all.** `docker.runtime: local`, below.

Diagnostics says which you have ("Who the daemon runs as"). The socket is never given to a container: `Check` refuses any mount whose path is or ends in a socket.

**The container.** `docker.Build` makes the spec in one place, and `docker.Check` checks every spec again before anything is created, so nothing weaker reaches Docker. A test holds each of these in place:

- Runs as uid:gid `1000:1000`, never root, never privileged, with `--init`.
- Read-only root filesystem.
- `--cap-drop ALL`, nothing added; `no-new-privileges`; Docker's default seccomp and AppArmor profiles. Any override, and anything "unconfined", is refused.
- pids, memory (no extra swap) and CPU limits on every one.
- `/tmp` and `$HOME` are tmpfs, `noexec,nosuid,nodev`. `/run/app` is the only tmpfs that allows execution, only for an AppImage (FUSE isn't available to an unprivileged container, so it unpacks itself there), still `nosuid,nodev`.
- Mounts, and no others: the one game at `/game/<file>`, the BIOS folder at `/bios` and the installed emulator at `/opt/emulator`, all read-only; `/saves` (this person's saves and settings for this emulator), writable. System folders, Docker's and Podman's own folders and any socket are refused as sources.
- No host network, no other container's, not the default bridge: WittyPlay's own bridge with inter-container traffic off, optionally internal. Ports only ever on `127.0.0.1`.
- No devices but `/dev/dri`, and only when an administrator turns it on. NVIDIA goes through its runtime's device request.
- The image is pinned to a digest, or is an image id on this machine. A tag alone is refused.
- Labelled `wittyplay.managed`, `.session`, `.user`, `.emulator`. WittyPlay only ever stops or removes containers carrying them.

`/saves` is a bind mount, which has no size limit of its own: an emulator can fill the disk the module's folder is on. Put that folder on a volume with a quota if that matters to you.

**The local sandbox** (`docker.runtime: local`, Linux only). bubblewrap with every namespace unshared (`--unshare-all`): the emulator sees no other process, has no capabilities, and has **no network at all**, only a loopback of its own. Its root is the system's `/usr` and a few folders of `/etc`, read-only, with the same four mounts as a container. A systemd scope (`systemd-run --user --scope`) holds it to the same memory, process and CPU limits, and it dies with the module. The picture, sound and input cross through one Unix socket, guarded by the session's secret. It is **weaker than a container** in two ways: there's no system-call filter, and the emulator runs on this machine's own libraries rather than an image's. It is stronger in one: nothing holds a daemon's socket. It needs `bwrap`, `systemd-run`, `Xvfb`, `xdpyinfo`, `xdotool`, `pulseaudio`, `pactl`, an `ffmpeg` with `x11grab` and `pulse`, and `wittyplay-input-agent` beside the module; Status says which are missing. macOS and Windows run emulators on their own desktop instead, in their own sandboxes: see [STANDALONE-EMULATORS.md](STANDALONE-EMULATORS.md#where-they-run). On FreeBSD the local sandbox runs without bubblewrap, with resource limits only.

**The desktop (KasmVNC).** Its page and scripts come out of the container, so that page must never be one of the server's own: it could read the app's storage and act as whoever is playing. It's shown in a frame sandboxed without `allow-same-origin`, and every answer carries `Content-Security-Policy: … sandbox allow-scripts allow-pointer-lock`, set by WittyPlay and again by the server whatever the container sent. A browser sends no sign-in from such a frame, so the route can't ask for one: the session's random 256-bit ticket, in the address, is the way in. Only the person who started the game is given it; it ends with the game; it's compared in constant time; and a wrong one gets the same "not found" as no session. The proxy adds the container's own password, strips cookies in both directions, and keeps redirects inside the session's address. Reading only.

**Downloads.** Only over HTTPS from hosts on a list; redirects elsewhere are refused. Each is held to the size the list gives and its SHA-256 before anything is unpacked, and a download that was cut short is carried on from, with the whole file hashed at the end. An entry without a checked hash can't be installed, and an install recorded as unverified is never run. Unpacking refuses paths that lead out, links that lead out, writing through a link, anything that isn't a file, folder or link, and more than 4 GiB or 100,000 files; nothing is ever setuid.

**Settings files.** The emulator can write to its own settings folder, so WittyPlay writes there through `os.Root`: a link left in that folder can't turn a settings write into a write elsewhere.

**Secrets.** Each session has a KasmVNC password (never sent to a browser), a ticket, and the input agent's token, which the agent and the entrypoint clear from the environment before the emulator starts.

**Who can do what.** Administrators: the backend's settings, installs, removal, admin-scoped emulator settings, stopping anyone's game. Anyone signed in: play a game their account's limits allow, their own settings, their own sessions. Nobody but the player connects to a game: an administrator can stop one, not watch it.

## Setting it up

1. Choose where emulators run (above) and make sure the server's account can reach it.
2. Docker only: build the base image and pin it (see [docker/images/README.md](../docker/images/README.md)). For an image you don't push, use its id: `docker image inspect --format '{{.Id}}' wittyplay-emu-base:dev`, and set that `sha256:…` for each emulator under **Your own image for each**. The base image runs whatever emulator is installed, mounted at `/opt/emulator`.
3. **Settings, Standalone emulators**: turn them on. On Docker Desktop or a rootless daemon also turn on **Publish on 127.0.0.1**. Read Diagnostics.
4. Install an emulator. Its licence is shown first.
5. Put firmware you own in a `BIOS` folder beside your games, or in the server's BIOS folder.
6. Open a game for that console and choose **Play with …**.

### Small machines

A Raspberry Pi 3 has 1 GB of memory: Diagnostics says no emulator in the list fits, each is marked with why, and a start is refused in a sentence before anything runs. On a Pi 4 or 5 the arm64 downloads (PPSSPP, melonDS) are offered; PCSX2 and the rest are x86-64 only and say so. Encoding 720p60 with x264 takes most of one fast core beside the emulator: use 480p or 30 frames a second on small machines, or `vaapi` where a graphics card is passed through.

### Graphics cards

- **Intel and AMD:** `docker.gpu` `{"DRI": true}`. The user uid 1000 maps to needs to open the render node: the `render` group, or a rootless daemon whose user is in it.
- **NVIDIA:** the NVIDIA Container Toolkit, then `{"Nvidia": true}`.
- Without either, Mesa's software renderer draws. Enough for older consoles; slow for PS2 and later, and a notice says so when a game starts.

## Delay

How the picture gets out: the emulator draws to a virtual X display (Xvfb); FFmpeg grabs it (`x11grab`) and PulseAudio's monitor, encodes H.264 and Opus, and sends RTP to WittyPlay, which hands the packets as they are to the browser's WebRTC connection. Nothing is encoded twice.

What keeps it short: no input buffering in FFmpeg, `zerolatency` (no B-frames, no lookahead), a rate buffer a quarter of a second deep, 10 ms Opus frames, a keyframe every second, RTP relayed without a jitter buffer of WittyPlay's own.

The budget for 720p at 60 frames a second on a home network, glass to glass (a button pressed to the picture changing):

| Step | Typical |
| --- | --- |
| Input: browser to data channel to the agent | 2 to 5 ms |
| The agent's `xdotool` (one process per event) | 2 to 5 ms |
| The emulator's own input delay | 1 to 3 frames: 17 to 50 ms |
| Waiting for the next capture | up to 17 ms, 8 on average |
| Encoding (x264 ultrafast, or VA-API) | 3 to 8 ms |
| Relay and network | 1 to 3 ms |
| The browser's jitter buffer, decoding, the display's refresh | 25 to 45 ms |
| **Outside the emulator** | **about 45 to 85 ms; the aim is under 80** |

**This has not been measured.** To measure it: film the screen and the controller with a phone at 240 frames a second and count frames between the press and the change (each is 4.2 ms); and in Chrome, `chrome://webrtc-internals` gives `jitterBufferDelay` and `totalDecodeTime` for the video track, which are the browser's part.

**Bitrate.** The stream starts at `stream.kbps`. Changing the encoder's bitrate means starting its FFmpeg again, a short freeze, so it moves in a few large steps: down 30% after loss has stayed above 8% for three reports in a row, back up after most of a minute without loss, never twice in 20 seconds, never below 800 kbit/s or above where it started.

**Remote players.** A game in an emulator uses the same WebRTC settings a core's game does: the server's fixed UDP port, public address and STUN servers (`gamingUdpPort`, `gamingPublicIp`, `gamingStun`). **No STUN server is used unless the owner sets one**, and there is no TURN relay: a player whose network blocks UDP to the server can't connect. The desktop fallback is HTTP and a WebSocket through the server, and works wherever the server's pages do.

## Input, and its limits

The agent turns the browser's input into X key and mouse events with `xdotool`. It doesn't make a virtual gamepad: `/dev/uinput` would have to be passed into the container, which the hardening refuses.

- **Four players**, each with keys of their own (`inputagent.DefaultKeys`), changeable with `keys.p1` to `keys.p4`. **The emulator's own settings have to map those keys to its controllers**; nothing does that for you yet, so out of the box only what the emulator's default keyboard layout happens to share with player 1's keys works. Player 4's defaults are the function keys, which some emulators keep for themselves.
- **No analogue.** Each stick becomes its four directions' keys past half-way. Triggers are on or off.
- A pointer or the first touch moves and clicks the mouse.
- **No rumble, motion or lights.** A keyboard has no way back.

## When something's wrong

| What you see | What to do |
| --- | --- |
| "Docker isn't reachable on this server: …" | The rest of the sentence is Docker's own error. Check the daemon is running, `DOCKER_HOST` is what the server was started with, and its account can use the socket. Diagnostics shows the address it tried |
| "… has no container image pinned yet" | Build the base image and set its id under **Your own image for each** |
| "… needs about 4 GB of memory, and each emulator is given …" | Raise `docker.memoryMB`, if the machine has it |
| "… needs firmware of your own …" | Put it in the BIOS folder. The list shows which files are looked for |
| "… was installed from a download nobody had checked" | Install it again |
| An install fails with "doesn't match the hash it was pinned to" | The project replaced the file, or something between you and it did. Nothing was installed. Report it |
| A black picture | The container isn't reaching the relay. On Docker Desktop or rootless, turn on `docker.publishLoopback`. On Linux, check a firewall allows UDP from WittyPlay's bridge to the host. `docker logs wittyplay-<session>` shows FFmpeg's errors |
| The game stops by itself after a minute or two | Nobody was connected: a game left unwatched for two minutes is stopped. If the emulator crashed, the module's log has its last lines |
| Buttons do nothing | The emulator doesn't know the agent's keys. Map them in its own settings (as a desktop), or set `keys.p1` to the keys it already uses |
| The desktop's frame is empty | Its ticket ended with the game, or the page was opened outside its frame. Start the game again |
| "already running as many emulator containers as it allows" | Raise `docker.maxContainers`, or stop a game in **Running now** |
| Containers left after a crash | Removed at the next start, or when the backend is turned on. By hand: `docker ps -a --filter label=wittyplay.managed=true` |

## Testing on a real Docker host

The tests in this repository run against `docker.Fake`, which applies the same `Check` the engine does. One test needs a daemon:

```
go test -tags docker_integration -run Integration -v ./sandbox/docker/engine/
```

It pulls busybox by digest, creates a container from `Build`'s spec, reads the hardening back through Docker's own inspect, and removes everything.

Then by hand, on Linux with Docker (and again with a rootless daemon, Podman, and `docker.runtime: local`):

1. `docker build -f docker/images/base/Dockerfile -t wittyplay-emu-base:dev .` from the repository's root. It must fail if KasmVNC's hash doesn't match.
2. Turn the backend on; Diagnostics shows the daemon, its API version, and whether it's rootless.
3. Install Flycast. Watch the progress bar; cancel half-way and install again, and see it carry on rather than start over. Unplug the network mid-download and see the job fail with a reason.
4. Pin the image's id for Flycast. Play a Dreamcast game: picture and sound within a few seconds, player 1's keys once mapped in Flycast.
5. `docker inspect wittyplay-<session>`: `ReadonlyRootfs`, `CapDrop: [ALL]`, `SecurityOpt: [no-new-privileges:true]`, `User: 1000:1000`, `PidsLimit`, `Memory`, `NanoCpus`, the four mounts and their modes, `NetworkMode: wittyplay`. `docker exec` in and confirm: `id`, `touch /x` fails, `cat /proc/1/status | grep Cap` is all zeros, no route to another session's container.
6. Pause and resume from the player. Stop from **Running now** as an administrator. Close the tab and see the container go after two minutes. Kill the module and start it: the container left behind is removed.
7. Start a second game as the same person: the first ends with "You started another game". As a second person at the limit: "already running as many …".
8. Set PCSX2's resolution scale for one game and check `PCSX2.ini` under the module's `docker/saves/<user>/pcsx2/config`.
9. An emulator shown as a desktop (xemu): the frame shows it; the page's address without its ticket is "not found"; in the browser's tools the frame's origin is `null` and it has no cookies.
10. Add packet loss (`tc qdisc add dev <bridge> root netem loss 15%`) and see the bitrate step down in the module's log, and back up after it's removed.
11. Measure the delay as "Delay" describes, and write the numbers into this page.

## What hasn't been run

Written plainly, so nobody takes a passing test for a played game. On the machine this was finished on there was no Docker and no Linux.

- **No game has been played through this on a real Docker host.** The integration test above has not been run by the people who wrote this version.
- **The container image has never been built.** The Dockerfile's base images and KasmVNC are pinned by digests read from the registries, but `docker build` hasn't been run, and KasmVNC 1.5.2's command line in the entrypoint is from its documentation.
- **The entrypoint's FFmpeg command**, the encoder loop, and VA-API encoding have only been checked for shell syntax.
- **The local sandbox** has unit tests for every flag it passes and for its socket, and has never started bubblewrap. `wittyplay-input-agent` isn't yet among the programs the module ships.
- **Rootless Docker and Podman** are documented from their own documentation. Podman may not accept every option WittyPlay sets on its network.
- **KasmVNC inside a sandboxed frame.** The frame has no origin of its own to keep storage in; KasmVNC's page may need a change to work that way. If it doesn't work, it fails closed: nothing is shown, and nothing is exposed.
- **Each emulator's own settings keys and command-line arguments** are from their documentation.
- **Whether the server's sandbox for modules lets this module reach a Docker socket** hasn't been checked on Linux.
- **Delay** has a budget and a way to measure it, and no measurement.

## Legal

- **WittyPlay never downloads a BIOS, firmware, keys, or games**, and the images contain none. Use only firmware you've dumped from hardware you own, and games you've obtained legally. Breaking copy protection to get them may be illegal where you live.
- Each emulator is a separate program under its own licence, named in the list and shown before an install, downloaded unmodified from its own project's release. A container image that bundles one has to carry its licence and point to its source.
- **DuckStation** (CC BY-NC-ND 4.0) is listed and never offered for download, as with non-commercial cores.
- **No Switch emulator** is in the list, and none will be: see [The Switch](STANDALONE-EMULATORS.md#the-switch).
- None of these projects is associated with WittyPlay, nor with any console maker.
