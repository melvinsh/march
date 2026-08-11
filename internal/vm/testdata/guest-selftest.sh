#!/bin/bash
# march's in-guest self-test: every application the desktop installs and every
# interaction its menu, bar and keys offer, exercised inside the running
# session rather than inferred from the install script.
#
# It runs as the desktop user, on the live Hyprland instance, and prints one
# line per check:
#
#   PASS <name>
#   FAIL <name> :: <detail>
#   SKIP <name> :: <reason>
#
# and finishes with SELFTEST-DONE <pass> <fail> <skip>. The Go test parses
# those lines, so nothing here should print anything else to stdout.
#
# Interactive pieces — the launcher's picker and the region selector — are
# stood in for by fakes on PATH, so the code paths behind them run for real
# while the test still answers their prompts.
set -uo pipefail

ok=0
bad=0
skipped=0

pass() { printf 'PASS %s\n' "$1"; ok=$((ok + 1)); }
fail() { printf 'FAIL %s :: %s\n' "$1" "${2//$'\n'/ }"; bad=$((bad + 1)); }
skip() { printf 'SKIP %s :: %s\n' "$1" "${2//$'\n'/ }"; skipped=$((skipped + 1)); }

# check NAME COMMAND... — passes when the command succeeds.
check() {
  local name=$1
  shift
  local out
  if out=$("$@" 2>&1); then
    pass "$name"
  else
    fail "$name" "${out:-exit $?}"
  fi
}

# contains NAME NEEDLE COMMAND... — passes when the output contains a string.
contains() {
  local name=$1 needle=$2
  shift 2
  local out
  out=$("$@" 2>&1)
  if [[ $out == *"$needle"* ]]; then
    pass "$name"
  else
    fail "$name" "wanted ${needle@Q} in: ${out:0:200}"
  fi
}

# ── the session ─────────────────────────────────────────────────────────────

export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export HYPRLAND_INSTANCE_SIGNATURE="$(ls -t "$XDG_RUNTIME_DIR/hypr" 2>/dev/null | head -1)"
export WAYLAND_DISPLAY="$(cd "$XDG_RUNTIME_DIR" && ls -1 wayland-[0-9]* 2>/dev/null | grep -v '\.lock$' | head -1)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
export XDG_CURRENT_DESKTOP=Hyprland

if [[ -z $HYPRLAND_INSTANCE_SIGNATURE || -z $WAYLAND_DISPLAY ]]; then
  fail session "no Hyprland instance for $(id -un): sig=${HYPRLAND_INSTANCE_SIGNATURE:-none} display=${WAYLAND_DISPLAY:-none}"
  printf 'SELFTEST-DONE %d %d %d\n' "$ok" "$bad" "$skipped"
  exit 1
fi
pass session

# A previous run that killed the lock screen leaves Hyprland holding the lock
# with nothing to unlock it, and in that state hyprctl answers with a warning
# instead of JSON. Clearing it is what makes a second run possible at all.
if ! hyprctl -j clients >/dev/null 2>&1; then
  pkill -9 hyprlock 2>/dev/null
  hyprctl eval 'hl.clear_crashed_lockscreen()' >/dev/null 2>&1
  sleep 1
fi

WORK=/tmp/march-selftest
rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/pkgbin" "$WORK/log"

# ── window helpers ──────────────────────────────────────────────────────────

# wait_window REGEX [SECONDS] — waits for a window whose class matches.
wait_window() {
  local re=$1 limit=${2:-40} i
  for ((i = 0; i < limit * 2; i++)); do
    if hyprctl -j clients 2>/dev/null |
      jq -e --arg re "$re" 'any(.[]; (.class // "") | test($re; "i"))' >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

# window_json REGEX FILTER — reads a field off the first matching window.
window_json() {
  hyprctl -j clients 2>/dev/null |
    jq -r --arg re "$1" "[.[] | select((.class // \"\") | test(\$re; \"i\"))][0] | $2" 2>/dev/null
}

# close_window REGEX — closes whatever mapped, by the pid the compositor knows.
# Killing the command instead would miss the launchers that fork and exit,
# LibreOffice above all.
close_window() {
  local pid
  pid=$(window_json "$1" '.pid')
  [[ $pid =~ ^[0-9]+$ ]] && kill "$pid" 2>/dev/null
  sleep 1
  pid=$(window_json "$1" '.pid')
  [[ $pid =~ ^[0-9]+$ ]] && kill -9 "$pid" 2>/dev/null
  sleep 0.5
}

# app NAME CLASS-REGEX SECONDS COMMAND... — launches a program and waits for its
# window. Nothing here asserts what the window contains: over a serial console,
# "it mapped a surface" is the strongest honest claim.
app() {
  local name=$1 re=$2 patience=$3
  shift 3
  setsid "$@" >"$WORK/log/$name.log" 2>&1 &
  if wait_window "$re" "$patience"; then
    pass "app:$name"
  else
    fail "app:$name" "no window matching /$re/ in ${patience}s; on screen: [$(hyprctl -j clients 2>/dev/null |
      jq -r '[.[] | "\(.class):\(.title)"] | join(", ")')]; $(tail -2 "$WORK/log/$name.log" 2>/dev/null)"
  fi
  close_window "$re"
  pkill -f "^$1" 2>/dev/null
  # Chrome's launcher is a shell script; the browser itself is somewhere else
  # entirely, and left alive it holds the profile lock against every later run.
  [[ $name == chrome* ]] && pkill -f /opt/google/chrome/chrome 2>/dev/null
  return 0
}

# ── fakes for the two interactive pieces ────────────────────────────────────

cat >"$WORK/bin/fuzzel" <<'FAKE'
#!/bin/bash
# Records the menu it was shown and answers with $FUZZEL_PICK, so the code
# behind a picker runs for real without anyone to click it.
# Only read a menu when one is being piped in. An action the menu launches
# inherits the console's terminal on stdin, and reading that would steal the
# characters the test harness is typing.
input=""
[[ -t 0 ]] || input=$(timeout 5 cat)
# Only a real menu is worth recording: an action the menu launches runs this
# same fake with nothing piped in, and would otherwise erase the log.
[[ -n $input ]] && printf '%s\n' "$input" >"${FUZZEL_LOG:-/tmp/march-selftest/log/fuzzel.last}"
pick=${FUZZEL_PICK:-0}
for arg in "$@"; do
  if [[ $arg == "--index" ]]; then
    printf '%s\n' "$pick"
    exit 0
  fi
done
printf '%s\n' "$input" | sed -n "$((pick + 1))p"
FAKE

cat >"$WORK/bin/slurp" <<'FAKE'
#!/bin/bash
# The region a user would drag.
printf '%s\n' "${SLURP_GEOMETRY:-0,0 320x200}"
FAKE

cat >"$WORK/pkgbin/march-term" <<'FAKE'
#!/bin/bash
# Records what would have been run in a terminal. Used only where the real
# thing would need a sudo password or would upgrade the machine mid-test.
printf '%s\n' "$*" >>/tmp/march-selftest/log/march-term.log
FAKE

chmod 0755 "$WORK/bin/"* "$WORK/pkgbin/"*

# ── 1. what the session started for itself ──────────────────────────────────

for proc in waybar mako swayosd-server swaybg wl-clip-persist; do
  if pgrep -u "$(id -u)" -x "$proc" >/dev/null 2>&1; then
    pass "autostart:$proc"
  else
    fail "autostart:$proc" "not running"
  fi
done
if pgrep -u "$(id -u)" -f 'wl-paste .*cliphist store' >/dev/null 2>&1; then
  pass autostart:cliphist-watch
else
  fail autostart:cliphist-watch "nothing is recording the clipboard"
fi
if pgrep -u "$(id -u)" -f polkit-gnome >/dev/null 2>&1; then
  pass autostart:polkit-agent
else
  fail autostart:polkit-agent "not running"
fi
# An idle daemon was deliberately removed; finding one means it came back.
if pgrep -u "$(id -u)" -x hypridle >/dev/null 2>&1; then
  fail autostart:no-hypridle "hypridle is running"
else
  pass autostart:no-hypridle
fi

# ── 2. the graphical applications ───────────────────────────────────────────

app alacritty 'Alacritty' 30 alacritty
app foot 'foot' 30 foot
app nautilus 'nautilus' 45 nautilus --new-window
# Chrome is started the way march's desktop entry starts it, keyring prompt and
# welcome tour and all disabled; that is the difference between a window in five
# seconds and no window at all.
CHROME_FLAGS="--ozone-platform=wayland --password-store=basic --no-first-run --no-default-browser-check"
# shellcheck disable=SC2086
app chrome 'google-chrome' 120 google-chrome-stable $CHROME_FLAGS

# Chrome again, for the two things that are not "a window appeared": that it is
# a Wayland client rather than XWayland, and that its engine actually renders.
# shellcheck disable=SC2086
setsid -f google-chrome-stable $CHROME_FLAGS >"$WORK/log/chrome2.log" 2>&1
if wait_window 'google-chrome' 120; then
  if [[ $(window_json 'google-chrome' '.xwayland') == "false" ]]; then
    pass chrome:wayland-native
  else
    fail chrome:wayland-native "Chrome mapped through XWayland"
  fi
else
  fail chrome:wayland-native "no Chrome window"
fi
pkill -f /opt/google/chrome/chrome 2>/dev/null
sleep 2
contains chrome:headless-render '<body>' \
  timeout 90 google-chrome-stable --headless=new --no-sandbox --dump-dom about:blank

# Media and documents need something to open, so make one of each.
# Big and high-contrast on purpose: this image is what the OCR check reads back
# off the screen, and a small one scaled up to a 3456-pixel display is a blur.
im() { magick "$@" 2>/dev/null || convert "$@" 2>/dev/null; }
im -size 1200x400 xc:white -pointsize 96 -fill black \
  -draw "text 40,220 'march selftest'" "$WORK/sample.png"
im "$WORK/sample.png" "$WORK/sample.pdf"

if [[ -s $WORK/sample.png ]]; then
  pass tool:imagemagick
else
  fail tool:imagemagick "produced no image"
fi

app imv 'imv' 30 imv "$WORK/sample.png"
app mpv 'mpv' 30 mpv --force-window=yes --idle=yes "$WORK/sample.png"
app evince 'evince|papers' 45 evince "$WORK/sample.pdf"
app xournalpp 'xournalpp' 60 xournalpp
app libreoffice 'libreoffice|soffice' 120 libreoffice --writer
app gnome-disks 'disk' 60 gnome-disks
app kdenlive 'kdenlive' 120 kdenlive
app satty 'satty' 45 satty --filename "$WORK/sample.png"

# The TUIs, each in the floating terminal march-term opens for it. That the
# window floats is the window rule doing its job.
for tui in btop wiremix lazygit lazydocker; do
  setsid -f march-term "$tui" >"$WORK/log/$tui.log" 2>&1
  if wait_window 'march-float' 30; then
    if [[ $(window_json 'march-float' '.floating') == "true" ]]; then
      pass "tui:$tui"
    else
      fail "tui:$tui" "march-term's window did not float"
    fi
  else
    fail "tui:$tui" "no floating terminal appeared"
  fi
  close_window 'march-float'
done

# The launcher itself, which is a layer surface rather than a window.
setsid -f fuzzel >"$WORK/log/fuzzel-real.log" 2>&1
sleep 3
if hyprctl -j layers 2>/dev/null | grep -q launcher; then
  pass app:fuzzel
else
  fail app:fuzzel "no launcher layer appeared"
fi
pkill -x fuzzel 2>/dev/null
sleep 0.5

# ── 3. from here on the pickers are answered by fakes ───────────────────────

export PATH="$WORK/bin:$PATH"

# ── 4. capture ──────────────────────────────────────────────────────────────

rm -f "$HOME"/Pictures/screenshot-*.png
if out=$(march-capture screen 2>&1) && [[ -s ${out##*$'\n'} ]]; then
  pass capture:screen
else
  fail capture:screen "$out"
fi
# The file is only half of it: Omarchy puts the shot on the clipboard too.
if wl-paste --list-types 2>/dev/null | grep -q 'image/png'; then
  pass capture:screen-clipboard
else
  fail capture:screen-clipboard "the screenshot is not on the clipboard"
fi

if out=$(march-capture region 2>&1) && ls "$HOME"/Pictures/screenshot-*.png >/dev/null 2>&1; then
  pass capture:region
else
  fail capture:region "$out"
fi

if march-capture latest >/dev/null 2>&1; then
  pass capture:latest
else
  fail capture:latest "cannot find the screenshot it just took"
fi

# OCR reads whatever is on screen, so put something on screen worth reading:
# the sample image, full screen, and select the whole display.
setsid imv "$WORK/sample.png" >"$WORK/log/imv-ocr.log" 2>&1 &
wait_window 'imv' 30
# Software rendering paints the first frame well after the window maps.
sleep 5
region=$(window_json 'imv' '"\(.at[0]),\(.at[1]) \(.size[0])x\(.size[1])"')
if out=$(SLURP_GEOMETRY="$region" march-capture text 2>&1); then
  if wl-paste 2>/dev/null | grep -qi 'march\|selftest'; then
    pass capture:text
  else
    fail capture:text "OCR copied nothing readable"
  fi
else
  fail capture:text "$out"
fi
close_window 'imv'

# Annotation opens satty on the newest screenshot.
setsid -f march-capture annotate >"$WORK/log/annotate.log" 2>&1
if wait_window 'satty' 30; then
  pass capture:annotate
else
  fail capture:annotate "satty did not open"
fi
pkill -x satty 2>/dev/null

# The colour picker is a toggle: first press starts it, second stops it.
# hyprpicker refuses to run without a pointer ("cannot work without a pointer"),
# and a guest started with no display has no pointer device to give it.
if [[ $(hyprctl -j devices 2>/dev/null | jq -r '.mice | length') == "0" ]]; then
  skip capture:color "this guest has no pointer device; hyprpicker needs one"
  skip capture:color-toggle "see capture:color"
else
setsid -f march-capture color >"$WORK/log/color.log" 2>&1
sleep 3
if pgrep -x hyprpicker >/dev/null 2>&1; then
  pass capture:color
  march-capture color >/dev/null 2>&1
  sleep 1
  if pgrep -x hyprpicker >/dev/null 2>&1; then
    fail capture:color-toggle "a second press did not close the picker"
    pkill -x hyprpicker
  else
    pass capture:color-toggle
  fi
else
  fail capture:color "hyprpicker is not running"
fi
fi

# ── 5. recording ────────────────────────────────────────────────────────────

rm -f "$HOME"/Videos/recording-*.mp4
if march-capture record >/dev/null 2>&1 && sleep 5 && pgrep -x wf-recorder >/dev/null; then
  pass record:start
  # The bar has to know, or the indicator is decorative.
  if march-bar recording | grep -q '"class":"active"'; then
    pass record:indicator
  else
    fail record:indicator "the bar does not show a recording in progress"
  fi
  march-capture record-stop >/dev/null 2>&1
  sleep 3
  video=$(ls -t "$HOME"/Videos/recording-*.mp4 2>/dev/null | head -1)
  if [[ -s $video ]]; then
    pass record:file
    # An mp4 that was never finalised has no readable stream.
    if ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
      -of csv=p=0 "$video" 2>/dev/null | grep -q .; then
      pass record:playable
    else
      fail record:playable "the recording has no decodable video stream"
    fi
  else
    fail record:file "no recording was written"
  fi
  if pgrep -x wf-recorder >/dev/null 2>&1; then
    fail record:stop "wf-recorder is still running"
    pkill -x wf-recorder
  else
    pass record:stop
  fi
else
  fail record:start "wf-recorder did not start"
fi

# ── 6. clipboard and emoji ──────────────────────────────────────────────────

printf 'march-clipboard-probe' | wl-copy
sleep 2
if cliphist list 2>/dev/null | grep -q 'march-clipboard-probe'; then
  pass clipboard:history
else
  fail clipboard:history "cliphist did not record what was copied"
fi

printf 'something-else' | wl-copy
sleep 1
FUZZEL_PICK=$(cliphist list | grep -n 'march-clipboard-probe' | head -1 | cut -d: -f1)
FUZZEL_PICK=$((FUZZEL_PICK - 1))
if FUZZEL_PICK=$FUZZEL_PICK march-clipboard pick >/dev/null 2>&1 &&
  [[ $(wl-paste 2>/dev/null) == "march-clipboard-probe" ]]; then
  pass clipboard:pick
else
  fail clipboard:pick "picking an entry did not put it back on the clipboard"
fi

if march-clipboard clear >/dev/null 2>&1 && [[ -z $(cliphist list 2>/dev/null) ]]; then
  pass clipboard:clear
else
  fail clipboard:clear "the history survived being wiped"
fi

# The emoji picker types by default; copying is what a test can read back.
printf 'not-an-emoji' | wl-copy
if FUZZEL_PICK=0 timeout 60 rofimoji --selector fuzzel --action copy >/dev/null 2>&1; then
  if [[ $(wl-paste 2>/dev/null) != "not-an-emoji" ]]; then
    pass emoji:pick
  else
    fail emoji:pick "nothing was copied"
  fi
else
  fail emoji:pick "rofimoji failed"
fi

# ── 7. the menu ─────────────────────────────────────────────────────────────

contains menu:list 'capture.region' march-menu --list

# Root, first row: Apps, which opens the launcher.
FUZZEL_LOG="$WORK/log/menu-root" FUZZEL_PICK=0 march-menu >/dev/null 2>&1
sleep 1
if grep -q 'Apps' "$WORK/log/menu-root" 2>/dev/null; then
  pass menu:root
else
  fail menu:root "the root menu did not list its branches"
fi

# A branch opened straight from a key, with Back in front of its rows.
FUZZEL_LOG="$WORK/log/menu-capture" FUZZEL_PICK=99 march-menu capture >/dev/null 2>&1
if head -1 "$WORK/log/menu-capture" 2>/dev/null | grep -q 'Back' &&
  grep -q 'Screenshot region' "$WORK/log/menu-capture" 2>/dev/null; then
  pass menu:branch
else
  fail menu:branch "$(head -3 "$WORK/log/menu-capture" 2>/dev/null)"
fi

# A row's condition really hides it: nothing is recording, so Stop is absent.
if ! grep -q 'Stop recording' "$WORK/log/menu-capture" 2>/dev/null; then
  pass menu:condition-hides
else
  fail menu:condition-hides "Stop recording is offered while nothing records"
fi

# And appears once something is.
march-capture record >/dev/null 2>&1
sleep 4
FUZZEL_LOG="$WORK/log/menu-recording" FUZZEL_PICK=99 march-menu capture >/dev/null 2>&1
if grep -q 'Stop recording' "$WORK/log/menu-recording" 2>/dev/null; then
  pass menu:condition-shows
else
  fail menu:condition-shows "Stop recording is hidden while recording"
fi
march-capture record-stop >/dev/null 2>&1
sleep 2

# Picking a row runs its action: row 1 of capture is a region screenshot.
rm -f "$HOME"/Pictures/screenshot-*.png
FUZZEL_PICK=1 march-menu capture >/dev/null 2>&1
sleep 3
if ls "$HOME"/Pictures/screenshot-*.png >/dev/null 2>&1; then
  pass menu:runs-action
else
  fail menu:runs-action "choosing Screenshot region produced no screenshot"
fi

# The Setup branch opens a config file in an editor, which is march-term's
# other job: a window that stays put until the program in it exits.
FUZZEL_PICK=1 march-menu setup >/dev/null 2>&1
if wait_window 'march-float' 40; then
  pass menu:setup-opens-editor
else
  fail menu:setup-opens-editor "no editor window appeared"
fi
close_window 'march-float'

# --hold is what keeps a window up long enough to read the output of something
# that has already finished.
setsid march-term --hold true >"$WORK/log/hold.log" 2>&1 &
if wait_window 'march-float' 30; then
  pass term:hold
else
  fail term:hold "a held terminal did not stay open"
fi
close_window 'march-float'

# Log out, reboot and shut down are in the menu and are deliberately not run:
# each one ends the session this suite is running in.
skip menu:system-destructive "logout, reboot and shutdown would end the session"

# ── 8. toggles ──────────────────────────────────────────────────────────────

march-toggle dnd >/dev/null 2>&1
sleep 1
if makoctl mode | grep -qx 'do-not-disturb'; then
  pass toggle:dnd-on
else
  fail toggle:dnd-on "mako is not silenced"
fi
if march-bar dnd | grep -q '"class":"active"'; then
  pass toggle:dnd-indicator
else
  fail toggle:dnd-indicator "the bar does not show notifications silenced"
fi
march-toggle dnd >/dev/null 2>&1
sleep 1
if makoctl mode | grep -qx 'do-not-disturb'; then
  fail toggle:dnd-off "notifications are still silenced"
else
  pass toggle:dnd-off
fi

march-toggle gaps >/dev/null 2>&1
sleep 1
if ! hyprctl getoption general:gaps_out | grep -qw 10; then
  pass toggle:gaps-off
else
  fail toggle:gaps-off "$(hyprctl getoption general:gaps_out | head -2)"
fi
march-toggle gaps >/dev/null 2>&1
sleep 1
if hyprctl getoption general:gaps_out | grep -qw 10; then
  pass toggle:gaps-on
else
  fail toggle:gaps-on "$(hyprctl getoption general:gaps_out | head -2)"
fi

march-toggle layout >/dev/null 2>&1
sleep 1
if hyprctl getoption general:layout | grep -q 'master'; then
  pass toggle:layout-master
else
  fail toggle:layout-master "$(hyprctl getoption general:layout | head -2)"
fi
march-toggle layout >/dev/null 2>&1
sleep 1
if hyprctl getoption general:layout | grep -q 'dwindle'; then
  pass toggle:layout-dwindle
else
  fail toggle:layout-dwindle "$(hyprctl getoption general:layout | head -2)"
fi

march-toggle bar >/dev/null 2>&1
sleep 2
if pgrep -u "$(id -u)" -x waybar >/dev/null 2>&1; then
  fail toggle:bar-off "the bar is still running"
else
  pass toggle:bar-off
fi
march-toggle bar >/dev/null 2>&1
sleep 3
if pgrep -u "$(id -u)" -x waybar >/dev/null 2>&1; then
  pass toggle:bar-on
else
  fail toggle:bar-on "the bar did not come back"
fi

# ── 9. the bar's own modules ────────────────────────────────────────────────

for module in updates dnd recording; do
  if march-bar "$module" | jq -e 'has("text") and has("class")' >/dev/null 2>&1; then
    pass "bar:$module"
  else
    fail "bar:$module" "$(march-bar "$module" 2>&1 | head -1)"
  fi
done

# Every click target in the shipped config, run as waybar would run it. The
# ones that open a window are started and killed; the rest must simply succeed.
config=$HOME/.config/waybar/config.jsonc
while read -r cmd; do
  [[ -z $cmd || $cmd == "activate" ]] && continue
  name="bar-click:${cmd%% *}"
  case $cmd in
    *march-menu*)
      if FUZZEL_PICK=99 timeout 30 bash -c "$cmd" >/dev/null 2>&1; then pass "$name"; else fail "$name" "$cmd"; fi
      ;;
    *march-term*)
      setsid -f bash -c "$cmd" >/dev/null 2>&1
      if wait_window 'march-float' 30; then pass "$name"; else fail "$name" "$cmd opened nothing"; fi
      pkill -f march-float 2>/dev/null
      sleep 0.5
      ;;
    *)
      if timeout 60 bash -c "$cmd" >/dev/null 2>&1; then pass "$name"; else fail "$name" "$cmd"; fi
      ;;
  esac
done < <(grep -oE '"(on-click|on-click-right|exec)"[[:space:]]*:[[:space:]]*"[^"]+"' "$config" |
  sed -E 's/.*:[[:space:]]*"([^"]+)"/\1/')

# Two of those buttons are toggles, and the checks below assume the state they
# started in: notifications visible and the sink unmuted.
makoctl mode -r do-not-disturb >/dev/null 2>&1
wpctl set-mute @DEFAULT_AUDIO_SINK@ 0 >/dev/null 2>&1

# ── 10. package management, without touching the machine ────────────────────

rm -f "$WORK/log/march-term.log"
if PATH="$WORK/pkgbin:$PATH" FUZZEL_PICK=0 march-pkg install >/dev/null 2>&1 &&
  grep -q 'pacman -S --needed' "$WORK/log/march-term.log" 2>/dev/null; then
  pass pkg:install
else
  fail pkg:install "$(cat "$WORK/log/march-term.log" 2>/dev/null)"
fi
if PATH="$WORK/pkgbin:$PATH" FUZZEL_PICK=0 march-pkg remove >/dev/null 2>&1 &&
  grep -q 'pacman -Rns' "$WORK/log/march-term.log" 2>/dev/null; then
  pass pkg:remove
else
  fail pkg:remove "$(cat "$WORK/log/march-term.log" 2>/dev/null)"
fi
if PATH="$WORK/pkgbin:$PATH" march-pkg update >/dev/null 2>&1 &&
  grep -q 'pacman -Syu' "$WORK/log/march-term.log" 2>/dev/null; then
  pass pkg:update
else
  fail pkg:update "$(cat "$WORK/log/march-term.log" 2>/dev/null)"
fi
if PATH="$WORK/pkgbin:$PATH" march-pkg clean >/dev/null 2>&1 &&
  grep -q 'paccache -r' "$WORK/log/march-term.log" 2>/dev/null; then
  pass pkg:clean
else
  fail pkg:clean "$(cat "$WORK/log/march-term.log" 2>/dev/null)"
fi
# Orphans either lists some or says there are none; both are a working path.
if PATH="$WORK/pkgbin:$PATH" march-pkg orphans >/dev/null 2>&1; then
  pass pkg:orphans
else
  fail pkg:orphans "the orphan sweep failed"
fi
# checkupdates is what the bar's counter runs, and it needs the network.
if timeout 120 checkupdates >/dev/null 2>&1 || [[ $? -eq 2 ]]; then
  pass pkg:checkupdates
else
  fail pkg:checkupdates "checkupdates cannot reach the repositories"
fi

# ── 11. keys, as the compositor has them ────────────────────────────────────

# The mapping is read back out of the running compositor, so this is the key
# a user presses resolving to the command march meant to bind to it.
binds=$(hyprctl -j binds)

# key_bound MODMASK KEY DESCRIPTION — the key is registered with the compositor
# and carries the description march wrote for it.
#
# The command behind it cannot be checked here: a Lua bind holds a closure, and
# `hyprctl binds` reports its registry index rather than a command line. What
# each command does is covered by running it directly elsewhere in this suite.
key_bound() {
  local mods=$1 key=$2 want_desc=$3
  local desc
  desc=$(printf '%s' "$binds" | jq -r --arg k "$key" --argjson m "$mods" \
    '[.[] | select(.key == $k and .modmask == $m)] | .[0].description // "\u0000"')
  if [[ $desc == $'\u0000' ]]; then
    fail "key:$mods+$key" "not bound at all"
  elif [[ $desc != *"$want_desc"* ]]; then
    fail "key:$mods+$key" "described as ${desc:-nothing}, want $want_desc"
  else
    pass "key:$mods+$key"
  fi
}
# 0 none, 64 SUPER, 65 SUPER+SHIFT, 68 SUPER+CTRL, 72 SUPER+ALT.
key_bound 64 space "Menu"
key_bound 72 space "Launch apps"
key_bound 64 Escape "System menu"
key_bound 68 C "Capture menu"
key_bound 68 O "Toggle menu"
key_bound 68 V "Clipboard history"
key_bound 68 E "Emoji picker"
key_bound 64 K "Show key bindings"
key_bound 0 Print "Screenshot region"
key_bound 1 Print "Screenshot screen"
key_bound 64 Print "Color picker"
key_bound 68 Print "Extract text (OCR)"
key_bound 8 Print "Record screen"
key_bound 65 space "Toggle top bar"
key_bound 68 A "Audio controls"
key_bound 64 Return "Terminal"
key_bound 64 B "Web browser"
key_bound 64 W "Close window"
key_bound 64 F "File manager"
key_bound 68 comma "Silence notifications"
key_bound 65 BackSpace "Toggle window gaps"

# And the helper that shows them to the user reads the same list.
contains keys:helper 'SUPER + space' march-keybindings --list

# ── 12. sound ───────────────────────────────────────────────────────────────

if wpctl status 2>/dev/null | sed -n '/Sinks:/,/^$/p' | grep -qi 'audio\|hda\|built-in'; then
  pass sound:sink
else
  fail sound:sink "pipewire reports no sink"
fi
setsid -f speaker-test -D pipewire -c 2 -t sine -l 1 >"$WORK/log/speaker.log" 2>&1
sleep 4
if wpctl status 2>/dev/null | grep -qi 'speaker-test'; then
  pass sound:stream
else
  fail sound:stream "nothing reached the sink while a tone was playing"
fi
pkill -x speaker-test 2>/dev/null
# The volume keys go through swayosd, which has to be able to talk to it.
check sound:volume-key swayosd-client --output-volume raise

# ── 13. notifications ───────────────────────────────────────────────────────

check notify:send notify-send -a march-selftest "selftest" "a notification"
sleep 1
if makoctl list 2>/dev/null | grep -q 'selftest'; then
  pass notify:visible
else
  fail notify:visible "mako is not showing the notification"
fi
check notify:dismiss makoctl dismiss --all

# ── 14. the lock screen ─────────────────────────────────────────────────────

setsid -f hyprlock >"$WORK/log/hyprlock.log" 2>&1
sleep 5
if hyprctl -j layers 2>/dev/null | grep -qi 'hyprlock' || pgrep -x hyprlock >/dev/null; then
  pass lock:starts
else
  fail lock:starts "$(tail -2 "$WORK/log/hyprlock.log" 2>/dev/null)"
fi
pkill -x hyprlock 2>/dev/null
sleep 2
# Killing a locker is not unlocking: the compositor still considers the session
# locked, and every window mapped after that is behind a lock nobody can answer.
# This is the supported way back, and it has to work or the suite has bricked
# the desktop it was testing.
hyprctl eval 'hl.clear_crashed_lockscreen()' >/dev/null 2>&1
sleep 1
if pgrep -x hyprlock >/dev/null 2>&1; then
  fail lock:clears "hyprlock is still holding the session"
else
  pass lock:clears
fi
# The proof that the session survived: something can still open a window.
setsid alacritty >"$WORK/log/postlock.log" 2>&1 &
if wait_window 'Alacritty' 30; then
  pass lock:session-usable
else
  fail lock:session-usable "nothing can map a window after the lock screen was cleared"
fi
close_window 'Alacritty' 

# ── 15. the default browser, as anything else would resolve it ──────────────

contains browser:desktop-entry-flags '--password-store=basic' \
  grep -m1 '^Exec=' /usr/share/applications/google-chrome.desktop
contains browser:xdg-settings 'google-chrome.desktop' xdg-settings get default-web-browser
contains browser:xdg-mime 'google-chrome.desktop' xdg-mime query default x-scheme-handler/https
if [[ ${BROWSER:-} == google-chrome-stable ]]; then
  pass browser:env
else
  fail browser:env "\$BROWSER is ${BROWSER:-unset}"
fi

# ── 16. the toolset, one command at a time ──────────────────────────────────

# system-config-printer is not among these on purpose: its aarch64 package is
# missing the cupshelpers module, so it cannot start at all. localhost:631 is
# how a printer gets added here.

missing=""
for bin in git rg bat eza fd fzf zoxide starship tldr expac dua fastfetch inxi \
  plocate tmux unzip whois socat inotifywait gum nvim tree-sitter luarocks ruby \
  clang llvm-config docker docker-compose lazydocker mpv imv magick ffmpeg \
  yt-dlp zbarimg qrencode tesseract evince xournalpp libreoffice gnome-disks \
  foot udiskie cupsd ufw fcitx5 wireplumber pactl pamixer wl-copy grim slurp \
  hyprctl hyprlock hyprpicker waybar fuzzel mako swayosd-client wiremix btop \
  jq playerctl cliphist wl-clip-persist rofimoji wtype wf-recorder satty \
  notify-send checkupdates google-chrome-stable; do
  command -v "$bin" >/dev/null 2>&1 || missing="$missing $bin"
done
if [[ -z $missing ]]; then
  pass toolset:on-path
else
  fail toolset:on-path "missing:$missing"
fi

# ── 17. the services the install turned on ──────────────────────────────────

# Group membership is only half of a usable docker; the socket has to answer
# without sudo, which is what the group was granted for.
if timeout 90 docker info >/dev/null 2>&1; then
  pass service:docker
else
  fail service:docker "docker is not usable without sudo: $(timeout 30 docker info 2>&1 | tail -1)"
fi
# Printing is the one path that works from behind user-mode networking, so the
# queue has to be reachable and cups-pdf has to be in it.
if timeout 30 lpstat -r >/dev/null 2>&1; then
  pass service:cups
else
  fail service:cups "$(timeout 30 lpstat -r 2>&1 | head -1)"
fi
for unit in plocate-updatedb.timer linux-modules-cleanup.service ufw.service; do
  if systemctl is-enabled "$unit" >/dev/null 2>&1; then
    pass "service:${unit%%.*}"
  else
    fail "service:${unit%%.*}" "not enabled"
  fi
done

# ── 18. nothing broke along the way ─────────────────────────────────────────

# The configs march ships that nothing else here would notice the absence of.
for cfg in hypr/hyprlock.conf mpv/mpv.conf waybar/config.jsonc mako/config \
  fuzzel/fuzzel.ini hypr/hyprland.lua; do
  if [[ -f $HOME/.config/$cfg ]]; then
    pass "config:${cfg%%/*}"
  else
    fail "config:${cfg%%/*}" "$HOME/.config/$cfg was never copied out of /etc/skel"
  fi
done

if [[ -z $(systemctl --failed --no-legend --no-pager) ]]; then
  pass system:no-failed-units
else
  fail system:no-failed-units "$(systemctl --failed --no-legend --no-pager | head -3)"
fi
errors=$(hyprctl configerrors 2>/dev/null)
if [[ -z $errors || $errors == *"no errors"* ]]; then
  pass system:clean-hyprland-config
else
  fail system:clean-hyprland-config "$(printf '%s' "$errors" | head -3)"
fi

printf 'SELFTEST-DONE %d %d %d\n' "$ok" "$bad" "$skipped"
