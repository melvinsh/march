-- Look and feel, from Omarchy v3.8.4 (see NOTICE).
--
-- The one substantive change: blur, shadows and animations are off *here*, as
-- the safe baseline for a guest whose frames are drawn by llvmpipe on the CPU,
-- where these effects turn a responsive desktop into a slideshow. When march
-- detects a virgl-capable QEMU it turns all three back on from effects.lua,
-- which is loaded after this file. Everything else is Omarchy's.

local activeBorder = { colors = { "rgba(33ccffee)", "rgba(00ff99ee)" }, angle = 45 }
local inactiveBorder = "rgba(595959aa)"

hl.config({
    general = {
        gaps_in = 5,
        gaps_out = 10,

        border_size = 2,

        col = {
            active_border = activeBorder,
            inactive_border = inactiveBorder,
        },

        resize_on_border = false,
        allow_tearing = false,

        layout = "dwindle",
    },

    decoration = {
        rounding = 0,

        -- Both disabled for software rendering.
        shadow = {
            enabled = false,
        },

        blur = {
            enabled = false,
        },
    },

    group = {
        col = {
            border_active = activeBorder,
            border_inactive = inactiveBorder,
            border_locked_active = activeBorder,
            border_locked_inactive = inactiveBorder,
        },

        groupbar = {
            font_size = 12,
            font_family = "monospace",
            font_weight_active = "ultraheavy",
            font_weight_inactive = "normal",

            indicator_height = 0,
            indicator_gap = 5,
            height = 22,
            gaps_in = 5,
            gaps_out = 0,

            text_color = "rgb(ffffff)",
            text_color_inactive = "rgba(ffffff90)",
            col = {
                active = "rgba(00000040)",
                inactive = "rgba(00000020)",
            },

            gradients = true,
            gradient_rounding = 0,
            gradient_round_only_edges = false,
        },
    },

    -- Disabled for software rendering: animating windows means redrawing them
    -- many times a second on the CPU.
    animations = {
        enabled = false,
    },

    dwindle = {
        preserve_split = true,
        force_split = 2,
    },

    master = {
        new_status = "master",
    },

    misc = {
        disable_hyprland_logo = true,
        disable_splash_rendering = true,
        disable_scale_notification = true,
        focus_on_activate = true,
        anr_missed_pings = 3,
        on_focus_under_fullscreen = 1,
    },

    cursor = {
        hide_on_key_press = true,
        warp_on_change_workspace = 1,
        -- There is no hardware cursor plane behind virtio-gpu; asking for one
        -- leaves the pointer invisible or trailing. This one takes an int
        -- (0 hw, 1 never, 2 auto), not a bool.
        no_hardware_cursors = 1,
    },

    binds = {
        hide_special_on_workspace_change = true,
    },
})
