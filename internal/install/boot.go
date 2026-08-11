package install

import (
	"embed"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// bootAssets holds the pieces of the guest's boot branding: the dark wallpaper
// the SDDM greeter and Hyprland both draw, and the SDDM theme that shows it.
//
//go:embed assets/boot
var bootAssets embed.FS

// bootBackground is where the wallpaper lands in the guest, read by both the
// SDDM theme and swaybg.
const bootBackground = "/usr/share/backgrounds/march.png"

// bootThemeDir is where the SDDM greeter theme lives, and what /etc/sddm.conf.d
// selects as Current.
const bootThemeDir = "/usr/share/sddm/themes/march"

// bootSnippet writes the branding that fills the window behind the desktop
// hand-off. Between the kernel's boot log and the bar animating in, the screen
// would otherwise sit black while SDDM logs the session in and Hyprland draws
// its first frame; instead both paint this same dark background — SDDM from
// the moment it takes the screen, swaybg on Hyprland's first frame — so the
// window always shows something rather than a void.
//
// The image travels as base64 inside a quoted heredoc: binary data through a
// text channel, decodable by coreutils' base64, which the base system ships.
func bootSnippet() string {
	read := func(name string) []byte {
		b, err := bootAssets.ReadFile(path.Join("assets/boot", name))
		if err != nil {
			// Embedded at build time; a stale name surfaces in tests.
			panic(err)
		}
		return b
	}

	var b strings.Builder
	w := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}
	text := func(content []byte) { w("%s", string(content)) }

	w(`mkdir -p /usr/share/backgrounds`)
	w(`base64 -d > %s <<'MARCHBGP'`, shellQuote(bootBackground))
	w(base64.StdEncoding.EncodeToString(read("background.png")))
	w(`MARCHBGP`)

	w(`mkdir -p %s`, shellQuote(bootThemeDir))
	w(`cat > %s/Main.qml <<'MARCHQML'`, shellQuote(bootThemeDir))
	text(read(path.Join("sddm-march", "Main.qml")))
	w(`MARCHQML`)
	w(`cat > %s/theme.conf <<'MARCHTC'`, shellQuote(bootThemeDir))
	text(read(path.Join("sddm-march", "theme.conf")))
	w(`MARCHTC`)
	w(`cat > %s/metadata.desktop <<'MARCHMT'`, shellQuote(bootThemeDir))
	text(read(path.Join("sddm-march", "metadata.desktop")))
	w(`MARCHMT`)

	w(`mkdir -p /etc/sddm.conf.d`)
	w(`cat > /etc/sddm.conf.d/theme.conf <<'MARCHTHMC'`)
	w(`[Theme]`)
	w(`Current=march`)
	w(`MARCHTHMC`)

	return b.String()
}
