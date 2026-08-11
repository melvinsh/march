-- march — Hyprland, in the style of Omarchy (see NOTICE).
--
-- Edit freely: this file is yours. It is copied from /etc/skel on first login
-- and is never overwritten afterwards.
--
-- Hyprland deprecated its own config language (hyprlang, the old .conf files)
-- in 0.55 and reads hyprland.lua in preference to hyprland.conf. Each require
-- below is a separate Lua scope, so an error in one file does not stop the
-- others from loading.

require("apps")
require("envs")
require("looknfeel")
require("bindings")

-- Written by march: restores blur, shadows and animations when the guest has
-- hardware rendering, and leaves them off when it does not.
require("effects")

-- Written by march at install time so the guest matches the window it opens
-- in. "preferred" follows whatever resolution QEMU reports.
require("monitor")

-- Autostart. Everything here is packaged for aarch64; Omarchy's own agents and
-- theme daemons are deliberately absent.
hl.on("hyprland.start", function()
    hl.exec_cmd("waybar")
    hl.exec_cmd("mako")
    hl.exec_cmd("swayosd-server")

    -- Clipboard history, which march-clipboard reads back. wl-clip-persist
    -- keeps what was copied after the program that copied it exits; on Wayland
    -- the clipboard otherwise dies with its owner.
    hl.exec_cmd("wl-paste --type text --watch cliphist store")
    hl.exec_cmd("wl-paste --type image --watch cliphist store")
    hl.exec_cmd("wl-clip-persist --clipboard regular")

    hl.exec_cmd('swaybg -c "#1a1b26"')
    hl.exec_cmd("/usr/lib/polkit-gnome/polkit-gnome-authentication-agent-1")

    -- Hand the environment to systemd and D-Bus, or portals and any service
    -- started later come up without WAYLAND_DISPLAY and fail in confusing ways.
    hl.exec_cmd("systemctl --user import-environment WAYLAND_DISPLAY XDG_CURRENT_DESKTOP HYPRLAND_INSTANCE_SIGNATURE")
    hl.exec_cmd("dbus-update-activation-environment --systemd WAYLAND_DISPLAY XDG_CURRENT_DESKTOP HYPRLAND_INSTANCE_SIGNATURE")
end)
