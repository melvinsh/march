#!/usr/bin/env bash
#
# The in-guest half of docs/capture.sh. Runs over ssh, prints one PNG on
# stdout, and says everything else on stderr.
#
# ssh gives no session bus, no Wayland display and no compositor handle, so the
# first thing this does is work out the live session the way the end-to-end
# suite does (internal/vm/testdata/guest-selftest.sh).

set -euo pipefail
exec 3>&1        # fd 3 is the picture; stdout is redirected to stderr below
exec 1>&2

scene=${1:?usage: capture-guest.sh <hero|desktop|menu>}

export XDG_RUNTIME_DIR=/run/user/$(id -u)
export HYPRLAND_INSTANCE_SIGNATURE=$(ls -t "$XDG_RUNTIME_DIR/hypr" | head -1)
export WAYLAND_DISPLAY=$(cd "$XDG_RUNTIME_DIR" && ls -d wayland-[0-9]* 2>/dev/null | grep -v '\.lock$' | head -1)
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
[ -n "$HYPRLAND_INSTANCE_SIGNATURE" ] && [ -n "$WAYLAND_DISPLAY" ] || {
	echo "no live Hyprland session for $(id -un)"; exit 1; }

# Windows this run opened, closed again on the way out. Nothing else is
# touched, so a machine that is in use gets it back as it was.
opened=()
scratch=9

# The config is Lua, so `hyprctl dispatch workspace 9` is now a syntax error:
# what follows `dispatch` is a Lua expression wrapped in hl.dispatch(), not
# words on a command line. Same form the shipped march-window uses.
dispatch() { hyprctl dispatch "$1" >/dev/null; }

restore() {
	local a
	for a in "${opened[@]:-}"; do
		[ -n "$a" ] && dispatch "hl.dsp.window.close({ window = [[address:$a]] })" || true
	done
	# -x, not -f: this script's own text contains the command lines it is
	# looking for, and pkill -f would match the shell running it.
	pkill -u "$(id -u)" -x fuzzel 2>/dev/null || true
	[ -n "${home_ws:-}" ] && dispatch "hl.dsp.focus({ workspace = $home_ws })" || true
}
trap restore EXIT

home_ws=$(hyprctl activeworkspace -j | jq -r '.id')
dispatch "hl.dsp.focus({ workspace = $scratch })"
sleep 0.5

# open <class> <command…> — launches it, waits for the window, remembers it.
open() {
	local class=$1; shift
	local before after i
	before=$(hyprctl clients -j | jq -r --arg c "$class" '[.[]|select(.class==$c)|.address]|join(" ")')
	dispatch "hl.dsp.exec_cmd([[$*]])"
	for i in $(seq 1 60); do
		after=$(hyprctl clients -j | jq -r --arg c "$class" --arg w "$scratch" \
			'[.[]|select(.class==$c and (.workspace.id|tostring)==$w)|.address]|join(" ")')
		for a in $after; do
			case " $before " in *" $a "*) ;; *) opened+=("$a"); echo "  window $class $a"; return 0;; esac
		done
		sleep 0.5
	done
	echo "  $class never mapped"
	return 1
}

# layer <namespace> — prints "x,y WxH" for a layer surface, or nothing.
layer() {
	hyprctl layers -j | jq -r --arg n "$1" '
		[.[].levels[][]?] | map(select(.namespace==$n)) | .[0] |
		if . == null then empty else "\(.x),\(.y) \(.w)x\(.h)" end'
}

shoot() {           # shoot [geometry]
	sleep 1
	if [ $# -gt 0 ] && [ -n "$1" ]; then
		grim -g "$1" - >&3
	else
		grim - >&3
	fi
}

case $scene in
hero)
	open Alacritty "alacritty -e bash -lc 'fastfetch; exec bash'"
	sleep 1
	# Without --no-first-run Chrome opens its welcome dialog instead of the
	# page, and that window has no app id at all — nothing to wait for.
	open google-chrome "google-chrome-stable --ozone-platform=wayland --password-store=basic --no-first-run --no-default-browser-check --new-window https://github.com/melvinsh/march"
	sleep 6
	# The menu holds the keyboard, not the screen: grim needs no input, so it
	# can shoot straight over an open fuzzel.
	dispatch "hl.dsp.exec_cmd([[march-menu]])"
	sleep 3
	shoot
	;;
desktop)
	open Alacritty "alacritty -e btop"
	sleep 1
	# -R -n: read-only and no swapfile, so a shot of somebody's real config
	# cannot change it or leave a .swp behind.
	open Alacritty "alacritty -e nvim -R -n $HOME/.config/hypr/hyprland.lua"
	sleep 1
	open Alacritty "alacritty -e bash -lc 'fastfetch; exec bash'"
	sleep 3
	shoot
	;;
menu)
	dispatch "hl.dsp.exec_cmd([[march-menu capture]])"
	sleep 3
	# fuzzel names its layer "launcher", not after itself.
	g=$(layer launcher)
	[ -n "$g" ] || { echo "the menu is not on screen"; exit 1; }
	# A margin around the picker, so it does not sit flush against the crop.
	read -r xy wh <<<"$g"
	x=${xy%,*}; y=${xy#*,}; w=${wh%x*}; h=${wh#*x}
	pad=60
	x=$((x > pad ? x - pad : 0)); y=$((y > pad ? y - pad : 0))
	shoot "$x,$y $((w + pad * 2))x$((h + pad * 2))"
	;;
*)
	echo "unknown scene: $scene"; exit 1;;
esac
