#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 WittyWilla and contributors
# WittyPlay's container entrypoint. It starts a display, sound and the input agent, then the
# emulator it was given as arguments, and streams it one of two ways, as WITTYPLAY_STREAM says:
#
#   pipe     FFmpeg captures the X display (x11grab) and PulseAudio and sends H.264 and Opus over
#            RTP to WITTYPLAY_RTP_HOST:WITTYPLAY_RTP_VIDEO / WITTYPLAY_RTP_AUDIO, which WittyPlay
#            relays into WebRTC.
#   kasmvnc  KasmVNC serves the display on WITTYPLAY_KASM_PORT, with WITTYPLAY_KASM_TOKEN as the
#            password. Only WittyPlay's authenticated proxy reaches it.
#
# The same script runs in the sandbox without Docker (sandbox/local), where WITTYPLAY_IO is set:
# there's no network there, so the agent carries the RTP and the input through a socket instead.
#
# The container's root is read-only; everything written goes to /tmp, $HOME (tmpfs) or /saves.
# When the emulator exits, so does the container.
set -eu

: "${WITTYPLAY_STREAM:?WITTYPLAY_STREAM must be pipe or kasmvnc}"
: "${WITTYPLAY_INPUT_PORT:=7000}"
: "${WITTYPLAY_WIDTH:=1280}"
: "${WITTYPLAY_HEIGHT:=720}"
: "${WITTYPLAY_FPS:=60}"
: "${WITTYPLAY_KBPS:=6000}"
: "${DISPLAY:=:1}"
: "${XDG_RUNTIME_DIR:=/tmp/runtime}"
export DISPLAY XDG_RUNTIME_DIR WITTYPLAY_WIDTH WITTYPLAY_HEIGHT

if [ "$#" -eq 0 ]; then
	echo "wittyplay-entrypoint: no emulator to run" >&2
	exit 2
fi

mkdir -p "$XDG_RUNTIME_DIR/pulse" "$HOME" "${XDG_CONFIG_HOME:-/saves/config}" "${XDG_DATA_HOME:-/saves/data}" 2>/dev/null || true
chmod 0700 "$XDG_RUNTIME_DIR"

pids=""
cleanup() {
	for p in $pids; do kill "$p" 2>/dev/null || true; done
	[ -r "$XDG_RUNTIME_DIR/encoder.pid" ] && kill "$(cat "$XDG_RUNTIME_DIR/encoder.pid")" 2>/dev/null || true
	wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- display
case "$WITTYPLAY_STREAM" in
pipe)
	Xvfb "$DISPLAY" -screen 0 "${WITTYPLAY_WIDTH}x${WITTYPLAY_HEIGHT}x24" -nolisten tcp -noreset &
	pids="$pids $!"
	;;
kasmvnc)
	: "${WITTYPLAY_KASM_PORT:?}" "${WITTYPLAY_KASM_TOKEN:?}"
	mkdir -p "$HOME/.vnc"
	# The password file; the proxy adds the matching basic auth, the browser never sees it.
	printf '%s\n%s\n' "$WITTYPLAY_KASM_TOKEN" "$WITTYPLAY_KASM_TOKEN" | kasmvncpasswd -u wittyplay -wo "$HOME/.kasmpasswd" >/dev/null
	cat >"$HOME/.vnc/kasmvnc.yaml" <<EOF
network:
  protocol: http
  interface: 0.0.0.0
  websocket_port: ${WITTYPLAY_KASM_PORT}
  ssl:
    require_ssl: false
desktop:
  resolution:
    width: ${WITTYPLAY_WIDTH}
    height: ${WITTYPLAY_HEIGHT}
  allow_resize: false
data_loss_prevention:
  clipboard:
    server_to_client:
      enabled: false
    client_to_server:
      enabled: false
  file_transfer:
    enabled: false
EOF
	touch "$HOME/.vnc/.de-was-selected"
	unset WITTYPLAY_KASM_TOKEN
	# HTTP basic auth from the password file guards the web client; the VNC protocol inside it
	# needs nothing more. These flags are KasmVNC's documented ones; they haven't been run against 1.5.2 here (see docs/DOCKER-BACKEND.md, What hasn't been run).
	Xvnc "$DISPLAY" -interface 0.0.0.0 -websocketPort "$WITTYPLAY_KASM_PORT" -KasmPasswordFile "$HOME/.kasmpasswd" \
		-geometry "${WITTYPLAY_WIDTH}x${WITTYPLAY_HEIGHT}" -depth 24 -SecurityTypes None -nolisten tcp &
	pids="$pids $!"
	;;
*)
	echo "wittyplay-entrypoint: WITTYPLAY_STREAM=$WITTYPLAY_STREAM is neither pipe nor kasmvnc" >&2
	exit 2
	;;
esac

i=0
until xdpyinfo -display "$DISPLAY" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 100 ]; then
		echo "wittyplay-entrypoint: the display didn't start" >&2
		exit 1
	fi
	sleep 0.1
done

# ---------------------------------------------------------------- sound
pulseaudio --daemonize=no --exit-idle-time=-1 --disallow-exit --system=false \
	-n --load="module-native-protocol-unix socket=$XDG_RUNTIME_DIR/pulse/native auth-anonymous=1" \
	--load="module-null-sink sink_name=wittyplay rate=48000" --load="module-always-sink" &
pids="$pids $!"
i=0
until pactl info >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -gt 100 ] && break
	sleep 0.1
done
pactl set-default-sink wittyplay >/dev/null 2>&1 || true

# ---------------------------------------------------------------- input
# The agent reads its secret and clears it from its own environment; it's cleared here too, so the
# emulator never sees it.
wittyplay-input-agent &
pids="$pids $!"
unset WITTYPLAY_INPUT_TOKEN

# ---------------------------------------------------------------- stream
# FFmpeg runs in a loop: when WittyPlay asks for another bitrate (the connection got worse, or
# better), the agent writes it to $XDG_RUNTIME_DIR/kbps and stops the running FFmpeg, and the
# next one starts at that rate with a keyframe. Anything else that stops it starts it again too,
# a second later, so one bad moment doesn't end the game.
#
# What keeps the delay down: one frame captured at a time with no input buffering, no B-frames,
# no lookahead (zerolatency), a keyframe every second so a browser that joins waits at most that
# long, a tight rate buffer, and 10 ms Opus frames.
encode() {
	while :; do
		kbps="$(cat "$XDG_RUNTIME_DIR/kbps" 2>/dev/null || echo "$WITTYPLAY_KBPS")"
		case "$kbps" in '' | *[!0-9]*) kbps="$WITTYPLAY_KBPS" ;; esac
		if [ "${WITTYPLAY_ENCODER:-x264}" = vaapi ] && [ -e /dev/dri/renderD128 ]; then
			# The graphics card encodes: far less work for the processor at 1080p.
			video="-vaapi_device /dev/dri/renderD128 -vf format=nv12,hwupload -c:v h264_vaapi -profile:v constrained_baseline -bf 0 -rc_mode CBR"
		else
			video="-c:v libx264 -preset ultrafast -tune zerolatency -profile:v baseline -pix_fmt yuv420p -bf 0"
		fi
		# shellcheck disable=SC2086
		ffmpeg -hide_banner -loglevel warning -nostdin -fflags nobuffer -flags low_delay \
			-f x11grab -framerate "$WITTYPLAY_FPS" -video_size "${WITTYPLAY_WIDTH}x${WITTYPLAY_HEIGHT}" -draw_mouse 0 -i "$DISPLAY" \
			-f pulse -fragment_size 960 -ar 48000 -ac 2 -i wittyplay.monitor \
			-map 0:v $video -g "$WITTYPLAY_FPS" -keyint_min "$WITTYPLAY_FPS" \
			-b:v "${kbps}k" -maxrate "${kbps}k" -bufsize "$((kbps / 4))k" \
			-f rtp -payload_type 102 "rtp://${WITTYPLAY_RTP_HOST}:${WITTYPLAY_RTP_VIDEO}?pkt_size=1200" \
			-map 1:a -c:a libopus -b:a 128k -application lowdelay -frame_duration 10 \
			-f rtp -payload_type 111 "rtp://${WITTYPLAY_RTP_HOST}:${WITTYPLAY_RTP_AUDIO}?pkt_size=1200" &
		echo "$!" >"$XDG_RUNTIME_DIR/encoder.pid"
		wait "$!" || true
		sleep 1
	done
}
if [ "$WITTYPLAY_STREAM" = pipe ]; then
	: "${WITTYPLAY_RTP_HOST:?}" "${WITTYPLAY_RTP_VIDEO:?}" "${WITTYPLAY_RTP_AUDIO:?}"
	encode &
	pids="$pids $!"
fi

# ---------------------------------------------------------------- the emulator
# An AppImage unpacks itself into /run/app (the spec sets TMPDIR and APPIMAGE_EXTRACT_AND_RUN);
# FUSE isn't available to an unprivileged container.
#
# An image with an emulator of its own is given no command by WittyPlay, so its own CMD is what
# arrives here, without the game: it's added.
case " $* " in
*" $WITTYPLAY_ROM "*) ;;
*) [ -n "${WITTYPLAY_ROM:-}" ] && set -- "$@" "$WITTYPLAY_ROM" ;;
esac
"$@" &
emu=$!
wait "$emu"
status=$?
echo "wittyplay-entrypoint: the emulator exited with $status" >&2
exit "$status"
