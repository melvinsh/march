package ui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/install"
)

func TestCPUChoices(t *testing.T) {
	caps := testCaps() // 10 cores
	opts := cpuChoices(caps, 5)

	if len(opts) == 0 {
		t.Fatal("no vCPU choices offered")
	}
	for _, o := range opts {
		n, err := strconv.Atoi(o.Value)
		if err != nil {
			t.Errorf("option value %q is not a number", o.Value)
			continue
		}
		if n > caps.HostCPUs {
			t.Errorf("offered %d vCPUs on a %d-core host", n, caps.HostCPUs)
		}
	}

	// The recommended default must always be selectable, even when it is not
	// on the fixed ladder.
	found := false
	for _, o := range opts {
		if o.Value == "5" && strings.Contains(o.Key, "recommended") {
			found = true
		}
	}
	if !found {
		t.Errorf("the recommended value 5 is missing from %v", optionKeys(opts))
	}
}

func TestCPUChoicesSmallHost(t *testing.T) {
	caps := testCaps()
	caps.HostCPUs = 2

	opts := cpuChoices(caps, 1)
	if len(opts) == 0 {
		t.Fatal("a two-core host should still offer choices")
	}
	for _, o := range opts {
		n, _ := strconv.Atoi(o.Value)
		if n > 2 {
			t.Errorf("offered %d vCPUs on a 2-core host", n)
		}
	}
}

func TestMemChoicesLeaveRoomForHost(t *testing.T) {
	caps := testCaps() // 32 GiB
	opts := memChoices(caps, 8192)

	if len(opts) == 0 {
		t.Fatal("no memory choices offered")
	}
	for _, o := range opts {
		n, err := strconv.Atoi(o.Value)
		if err != nil {
			t.Errorf("option value %q is not a number", o.Value)
			continue
		}
		if n > caps.HostMemMiB-2048 {
			t.Errorf("offered %d MiB, which does not leave 2 GiB for the host", n)
		}
	}
}

func TestMemChoicesTinyHost(t *testing.T) {
	caps := testCaps()
	caps.HostMemMiB = 4096

	opts := memChoices(caps, 2048)
	if len(opts) == 0 {
		t.Fatal("a 4 GiB host should still offer at least one size")
	}
	for _, o := range opts {
		n, _ := strconv.Atoi(o.Value)
		if n > 2048 {
			t.Errorf("offered %d MiB on a 4 GiB host", n)
		}
	}
}

func TestDisplayChoices(t *testing.T) {
	opts := displayChoices(testCaps())
	if len(opts) < 2 {
		t.Fatalf("expected headless plus a window backend, got %v", optionKeys(opts))
	}
	if opts[0].Value != config.DisplayNone {
		t.Errorf("first option is %q, want headless as the default", opts[0].Value)
	}

	// A build with only the null display must offer just that.
	bare := testCaps()
	bare.Displays = []string{"none"}
	opts = displayChoices(bare)
	if len(opts) != 1 || opts[0].Value != config.DisplayNone {
		t.Errorf("a display-less build offered %v", optionKeys(opts))
	}
}

func TestDesktopChoices(t *testing.T) {
	opts := desktopChoices()
	if len(opts) != len(install.Desktops) {
		t.Fatalf("got %d desktops, want one per supported environment", len(opts))
	}
	// Hyprland leads and carries the marker: it is march's default desktop.
	if opts[0].Value != string(install.DesktopHyprland) {
		t.Errorf("first option is %q, want Hyprland", opts[0].Value)
	}
	if !strings.Contains(opts[0].Key, "recommended") {
		t.Errorf("XFCE option %q is not marked recommended", opts[0].Key)
	}
	for _, o := range opts {
		if !strings.Contains(o.Key, "—") {
			t.Errorf("option %q carries no explanation", o.Key)
		}
	}
}

func TestCreateModelSpec(t *testing.T) {
	caps := testCaps()
	m := newCreateModel(caps, nil)

	m.name = "myvm"
	m.cpus = "4"
	m.memory = "4096"
	m.disk = "50"

	spec := m.Spec()
	if spec.Name != "myvm" {
		t.Errorf("Name = %q", spec.Name)
	}
	if spec.CPUs != 4 {
		t.Errorf("CPUs = %d, want 4", spec.CPUs)
	}
	if spec.MemoryMiB != 4096 {
		t.Errorf("MemoryMiB = %d, want 4096", spec.MemoryMiB)
	}
	if spec.DiskGiB != 50 {
		t.Errorf("DiskGiB = %d, want 50", spec.DiskGiB)
	}
	// The product exists to land on a desktop, so a created VM is graphical.
	if !spec.GPU {
		t.Error("an installed desktop VM needs a GPU")
	}
	if spec.Display == config.DisplayNone {
		t.Error("a desktop VM should get a window, not a headless console")
	}
	if spec.Accel != host.AccelHVF {
		t.Errorf("Accel = %q, want the host's best accelerator", spec.Accel)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("the wizard produced an invalid spec: %v", err)
	}
}

// Malformed values fall back to the tuned defaults rather than producing a
// zeroed, invalid spec.
func TestCreateModelSpecToleratesJunk(t *testing.T) {
	caps := testCaps()
	m := newCreateModel(caps, nil)
	m.name = "vm"
	m.cpus = "not a number"
	m.memory = ""
	m.disk = "abc"

	spec := m.Spec()
	def := config.Defaults("vm", caps)
	if spec.CPUs != def.CPUs {
		t.Errorf("CPUs = %d, want the default %d", spec.CPUs, def.CPUs)
	}
	if spec.MemoryMiB != def.MemoryMiB {
		t.Errorf("MemoryMiB = %d, want the default %d", spec.MemoryMiB, def.MemoryMiB)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("spec should still validate: %v", err)
	}
}

func TestCreateModelInstallProfile(t *testing.T) {
	m := newCreateModel(testCaps(), nil)
	m.name = "box"
	m.username = "melvin"
	m.password = "hunter2"
	m.desktop = string(install.DesktopPlasma)

	p := m.InstallProfile()
	if p.Hostname != "box" {
		t.Errorf("Hostname = %q", p.Hostname)
	}
	if p.Username != "melvin" || p.Password != "hunter2" {
		t.Errorf("account = %q/%q", p.Username, p.Password)
	}
	if p.Desktop != install.DesktopPlasma {
		t.Errorf("Desktop = %q", p.Desktop)
	}
	if !p.Autologin {
		t.Error("autologin should be on so the VM lands on a desktop unattended")
	}
	if err := p.Validate(); err != nil {
		t.Errorf("the wizard produced an invalid profile: %v", err)
	}
}

// The password must never reach disk; only the username and desktop do.
func TestSpecOmitsPassword(t *testing.T) {
	m := newCreateModel(testCaps(), nil)
	m.name = "box"
	m.username = "melvin"
	m.password = "sup3rsecret"

	spec := m.Spec()
	blob, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "sup3rsecret") {
		t.Errorf("the persisted VM spec contains the password: %s", blob)
	}
	if spec.Username != "melvin" {
		t.Errorf("Username = %q, want it recorded", spec.Username)
	}
}

func TestSuggestName(t *testing.T) {
	if got := suggestName(nil); got != "arch" {
		t.Errorf("suggestName(nil) = %q, want arch", got)
	}
	if got := suggestName([]string{"arch"}); got != "arch2" {
		t.Errorf("suggestName = %q, want arch2", got)
	}
	if got := suggestName([]string{"arch", "arch2", "arch3"}); got != "arch4" {
		t.Errorf("suggestName = %q, want arch4", got)
	}

	// Whatever it suggests must be a legal VM name.
	for _, existing := range [][]string{nil, {"arch"}, {"arch", "arch2"}} {
		if err := config.ValidateName(suggestName(existing)); err != nil {
			t.Errorf("suggestName produced an invalid name: %v", err)
		}
	}
}

func TestShortenISO(t *testing.T) {
	const long = "archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-latest-aarch64.iso"
	got := shortenISO(long)

	if strings.HasSuffix(got, ".iso") {
		t.Errorf("shortenISO kept the extension: %q", got)
	}
	if len(got) >= len(long) {
		t.Errorf("shortenISO(%q) = %q, which is no shorter", long, got)
	}
	if !strings.Contains(got, "2026.08.11") {
		t.Errorf("shortenISO dropped the date: %q", got)
	}
	if strings.Contains(got, "latest") == false {
		t.Errorf("shortenISO dropped the variant: %q", got)
	}
}

func optionKeys[T comparable](opts []huh.Option[T]) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Key)
	}
	return out
}
