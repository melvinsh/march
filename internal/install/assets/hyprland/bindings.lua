-- Keybindings, mirroring Omarchy v3.8.4 (see NOTICE).
--
-- The window-management half is Omarchy's verbatim: those are native Hyprland
-- dispatchers and need nothing from Omarchy's tooling. The utility half is
-- repointed at programs packaged for Arch Linux ARM, since none of Omarchy's own
-- binaries are built for aarch64.
--
-- The menu keys follow Omarchy's quattro branch rather than v3.8.4: the menu
-- itself, and the routes into it, are what that release moved onto SUPER +
-- SPACE and the SUPER + CTRL row.

local apps = require("apps")

-- ─── Applications ────────────────────────────────────────────────────────────
hl.bind("SUPER + Return", hl.dsp.exec_cmd(apps.terminal), { description = "Terminal" })
hl.bind("SUPER + F", hl.dsp.exec_cmd(apps.file_manager), { description = "File manager" })
hl.bind("SUPER + B", hl.dsp.exec_cmd(apps.browser), { description = "Web browser" })
hl.bind("SUPER + N", hl.dsp.exec_cmd(apps.terminal .. " -e nvim"), { description = "Editor" })
hl.bind("SUPER + T", hl.dsp.exec_cmd(apps.terminal .. " -e btop"), { description = "Activity monitor" })

-- ─── Launcher and menus ──────────────────────────────────────────────────────
-- Omarchy moved SUPER + SPACE from the launcher to its menu in quattro, leaving
-- the launcher one level deeper. march follows, with march-menu standing in for
-- omarchy-menu and fuzzel for walker.
hl.bind("SUPER + space", hl.dsp.exec_cmd("march-menu"), { description = "Menu" })
hl.bind("SUPER + ALT + space", hl.dsp.exec_cmd("fuzzel"), { description = "Launch apps" })
hl.bind("SUPER + Escape", hl.dsp.exec_cmd("march-menu system"), { description = "System menu" })
hl.bind("SUPER + CTRL + C", hl.dsp.exec_cmd("march-menu capture"), { description = "Capture menu" })
hl.bind("SUPER + CTRL + O", hl.dsp.exec_cmd("march-menu toggle"), { description = "Toggle menu" })
hl.bind("SUPER + K", hl.dsp.exec_cmd("march-keybindings"), { description = "Show key bindings" })
hl.bind("SUPER + CTRL + V", hl.dsp.exec_cmd("march-clipboard pick"), { description = "Clipboard history" })
hl.bind("SUPER + CTRL + E", hl.dsp.exec_cmd("rofimoji --selector fuzzel --action type"), { description = "Emoji picker" })

-- ─── Windows ─────────────────────────────────────────────────────────────────
hl.bind("SUPER + W", hl.dsp.window.close(), { description = "Close window" })
hl.bind("SUPER + J", hl.dsp.layout("togglesplit"), { description = "Toggle window split" })
hl.bind("SUPER + P", hl.dsp.window.pseudo(), { description = "Pseudo window" })
hl.bind("SUPER + SHIFT + V", hl.dsp.window.float({ action = "toggle" }), { description = "Toggle window floating/tiling" })
hl.bind("SHIFT + F11", hl.dsp.window.fullscreen({ mode = "fullscreen" }), { description = "Force full screen" })
hl.bind("ALT + F11", hl.dsp.window.fullscreen({ mode = "maximized" }), { description = "Full width" })

-- Move focus
hl.bind("SUPER + left", hl.dsp.focus({ direction = "left" }), { description = "Move focus left" })
hl.bind("SUPER + right", hl.dsp.focus({ direction = "right" }), { description = "Move focus right" })
hl.bind("SUPER + up", hl.dsp.focus({ direction = "up" }), { description = "Move focus up" })
hl.bind("SUPER + down", hl.dsp.focus({ direction = "down" }), { description = "Move focus down" })

-- Swap windows
hl.bind("SUPER + SHIFT + left", hl.dsp.window.swap({ direction = "left" }), { description = "Swap window to the left" })
hl.bind("SUPER + SHIFT + right", hl.dsp.window.swap({ direction = "right" }), { description = "Swap window to the right" })
hl.bind("SUPER + SHIFT + up", hl.dsp.window.swap({ direction = "up" }), { description = "Swap window up" })
hl.bind("SUPER + SHIFT + down", hl.dsp.window.swap({ direction = "down" }), { description = "Swap window down" })

-- Cycle windows
hl.bind("ALT + Tab", hl.dsp.window.cycle_next(), { description = "Cycle to next window" })
hl.bind("ALT + SHIFT + Tab", hl.dsp.window.cycle_next({ next = false }), { description = "Cycle to prev window" })
hl.bind("ALT + Tab", hl.dsp.window.alter_zorder({ mode = "top" }), { description = "Reveal active window on top" })
hl.bind("ALT + SHIFT + Tab", hl.dsp.window.alter_zorder({ mode = "top" }), { description = "Reveal active window on top" })

-- Resize. code:20 is the "-" key and code:21 is "=", as in Omarchy.
hl.bind("SUPER + code:20", hl.dsp.window.resize({ x = -100, y = 0, relative = true }), { description = "Expand window left" })
hl.bind("SUPER + code:21", hl.dsp.window.resize({ x = 100, y = 0, relative = true }), { description = "Shrink window left" })
hl.bind("SUPER + SHIFT + code:20", hl.dsp.window.resize({ x = 0, y = -100, relative = true }), { description = "Shrink window up" })
hl.bind("SUPER + SHIFT + code:21", hl.dsp.window.resize({ x = 0, y = 100, relative = true }), { description = "Expand window down" })

-- Grouping
hl.bind("SUPER + G", hl.dsp.group.toggle(), { description = "Toggle window grouping" })
hl.bind("SUPER + ALT + Tab", hl.dsp.group.next(), { description = "Next window in group" })
hl.bind("SUPER + ALT + SHIFT + Tab", hl.dsp.group.prev(), { description = "Previous window in group" })

-- Mouse
hl.bind("SUPER + mouse:272", hl.dsp.window.drag(), { mouse = true, description = "Move window" })
hl.bind("SUPER + mouse:273", hl.dsp.window.resize(), { mouse = true, description = "Resize window" })

-- ─── Workspaces ──────────────────────────────────────────────────────────────
-- Keycodes rather than digits, as in Omarchy, so the bindings hold on any
-- keyboard layout: code:10 is the "1" key through code:19 for "0".
for i = 1, 10 do
    local key = "code:" .. (9 + i)
    hl.bind("SUPER + " .. key, hl.dsp.focus({ workspace = i }), { description = "Switch to workspace " .. i })
    hl.bind("SUPER + SHIFT + " .. key, hl.dsp.window.move({ workspace = i }), { description = "Move window to workspace " .. i })
end

hl.bind("SUPER + Tab", hl.dsp.focus({ workspace = "e+1" }), { description = "Next workspace" })
hl.bind("SUPER + SHIFT + Tab", hl.dsp.focus({ workspace = "e-1" }), { description = "Previous workspace" })
hl.bind("SUPER + CTRL + Tab", hl.dsp.focus({ workspace = "previous" }), { description = "Former workspace" })

hl.bind("SUPER + S", hl.dsp.workspace.toggle_special("magic"), { description = "Toggle scratchpad" })
hl.bind("SUPER + ALT + S", hl.dsp.window.move({ workspace = "special:magic" }), { description = "Move window to scratchpad" })

hl.bind("SUPER + mouse_down", hl.dsp.focus({ workspace = "e+1" }), { description = "Scroll active workspace forward" })
hl.bind("SUPER + mouse_up", hl.dsp.focus({ workspace = "e-1" }), { description = "Scroll active workspace backward" })

-- ─── Utilities ───────────────────────────────────────────────────────────────
hl.bind("SUPER + SHIFT + space", hl.dsp.exec_cmd("march-toggle bar"), { description = "Toggle top bar" })
hl.bind("SUPER + SHIFT + BackSpace", hl.dsp.exec_cmd("march-toggle gaps"), { description = "Toggle window gaps" })
hl.bind("SUPER + CTRL + L", hl.dsp.exec_cmd("hyprlock"), { description = "Lock system" })

-- Screenshots and recording. Omarchy wraps these in omarchy-capture-*; march
-- wraps the same grim/slurp/tesseract calls in march-capture, so a screenshot
-- lands on disk and on the clipboard rather than only on the clipboard.
hl.bind("Print", hl.dsp.exec_cmd("march-capture region"), { description = "Screenshot region" })
hl.bind("SHIFT + Print", hl.dsp.exec_cmd("march-capture screen"), { description = "Screenshot screen" })
hl.bind("SUPER + Print", hl.dsp.exec_cmd("march-capture color"), { description = "Color picker" })
hl.bind("SUPER + CTRL + Print", hl.dsp.exec_cmd("march-capture text"), { description = "Extract text (OCR)" })
-- One key both starts and stops, as in Omarchy: stopping fails when nothing is
-- recording, and then starting is what was meant.
hl.bind("ALT + Print", hl.dsp.exec_cmd("march-capture record-stop || march-capture record"),
    { description = "Record screen" })

-- Notifications
hl.bind("SUPER + comma", hl.dsp.exec_cmd("makoctl dismiss"), { description = "Dismiss last notification" })
hl.bind("SUPER + SHIFT + comma", hl.dsp.exec_cmd("makoctl dismiss --all"), { description = "Dismiss all notifications" })
hl.bind("SUPER + ALT + comma", hl.dsp.exec_cmd("makoctl invoke"), { description = "Invoke last notification" })
hl.bind("SUPER + SHIFT + ALT + comma", hl.dsp.exec_cmd("makoctl restore"), { description = "Restore last notification" })
hl.bind("SUPER + CTRL + comma", hl.dsp.exec_cmd("march-toggle dnd"), { description = "Silence notifications" })

-- Control panels. Omarchy opens its own; these are the TUIs that do the same
-- job, in the floating window march-term gives them.
hl.bind("SUPER + CTRL + A", hl.dsp.exec_cmd("march-term wiremix"), { description = "Audio controls" })
hl.bind("SUPER + CTRL + W", hl.dsp.exec_cmd("march-term nmtui"), { description = "Wifi controls" })
hl.bind("SUPER + CTRL + T", hl.dsp.exec_cmd("march-term btop"), { description = "Activity" })

-- Zoom. Omarchy reads the current factor back with `hyprctl getoption` and
-- writes it with `hyprctl keyword`; the latter no longer exists now that the
-- config is Lua, so the level lives here instead. Keeping the work in Lua also
-- avoids spawning jq on a bind callback, which runs on the compositor's own
-- event loop and must not block.
local zoom = 1
hl.bind("SUPER + CTRL + Z", function()
    zoom = zoom + 1
    hl.config({ cursor = { zoom_factor = zoom } })
end, { description = "Zoom in" })
hl.bind("SUPER + CTRL + ALT + Z", function()
    zoom = 1
    hl.config({ cursor = { zoom_factor = zoom } })
end, { description = "Reset zoom" })

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
