package install

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// hyprlandAssets holds the Hyprland desktop's configuration, derived from
// Omarchy v3.8.4. See assets/hyprland/NOTICE for provenance and the changes
// made for aarch64 and software rendering.
//
//go:embed assets/hyprland
var hyprlandAssets embed.FS

// hyprlandPackages is the desktop itself. Every entry is packaged for aarch64
// in Arch Linux ARM's repositories — Omarchy's own packages are not, which is
// why its menu, launcher and helper binaries are substituted rather than
// installed.
var hyprlandPackages = []string{
	// Compositor and session. No hypridle: a VM window sits inside a host that
	// locks itself, and an idle daemon with nothing to do is still a daemon.
	"hyprland", "xdg-desktop-portal-hyprland", "qt6-wayland",
	"hyprlock", "hyprpicker",
	"sddm", "polkit-gnome",

	// Bar, launcher, notifications, on-screen display
	"waybar", "fuzzel", "mako", "swayosd", "swaybg",

	// The programs the keybindings actually invoke
	// No chromium: the browser is Google Chrome, unpacked from Google's own
	// arm64 build because no Arch package carries one. See chrome.go.
	"alacritty", "nautilus",
	"grim", "slurp", "wl-clipboard",
	// No brightnessctl: there is no backlight behind virtio-gpu to set.
	"wiremix", "btop", "jq", "playerctl",

	// What the menu is made of. Omarchy's equivalents are Quickshell panels and
	// its own binaries; these are what Arch Linux ARM packages for the same
	// jobs. wf-recorder rather than gpu-screen-recorder: it encodes with
	// libx264 on the CPU, and virtio-gpu exposes no hardware encoder.
	"cliphist", "wl-clip-persist", "rofimoji", "wtype",
	"wf-recorder", "satty", "libnotify",

	// Fonts and icons the bar and launcher expect
	"ttf-jetbrains-mono-nerd", "papirus-icon-theme",
}

// guestConfigRoot is where the Hyprland config lands. Writing to /etc/skel
// means useradd copies it into the new account, so the files belong to the user
// and can be edited without root.
const guestConfigRoot = "/etc/skel"

// hyprlandFileMap maps each embedded asset to its path in the guest.
var hyprlandFileMap = map[string]string{
	"hyprland.lua":        guestConfigRoot + "/.config/hypr/hyprland.lua",
	"bindings.lua":        guestConfigRoot + "/.config/hypr/bindings.lua",
	"looknfeel.lua":       guestConfigRoot + "/.config/hypr/looknfeel.lua",
	"apps.lua":            guestConfigRoot + "/.config/hypr/apps.lua",
	"envs.lua":            guestConfigRoot + "/.config/hypr/envs.lua",
	"waybar/config.jsonc": guestConfigRoot + "/.config/waybar/config.jsonc",
	"waybar/style.css":    guestConfigRoot + "/.config/waybar/style.css",
	"fuzzel.ini":          guestConfigRoot + "/.config/fuzzel/fuzzel.ini",
	"mako.conf":           guestConfigRoot + "/.config/mako/config",
	"hyprlock.conf":       guestConfigRoot + "/.config/hypr/hyprlock.conf",
	"mpv.conf":            guestConfigRoot + "/.config/mpv/mpv.conf",

	// Helpers replacing Omarchy's menu binaries; on PATH for every user.
	"bin/march-menu":        "/usr/local/bin/march-menu",
	"bin/march-keybindings": "/usr/local/bin/march-keybindings",
	"bin/march-term":        "/usr/local/bin/march-term",
	"bin/march-bar":         "/usr/local/bin/march-bar",
	"bin/march-toggle":      "/usr/local/bin/march-toggle",
	"bin/march-capture":     "/usr/local/bin/march-capture",
	"bin/march-clipboard":   "/usr/local/bin/march-clipboard",
	"bin/march-pkg":         "/usr/local/bin/march-pkg",
}

// HyprlandAsset returns one embedded configuration file, so tests can assert on
// exactly what the guest will receive.
func HyprlandAsset(name string) (string, error) {
	b, err := hyprlandAssets.ReadFile(path.Join("assets/hyprland", name))
	if err != nil {
		return "", fmt.Errorf("reading Hyprland asset %q: %w", name, err)
	}
	return string(b), nil
}

// HyprlandAssetNames lists every embedded asset that is written to the guest.
func HyprlandAssetNames() []string {
	names := make([]string, 0, len(hyprlandFileMap))
	for name := range hyprlandFileMap {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hyprlandConfigSnippet renders the shell that writes every asset into the
// guest. Each file is emitted as a quoted heredoc so its contents reach the
// guest byte for byte, with no shell expansion.
func hyprlandConfigSnippet(p Profile) string {
	var b strings.Builder

	b.WriteString("# Hyprland configuration, derived from Omarchy (see march's NOTICE).\n")
	for _, name := range HyprlandAssetNames() {
		dest := hyprlandFileMap[name]
		content, err := HyprlandAsset(name)
		if err != nil {
			// The assets are embedded at build time, so this cannot happen
			// unless the map and the directory have drifted apart.
			panic(err)
		}

		fmt.Fprintf(&b, "mkdir -p %s\n", shellQuote(path.Dir(dest)))
		// EOF markers are unique per file so a file that happens to contain the
		// word cannot terminate its own heredoc early.
		marker := heredocMarker(name)
		fmt.Fprintf(&b, "cat > %s <<'%s'\n", shellQuote(dest), marker)
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s\n", marker)
		if strings.HasPrefix(dest, "/usr/local/bin/") {
			fmt.Fprintf(&b, "chmod 0755 %s\n", shellQuote(dest))
		}
	}

	// Visual effects depend on how the guest renders, which is a property of
	// the machine rather than of Omarchy's configuration.
	fmt.Fprintf(&b, "cat > %s <<'MARCHEFFECTS'\n", guestConfigRoot+"/.config/hypr/effects.lua")
	b.WriteString(hyprlandEffectsConfig(p))
	b.WriteString("MARCHEFFECTS\n")

	// The monitor line is generated rather than embedded: the resolution and
	// scale are march's, not Omarchy's.
	fmt.Fprintf(&b, "cat > %s <<'MARCHMONITOR'\n", guestConfigRoot+"/.config/hypr/monitor.lua")
	b.WriteString(hyprlandMonitorConfig(p))
	b.WriteString("MARCHMONITOR\n")

	return b.String()
}

// hyprlandMonitorConfig sets the display scale. Wayland scales in the
// compositor rather than through GDK_SCALE, so this replaces the X11 scaling
// path entirely for Hyprland.
func hyprlandMonitorConfig(p Profile) string {
	scale := p.scaleFactor()
	return fmt.Sprintf(`-- Written by march. "preferred" follows whatever resolution QEMU reports,
-- so the desktop tracks the window rather than being pinned to one size.
hl.monitor({
    output = "",
    mode = "preferred",
    position = "auto",
    scale = %d,
})
`, scale)
}

// hyprlandEffectsConfig restores Omarchy's blur, shadows and animations when
// the guest has real hardware rendering, and leaves them off when every frame
// would otherwise be drawn on the CPU.
func hyprlandEffectsConfig(p Profile) string {
	if !p.GPUAccelerated {
		return `-- This guest renders in software (llvmpipe), so Omarchy's blur, shadows and
-- animations stay off — on the CPU they cost far more than they are worth.
-- Installing march's accelerated QEMU turns them back on automatically.
`
	}
	return `-- This guest renders on the host GPU, so Omarchy's effects are restored.
hl.config({
    decoration = {
        rounding = 0,

        shadow = {
            enabled = true,
            range = 2,
            render_power = 3,
            color = "rgba(1a1a1aee)",
        },

        blur = {
            enabled = true,
            size = 2,
            passes = 2,
            special = true,
            brightness = 0.60,
            contrast = 0.75,
        },
    },

    animations = {
        enabled = true,
    },
})

-- Curves have to exist before an animation can name one.
hl.curve("easeOutQuint", { type = "bezier", points = { { 0.23, 1 }, { 0.32, 1 } } })
hl.curve("easeInOutCubic", { type = "bezier", points = { { 0.65, 0.05 }, { 0.36, 1 } } })
hl.curve("linear", { type = "bezier", points = { { 0, 0 }, { 1, 1 } } })
hl.curve("almostLinear", { type = "bezier", points = { { 0.5, 0.5 }, { 0.75, 1.0 } } })
hl.curve("quick", { type = "bezier", points = { { 0.15, 0 }, { 0.1, 1 } } })

hl.animation({ leaf = "global", enabled = true, speed = 10, bezier = "default" })
hl.animation({ leaf = "border", enabled = true, speed = 5.39, bezier = "easeOutQuint" })
hl.animation({ leaf = "windows", enabled = true, speed = 3.79, bezier = "easeOutQuint" })
hl.animation({ leaf = "windowsIn", enabled = true, speed = 4.1, bezier = "easeOutQuint", style = "popin 87%" })
hl.animation({ leaf = "windowsOut", enabled = true, speed = 1.49, bezier = "linear", style = "popin 87%" })
hl.animation({ leaf = "fadeIn", enabled = true, speed = 1.73, bezier = "almostLinear" })
hl.animation({ leaf = "fadeOut", enabled = true, speed = 1.46, bezier = "almostLinear" })
hl.animation({ leaf = "fade", enabled = true, speed = 3.03, bezier = "quick" })
hl.animation({ leaf = "layers", enabled = true, speed = 3.81, bezier = "easeOutQuint" })
hl.animation({ leaf = "layersIn", enabled = true, speed = 4, bezier = "easeOutQuint", style = "fade" })
hl.animation({ leaf = "layersOut", enabled = true, speed = 1.5, bezier = "linear", style = "fade" })
hl.animation({ leaf = "workspaces", enabled = false })
hl.animation({ leaf = "specialWorkspace", enabled = true, speed = 3, bezier = "easeOutQuint", style = "slidevert" })
`
}

// heredocMarker derives a unique, shell-safe terminator from a file name.
func heredocMarker(name string) string {
	upper := strings.ToUpper(name)
	upper = strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(upper)
	return "MARCH_" + upper
}

// hyprlandAssetsValid reports whether every mapped asset actually exists in the
// embedded filesystem. Used by tests to catch a rename that would otherwise
// only fail during a real install.
func hyprlandAssetsValid() error {
	return fs.WalkDir(hyprlandAssets, "assets/hyprland", func(p string, d fs.DirEntry, err error) error {
		return err
	})
}
