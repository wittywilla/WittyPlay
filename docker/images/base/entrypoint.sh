#!/bin/sh
# WittyPlay's container entrypoint. It starts a display, sound and the input agent, then the
# emulator it was given as arguments, and streams it one of two ways, as WITTYPLAY_STREAM says:
#
#   pipe     FFmpeg captures the X display (x11grab) and PulseAudio and sends H.264 and Opus over
#            RTP to WITTYPLAY_RTP_HOST:WITTYPLAY_RTP_VIDEO / WITTYPLAY_RTP_AUDIO, which WittyPlay
#            relays into WebRTC.
#   kasmvnc  KasmVNC serves the display on WITTYPLAY_KASM_PORT, with WITTYPLAY_KASM_TOKEN as the
#            password. Only WittyPlay's authenticated proxy reaches it.
#
# The container's root is read-only; everything written goes to /tmp, $HOME (tmpfs) or /saves.
# When the emulator exits, so does the container.
set -eu

: "${WITTYPLAY_STREAM:?WITTYPLAY_STREAM must be pipe or kasmvnc}"
: "${WITTYPLAY_INPUT_PORT:=7000}"
: "${WITTYPLAY_WIDTH:=1280}"
: "${WITTYPLAY_HEIGHT:=720}"
: "${WITTYPLAY_FPS:=60}"
: "${DISPLAY:=:1}"
: "${XDG_RUNTIME_DIR:=/tmp/runtime}"
export DISPLAY XDG_RUNTIME_DIR WITTYPLAY_WIDTH WITTYPLAY_HEIGHT

if [ "$#" -eq 0 ]; then
	echo "wittyplay-entrypoint: no emulator to run" >&2
	exit 2
fi

mkdir -p "$XDG_RUNTIME_DIR/pulse" "$HOME" /saves/config /saves/data 2>/dev/null || true
chmod 0700 "$XDG_RUNTIME_DIR"

pids=""
cleanup() {
	for p in $pids; do kill "$p" 2>/dev/null || true; done
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
	# needs nothing more. TODO: check these flags against a running KasmVNC 1.3.3.
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
if [ "$WITTYPLAY_STREAM" = pipe ]; then
	: "${WITTYPLAY_RTP_HOST:?}" "${WITTYPLAY_RTP_VIDEO:?}" "${WITTYPLAY_RTP_AUDIO:?}"
	ffmpeg -hide_banner -loglevel warning -nostdin \
		-f x11grab -framerate "$WITTYPLAY_FPS" -video_size "${WITTYPLAY_WIDTH}x${WITTYPLAY_HEIGHT}" -draw_mouse 0 -i "$DISPLAY" \
		-f pulse -ar 48000 -ac 2 -i wittyplay.monitor \
		-map 0:v -c:v libx264 -preset ultrafast -tune zerolatency -profile:v baseline -level 3.1 -pix_fmt yuv420p \
		-g "$WITTYPLAY_FPS" -keyint_min "$WITTYPLAY_FPS" -bf 0 -b:v 6M -maxrate 8M -bufsize 2M \
		-f rtp -payload_type 102 "rtp://${WITTYPLAY_RTP_HOST}:${WITTYPLAY_RTP_VIDEO}?pkt_size=1200" \
		-map 1:a -c:a libopus -b:a 128k -application lowdelay -frame_duration 10 \
		-f rtp -payload_type 111 "rtp://${WITTYPLAY_RTP_HOST}:${WITTYPLAY_RTP_AUDIO}?pkt_size=1200" &
	pids="$pids $!"
fi

# ---------------------------------------------------------------- the emulator
# An AppImage unpacks itself into /run/app (the spec sets TMPDIR and APPIMAGE_EXTRACT_AND_RUN);
# FUSE isn't available to an unprivileged container.
"$@" &
emu=$!
wait "$emu"
status=$?
echo "wittyplay-entrypoint: the emulator exited with $status" >&2
exit "$status"
