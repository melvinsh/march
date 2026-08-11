-- Keybindings, ported from Omarchy's own Lua configuration (see NOTICE).
--
-- Omarchy keeps its bindings in four files — bindings/clipboard.lua,
-- bindings/tiling-v2.lua, bindings/utilities.lua and the user-editable
-- bindings.lua — and all four are vendored here, in that order, key for key.
-- Where Omarchy calls one of its own binaries (omarchy-menu, omarchy-launch-*,
-- omarchy-hyprland-*, ...) march calls its equivalent, since none of Omarchy's
-- packages are built for aarch64. Nothing else is moved: a key that means
-- "full screen" on an Omarchy machine means it here.
--
-- The keys Omarchy binds that march deliberately leaves free are listed at the
-- bottom of this file, with the reason for each.
--
-- Edit freely: this file is yours, copied from /etc/skel on first login.

-- ─── Applications ────────────────────────────────────────────────────────────
-- Which terminal, browser, file manager and editor these open is apps.lua's to
-- say; march-launch reads it from the environment apps.lua exports.
hl.bind("SUPER + Return", hl.dsp.exec_cmd("march-launch terminal"), { description = "Terminal" })
hl.bind("SUPER + ALT + Return", hl.dsp.exec_cmd("march-launch tmux"), { description = "Tmux" })
hl.bind("SUPER + SHIFT + Return", hl.dsp.exec_cmd("march-launch browser"), { description = "Browser" })
hl.bind("SUPER + SHIFT + B", hl.dsp.exec_cmd("march-launch browser"), { description = "Browser" })
hl.bind("SUPER + SHIFT + ALT + B", hl.dsp.exec_cmd("march-launch browser --private"),
    { description = "Browser (private)" })
hl.bind("SUPER + SHIFT + F", hl.dsp.exec_cmd("march-launch files"), { description = "File manager" })
hl.bind("SUPER + ALT + SHIFT + F", hl.dsp.exec_cmd("march-launch files --cwd"),
    { description = "File manager (cwd)" })
hl.bind("SUPER + SHIFT + N", hl.dsp.exec_cmd("march-launch editor"), { description = "Editor" })
hl.bind("SUPER + SHIFT + D", hl.dsp.exec_cmd("march-launch tui lazydocker"), { description = "Docker" })

-- Web apps, opened in Chrome's app mode as Omarchy opens them in Chromium's.
-- These are Omarchy's own defaults down to the URLs; they are the first thing
-- worth editing to taste.
hl.bind("SUPER + SHIFT + A", hl.dsp.exec_cmd("march-launch webapp https://chatgpt.com"),
    { description = "ChatGPT" })
hl.bind("SUPER + SHIFT + ALT + A", hl.dsp.exec_cmd("march-launch webapp https://grok.com"),
    { description = "Grok" })
hl.bind("SUPER + SHIFT + C", hl.dsp.exec_cmd("march-launch webapp https://app.hey.com/calendar/weeks/"),
    { description = "Calendar" })
hl.bind("SUPER + SHIFT + E", hl.dsp.exec_cmd("march-launch webapp https://app.hey.com"),
    { description = "Email" })
hl.bind("SUPER + SHIFT + Y", hl.dsp.exec_cmd("march-launch webapp https://youtube.com/"),
    { description = "YouTube" })
hl.bind("SUPER + SHIFT + X", hl.dsp.exec_cmd("march-launch webapp https://x.com/"), { description = "X" })
hl.bind("SUPER + SHIFT + ALT + X", hl.dsp.exec_cmd("march-launch webapp https://x.com/compose/post"),
    { description = "X Post" })
-- The ones Omarchy focuses rather than opening twice.
hl.bind("SUPER + SHIFT + ALT + G", hl.dsp.exec_cmd([[march-launch webapp-focus WhatsApp https://web.whatsapp.com/]]),
    { description = "WhatsApp" })
hl.bind("SUPER + SHIFT + CTRL + G",
    hl.dsp.exec_cmd([[march-launch webapp-focus "Google Messages" https://messages.google.com/web/conversations]]),
    { description = "Google Messages" })
hl.bind("SUPER + SHIFT + P",
    hl.dsp.exec_cmd([[march-launch webapp-focus "Google Photos" https://photos.google.com/]]),
    { description = "Google Photos" })
hl.bind("SUPER + SHIFT + S", hl.dsp.exec_cmd([[march-launch webapp-focus "Google Maps" https://maps.google.com/]]),
    { description = "Google Maps" })

-- ─── Clipboard ───────────────────────────────────────────────────────────────
-- One pair of keys that copies and pastes everywhere: SUPER + C and SUPER + V
-- send CTRL + Insert and SHIFT + Insert to the focused window, which is what a
-- terminal and a browser both understand. alacritty.toml binds the terminal
-- half; GTK and Chrome answer to them out of the box.
--
-- Pressed and released on a timer rather than through hl.dsp.send_shortcut,
-- which can leave the synthetic key stuck down and repeating — Omarchy's own
-- workaround. https://github.com/hyprwm/Hyprland/discussions/14099
local function send_shortcut_once(mods, key)
    return function()
        hl.dispatch(hl.dsp.send_key_state({ mods = mods, key = key, state = "down", window = "activewindow" }))

        hl.timer(function()
            hl.dispatch(hl.dsp.send_key_state({ mods = mods, key = key, state = "up", window = "activewindow" }))
        end, { timeout = 50, type = "oneshot" })
    end
end

hl.bind("SUPER + C", send_shortcut_once("CTRL", "Insert"), { description = "Universal copy" })
hl.bind("SUPER + V", send_shortcut_once("SHIFT", "Insert"), { description = "Universal paste" })
hl.bind("SUPER + X", send_shortcut_once("CTRL", "X"), { description = "Universal cut" })
hl.bind("SUPER + CTRL + V", hl.dsp.exec_cmd("march-clipboard pick"), { description = "Clipboard manager" })

-- ─── Windows ─────────────────────────────────────────────────────────────────
hl.bind("SUPER + W", hl.dsp.window.close(), { description = "Close window" })
hl.bind("CTRL + ALT + Delete", hl.dsp.exec_cmd("march-window close-all"), { description = "Close all windows" })

hl.bind("SUPER + J", hl.dsp.layout("togglesplit"), { description = "Toggle window split" })
hl.bind("SUPER + P", hl.dsp.window.pseudo(), { description = "Pseudo window" })
hl.bind("SUPER + T", hl.dsp.window.float({ action = "toggle" }), { description = "Toggle window floating/tiling" })
hl.bind("SUPER + F", hl.dsp.window.fullscreen({ mode = "fullscreen" }), { description = "Full screen" })
hl.bind("SUPER + CTRL + F", hl.dsp.window.fullscreen_state({ internal = 0, client = 2 }),
    { description = "Tiled full screen" })
hl.bind("SUPER + ALT + F", hl.dsp.window.fullscreen({ mode = "maximized" }), { description = "Full width" })
hl.bind("SUPER + O", hl.dsp.exec_cmd("march-window pop"), { description = "Pop window out (float & pin)" })
hl.bind("SUPER + L", hl.dsp.exec_cmd("march-toggle layout"), { description = "Toggle workspace layout" })

-- Move focus
hl.bind("SUPER + left", hl.dsp.focus({ direction = "left" }), { description = "Focus on left window" })
hl.bind("SUPER + right", hl.dsp.focus({ direction = "right" }), { description = "Focus on right window" })
hl.bind("SUPER + up", hl.dsp.focus({ direction = "up" }), { description = "Focus on above window" })
hl.bind("SUPER + down", hl.dsp.focus({ direction = "down" }), { description = "Focus on below window" })

-- Swap windows
hl.bind("SUPER + SHIFT + left", hl.dsp.window.swap({ direction = "left" }), { description = "Swap window to the left" })
hl.bind("SUPER + SHIFT + right", hl.dsp.window.swap({ direction = "right" }),
    { description = "Swap window to the right" })
hl.bind("SUPER + SHIFT + up", hl.dsp.window.swap({ direction = "up" }), { description = "Swap window up" })
hl.bind("SUPER + SHIFT + down", hl.dsp.window.swap({ direction = "down" }), { description = "Swap window down" })

-- Cycle windows
hl.bind("ALT + Tab", hl.dsp.window.cycle_next(), { description = "Focus on next window" })
hl.bind("ALT + SHIFT + Tab", hl.dsp.window.cycle_next({ next = false }), { description = "Focus on previous window" })
hl.bind("ALT + Tab", hl.dsp.window.bring_to_top(), { description = "Reveal active window on top" })
hl.bind("ALT + SHIFT + Tab", hl.dsp.window.bring_to_top(), { description = "Reveal active window on top" })

-- Resize. code:20 is the "-" key and code:21 is "=", as in Omarchy.
hl.bind("SUPER + code:20", hl.dsp.window.resize({ x = -100, y = 0, relative = true }),
    { description = "Expand window left" })
hl.bind("SUPER + code:21", hl.dsp.window.resize({ x = 100, y = 0, relative = true }),
    { description = "Shrink window left" })
hl.bind("SUPER + SHIFT + code:20", hl.dsp.window.resize({ x = 0, y = -100, relative = true }),
    { description = "Shrink window up" })
hl.bind("SUPER + SHIFT + code:21", hl.dsp.window.resize({ x = 0, y = 100, relative = true }),
    { description = "Expand window down" })

hl.bind("SUPER + ALT + code:20", hl.dsp.window.resize({ x = -25, y = 0, relative = true }),
    { description = "Expand window left a little" })
hl.bind("SUPER + ALT + code:21", hl.dsp.window.resize({ x = 25, y = 0, relative = true }),
    { description = "Shrink window left a little" })
hl.bind("SUPER + SHIFT + ALT + code:20", hl.dsp.window.resize({ x = 0, y = -25, relative = true }),
    { description = "Shrink window up a little" })
hl.bind("SUPER + SHIFT + ALT + code:21", hl.dsp.window.resize({ x = 0, y = 25, relative = true }),
    { description = "Expand window down a little" })

hl.bind("SUPER + CTRL + code:20", hl.dsp.window.resize({ x = -300, y = 0, relative = true }),
    { description = "Expand window left a lot" })
hl.bind("SUPER + CTRL + code:21", hl.dsp.window.resize({ x = 300, y = 0, relative = true }),
    { description = "Shrink window left a lot" })
hl.bind("SUPER + CTRL + SHIFT + code:20", hl.dsp.window.resize({ x = 0, y = -300, relative = true }),
    { description = "Shrink window up a lot" })
hl.bind("SUPER + CTRL + SHIFT + code:21", hl.dsp.window.resize({ x = 0, y = 300, relative = true }),
    { description = "Expand window down a lot" })

-- Mouse
hl.bind("SUPER + mouse:272", hl.dsp.window.drag(), { mouse = true, description = "Move window" })
hl.bind("SUPER + mouse:273", hl.dsp.window.resize(), { mouse = true, description = "Resize window" })

-- ─── Workspaces ──────────────────────────────────────────────────────────────
-- Keycodes rather than digits, as in Omarchy, so the bindings hold on any
-- keyboard layout: code:10 is the "1" key through code:19 for "0".
for i = 1, 10 do
    local key = "code:" .. (9 + i)
    hl.bind("SUPER + " .. key, hl.dsp.focus({ workspace = i }), { description = "Switch to workspace " .. i })
    hl.bind("SUPER + SHIFT + " .. key, hl.dsp.window.move({ workspace = i }),
        { description = "Move window to workspace " .. i })
    hl.bind("SUPER + SHIFT + ALT + " .. key, hl.dsp.window.move({ workspace = i, follow = false }),
        { description = "Move window silently to workspace " .. i })
end

hl.bind("SUPER + Tab", hl.dsp.focus({ workspace = "e+1" }), { description = "Next workspace" })
hl.bind("SUPER + SHIFT + Tab", hl.dsp.focus({ workspace = "e-1" }), { description = "Previous workspace" })
hl.bind("SUPER + CTRL + Tab", hl.dsp.focus({ workspace = "previous" }), { description = "Former workspace" })

hl.bind("SUPER + S", hl.dsp.workspace.toggle_special("scratchpad"), { description = "Toggle scratchpad" })
hl.bind("SUPER + ALT + S", hl.dsp.window.move({ workspace = "special:scratchpad", follow = false }),
    { description = "Move window to scratchpad" })

hl.bind("SUPER + mouse_down", hl.dsp.focus({ workspace = "e+1" }),
    { description = "Scroll active workspace forward" })
hl.bind("SUPER + mouse_up", hl.dsp.focus({ workspace = "e-1" }),
    { description = "Scroll active workspace backward" })

-- Monitors. A march guest has one, so these are the bindings a second display
-- would need rather than ones with anything to do today.
hl.bind("CTRL + ALT + Tab", hl.dsp.focus({ monitor = "+1" }), { description = "Focus on next monitor" })
hl.bind("CTRL + ALT + SHIFT + Tab", hl.dsp.focus({ monitor = "-1" }), { description = "Focus on previous monitor" })
hl.bind("SUPER + SHIFT + ALT + left", hl.dsp.workspace.move({ monitor = "left" }),
    { description = "Move workspace to left monitor" })
hl.bind("SUPER + SHIFT + ALT + right", hl.dsp.workspace.move({ monitor = "right" }),
    { description = "Move workspace to right monitor" })
hl.bind("SUPER + SHIFT + ALT + up", hl.dsp.workspace.move({ monitor = "up" }),
    { description = "Move workspace to up monitor" })
hl.bind("SUPER + SHIFT + ALT + down", hl.dsp.workspace.move({ monitor = "down" }),
    { description = "Move workspace to down monitor" })

-- code:61 is the "/" key.
hl.bind("SUPER + code:61", hl.dsp.exec_cmd("march-display scale-cycle"), { description = "Cycle monitor scaling" })
hl.bind("SUPER + ALT + code:61", hl.dsp.exec_cmd("march-display scale-cycle --reverse"),
    { description = "Cycle monitor scaling backwards" })

-- ─── Groups ──────────────────────────────────────────────────────────────────
hl.bind("SUPER + G", hl.dsp.group.toggle(), { description = "Toggle window grouping" })
hl.bind("SUPER + ALT + G", hl.dsp.window.move({ out_of_group = true }),
    { description = "Move active window out of group" })

hl.bind("SUPER + ALT + left", hl.dsp.window.move({ into_group = "left" }),
    { description = "Move window to group on left" })
hl.bind("SUPER + ALT + right", hl.dsp.window.move({ into_group = "right" }),
    { description = "Move window to group on right" })
hl.bind("SUPER + ALT + up", hl.dsp.window.move({ into_group = "up" }),
    { description = "Move window to group on top" })
hl.bind("SUPER + ALT + down", hl.dsp.window.move({ into_group = "down" }),
    { description = "Move window to group on bottom" })

hl.bind("SUPER + ALT + Tab", hl.dsp.group.next(), { description = "Next window in group" })
hl.bind("SUPER + ALT + SHIFT + Tab", hl.dsp.group.prev(), { description = "Previous window in group" })

hl.bind("SUPER + CTRL + left", hl.dsp.group.prev(), { description = "Move grouped window focus left" })
hl.bind("SUPER + CTRL + right", hl.dsp.group.next(), { description = "Move grouped window focus right" })

hl.bind("SUPER + ALT + mouse_down", hl.dsp.group.next(), { description = "Next window in group" })
hl.bind("SUPER + ALT + mouse_up", hl.dsp.group.prev(), { description = "Previous window in group" })

for i = 1, 5 do
    hl.bind("SUPER + ALT + code:" .. (9 + i), hl.dsp.group.active({ index = i }),
        { description = "Switch to group window " .. i })
end

-- ─── Launcher and menus ──────────────────────────────────────────────────────
-- fuzzel stands in for walker and march-menu for omarchy-menu; the keys and the
-- routes behind them are Omarchy's.
hl.bind("SUPER + space", hl.dsp.exec_cmd("fuzzel"), { description = "Launch apps" })
hl.bind("SUPER + ALT + space", hl.dsp.exec_cmd("march-menu"), { description = "March menu" })
hl.bind("SUPER + Escape", hl.dsp.exec_cmd("march-menu system"), { description = "System menu" })
hl.bind("XF86PowerOff", hl.dsp.exec_cmd("march-menu system"), { locked = true, description = "Power menu" })
hl.bind("SUPER + CTRL + C", hl.dsp.exec_cmd("march-menu capture"), { description = "Capture menu" })
hl.bind("SUPER + CTRL + O", hl.dsp.exec_cmd("march-menu toggle"), { description = "Toggle menu" })
hl.bind("SUPER + CTRL + E", hl.dsp.exec_cmd("rofimoji --selector fuzzel --action type"),
    { description = "Emoji picker" })
hl.bind("SUPER + K", hl.dsp.exec_cmd("march-keybindings"), { description = "Show key bindings" })
hl.bind("XF86Calculator", hl.dsp.exec_cmd("gnome-calculator"), { description = "Calculator" })

-- ─── Aesthetics ──────────────────────────────────────────────────────────────
hl.bind("SUPER + SHIFT + space", hl.dsp.exec_cmd("march-toggle bar"), { description = "Toggle top bar" })
hl.bind("SUPER + SHIFT + CTRL + up", hl.dsp.exec_cmd("march-bar position top"), { description = "Move bar to top" })
hl.bind("SUPER + SHIFT + CTRL + down", hl.dsp.exec_cmd("march-bar position bottom"),
    { description = "Move bar to bottom" })
hl.bind("SUPER + SHIFT + CTRL + left", hl.dsp.exec_cmd("march-bar position left"), { description = "Move bar to left" })
hl.bind("SUPER + SHIFT + CTRL + right", hl.dsp.exec_cmd("march-bar position right"),
    { description = "Move bar to right" })
hl.bind("SUPER + BackSpace", hl.dsp.exec_cmd("march-window transparency"),
    { description = "Toggle window transparency" })
hl.bind("SUPER + SHIFT + BackSpace", hl.dsp.exec_cmd("march-toggle gaps"), { description = "Toggle window gaps" })
hl.bind("SUPER + CTRL + BackSpace", hl.dsp.exec_cmd("march-toggle square"),
    { description = "Toggle single-window square aspect" })

-- ─── Notifications ───────────────────────────────────────────────────────────
hl.bind("SUPER + comma", hl.dsp.exec_cmd("makoctl dismiss"), { description = "Dismiss last notification" })
hl.bind("SUPER + SHIFT + comma", hl.dsp.exec_cmd("makoctl dismiss --all"), { description = "Dismiss all notifications" })
hl.bind("SUPER + CTRL + comma", hl.dsp.exec_cmd("march-toggle dnd"),
    { description = "Toggle silencing notifications" })
hl.bind("SUPER + ALT + comma", hl.dsp.exec_cmd("makoctl invoke"), { description = "Invoke last notification" })
hl.bind("SUPER + SHIFT + ALT + comma", hl.dsp.exec_cmd("makoctl restore"), { description = "Restore last notification" })

-- ─── Captures ────────────────────────────────────────────────────────────────
-- Omarchy wraps these in omarchy-capture-*; march wraps the same grim, slurp
-- and tesseract calls in march-capture, so a screenshot lands on disk as well
-- as on the clipboard.
hl.bind("Print", hl.dsp.exec_cmd("march-capture region"), { description = "Screenshot" })
-- march's own: Omarchy's screenshot menu covers the whole screen, and one key
-- for it costs nothing.
hl.bind("SHIFT + Print", hl.dsp.exec_cmd("march-capture screen"), { description = "Screenshot screen" })
hl.bind("ALT + Print", hl.dsp.exec_cmd("march-menu capture.record"), { description = "Screenrecording" })
hl.bind("SUPER + Print", hl.dsp.exec_cmd("march-capture color"), { description = "Color picker" })
hl.bind("SUPER + CTRL + Print", hl.dsp.exec_cmd("march-capture text"),
    { description = "Extract text (OCR) from screenshot" })

-- ─── Control panels and information ──────────────────────────────────────────
-- Omarchy opens its own panels; these are the TUIs that do the same job, in the
-- floating window march-term gives them.
hl.bind("SUPER + CTRL + A", hl.dsp.exec_cmd("march-term wiremix"), { description = "Audio controls" })
hl.bind("SUPER + CTRL + W", hl.dsp.exec_cmd("march-term nmtui"), { description = "Wifi controls" })
hl.bind("SUPER + CTRL + T", hl.dsp.exec_cmd("march-term btop"), { description = "Activity" })
-- Omarchy's omarchy-notification-time, which is a date(1) call in a notification.
hl.bind("SUPER + CTRL + ALT + T",
    hl.dsp.exec_cmd([[notify-send -u low "󰥔    $(date +'%A %H:%M  ·  %d %B %Y  ·  Week %V')"]]),
    { description = "Show time" })

-- ─── Zoom ────────────────────────────────────────────────────────────────────
hl.bind("SUPER + CTRL + Z", function()
    local zoom = hl.get_config("cursor.zoom_factor") or 1
    hl.config({ cursor = { zoom_factor = zoom + 1 } })
end, { description = "Zoom in" })

hl.bind("SUPER + CTRL + ALT + Z", function()
    hl.config({ cursor = { zoom_factor = 1 } })
end, { description = "Reset zoom" })

-- ─── Lock ────────────────────────────────────────────────────────────────────
hl.bind("SUPER + CTRL + L", hl.dsp.exec_cmd("hyprlock"), { description = "Lock system" })

-- ─── Media keys ──────────────────────────────────────────────────────────────
-- swayosd-client is what Omarchy's omarchy-swayosd-client wraps.
hl.bind("XF86AudioRaiseVolume", hl.dsp.exec_cmd("swayosd-client --output-volume raise"),
    { locked = true, repeating = true, description = "Volume up" })
hl.bind("XF86AudioLowerVolume", hl.dsp.exec_cmd("swayosd-client --output-volume lower"),
    { locked = true, repeating = true, description = "Volume down" })
hl.bind("XF86AudioMute", hl.dsp.exec_cmd("swayosd-client --output-volume mute-toggle"),
    { locked = true, repeating = true, description = "Mute" })
hl.bind("XF86AudioMicMute", hl.dsp.exec_cmd("swayosd-client --input-volume mute-toggle"),
    { locked = true, repeating = true, description = "Mute microphone" })
hl.bind("ALT + XF86AudioRaiseVolume", hl.dsp.exec_cmd("swayosd-client --output-volume +1"),
    { locked = true, repeating = true, description = "Volume up precise" })
hl.bind("ALT + XF86AudioLowerVolume", hl.dsp.exec_cmd("swayosd-client --output-volume -1"),
    { locked = true, repeating = true, description = "Volume down precise" })

hl.bind("XF86AudioNext", hl.dsp.exec_cmd("playerctl next"), { locked = true, description = "Next track" })
hl.bind("XF86AudioPause", hl.dsp.exec_cmd("playerctl play-pause"), { locked = true, description = "Pause" })
hl.bind("XF86AudioPlay", hl.dsp.exec_cmd("playerctl play-pause"), { locked = true, description = "Play" })
hl.bind("XF86AudioPrev", hl.dsp.exec_cmd("playerctl previous"), { locked = true, description = "Previous track" })

-- ─── What Omarchy binds and march does not ───────────────────────────────────
--
-- Hardware QEMU's virt machine does not have:
--
--   XF86MonBrightness*, XF86KbdBrightness*, XF86KbdLightOnOff   no backlight
--   XF86TouchpadToggle/On/Off, switch:Lid Switch                no laptop
--   SUPER + CTRL + Delete (+ ALT)      toggle/mirror laptop display
--   SUPER + CTRL + B                   bluetooth controls, no controller
--   SUPER + CTRL + ALT + B             battery remaining, no battery
--   SUPER + CTRL + N                   nightlight; its daemon needs a DRM
--                                      gamma ramp virtio-gpu does not expose
--   SUPER + CTRL + I                   idle locking; march runs no idle
--                                      daemon, the host already locks the
--                                      window this guest sits in
--   SUPER + XF86AudioMute              switch audio output, one sink here
--
-- Omarchy features march does not carry:
--
--   SUPER + CTRL + SPACE, SUPER + SHIFT + CTRL + SPACE   themes; march ships one
--   SUPER + CTRL + H                   hardware menu, all of it virtual
--   SUPER + CTRL + S                   share (LocalSend has no aarch64 build)
--   SUPER + CTRL + PERIOD              transcode
--   SUPER + CTRL + R (+ ALT, + SHIFT)  reminders
--   SUPER + CTRL + X, F9               dictation; voxtype is Omarchy's own
--   SUPER + CTRL + ALT + W             weather
--   SUPER + ALT + K                    tmux bindings, which march does not set
--   SUPER + SHIFT + code:201           the menu on a Logitech MX Keys button
--
-- Applications with no aarch64 package: SUPER + SHIFT + M (Spotify),
-- SUPER + SHIFT + ALT + M (cliamp), SUPER + SHIFT + G (Signal),
-- SUPER + SHIFT + O (Obsidian), SUPER + SHIFT + W (Typora),
-- SUPER + SHIFT + SLASH (1Password).
