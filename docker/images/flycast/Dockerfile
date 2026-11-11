# syntax=docker/dockerfile:1
#
# An example emulator image: Flycast (Dreamcast, NAOMI), GPL-2.0-only, upstream
# https://github.com/flyinghead/flycast. Built FROM WittyPlay's base image, with Flycast's own
# AppImage baked in at /opt/flycast, checked against the same SHA-256 as emulators/manifest.json.
#
# Flycast runs Dreamcast games without a BIOS of the person's own (it has an HLE BIOS); a
# person who wants the real one puts it in their BIOS folder, which is mounted read-only at /bios.
# Nothing here downloads a BIOS, firmware, keys or games.
#
# Build (after the base image):
#   docker build -f docker/images/flycast/Dockerfile -t ghcr.io/wittywilla/wittyplay-emu-flycast:2.4 docker/images/flycast
#
# Then pin it: put the pushed image's digest in emulators/manifest.json, or in the
# docker.images setting for an image of your own.

# TODO: pin by digest (ghcr.io/wittywilla/wittyplay-emu-base:<version>@sha256:...).
ARG BASE_IMAGE=ghcr.io/wittywilla/wittyplay-emu-base:dev
FROM ${BASE_IMAGE}

ARG FLYCAST_VERSION=2.4
ARG FLYCAST_SHA256=35be06ecbdea633a6985ebd9a05926a31974036e3a14d78c3b7810639e81db5f

USER root
# ADD with --checksum fails the build if the file isn't the one that was hashed.
ADD --checksum=sha256:${FLYCAST_SHA256} --chmod=0755 \
    https://github.com/flyinghead/flycast/releases/download/v${FLYCAST_VERSION}/flycast-x86_64.AppImage \
    /opt/flycast/flycast.AppImage
USER 1000:1000

# Flycast reads its settings from $XDG_CONFIG_HOME/flycast, which is under /saves. Map its
# keyboard controls to the input agent's keys (sandbox/docker/inputagent, Keys) there.
# TODO: ship an emu.cfg with that mapping.
ENV APPIMAGE_EXTRACT_AND_RUN=1 \
    TMPDIR=/run/app
CMD ["/opt/flycast/flycast.AppImage"]
