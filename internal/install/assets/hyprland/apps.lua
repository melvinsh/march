-- Default applications, from Omarchy v3.8.4 (see NOTICE), pointed at what is
-- packaged for Arch Linux ARM. Omarchy defaults to Ghostty, which has no aarch64
-- package; Alacritty is the closest equivalent that does.
--
-- The browser is Google Chrome, which is not a pacman package at all: march
-- unpacks Google's own arm64 build. The flags are the ones march patches into
-- Chrome's desktop entry, so a key and a click start the same browser; see
-- chromeFlags in internal/install/chrome.go for what each one is for.
--
-- The table is returned so bindings.lua can `require("apps")` for it, which is
-- what the $terminal / $browser / $fileManager hyprlang variables used to do.

local apps = {
    terminal = "alacritty",
    browser = "google-chrome-stable --ozone-platform=wayland --password-store=basic --no-first-run --no-default-browser-check",
    file_manager = "nautilus --new-window",
}

-- Window rules. A rule is a match table of conditions plus the effects to
-- apply, and rules are evaluated in the order written.
hl.window_rule({
    name = "suppress-maximize-events",
    match = { class = ".*" },
    suppress_event = "maximize",
})

-- The window march-term opens for a TUI. Omarchy floats the same way for its
-- omarchy-launch-floating-terminal, so a menu action never rearranges the
-- windows already on screen.
hl.window_rule({
    name = "float-march-terminal",
    match = { class = "^(march-float)$" },
    float = true,
    -- Sizes are expressions, not percentages: the rule language has no "%".
    size = { "monitor_w * 0.6", "monitor_h * 0.6" },
    center = true,
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
