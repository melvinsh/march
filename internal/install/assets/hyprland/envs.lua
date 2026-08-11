-- Environment, from Omarchy v3.8.4 (see NOTICE).
--
-- Omarchy's kvantum override and theme sourcing are dropped: march ships no Qt
-- theme engine and no theme switcher.

hl.env("XCURSOR_SIZE", "24")
hl.env("HYPRCURSOR_SIZE", "24")

-- Push apps onto Wayland rather than XWayland wherever they support it.
hl.env("GDK_BACKEND", "wayland,x11,*")
hl.env("QT_QPA_PLATFORM", "wayland;xcb")
hl.env("MOZ_ENABLE_WAYLAND", "1")
hl.env("ELECTRON_OZONE_PLATFORM_HINT", "wayland")
hl.env("OZONE_PLATFORM", "wayland")
hl.env("XDG_SESSION_TYPE", "wayland")

-- What xdg-open and anything else that opens a link falls back to. The desktop
-- entry in mimeapps.list is the real default; this covers the tools that never
-- ask xdg-mime.
hl.env("BROWSER", "google-chrome-stable")

-- Portals key off these, which is what makes screen sharing and file pickers work.
hl.env("XDG_CURRENT_DESKTOP", "Hyprland")
hl.env("XDG_SESSION_DESKTOP", "Hyprland")

hl.config({
    xwayland = {
        force_zero_scaling = true,
    },
})
