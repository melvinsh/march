package install

import (
	"fmt"
	"strings"
)

// Google Chrome is march's browser. Google began shipping Linux builds for
// aarch64 in 2026 and publishes them as .deb and .rpm only — there is no Arch
// package, official or in the AUR, that carries an aarch64 build, and march
// installs no AUR helper.
//
// So the official .deb is fetched and unpacked, which is what an AUR PKGBUILD
// would do anyway. That makes Google's CDN a dependency of every install, where
// everything else comes from an Arch Linux ARM mirror. The alternative was
// Chromium, which is in the repositories and is not what was asked for.
//
// Updates do not come from pacman: /etc/cron.daily/google-chrome, which the
// package ships to add Google's apt repository, is deliberately left out. On a
// machine with no apt it would run daily and achieve nothing.
const chromeDebURL = "https://dl.google.com/linux/direct/google-chrome-stable_current_arm64.deb"

// chromeBinary is the launcher the .deb installs — a symlink into
// /opt/google/chrome. There is no plain "google-chrome": the Debian package
// creates that through update-alternatives, which is a postinst step and no
// part of the archive.
const chromeBinary = "google-chrome-stable"

// chromeDesktopFile identifies Chrome to anything that resolves a default
// application, which is how it becomes *the* browser rather than merely an
// installed one.
const chromeDesktopFile = "google-chrome.desktop"

// chromeBaseFlags is how march starts Chrome on any guest.
//
//	--ozone-platform=wayland     Chrome still picks X11 on its own, and under
//	                             XWayland it ignores the compositor's scale.
//	--password-store=basic       Chrome otherwise asks gnome-keyring to unlock
//	                             the login keyring, and on a machine that logs
//	                             itself in there is no password to unlock it
//	                             with: gcr-prompter opens a dialog and the
//	                             browser waits behind it forever. Passwords then
//	                             live in Chrome's own store rather than the
//	                             keyring, which is the trade a guest that logs
//	                             itself in has already made.
//	--no-first-run               the welcome tour maps a window with no app_id,
//	--no-default-browser-check   and march has already answered the question it
//	                             asks.
//
// Found by watching a guest sit on "Welcome to Google Chrome" for four minutes;
// with these it is up in five seconds.
var chromeBaseFlags = []string{
	"--ozone-platform=wayland",
	"--password-store=basic",
	"--no-first-run",
	"--no-default-browser-check",
}

// chromeLauncher is the one place Chrome's flags live.
//
// They used to be a constant repeated verbatim in five places — the desktop
// entry, apps.lua, march-menu, march-launch's fallback and the in-guest
// self-test — which was tolerable only while every guest wanted the same ones.
// The graphics flags do not work that way: they depend on whether this
// particular guest renders on the host GPU, which is a property of the machine
// rather than of Chrome. So the flags move into a generated launcher, the way
// effects.lua already generates the compositor's, and everything that starts a
// browser starts this instead.
const chromeLauncher = "march-chrome"

const chromeLauncherPath = "/usr/local/bin/" + chromeLauncher

// chromeGPUFlags are the flags a guest earns by rendering on the host GPU.
//
// There are none, and the emptiness is the finding rather than a gap. Every
// backend Chrome can be pointed at was measured in a guest whose Mesa renders
// through virgl, and all of them end at the same line: Chrome asks EGL for an
// ES 3.0 context, virgl answers EGL_BAD_ATTRIBUTE, and because Chromium
// compiles the ES-2.0 retry out on Linux the GPU process exits instead of
// stepping down. No flag reaches past that, because it happens before any
// backend is chosen. docs/GRAPHICS.md has the measurements and the log lines.
//
// The hook stays because the wall is not march's and may not be permanent: a
// Chrome or Mesa that grants the context turns this into a one-line change,
// and TestChromeLauncherCarriesTheFlags already covers both sides of the gate.
//
// Deliberately absent: --enable-unsafe-swiftshader, which does restore WebGL by
// re-enabling the software fallback Chrome 151 turned off. SwiftShader JITs
// shader code inside the GPU process, which is why Chrome stopped reaching for
// it on its own, and a browser that renders untrusted pages is the last place
// to buy a feature back with that. The guest has no WebGL instead.
func chromeGPUFlags(p Profile) []string {
	if !p.GPUAccelerated {
		// Kept as the explicit half of the gate: a software-rendered guest
		// would take ANGLE's translation layer on top of llvmpipe, which is
		// slower than the CPU rasterizer Chrome already ships.
		return nil
	}
	return nil
}

// chromeLauncherSnippet writes the launcher into the guest.
func chromeLauncherSnippet(p Profile) string {
	flags := append(append([]string{}, chromeBaseFlags...), chromeGPUFlags(p)...)

	var b strings.Builder
	fmt.Fprintf(&b, "cat > %s <<'MARCHCHROME'\n", chromeLauncherPath)
	b.WriteString("#!/bin/bash\n")
	b.WriteString(`# Written by march (internal/install/chrome.go): the flags Chrome is started
# with, in the one place every launcher reads. Chrome has no flags file of its
# own, so this script is it.
#
# "$@" carries whatever the caller adds — the desktop entry's %U, a URL from
# march-menu, --app= from march-launch.
`)
	fmt.Fprintf(&b, "exec /usr/bin/%s \\\n", chromeBinary)
	for _, f := range flags {
		fmt.Fprintf(&b, "  %s \\\n", f)
	}
	b.WriteString("  \"$@\"\n")
	b.WriteString("MARCHCHROME\n")
	fmt.Fprintf(&b, "chmod 0755 %s\n", chromeLauncherPath)
	return b.String()
}

// chromePackages are Chrome's own Debian dependencies mapped to Arch names.
// Most arrive with the desktop anyway — the overlap is deliberate, since the
// point of the list is to be Chrome's requirements rather than the difference
// between them and somebody else's. A missing one shows up as a browser that
// exits with a linker error and no other explanation.
var chromePackages = []string{
	"ttf-liberation", "xdg-utils", "ca-certificates",
	"alsa-lib", "at-spi2-core", "cairo", "pango", "gtk3", "nss", "nspr",
	"libcups", "libdrm", "libxcomposite", "libxdamage", "libxext", "libxfixes",
	"libxkbcommon", "libxrandr", "libx11", "libxcb", "expat", "glib2", "mesa",
	// Chrome's compositor looks for Vulkan whether or not the host forwards it,
	// and finding no loader at all is a different failure from finding one with
	// no device behind it.
	"vulkan-icd-loader",
}

// chromeSnippet fetches and unpacks Chrome into the target root.
//
// It runs in the live environment rather than in the chroot: pacstrap has
// already proved there is a network there, while a chroot's resolver setup is
// one more thing that can differ between Archboot versions.
func chromeSnippet() string {
	return fmt.Sprintf(`# Google Chrome, from Google's own arm64 package (see internal/install/chrome.go).
curl -fL --retry 3 --retry-delay 2 -o /tmp/chrome.deb %s
mkdir -p /tmp/chrome
bsdtar -xf /tmp/chrome.deb -C /tmp/chrome
# A .deb is an ar archive holding a tarball; the compression of the inner
# tarball is Google's to change, so it is matched rather than named.
bsdtar -xpf /tmp/chrome/data.tar.* -C /mnt --exclude './etc/cron.daily*'
rm -rf /tmp/chrome /tmp/chrome.deb
[ -x /mnt/opt/google/chrome/chrome ] || { echo "march: Chrome did not unpack"; exit 1; }
# Point the desktop entry at march's launcher, which is where the flags live.
# The anchor is deliberately the whole binary path and nothing after it: it
# rewrites the [Desktop Action] entries too, and leaves each line's trailing
# %%U alone so a clicked link still reaches the browser.
found=0
for entry in /mnt/usr/share/applications/google-chrome.desktop \
             /mnt/usr/share/applications/com.google.Chrome.desktop; do
  if [ -f "$entry" ]; then
    sed -i 's|^Exec=/usr/bin/%s|Exec=%s|' "$entry"
    grep -q '^Exec=%s' "$entry" && found=1
  fi
done
# A desktop entry march did not manage to rewrite is a browser that starts with
# none of these flags, which is a four-minute welcome tour and no Wayland.
[ "$found" = 1 ] || { echo "march: Chrome's desktop entry kept its own Exec line"; exit 1; }
`, chromeDebURL, chromeBinary, chromeLauncherPath, chromeLauncherPath)
}

// chromeDefaultsSnippet makes Chrome the browser the desktop opens links with.
//
// mimeapps.list is written rather than xdg-settings run: xdg-settings needs a
// session to talk to, and this is a chroot. Writing it into /etc/skel means
// useradd copies it into the account, where the user can change it.
//
// $BROWSER goes into /etc/environment as well as into the compositor's own
// environment, so a shell over ssh resolves a link the same way the desktop
// does.
func chromeDefaultsSnippet() string {
	return fmt.Sprintf(`cat >> /etc/environment <<'BROWSERENV'
BROWSER=%s
BROWSERENV
mkdir -p %s/.config
cat > %s/.config/mimeapps.list <<'MIMEAPPS'
[Default Applications]
x-scheme-handler/http=%s
x-scheme-handler/https=%s
x-scheme-handler/about=%s
x-scheme-handler/unknown=%s
text/html=%s
MIMEAPPS
`, chromeLauncher, guestConfigRoot, guestConfigRoot,
		chromeDesktopFile, chromeDesktopFile, chromeDesktopFile,
		chromeDesktopFile, chromeDesktopFile)
}
