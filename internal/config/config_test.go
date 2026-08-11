package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melvinsh/march/internal/host"
)

func testCaps() *host.Caps {
	return &host.Caps{
		QemuSystem: "/usr/bin/qemu-system-aarch64",
		QemuImg:    "/usr/bin/qemu-img",
		Firmware:   "/usr/share/qemu/edk2-aarch64-code.fd",
		Accels:     []string{"hvf", "tcg"},
		AIOModes:   []string{"threads"},
		Displays:   []string{"none", "cocoa"},
		Devices:    map[string]bool{"virtio-blk-pci": true, "virtio-balloon-pci": true},
		HostCPUs:   10,
		HostMemMiB: 32768,
		Arch:       "arm64",
		OS:         "darwin",
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"arch", "a", "arch-01", "arch_dev", "arch.test", strings.Repeat("a", 32)}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{
		"", "-arch", ".arch", "_x", "arch vm", "arch/../etc", "arch/sub",
		strings.Repeat("a", 33), "arch$", "../escape",
	}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
		}
	}
}

// A name that escapes the store directory would let a VM overwrite arbitrary
// files, so path separators must never validate.
func TestValidateNameRejectsPathTraversal(t *testing.T) {
	for _, n := range []string{"..", "../x", "a/b", `a\b`} {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) accepted a path-traversing name", n)
		}
	}
}

func TestVMValidate(t *testing.T) {
	base := Defaults("arch", testCaps())
	if err := base.Validate(); err != nil {
		t.Fatalf("defaults should validate, got %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VM)
		want   string
	}{
		{"zero cpus", func(v *VM) { v.CPUs = 0 }, "cpus"},
		{"too many cpus", func(v *VM) { v.CPUs = 999 }, "cpus"},
		{"tiny memory", func(v *VM) { v.MemoryMiB = 128 }, "memory"},
		{"tiny disk", func(v *VM) { v.DiskGiB = 1 }, "disk"},
		{"bad port", func(v *VM) { v.SSHPort = 80 }, "port"},
		{"bad mac", func(v *VM) { v.MAC = "not-a-mac" }, "MAC"},
		{"bad accel", func(v *VM) { v.Accel = "xen" }, "accelerator"},
		{"no cpu model", func(v *VM) { v.CPUModel = "" }, "cpu model"},
		{"no machine", func(v *VM) { v.Machine = "" }, "machine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.mutate(&v)
			err := v.Validate()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestDefaultsAccelerated(t *testing.T) {
	v := Defaults("arch", testCaps())

	if v.Accel != host.AccelHVF {
		t.Errorf("Accel = %q, want hvf", v.Accel)
	}
	if v.CPUModel != "host" {
		t.Errorf("CPUModel = %q, want host with acceleration", v.CPUModel)
	}
	// Half of ten cores, and a quarter of 32 GiB.
	if v.CPUs != 5 {
		t.Errorf("CPUs = %d, want 5 (half of 10)", v.CPUs)
	}
	if v.MemoryMiB != 8192 {
		t.Errorf("MemoryMiB = %d, want 8192 (a quarter of 32 GiB)", v.MemoryMiB)
	}
	if !v.Highmem {
		t.Error("Highmem should default on so guests can use more than 3 GiB")
	}
	if v.AIO != "threads" {
		t.Errorf("AIO = %q, want the only backend the host reported", v.AIO)
	}
}

// Under TCG every instruction is translated, so the defaults must be smaller
// and must not try to pass the host CPU through.
func TestDefaultsTCGIsConservative(t *testing.T) {
	caps := testCaps()
	caps.Accels = []string{"tcg"}

	v := Defaults("arch", caps)
	if v.Accel != host.AccelTCG {
		t.Fatalf("Accel = %q, want tcg", v.Accel)
	}
	if v.CPUModel != "max" {
		t.Errorf("CPUModel = %q, want max — 'host' is meaningless under TCG", v.CPUModel)
	}
	if v.CPUs > 4 {
		t.Errorf("CPUs = %d, want at most 4 under emulation", v.CPUs)
	}
	if v.MemoryMiB > 4096 {
		t.Errorf("MemoryMiB = %d, want at most 4096 under emulation", v.MemoryMiB)
	}
}

func TestDefaultsClampsSmallHost(t *testing.T) {
	caps := testCaps()
	caps.HostCPUs = 1
	caps.HostMemMiB = 2048

	v := Defaults("tiny", caps)
	if v.CPUs < 1 {
		t.Errorf("CPUs = %d, want at least 1", v.CPUs)
	}
	if v.MemoryMiB < 2048 {
		t.Errorf("MemoryMiB = %d, want the 2048 floor", v.MemoryMiB)
	}
	if err := v.Validate(); err != nil {
		t.Errorf("defaults for a small host should still validate: %v", err)
	}
}

func TestDefaultsNilCaps(t *testing.T) {
	v := Defaults("arch", nil)
	if err := v.Validate(); err != nil {
		t.Errorf("defaults without caps should validate: %v", err)
	}
}

func TestCheckAgainstHost(t *testing.T) {
	caps := testCaps()

	t.Run("clean spec has no warnings", func(t *testing.T) {
		v := Defaults("arch", caps)
		if w := v.CheckAgainstHost(caps); len(w) != 0 {
			t.Errorf("expected no warnings, got %v", w)
		}
	})

	t.Run("overcommitted cpus warn", func(t *testing.T) {
		v := Defaults("arch", caps)
		v.CPUs = 64
		if w := v.CheckAgainstHost(caps); len(w) == 0 {
			t.Error("expected a warning about exceeding host cores")
		}
	})

	t.Run("memory that starves the host warns", func(t *testing.T) {
		v := Defaults("arch", caps)
		v.MemoryMiB = caps.HostMemMiB
		if w := v.CheckAgainstHost(caps); len(w) == 0 {
			t.Error("expected a warning about leaving too little for the host")
		}
	})

	t.Run("highmem off with big RAM warns", func(t *testing.T) {
		v := Defaults("arch", caps)
		v.Highmem = false
		v.MemoryMiB = 8192
		found := false
		for _, w := range v.CheckAgainstHost(caps) {
			if strings.Contains(w, "highmem") {
				found = true
			}
		}
		if !found {
			t.Error("expected a warning that highmem=off caps usable RAM")
		}
	})

	t.Run("unavailable display warns", func(t *testing.T) {
		v := Defaults("arch", caps)
		v.Display = "gtk"
		found := false
		for _, w := range v.CheckAgainstHost(caps) {
			if strings.Contains(w, "gtk") {
				found = true
			}
		}
		if !found {
			t.Error("expected a warning about the unavailable display")
		}
	})
}

func TestRandomMAC(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		mac := RandomMAC()
		if _, err := net.ParseMAC(mac); err != nil {
			t.Fatalf("RandomMAC produced %q, which does not parse: %v", mac, err)
		}
		if !strings.HasPrefix(mac, "52:54:00:") {
			t.Errorf("MAC %q is outside QEMU's 52:54:00 OUI", mac)
		}
		seen[mac] = true
	}
	// 24 bits of randomness across 100 draws should essentially never collide
	// enough to fall below this bound.
	if len(seen) < 95 {
		t.Errorf("only %d unique MACs from 100 draws; randomness looks broken", len(seen))
	}
}

func TestFreePort(t *testing.T) {
	port, err := FreePort(0)
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if port < 1024 {
		t.Errorf("FreePort returned a privileged port %d", port)
	}

	// A port that is genuinely in use must not be handed out.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	busy := l.Addr().(*net.TCPAddr).Port

	got, err := FreePort(busy)
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}
	if got == busy {
		t.Errorf("FreePort returned the occupied port %d", busy)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	v := Defaults("arch", testCaps())
	v.SSHPort = 2222

	if s.Exists("arch") {
		t.Error("Exists reported a VM before it was saved")
	}
	if err := s.Save(v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !s.Exists("arch") {
		t.Error("Exists = false after Save")
	}

	got, err := s.Load("arch")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != v.Name || got.CPUs != v.CPUs || got.MemoryMiB != v.MemoryMiB ||
		got.SSHPort != v.SSHPort || got.MAC != v.MAC || got.Accel != v.Accel {
		t.Errorf("round trip changed the spec:\n got %+v\nwant %+v", got, v)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "arch" {
		t.Errorf("List = %v, want exactly [arch]", list)
	}

	if err := s.Delete("arch"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Exists("arch") {
		t.Error("VM still exists after Delete")
	}
}

func TestStoreSaveRejectsInvalid(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := Defaults("arch", testCaps())
	v.CPUs = 0
	if err := s.Save(v); err == nil {
		t.Error("Save accepted an invalid spec")
	}
}

func TestStoreListSorted(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if err := s.Save(Defaults(n, testCaps())); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range list {
		names = append(names, v.Name)
	}
	want := []string{"alpha", "mid", "zeta"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("List order = %v, want %v", names, want)
	}
}

// One unreadable VM must not make the whole list unusable.
func TestStoreListSkipsCorrupt(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Defaults("good", testCaps())); err != nil {
		t.Fatal(err)
	}

	bad := s.VMDir("broken")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "vm.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List should tolerate a corrupt VM, got %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("List = %v, want just the readable VM", list)
	}
}

func TestStoreDeleteMissing(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("nope"); err == nil {
		t.Error("Delete of a missing VM should error")
	}
	if err := s.Delete("../escape"); err == nil {
		t.Error("Delete should reject a traversing name")
	}
}

func TestStorePaths(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := s.Paths("arch")

	if filepath.Base(p.Disk) != "disk.qcow2" {
		t.Errorf("Disk = %q", p.Disk)
	}
	if filepath.Base(p.EFIVars) != "efi-vars.fd" {
		t.Errorf("EFIVars = %q", p.EFIVars)
	}
	if !strings.HasPrefix(p.Disk, p.Dir) {
		t.Errorf("Disk %q is outside the VM dir %q", p.Disk, p.Dir)
	}
	if len(p.QMPSocket) > maxSockPath {
		t.Errorf("QMP socket path is %d bytes, over the %d limit", len(p.QMPSocket), maxSockPath)
	}
}

// A deeply nested store must still yield bindable socket paths.
func TestStorePathsRelocatesLongSockets(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("nested-directory/", 8))
	s := &Store{Root: deep}

	p := s.Paths("a-machine-with-a-long-name")
	if len(p.QMPSocket) > maxSockPath {
		t.Errorf("QMP socket path is %d bytes (%q), over the %d limit",
			len(p.QMPSocket), p.QMPSocket, maxSockPath)
	}
	if len(p.SerialSocket) > maxSockPath {
		t.Errorf("serial socket path is %d bytes, over the %d limit", len(p.SerialSocket), maxSockPath)
	}
	// The disk is a normal file and stays with the VM.
	if !strings.HasPrefix(p.Disk, deep) {
		t.Errorf("Disk %q should stay under the store root", p.Disk)
	}
}

func TestDefaultRootHonoursEnv(t *testing.T) {
	t.Setenv("MARCH_HOME", "/tmp/march-test-home")
	if got := DefaultRoot(); got != "/tmp/march-test-home" {
		t.Errorf("DefaultRoot = %q, want the MARCH_HOME override", got)
	}

	t.Setenv("MARCH_HOME", "")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg")
	if got := DefaultRoot(); got != "/tmp/xdg/march" {
		t.Errorf("DefaultRoot = %q, want it under XDG_DATA_HOME", got)
	}
}

// The VM window should open large enough to be useful without covering the
// whole desktop.
func TestDefaultResolution(t *testing.T) {
	caps := testCaps()
	caps.ScreenWidth, caps.ScreenHeight = 1728, 1117

	w, h := DefaultResolution(caps, false)
	if w >= caps.ScreenWidth || h >= caps.ScreenHeight {
		t.Errorf("resolution %dx%d is not smaller than the %dx%d screen",
			w, h, caps.ScreenWidth, caps.ScreenHeight)
	}
	// It should still fill most of it, not sit in a corner.
	if w < caps.ScreenWidth*3/4 || h < caps.ScreenHeight*3/4 {
		t.Errorf("resolution %dx%d is too small for a %dx%d screen",
			w, h, caps.ScreenWidth, caps.ScreenHeight)
	}
	if w%8 != 0 || h%8 != 0 {
		t.Errorf("resolution %dx%d is not on an 8-pixel boundary", w, h)
	}
}

func TestDefaultResolutionBounds(t *testing.T) {
	tests := []struct {
		name            string
		screenW         int
		screenH         int
		wantMin, wantMx bool
	}{
		{"unknown screen", 0, 0, false, false},
		{"tiny screen", 800, 600, true, false},
		{"huge screen", 6016, 3384, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := testCaps()
			caps.ScreenWidth, caps.ScreenHeight = tc.screenW, tc.screenH
			w, h := DefaultResolution(caps, false)

			if w < minDisplayWidth || h < minDisplayHeight {
				t.Errorf("resolution %dx%d is below the usable minimum", w, h)
			}
			if w > maxDisplayWidth || h > maxDisplayHeight {
				t.Errorf("resolution %dx%d exceeds the maximum", w, h)
			}
		})
	}

	// Without caps at all it must still produce something usable.
	w, h := DefaultResolution(nil, false)
	if w < minDisplayWidth || h < minDisplayHeight {
		t.Errorf("fallback resolution %dx%d is too small", w, h)
	}
}

// A fullscreen machine must fill the screen exactly, or the desktop is
// letterboxed inside its own window.
func TestDefaultResolutionFullscreenFillsScreen(t *testing.T) {
	caps := testCaps()
	caps.ScreenWidth, caps.ScreenHeight = 3456, 2234

	w, h := DefaultResolution(caps, true)
	if w != caps.ScreenWidth || h != caps.ScreenHeight {
		t.Errorf("fullscreen resolution = %dx%d, want the whole %dx%d screen",
			w, h, caps.ScreenWidth, caps.ScreenHeight)
	}

	// A windowed machine deliberately leaves a margin.
	ww, wh := DefaultResolution(caps, false)
	if ww >= w || wh >= h {
		t.Errorf("windowed resolution %dx%d is not smaller than fullscreen %dx%d", ww, wh, w, h)
	}
}

func TestDefaultsCarryResolution(t *testing.T) {
	caps := testCaps()
	caps.ScreenWidth, caps.ScreenHeight = 3456, 2234

	v := Defaults("arch", caps)
	if v.DisplayWidth == 0 || v.DisplayHeight == 0 {
		t.Fatal("defaults left the display size unset")
	}
	if !v.Fullscreen {
		t.Error("machines should default to opening fullscreen")
	}
	// Fullscreen and resolution must agree, or the desktop is letterboxed.
	if v.DisplayWidth != caps.ScreenWidth || v.DisplayHeight != caps.ScreenHeight {
		t.Errorf("fullscreen default is %dx%d but the screen is %dx%d",
			v.DisplayWidth, v.DisplayHeight, caps.ScreenWidth, caps.ScreenHeight)
	}
	if err := v.Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}

	// A half-specified size is a bug rather than a default.
	v.DisplayHeight = 0
	if err := v.Validate(); err == nil {
		t.Error("a width without a height should be rejected")
	}
}
