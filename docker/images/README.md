# WittyPlay's emulator images

Container images for WittyPlay's optional Docker backend. To set it up, see [docs/DOCKER-BACKEND.md](../../docs/DOCKER-BACKEND.md).

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

**TODO:** the published images don't have digests yet. Every `image.digest` in the manifest still says `TODO`, as do the `FROM` lines and KasmVNC's `.deb` hash in the base Dockerfile. Until a digest is filled in, that emulator falls back to the process sandbox and shows a notice.

## What the entrypoint does

[`entrypoint.sh`](base/entrypoint.sh) runs the emulator it's given as arguments. `WITTYPLAY_STREAM` picks the output:

- **`pipe`**: Xvfb draws the display. FFmpeg captures it (`x11grab`) and the PulseAudio null sink's monitor, then sends H.264 (baseline, a keyframe every second) and Opus as RTP to `WITTYPLAY_RTP_HOST` on `WITTYPLAY_RTP_VIDEO` and `WITTYPLAY_RTP_AUDIO`. WittyPlay relays both into WebRTC.
- **`kasmvnc`**: KasmVNC serves the display on `WITTYPLAY_KASM_PORT`. `WITTYPLAY_KASM_TOKEN` is the password. Clipboard and file transfer are off, and only WittyPlay's authenticated proxy can reach it.

In both cases the input agent listens on `WITTYPLAY_INPUT_PORT` and requires `WITTYPLAY_INPUT_TOKEN`. That token is removed from the environment before the emulator starts. When the emulator exits, the container stops.

## How it runs

WittyPlay sets all of these. An image must work with them:

- uid:gid `1000:1000`, with a read-only root filesystem.
- All capabilities dropped, `no-new-privileges`, Docker's default seccomp profile, and never privileged.
- `/tmp` and `$HOME` (`/home/emu`) are tmpfs mounts with `noexec`. `/run/app` is the only tmpfs that allows execution (still `nosuid,nodev`), and it's only there for an AppImage.
- `/game/<file>` and `/bios` are read-only, `/saves` is read-write, and `/opt/emulator` (an installed emulator) is read-only.
