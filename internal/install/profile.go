// Package install performs unattended Arch Linux ARM installations.
//
// The mechanism relies on two things discovered about the Archboot live
// environment: it honours an autorun=<url> kernel parameter that fetches and
// executes a script as root once the system is up, and its GRUB menu exposes a
// command line that can be driven over the serial console. march combines them
// — it serves an install script from the host, boots the ISO with a cmdline
// pointing at it, and the guest installs itself with no interaction.
package install

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Desktop is a graphical environment march can install.
type Desktop string

const (
	// DesktopHyprland is the default: a tiling Wayland compositor configured
	// after Omarchy. It runs acceptably under software rendering once blur,
	// shadows and animations are off.
	DesktopHyprland Desktop = "hyprland"
	// DesktopXFCE is the traditional X11 fallback. Under QEMU there is no host GPU passthrough
	// and Mesa falls back to software rendering, which XFCE handles well while
	// heavier compositors struggle.
	DesktopXFCE Desktop = "xfce"
	// DesktopGNOME is a full GNOME session on GDM.
	DesktopGNOME Desktop = "gnome"
	// DesktopPlasma is a full KDE Plasma session on SDDM.
	DesktopPlasma Desktop = "plasma"
)

// Desktops lists the choices in presentation order.
var Desktops = []Desktop{DesktopHyprland, DesktopXFCE, DesktopGNOME, DesktopPlasma}

// Description explains a desktop in terms of the trade-off it makes.
func (d Desktop) Description() string {
	switch d {
	case DesktopHyprland:
		return "Tiling Wayland compositor, configured after Omarchy"
	case DesktopXFCE:
		return "Traditional X11 desktop — the most forgiving fallback"
	case DesktopGNOME:
		return "Full GNOME — heavier; sluggish without GPU acceleration"
	case DesktopPlasma:
		return "Full KDE Plasma — feature-rich; heavier to render"
	default:
		return "Tiling Wayland compositor, configured after Omarchy"
	}
}

// packages returns the desktop's package list and the display manager and
// session name that go with it.
func (d Desktop) packages() (pkgs []string, displayManager, session string) {
	switch d {
	case DesktopHyprland:
		return hyprlandPackages, "sddm", "hyprland"
	case DesktopGNOME:
		return []string{"gnome", "gdm"}, "gdm", "gnome"
	case DesktopPlasma:
		return []string{"plasma-meta", "sddm", "konsole", "dolphin"}, "sddm", "plasma"
	default:
		return []string{
			"xfce4", "xfce4-goodies",
			"lightdm", "lightdm-gtk-greeter",
			"xterm",
		}, "lightdm", "xfce"
	}
}

// Profile describes the system to install.
type Profile struct {
	Hostname string
	Username string
	Password string

	Desktop   Desktop
	Autologin bool

	// ScalePercent is the desktop's UI scale. A VM framebuffer matches the
	// host's physical pixels, so on a high-density screen everything renders
	// at half the size it would on the host and is unreadable without this.
	ScalePercent int

	// GPUAccelerated says the guest's OpenGL reaches the host GPU. Visual
	// effects are disabled by default because software rendering makes them
	// painful; with real acceleration they cost little and are worth having.
	GPUAccelerated bool

	// VulkanAccelerated says the guest's *Vulkan* reaches the host GPU through
	// Venus. It pulls in the virtio Vulkan driver, without which Chromium —
	// which renders through ANGLE's Vulkan backend — falls back to software
	// even on a guest whose desktop is accelerated.
	VulkanAccelerated bool

	// FollowHostResize installs a helper that makes the guest adopt the host
	// window's size. It belongs only with a resizable window: when the window
	// is instead sized from the guest, the helper would undo any resolution
	// the user picks inside the guest.
	FollowHostResize bool

	Timezone string
	Locale   string
	Keymap   string

	// Disk is the guest block device to install onto. march always attaches
	// exactly one virtio disk, so this is /dev/vda.
	Disk string

	// ExtraPackages are appended to the installation.
	ExtraPackages []string
}

// DefaultProfile returns a sensible profile for a VM of the given name.
func DefaultProfile(hostname string) Profile {
	return Profile{
		Hostname:     hostname,
		Username:     "arch",
		Desktop:      DesktopHyprland,
		Autologin:    true,
		Timezone:     "UTC",
		Locale:       "en_US.UTF-8",
		Keymap:       "us",
		Disk:         "/dev/vda",
		ScalePercent: 200,
	}
}

// usernameRe follows the conservative POSIX rules useradd enforces.
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// reservedUsers are accounts a fresh Arch system already defines; reusing one
// would make useradd fail partway through the install.
var reservedUsers = map[string]bool{
	"root": true, "bin": true, "daemon": true, "mail": true, "ftp": true,
	"http": true, "nobody": true, "dbus": true, "systemd-network": true,
}

// Validate checks a profile before an install is attempted, so problems
// surface immediately rather than minutes into a run.
func (p *Profile) Validate() error {
	if p.Hostname == "" {
		return errors.New("hostname is required")
	}
	if !usernameRe.MatchString(p.Username) {
		return errors.New("username must start with a lowercase letter or underscore and contain only lowercase letters, digits, dash or underscore")
	}
	if reservedUsers[p.Username] {
		return fmt.Errorf("%q is a reserved system account", p.Username)
	}
	if len(p.Password) < 1 {
		return errors.New("a password is required")
	}
	// The password is delivered inside a shell script, so a newline would
	// break the script apart and a NUL cannot survive the transport.
	if strings.ContainsAny(p.Password, "\n\r\x00") {
		return errors.New("password cannot contain newlines")
	}
	if p.Disk == "" {
		return errors.New("target disk is required")
	}
	switch p.Desktop {
	case DesktopHyprland, DesktopXFCE, DesktopGNOME, DesktopPlasma:
	default:
		return fmt.Errorf("unknown desktop %q", p.Desktop)
	}
	if strings.ContainsAny(p.Hostname, " \t\n") {
		return errors.New("hostname cannot contain whitespace")
	}
	if p.ScalePercent != 0 && (p.ScalePercent < 100 || p.ScalePercent > 400) {
		return fmt.Errorf("display scale must be between 100%% and 400%%, got %d%%", p.ScalePercent)
	}
	return nil
}

// withDefaults fills in anything the caller left blank.
func (p Profile) withDefaults() Profile {
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if p.Locale == "" {
		p.Locale = "en_US.UTF-8"
	}
	if p.Keymap == "" {
		p.Keymap = "us"
	}
	if p.Disk == "" {
		p.Disk = "/dev/vda"
	}
	if p.Desktop == "" {
		p.Desktop = DesktopHyprland
	}
	if p.Username == "" {
		p.Username = "arch"
	}
	if p.ScalePercent == 0 {
		p.ScalePercent = 200
	}
	return p
}

// scaleFactor returns the integer UI scale, which is the only kind GTK and Qt
// apply cleanly. Anything that is not a whole multiple falls back to 1 and is
// handled through font DPI instead.
func (p Profile) scaleFactor() int {
	if p.ScalePercent >= 100 && p.ScalePercent%100 == 0 {
		return p.ScalePercent / 100
	}
	return 1
}
