-- Default applications, from Omarchy v3.8.4 (see NOTICE), pointed at what is
-- packaged for Arch Linux ARM. Omarchy defaults to Ghostty, which has no aarch64
-- package; Alacritty is the closest equivalent that does.
--
-- The table is returned so bindings.lua can `require("apps")` for it, which is
-- what the $terminal / $browser / $fileManager hyprlang variables used to do.

local apps = {
    terminal = "alacritty",
    browser = "chromium --ozone-platform=wayland",
    file_manager = "nautilus --new-window",
}

-- Window rules. A rule is a match table of conditions plus the effects to
-- apply, and rules are evaluated in the order written.
hl.window_rule({
    name = "suppress-maximize-events",
    match = { class = ".*" },
    suppress_event = "maximize",
})

hl.window_rule({
    name = "float-calculator",
    match = { class = "^(org.gnome.Calculator)$" },
    float = true,
})

hl.window_rule({
    name = "float-network-editor",
    match = { class = "^(nm-connection-editor)$" },
    float = true,
})

hl.window_rule({
    name = "float-picture-in-picture",
    match = { title = "^(Picture-in-Picture)$" },
    float = true,
    pin = true,
})

-- Fix an XWayland quirk where a stray unnamed window steals focus.
hl.window_rule({
    name = "fix-xwayland-focus-steal",
    match = { class = "^$", title = "^$", xwayland = true },
    no_focus = true,
})

return apps
