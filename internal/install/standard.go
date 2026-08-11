package install

// standardPackages is the toolset every march guest gets, desktop or not.
//
// It mirrors Omarchy's own base package list (basecamp/omarchy, quattro), which
// is what makes an Omarchy machine feel finished the moment it boots rather
// than like a bare Arch install. Of Omarchy's 147 packages, 120 exist for
// aarch64 in Arch Linux ARM's repositories and are taken here. The 27 that do
// not are Omarchy's own binaries (omacut, omawrite, herdr, omarchy-nvim,
// tensaku, ttfx, aether, cliamp, tobi-try) plus obsidian, obs-studio, mise,
// localsend, pinta, yay, quickshell-git, dotnet-runtime, asdcontrol,
// hyprland-preview-share-picker, xdg-terminal-exec, ttf-ia-writer,
// yaru-icon-theme, tzupdate, ufw-docker and qemu-user-static-binfmt. There is
// no aarch64 build to install, so they are simply absent.
//
// This list costs roughly 940 MB of download, which is most of why an install
// takes about four minutes rather than three. Dropping linux-firmware from
// basePackages pays back 277 MB of that.
var standardPackages = []string{
	// Shell, search and the things one reaches for in a terminal.
	"bat", "eza", "fd", "fzf", "ripgrep", "zoxide", "starship", "tldr",
	"expac", "dua-cli", "fastfetch", "inxi", "plocate", "bash-completion",
	"pacman-contrib", "tmux", "unzip", "whois", "socat", "inetutils",
	"inotify-tools", "gum", "libyaml",

	// Editors, version control and language runtimes.
	"git", "lazygit", "neovim", "tree-sitter-cli", "lua51", "luarocks",
	"ruby", "python-gobject", "python-poetry-core", "usage", "fakeroot",
	"clang", "llvm", "mariadb-libs", "postgresql-libs",

	// Containers.
	"docker", "docker-buildx", "docker-compose", "lazydocker",

	// Media, images and the tools that read them. gpu-screen-recorder is not
	// here: it records through VAAPI or NVENC, and virtio-gpu exposes no
	// hardware encoder.
	"mpv", "mpv-mpris", "imv", "imagemagick", "libvips", "ffmpegthumbnailer",
	"yt-dlp", "kdenlive", "zbar", "qrencode",
	"tesseract", "tesseract-data-eng",

	// Desktop applications. hyprsunset is not here: it shifts colour through
	// the DRM gamma ramp, and aquamarine already reports "Couldn't get the
	// gamma_size prop" on this hardware. moonlight-qt is not here either — it
	// is a game-streaming client that needs hardware video decode.
	"evince", "xournalpp", "libreoffice-fresh", "gnome-disk-utility",
	"system-config-printer", "foot", "hyprland-guiutils",

	// Files, mounts and network shares. gvfs-mtp is not here: MTP needs a USB
	// device passed through, and march passes none.
	"gvfs-nfs", "gvfs-smb", "sushi", "nautilus-python",
	"udiskie", "exfatprogs",

	// Fonts and theming.
	"noto-fonts-cjk", "woff2-font-awesome", "gnome-themes-extra", "fontconfig",

	// Secrets.
	"gnome-keyring", "libsecret",

	// Printing. cups-pdf gives a guest with no printer somewhere to print,
	// which is the only printing that works from behind user-mode networking.
	// cups-browsed is not here: it finds printers by mDNS, which does not
	// cross QEMU's NAT.
	"cups", "cups-filters", "cups-pdf",

	// Firewall.
	"ufw",

	// Input methods. Installed for parity; they stay inert until someone sets
	// GTK_IM_MODULE/QT_IM_MODULE and starts the daemon.
	"fcitx5", "fcitx5-gtk", "fcitx5-qt",

	// Audio. pipewire already arrives as a dependency, but wireplumber does
	// not, and without the session manager pipewire produces no sound at all.
	"wireplumber", "pipewire-pulse", "pipewire-alsa", "alsa-utils", "pamixer",

	// Keeps the running kernel's modules loadable after an upgrade, so a live
	// session does not lose the ability to modprobe halfway through the day.
	"kernel-modules-hook",
}

// Omarchy ships these and march does not, because nothing behind QEMU's virt
// machine answers to them. Installing a package whose hardware is absent buys
// a command that fails, a daemon that idles, or a service that shows up in
// `systemctl --failed`:
//
//	bluez, bluez-utils, bluez-tools  no Bluetooth controller
//	bolt                             no Thunderbolt
//	ddcutil                          the virtual display has no DDC/CI channel
//	power-profiles-daemon            no battery, no platform profile
//	wireless-regdb                   no radio
//	avahi, nss-mdns                  multicast does not cross QEMU's user-mode NAT
//	cups-browsed                     finds printers by mDNS, see above
//	gvfs-mtp                         MTP needs a USB device passed through
//	gpu-screen-recorder              virtio-gpu exposes no hardware encoder
//	hyprsunset                       needs a DRM gamma ramp this GPU lacks
//	moonlight-qt                     game streaming without hardware decode
//	plymouth                         a boot splash needs a quiet kernel command
//	                                 line, which would hide the console output
//	                                 the installer reads
//	uwsm                             would replace the SDDM session march uses
//
// The same reasoning removed brightnessctl from hyprlandPackages: there is no
// backlight to set.
var omittedForVirtualHardware = []string{
	"bluez", "bluez-utils", "bluez-tools", "bolt", "ddcutil",
	"power-profiles-daemon", "wireless-regdb", "avahi", "nss-mdns",
	"cups-browsed", "gvfs-mtp", "gpu-screen-recorder", "hyprsunset",
	"moonlight-qt", "plymouth", "uwsm", "brightnessctl",
}
