# The Docker backend: standalone emulators in containers

WittyPlay normally runs libretro cores in its own sandboxed game runner (the **process** backend). The Docker backend is an optional second way to play. It runs a *standalone* emulator (PCSX2, Cemu, xemu, Flycast and others), each in a hardened container of its own, one container per game.

- **It's off by default.** `docker.enabled` is `false`, and while it's off nothing reaches the Docker daemon.
- **Libretro cores always use the process sandbox**, whatever the Docker settings say.
- **It falls back by itself.** If Docker isn't installed or can't be reached, the module starts normally and games play on a core instead, with a notice. Nothing fails.
- **WebRTC first, KasmVNC as a fallback.** A container normally streams through the same WebRTC session a core does. KasmVNC, a desktop in the browser, is only for emulators whose setup needs a desktop (their own menus and a mouse).

## Architecture

```
 Browser                 wittymediaserver         wittyplay-module                   Docker
+-----------+           +--------------+  HTTP  +-------------------------+        +----------------------------------+
| <video>   |  WebRTC   | sign-in,     | 127.   | DockerBackend           |  API   | wittyplay-<session> (uid 1000)   |
| gamepad,  |<----------| admin check, | 0.0.1  |  Lifecycle (limits,     |------->|  read-only root, cap-drop ALL,   |
| keyboard  |  H.264 +  | sets         |------->|   idle, orphan sweep)   |        |  no-new-privileges, seccomp,     |
|           |  Opus     | X-Witty-User |        |  emulators.Installer    |        |  pids/mem/cpu limits             |
|           |           | X-Witty-Admin|        |  stream.Relay  <--------+--RTP---|  Xvfb -> FFmpeg x11grab + pulse  |
|           |  /kasm/   |              |        |   (RTP -> WebRTC)       |  UDP   |  (pipe stream)                   |
|  KasmVNC  |---------->|  proxy       |------->|  KasmProxy (ticket +    |--HTTP->|  KasmVNC :6901 (kasmvnc stream)  |
|  page     |           |              |        |   per-session password) |        |                                  |
|           |  input    |              |        |  inputagent.Forwarder --+--TCP-->|  wittyplay-input-agent :7000     |
+-----------+  channel  +--------------+        +-------------------------+        |   -> xdotool keys on the display |
                                                                                   |  the emulator (/opt/emulator,    |
                                                                                   |   read-only; AppImage -> /run/app)|
                                                                                   +----------------------------------+
                                                     WittyPlay's own bridge network: no inter-container traffic,
                                                     optionally internal (no internet); ports only ever on 127.0.0.1
```

The pieces:

| Package | What it does |
| --- | --- |
| `emulators` | The built-in list of emulators (`manifest.json`) and the installer. Downloads come only from allowed hosts over HTTPS, are pinned to a SHA-256 and unpacked safely, and are recorded in `installed.json`. |
| `sandbox/docker` | The `Runtime` interface, the hardened `Build` and `Check`, the `Lifecycle`, the `KasmProxy`, the settings and backend choice, and a `Fake` for tests. |
| `sandbox/docker/engine` | The only package that imports the Docker Engine SDK. |
| `sandbox/docker/inputagent`, `cmd/wittyplay-input-agent` | The input agent inside the container. |
| `stream/relay.go` | Turns RTP from a container into WebRTC. |
| `rpc/docker.go` | The routes, the session start and the startup sweep. |
| `docker/images/` | The base image, an example emulator image, and how to build and pin them. |

## When a game goes to Docker

Starting a game (`stream.Manager.Start`) goes to Docker only when **all** of these are true:

1. `docker.enabled` is on.
2. An emulator in the list plays the game's console, and that emulator is installed.
3. No administrator has set that emulator to `process` in `docker.backend`.
4. The emulator has an image pinned to a digest, either in the list or in `docker.images`.
5. Docker answers.

If 1, 2 or 3 isn't true, the game takes the usual process path, unchanged. If 4 or 5 isn't true, it also takes the process path, and the player gets a notice explaining why. The answer to a start is `{"id", "backend", "emulator"}` plus one of these:

- `connect`: for `docker+webrtc`, the route the client posts its hello and offer to. The answer is the same `Connected` a core's game gives.
- `kasmUrl`: for `docker+kasmvnc`, `/kasm/<session>/?ticket=...`.

## Routes

These are on the module's service, behind the service token. Only the server can reach them, and it passes people's requests through. The server sets `X-Witty-User` to the signed-in account and `X-Witty-Admin: 1` for an administrator, and must strip both from anything a browser sends. A request without them is refused, so a server that doesn't send them yet gets refusals, never access.

| Method | Path | Who | What |
| --- | --- | --- | --- |
| GET | `/docker/status` | admin | Whether the backend is on, whether Docker answers, its API version, how many containers are running |
| GET | `/docker/settings` | admin | The settings, their schema and the backends |
| PUT | `/docker/settings` | admin | Replace the settings. They're checked, saved under the module's data folder, and turning Docker on runs the orphan sweep |
| GET | `/docker/emulators` | admin | The list, with what's installed and why an entry can't be installed |
| GET | `/docker/installed` | admin | What's installed |
| POST | `/docker/emulators/{id}/install` | admin | Start an install. Returns `202` and the job at once |
| GET | `/docker/emulators/{id}/install` | admin | The job: `running`, `done` or `failed`, with bytes received and the total |
| DELETE | `/docker/emulators/{id}` | admin | Uninstall. Refused while someone is playing with it |
| POST | `/docker/sessions/{sid}/connect` | the player | WebRTC: `{"hello", "offer"}` returns `Connected` |
| DELETE | `/docker/sessions/{sid}` | the player or an admin | Stop a game in a container |
| any | `/kasm/{sid}/...` | the player, with the session's ticket | The KasmVNC proxy |

Without a user the answer is `401`, without admin it's `403`, and with Docker off an install gets `409`. A session that belongs to someone else answers `404`.

## Settings

Every setting is admin-only. They're listed in `module.json` (`capabilities.dockerSettings`) and described by `docker.Schema()`, which `GET /docker/settings` returns.

| Key | Default | What it does |
| --- | --- | --- |
| `docker.enabled` | `false` | The switch |
| `docker.network` | `wittyplay` | WittyPlay's own bridge network |
| `docker.internal` | `false` | No way out to the internet. Use it where the module can reach container addresses directly (Linux without Docker Desktop) |
| `docker.publishLoopback` | `false` | Publish the stream ports on 127.0.0.1 instead, for Docker Desktop |
| `docker.maxContainers` | 2 | Containers at once |
| `docker.memoryMB`, `docker.cpus`, `docker.pids` | 4096, 2, 512 | Limits for each container |
| `docker.idleMinutes`, `docker.maxHours` | 10, 6 | A container with no input for this long is stopped, and none runs longer than this |
| `docker.gpu` | `{}` | `{"DRI": true}` for `/dev/dri`, `{"Nvidia": true}` for the NVIDIA runtime |
| `docker.backend` | `{}` | Per emulator: `docker+webrtc`, `docker+kasmvnc`, or `process` (don't use Docker for it) |
| `docker.images` | `{}` | Per emulator: your own image, pinned `repo:tag@sha256:...` |

## Security model

A standalone emulator is a large native program that parses untrusted files. It's treated the way a core is: as something that might be compromised.

**The container.** `docker.Build` makes the spec in one place, and `docker.Check` checks every spec again before the engine creates it, so nothing weaker reaches Docker.

- It runs as uid:gid `1000:1000`, never root, and is never privileged.
- The root filesystem is read-only.
- `--cap-drop ALL` with no capabilities added, `no-new-privileges`, and Docker's default seccomp profile. Any `seccomp=` or `apparmor=` override, and anything "unconfined", is refused.
- Every container has pids, memory (with no extra swap) and CPU limits, and `--init`.
- `/tmp` and `$HOME` are tmpfs mounts with `noexec,nosuid,nodev`. **`/run/app` is the only tmpfs that allows execution, and only for an AppImage**: FUSE isn't available to an unprivileged container, so an AppImage unpacks itself there (`APPIMAGE_EXTRACT_AND_RUN=1`, `TMPDIR=/run/app`). It's still `nosuid,nodev`, owned by uid 1000 with mode 0700.
- Mounts: the game at `/game/<file>`, the BIOS folder at `/bios` and the installed emulator at `/opt/emulator` are all read-only. `/saves` (this person's saves and settings for this emulator) is the only writable one. `/`, `/etc`, `/proc`, `/sys`, `/dev`, `/run` and `/var/run` are refused, and so is anything containing `docker.sock`.
- The only device passed through is `/dev/dri`, and only when an admin turns it on. NVIDIA GPUs go through the NVIDIA runtime's device request instead.
- Images must be pinned to a digest. A tag alone is refused.
- Every container carries `wittyplay.managed`, `wittyplay.session`, `wittyplay.user` and `wittyplay.emulator` labels. WittyPlay only ever stops or removes containers that have them.

**The network.** WittyPlay's network is a bridge of its own with inter-container traffic off, so one session can't reach another. It can be internal. Ports are only published on `127.0.0.1` (for Docker Desktop), never on another address. The relay only listens on a private or loopback address, and only takes RTP from the container's own address.

**The secrets.** Every session gets three random 256-bit secrets:

- **The KasmVNC password.** The proxy adds it on the way in, so the browser never sees it. The proxy also strips the browser's cookies and authorisation headers, and KasmVNC's `WWW-Authenticate` and `Set-Cookie`.
- **The KasmVNC ticket**, which the proxy requires on top of the server's sign-in. It travels as a header, a query parameter, or a `SameSite=Strict`, `HttpOnly` cookie scoped to the session's path.
- **The input agent's token**, which the agent clears from its environment and the entrypoint clears before the emulator starts.

Only the person playing can reach a session's desktop or WebRTC connection. An administrator can stop it, but can't watch it.

**Its lifetime.** A container is stopped after `docker.idleMinutes` with no input, after `docker.maxHours` whatever happens, and when its emulator exits. When the module starts, and when Docker is turned on, it removes any container carrying WittyPlay's label that it didn't start, which is what a crash leaves behind. This runs only when Docker is on and reachable, and never stops the module from starting.

## Setup

1. Install Docker Engine (Linux) or Docker Desktop (macOS, Windows) on the server's machine, and make sure the user WittyMediaServer runs as can reach the daemon. `DOCKER_HOST` is honoured. Use a rootless daemon if you can.
2. Build and push the images, or pull published ones once they have digests: see [docker/images/README.md](../docker/images/README.md).
3. In WittyMediaServer, as an admin, open WittyPlay's Docker settings: turn on `docker.enabled`, and on Docker Desktop also `docker.publishLoopback`.
4. Install an emulator from the list. Its download is checked against its SHA-256 before anything is unpacked.
5. Put any BIOS or firmware you own in your BIOS folder. It's mounted read-only.
6. Start a game for that emulator's console.

`GET /docker/status` says whether Docker answered, and why not if it didn't.

### Hashes in the list

| Emulator | Download | SHA-256 |
| --- | --- | --- |
| PCSX2 2.2.0 | `pcsx2-v2.2.0-linux-appimage-x64-Qt.AppImage` | checked |
| DuckStation 0.1-12442 | `DuckStation-x64.AppImage`, pinned to the tag `v0.1-12442` instead of the moving `latest` | checked, but never offered for download (non-commercial licence) |
| xemu 0.8.5 | `xemu-v0.8.5-x86_64.AppImage` | checked |
| Cemu 2.6 | `Cemu-2.6-x86_64.AppImage` | checked, and matches GitHub's own digest |
| melonDS 1.0 | `melonDS-appimage-x86_64.zip` (the asset's real name), holding `melonDS-x86_64.AppImage` | checked, and matches GitHub's own digest |
| Flycast 2.4 | `flycast-x86_64.AppImage` | checked |
| Lichii build-130 | x86-64 and arm64 AppImages | checked |
| Dolphin, PPSSPP, RPCS3 | none in the list (image only) | none |

Every container image digest is still `TODO`. Until one is filled in, either in the list or in `docker.images`, that emulator plays on a core instead.

## GPU

- **Intel and AMD:** set `docker.gpu` to `{"DRI": true}`. `/dev/dri` is passed through, and the image's Mesa drivers use it. The user uid 1000 maps to on the host needs to be able to open the render node: add it to the `render` group, or use a rootless daemon whose user is in that group.
- **NVIDIA:** install the NVIDIA Container Toolkit, then set `{"Nvidia": true}`. The container asks the NVIDIA runtime for every GPU.
- Without either, the picture is drawn by Mesa's software renderer (llvmpipe). That's enough for older consoles, but slow for PS2 and later.

## Troubleshooting

| What you see | What to do |
| --- | --- |
| "Docker isn't reachable on this server, so this game runs on a core instead." | `GET /docker/status` says why. Check that the daemon is running, that `DOCKER_HOST` is right, and that the server's user can use the socket. |
| "… has no container image pinned to a digest yet" | Build and push the image, then set its digest in `docker.images`. |
| An install fails with "nobody has checked this emulator's download yet" | Its hash is `TODO`, so it can't be installed. That's on purpose. |
| A black picture over WebRTC | The container isn't reaching the relay. On Docker Desktop turn on `docker.publishLoopback`. On Linux, check that a firewall allows UDP from WittyPlay's bridge to the host. The container's logs (`docker logs wittyplay-<session>`) show FFmpeg's errors. |
| The KasmVNC page says "no" | The ticket is missing or wrong. Open the `kasmUrl` from the start answer, not a bookmark. Tickets end with the session. |
| "already running as many emulator containers as it allows" | Raise `docker.maxContainers`, or stop a game. |
| Containers left after a crash | They're removed at the next start, or when Docker is turned on. You can also remove them by hand: `docker ps -a --filter label=wittyplay.managed=true`. |
| An AppImage won't start | It needs `/run/app` with exec, which only a program whose entry ends in `.AppImage` gets. Check the image's `TMPDIR` and `APPIMAGE_EXTRACT_AND_RUN`. |

To run the integration test against a real daemon: `go test -tags docker_integration -run Integration -v ./sandbox/docker/engine/`. It pulls busybox pinned by digest, creates a container from `Build`'s spec, checks the hardening through Docker's own inspect, and removes everything.

## The input agent's limits

The agent turns the browser's input into X key and mouse events with `xdotool`. It doesn't use a virtual gamepad, because `/dev/uinput` would have to be passed into the container, which the hardening refuses.

- **Player 1 only.** Input for other players is ignored.
- **No analogue.** The left stick becomes its four directions' keys (`i`, `k`, `j`, `l`) once it's past half-way. The right stick and analogue triggers are dropped. Pressure-sensitive buttons are either on or off.
- **Keys, not a pad.** Each emulator's own settings have to map its controls to the agent's keys (`inputagent.Keys`: `z`, `x`, `a`, `s`, `q`, `w`, the arrow keys, Return, BackSpace and so on). An image ships those settings.
- A pointer or the first touch moves and clicks the mouse.
- There's no rumble, motion or lights.

## Legal

- **WittyPlay never downloads a BIOS, firmware, keys, or games**, and the images don't contain any. Many of these emulators won't start a game without firmware (PS2 BIOS, Xbox BIOS and EEPROM, Wii U and Switch keys and firmware, and so on). Use only firmware you've dumped from hardware you own, and games you've obtained legally. Breaking copy protection to get them may be illegal where you live.
- **DuckStation** is licensed CC BY-NC-ND 4.0, which doesn't allow commercial use or modified versions. WittyPlay lists it, but never offers it for download, just as with non-commercial cores. An admin who's sure their use is non-commercial can build an image of their own, unmodified, and set it in `docker.images`.
- Every other emulator is under its own licence (GPL, MPL), named in the list. WittyPlay doesn't modify them. The images run upstream's own release builds, and a container image that bundles one has to carry its licence and point to its source.
- Lichii is marked experimental by its own authors.
- None of these projects is associated with WittyPlay, nor with any console maker.
