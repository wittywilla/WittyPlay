# syntax=docker/dockerfile:1
#
# WittyPlay's base image for standalone emulators (docs/DOCKER-BACKEND.md). It has an X server,
# PulseAudio, FFmpeg for the pipe stream, KasmVNC for the desktop fallback, xdotool and the input
# agent. It has no emulator: an image built FROM this one adds one, or the module mounts an
# installed one read-only at /opt/emulator.
#
# It never contains a BIOS, firmware, keys or games, and never will.
#
# Build from the WittyPlay repository's root (the input agent is built from source):
#   docker build -f docker/images/base/Dockerfile -t ghcr.io/wittywilla/wittyplay-emu-base:dev .

# Both base images are pinned by digest (the multi-platform index, read from Docker Hub on
# 2026-11-11), so a build can't be handed something else under the same tag. To move to newer
# ones: docker buildx imagetools inspect golang:1.26-bookworm, and the same for debian.
ARG GO_IMAGE=golang:1.26-bookworm@sha256:dc40a13b61304647ed7e5d581d246b86413dfc29d63c6d018310feb93cd98fc0
ARG DEBIAN_IMAGE=debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
# KasmVNC's own .deb for bookworm, checked against GitHub's digest for each asset. A build for
# a platform with no hash here stops: nothing unchecked is ever installed.
ARG KASMVNC_VERSION=1.5.2
ARG KASMVNC_SHA256_AMD64=5f4bfda43e89178782eac37ae463b1d95d16ac5e45da8b7989ec75404e764993
ARG KASMVNC_SHA256_ARM64=b847429cbcf95736fdd5c3be682eea38cae4d689f012eec9c2d5f221b175d1c2

# ---------------------------------------------------------------- the input agent
FROM ${GO_IMAGE} AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wittyplay-input-agent ./cmd/wittyplay-input-agent

# ---------------------------------------------------------------- the image
FROM ${DEBIAN_IMAGE}
ARG KASMVNC_VERSION
ARG KASMVNC_SHA256_AMD64
ARG KASMVNC_SHA256_ARM64
ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl \
      xvfb x11-utils xdotool \
      pulseaudio pulseaudio-utils \
      ffmpeg \
      libgl1 libegl1 libgles2 libvulkan1 mesa-vulkan-drivers libgl1-mesa-dri \
      libfuse2 fonts-dejavu-core tini \
 && case "${TARGETARCH}" in \
      amd64) kasm_sha="${KASMVNC_SHA256_AMD64}" ;; \
      arm64) kasm_sha="${KASMVNC_SHA256_ARM64}" ;; \
      *) echo "no checked KasmVNC download for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL --proto '=https' -o /tmp/kasmvnc.deb \
      "https://github.com/kasmtech/KasmVNC/releases/download/v${KASMVNC_VERSION}/kasmvncserver_debian_bookworm_${KASMVNC_VERSION}_${TARGETARCH}.deb" \
 && echo "${kasm_sha}  /tmp/kasmvnc.deb" | sha256sum -c - \
 && apt-get install -y --no-install-recommends /tmp/kasmvnc.deb \
 && rm -f /tmp/kasmvnc.deb \
 && apt-get purge -y curl && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/*

# uid 1000, the user every session runs as. The spec sets it too (--user 1000:1000); this makes
# its name and home exist. The home is a tmpfs at run time.
RUN groupadd -g 1000 emu \
 && useradd -u 1000 -g 1000 -d /home/emu -s /usr/sbin/nologin -M emu \
 && usermod -aG ssl-cert emu 2>/dev/null || true \
 && mkdir -p /home/emu /game /saves /bios /opt/emulator /run/app \
 && chown 1000:1000 /home/emu /run/app

COPY --from=agent /out/wittyplay-input-agent /usr/local/bin/wittyplay-input-agent
COPY docker/images/base/entrypoint.sh /usr/local/bin/wittyplay-entrypoint
RUN chmod 0755 /usr/local/bin/wittyplay-entrypoint /usr/local/bin/wittyplay-input-agent

USER 1000:1000
WORKDIR /home/emu
EXPOSE 6901 7000
ENTRYPOINT ["/usr/local/bin/wittyplay-entrypoint"]
