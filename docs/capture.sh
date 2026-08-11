#!/usr/bin/env bash
#
# Regenerates the desktop screenshots in docs/images from a running march VM.
#
#   ssh-copy-id -p <port> <user>@127.0.0.1   # once, so the script can log in
#   docs/capture.sh [vm-name]                # default: arch
#
# It drives the guest's own compositor over ssh: switches to an empty
# workspace, opens the windows a shot needs, captures with grim — which the
# desktop already ships — and closes what it opened. The workspace you were on
# comes back at the end, so it is safe to run against a machine you are using.
#
# The only host requirement is sips, which macOS has built in.

set -euo pipefail

vm=${1:-arch}
store=${MARCH_HOME:-$HOME/.local/share/march}
spec="$store/vms/$vm/vm.json"
out=$(cd "$(dirname "$0")" && pwd)/images

# The width every image is scaled to: 2x what GitHub renders a README at, so
# the result is sharp on a Retina display without carrying a 3456px frame.
width=1800

[ -f "$spec" ] || { echo "no such VM: $spec" >&2; exit 1; }
port=$(sed -n 's/.*"ssh_port": *\([0-9]*\).*/\1/p' "$spec")
user=$(sed -n 's/.*"username": *"\([^"]*\)".*/\1/p' "$spec")
[ -n "$port" ] && [ -n "$user" ] || { echo "cannot read port/user from $spec" >&2; exit 1; }

echo "capturing $vm ($user@127.0.0.1:$port) into $out" >&2

# scene <name> <file> — runs the in-guest half and saves what it prints.
#
# Everything the guest says goes to stderr; stdout carries the PNG and nothing
# else, so the capture is a straight redirect with no temporary file on either
# side.
scene() {
	local name=$1 file=$2
	echo "  $name" >&2
	ssh -p "$port" -o BatchMode=yes -o StrictHostKeyChecking=no \
		"$user@127.0.0.1" "bash -s -- $name" <"$(dirname "$0")/capture-guest.sh" >"$out/$file"
	[ -s "$out/$file" ] || { echo "  $name produced nothing" >&2; exit 1; }
	# Only ever downscale. sips -Z would happily blow a cropped picker up to
	# 1800px and make it blurry.
	if [ "$(sips -g pixelWidth "$out/$file" | sed -n 's/.*pixelWidth: *//p')" -gt "$width" ]; then
		sips -Z "$width" "$out/$file" >/dev/null
	fi
	echo "    $(sips -g pixelWidth -g pixelHeight "$out/$file" | tail -2 | tr -d ' \n') $(du -h "$out/$file" | cut -f1)" >&2
}

scene hero    hero.png
scene desktop desktop.png
scene menu    menu.png

echo "done" >&2
