package install

import "fmt"

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

// chromeFlags is how march starts Chrome, wherever it is started from.
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
const chromeFlags = "--ozone-platform=wayland --password-store=basic " +
	"--no-first-run --no-default-browser-check"

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
# Chrome has no flags file of its own, so the flags go in the desktop entry —
# the one thing every launcher, xdg-open included, reads.
for entry in /mnt/usr/share/applications/google-chrome.desktop \
             /mnt/usr/share/applications/com.google.Chrome.desktop; do
  if [ -f "$entry" ]; then
    sed -i 's|^Exec=/usr/bin/google-chrome-stable|Exec=/usr/bin/google-chrome-stable %s|' "$entry"
  fi
done
`, chromeDebURL, chromeFlags)
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
`, chromeBinary, guestConfigRoot, guestConfigRoot,
		chromeDesktopFile, chromeDesktopFile, chromeDesktopFile,
		chromeDesktopFile, chromeDesktopFile)
}
