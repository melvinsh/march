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

// The desktop march installs. There is exactly one: a tiling Wayland
// compositor configured after Omarchy, on SDDM. march used to offer XFCE,
// GNOME and Plasma as well, which meant every path — scaling, autologin,
// window resizing, the whole toolset — existed in two or three variants that
// nothing verified equally. The desktop is the product, so it is no longer a
// choice.
const (
	displayManager = "sddm"
	sessionName    = "hyprland"
)

// Profile describes the system to install.
type Profile struct {
	Hostname string
	Username string
	Password string

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
