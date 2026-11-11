# WittyPlay's emulator images

Container images for WittyPlay's optional standalone emulators. To set it up, see [docs/DOCKER-BACKEND.md](../../docs/DOCKER-BACKEND.md).

| Image | What it is |
| --- | --- |
| [`base/`](base/Dockerfile) | Debian slim with Xvfb, KasmVNC, PulseAudio, FFmpeg, xdotool, the input agent and user `1000`. It has no emulator in it. |
| [`flycast/`](flycast/Dockerfile) | An example: Flycast 2.4, built `FROM` the base image, with its AppImage checked against the SHA-256 in `emulators/manifest.json`. |

These images never contain a BIOS, firmware, keys or games, and nothing in them downloads any.

## Building

Run these from the WittyPlay repository's root. The base image compiles the input agent from source.

```sh
# The base image
docker build -f docker/images/base/Dockerfile \
  -t ghcr.io/wittywilla/wittyplay-emu-base:dev .

# The example emulator image
docker build -f docker/images/flycast/Dockerfile \
  --build-arg BASE_IMAGE=ghcr.io/wittywilla/wittyplay-emu-base:dev \
  -t ghcr.io/wittywilla/wittyplay-emu-flycast:2.4 docker/images/flycast
```

For arm64 as well, use `docker buildx build --platform linux/amd64,linux/arm64 --push ...`.

## Pinning

WittyPlay only runs an image that's pinned to a digest (`repo:tag@sha256:...`), so a tag that moves can't change what runs. After pushing:

```sh
docker buildx imagetools inspect ghcr.io/wittywilla/wittyplay-emu-flycast:2.4 --format '{{json .Manifest.Digest}}'
```

Put the digest in one of two places:

- `emulators/manifest.json`, in `image.digest`, for an image this project publishes.
- The `docker.images` setting, for an image you built yourself, e.g. `{"flycast": "registry.example/flycast:2.4@sha256:..."}`.

- Or, for an image you built on this machine and never pushed, its own id: `docker image inspect --format '{{.Id}}' ghcr.io/wittywilla/wittyplay-emu-base:dev` gives `sha256:...`, which goes in `docker.images` as it is. An id names the image's contents, so it's as fixed as a digest, and is never pulled.

The base image alone is enough for every emulator WittyPlay installs: the installed emulator is mounted at `/opt/emulator` and run from there. An image with an emulator in it (the Flycast example) is for one WittyPlay has no download of.

**Not published yet.** This project hasn't pushed these images, so every `image.digest` in the manifest says `TODO`, and an emulator runs only once you've pinned an image of your own. What the base Dockerfile builds from is pinned: both `FROM` images by digest, and KasmVNC's `.deb` by SHA-256 for amd64 and arm64. A build for any other platform stops rather than install something unchecked. The Dockerfile has not been built by the people who wrote it: see "What hasn't been run" in [docs/DOCKER-BACKEND.md](../../docs/DOCKER-BACKEND.md).

## What the entrypoint does

[`entrypoint.sh`](base/entrypoint.sh) runs the emulator it's given as arguments. `WITTYPLAY_STREAM` picks the output:

- **`pipe`**: Xvfb draws the display at `WITTYPLAY_WIDTH` by `WITTYPLAY_HEIGHT`. FFmpeg captures it (`x11grab`) at `WITTYPLAY_FPS` and the PulseAudio null sink's monitor, then sends H.264 (baseline, a keyframe every second, `WITTYPLAY_KBPS` to start with; x264, or VA-API when `WITTYPLAY_ENCODER=vaapi`) and Opus as RTP to `WITTYPLAY_RTP_HOST` on `WITTYPLAY_RTP_VIDEO` and `WITTYPLAY_RTP_AUDIO`. WittyPlay relays both into WebRTC. FFmpeg runs in a loop: when WittyPlay asks for another bitrate, the agent writes it to `$XDG_RUNTIME_DIR/kbps` and stops the running FFmpeg, and the next starts at that rate.
- **`kasmvnc`**: KasmVNC serves the display on `WITTYPLAY_KASM_PORT`. `WITTYPLAY_KASM_TOKEN` is the password. Clipboard and file transfer are off, and only WittyPlay's authenticated proxy can reach it.

In both cases the input agent listens on `WITTYPLAY_INPUT_PORT` and requires `WITTYPLAY_INPUT_TOKEN`; `WITTYPLAY_KEYS`, when set, is its key map. An image whose `CMD` is its own emulator is given the game as a last argument (`WITTYPLAY_ROM`). That token is removed from the environment before the emulator starts. When the emulator exits, the container stops.

## How it runs

WittyPlay sets all of these. An image must work with them:

- uid:gid `1000:1000`, with a read-only root filesystem.
- All capabilities dropped, `no-new-privileges`, Docker's default seccomp profile, and never privileged.
- `/tmp` and `$HOME` (`/home/emu`) are tmpfs mounts with `noexec`. `/run/app` is the only tmpfs that allows execution (still `nosuid,nodev`), and it's only there for an AppImage.
- `/game/<file>` and `/bios` are read-only, `/saves` is read-write, and `/opt/emulator` (an installed emulator) is read-only.
