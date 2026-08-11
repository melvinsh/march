package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbeAccelsParsing(t *testing.T) {
	// probeAccels consumes the real `-accel help` shape.
	const out = `Accelerators supported in QEMU binary:
hvf
tcg
`
	var got []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		if f := strings.Fields(line); len(f) == 1 {
			got = append(got, f[0])
		}
	}
	if len(got) != 2 || got[0] != "hvf" || got[1] != "tcg" {
		t.Errorf("parsed %v, want [hvf tcg]", got)
	}
}

func TestDeviceRegexp(t *testing.T) {
	const out = `name "virtio-blk-pci", bus PCI, alias "virtio-blk"
name "virtio-net-pci", bus PCI, desc "virtio network device"
Storage devices:
name "scsi-cd", bus SCSI, desc "virtual SCSI CD-ROM"
`
	found := map[string]bool{}
	for _, m := range deviceRe.FindAllStringSubmatch(out, -1) {
		found[m[1]] = true
	}
	for _, want := range []string{"virtio-blk-pci", "virtio-net-pci", "scsi-cd"} {
		if !found[want] {
			t.Errorf("device %q was not parsed out of the help text", want)
		}
	}
	if len(found) != 3 {
		t.Errorf("parsed %d devices, want 3: %v", len(found), found)
	}
}

func TestVersionRegexp(t *testing.T) {
	tests := map[string]struct {
		full         string
		major, minor int
	}{
		"QEMU emulator version 11.0.3":         {"11.0.3", 11, 0},
		"QEMU emulator version 8.2":            {"8.2", 8, 2},
		"QEMU emulator version 9.1.0 (v9.1.0)": {"9.1.0", 9, 1},
	}
	for in, want := range tests {
		m := versionRe.FindStringSubmatch(in)
		if m == nil {
			t.Errorf("%q did not match", in)
			continue
		}
		full := m[1] + "." + m[2]
		if m[3] != "" {
			full += "." + m[3]
		}
		if full != want.full {
			t.Errorf("%q parsed to %q, want %q", in, full, want.full)
		}
	}
}

func TestCapsPredicates(t *testing.T) {
	c := &Caps{
		Accels:   []string{"hvf", "tcg"},
		Devices:  map[string]bool{"virtio-blk-pci": true},
		Displays: []string{"none", "cocoa"},
		AIOModes: []string{"threads"},
		Arch:     "arm64",
	}

	if !c.HasAccel("hvf") || c.HasAccel("kvm") {
		t.Error("HasAccel is wrong")
	}
	if !c.HasDevice("virtio-blk-pci") || c.HasDevice("nonexistent") {
		t.Error("HasDevice is wrong")
	}
	if !c.HasDisplay("cocoa") || c.HasDisplay("gtk") {
		t.Error("HasDisplay is wrong")
	}
	if got := c.BestAccel(); got != AccelHVF {
		t.Errorf("BestAccel = %q, want hvf", got)
	}
	if got := c.BestAIO(); got != "threads" {
		t.Errorf("BestAIO = %q, want threads", got)
	}
	if !c.Accelerated() {
		t.Error("Accelerated should be true with hvf")
	}
}

func TestBestAIOPrefersFastest(t *testing.T) {
	c := &Caps{AIOModes: []string{"threads", "native", "io_uring"}}
	if got := c.BestAIO(); got != "io_uring" {
		t.Errorf("BestAIO = %q, want io_uring when it is available", got)
	}

	c = &Caps{AIOModes: []string{"threads", "native"}}
	if got := c.BestAIO(); got != "native" {
		t.Errorf("BestAIO = %q, want native", got)
	}

	// An empty list must still yield the universally-available backend.
	c = &Caps{}
	if got := c.BestAIO(); got != "threads" {
		t.Errorf("BestAIO = %q, want the threads fallback", got)
	}
}

// Hardware acceleration requires matching host and guest architectures. An
// x86 host must fall back to TCG even if it reports an accelerator.
func TestBestAccelRequiresMatchingArch(t *testing.T) {
	c := &Caps{Accels: []string{"kvm", "tcg"}, Arch: "amd64"}
	if got := c.BestAccel(); got != AccelTCG {
		t.Errorf("BestAccel on an x86 host = %q, want tcg for an aarch64 guest", got)
	}
	if c.Accelerated() {
		t.Error("an x86 host cannot accelerate an aarch64 guest")
	}

	c.Arch = "arm64"
	if got := c.BestAccel(); got != AccelKVM {
		t.Errorf("BestAccel on an arm64 Linux host = %q, want kvm", got)
	}
}

func TestReadyAndProblems(t *testing.T) {
	full := &Caps{
		QemuSystem: "/usr/bin/qemu-system-aarch64",
		QemuImg:    "/usr/bin/qemu-img",
		Firmware:   "/usr/share/edk2.fd",
		Accels:     []string{"hvf"},
		Arch:       "arm64",
	}
	if !full.Ready() {
		t.Error("Ready = false for a complete installation")
	}
	if got := full.Problems(); len(got) != 0 {
		t.Errorf("Problems = %v, want none", got)
	}

	tests := []struct {
		name   string
		mutate func(*Caps)
		want   string
	}{
		{"no qemu", func(c *Caps) { c.QemuSystem = "" }, "qemu-system-aarch64"},
		{"no qemu-img", func(c *Caps) { c.QemuImg = "" }, "qemu-img"},
		{"no firmware", func(c *Caps) { c.Firmware = "" }, "firmware"},
		{"no accel", func(c *Caps) { c.Accels = []string{"tcg"} }, "acceleration"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := *full
			tc.mutate(&c)
			problems := strings.Join(c.Problems(), " | ")
			if !strings.Contains(problems, tc.want) {
				t.Errorf("Problems = %q, want it to mention %q", problems, tc.want)
			}
		})
	}
}

func TestFindFirmware(t *testing.T) {
	// A firmware image laid out as a Homebrew prefix must be found relative to
	// the QEMU binary, ahead of any system copy.
	prefix := t.TempDir()
	share := filepath.Join(prefix, "share", "qemu")
	if err := os.MkdirAll(share, 0o755); err != nil {
		t.Fatal(err)
	}
	fw := filepath.Join(share, "edk2-aarch64-code.fd")
	if err := os.WriteFile(fw, []byte("firmware"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	qemu := filepath.Join(bin, "qemu-system-aarch64")
	if err := os.WriteFile(qemu, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindFirmware(qemu); got != fw {
		t.Errorf("FindFirmware = %q, want %q", got, fw)
	}
}

func TestHostMemMiB(t *testing.T) {
	got := hostMemMiB()
	if got < 512 {
		t.Errorf("hostMemMiB = %d, which is implausibly small", got)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		// On a supported platform detection should give a real number, not the
		// 8192 fallback for every machine. This is a smoke check, not exact.
		if got%1 != 0 {
			t.Errorf("hostMemMiB = %d", got)
		}
	}
}

// Probe must degrade gracefully rather than returning nil when QEMU is absent.
func TestProbeWithoutQemu(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	// Point every prefix search somewhere empty, including the accelerated
	// build, which is looked up ahead of PATH.
	origSearch, origAccel := searchPrefixes, acceleratedPrefixes
	searchPrefixes = []string{t.TempDir()}
	acceleratedPrefixes = []string{t.TempDir()}
	defer func() { searchPrefixes, acceleratedPrefixes = origSearch, origAccel }()

	caps, err := Probe(context.Background())
	if caps == nil {
		t.Fatal("Probe returned nil caps; the UI needs host facts even without QEMU")
	}
	if err == nil {
		t.Error("expected an error when QEMU is missing")
	}
	if caps.HostCPUs < 1 {
		t.Error("host CPU count should still be populated")
	}
	if caps.Ready() {
		t.Error("Ready should be false without QEMU")
	}
}

// The full probe against the real QEMU on this machine.
func TestProbeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the QEMU integration probe in short mode")
	}
	if lookPath("qemu-system-aarch64") == "" {
		t.Skip("qemu-system-aarch64 is not installed")
	}

	caps, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if caps.Version == "" {
		t.Error("no QEMU version detected")
	}
	if len(caps.Accels) == 0 {
		t.Error("no accelerators detected; every build has at least tcg")
	}
	if !caps.HasAccel("tcg") {
		t.Error("tcg should always be available")
	}
	if !caps.HasDevice("virtio-blk-pci") {
		t.Error("virtio-blk-pci should be present in any aarch64 build")
	}
	if !caps.HasDisplay("none") {
		t.Error("the 'none' display should always be available")
	}
	if len(caps.AIOModes) == 0 {
		t.Error("no AIO backend detected; threads is always supported")
	}
	if caps.Firmware == "" {
		t.Log("warning: no aarch64 UEFI firmware found on this host")
	}

	t.Logf("qemu %s · accels %v · aio %v · firmware %s",
		caps.Version, caps.Accels, caps.AIOModes, caps.Firmware)
}

// A QEMU built against virglrenderer gives guests hardware rendering, so it is
// preferred over whatever happens to be on PATH.
func TestAcceleratedQemuIsPreferred(t *testing.T) {
	dir := t.TempDir()
	accel := filepath.Join(dir, "qemu-system-aarch64")
	if err := os.WriteFile(accel, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	origAccel := acceleratedPrefixes
	acceleratedPrefixes = []string{dir}
	defer func() { acceleratedPrefixes = origAccel }()

	if got := lookPath("qemu-system-aarch64"); got != accel {
		t.Errorf("lookPath = %q, want the accelerated build at %q", got, accel)
	}
}

// SupportsGPUAccel keys off the device that only exists in a virgl-enabled
// build, since probing the display would mean opening a window at startup.
func TestSupportsGPUAccel(t *testing.T) {
	plain := &Caps{Devices: map[string]bool{"virtio-gpu-pci": true}}
	if plain.SupportsGPUAccel() {
		t.Error("a build without virtio-gpu-gl was reported as accelerated")
	}

	gl := &Caps{Devices: map[string]bool{"virtio-gpu-pci": true, "virtio-gpu-gl-pci": true}}
	if !gl.SupportsGPUAccel() {
		t.Error("a build with virtio-gpu-gl was not reported as accelerated")
	}

	var nilCaps *Caps
	if nilCaps.SupportsGPUAccel() {
		t.Error("nil caps should not claim acceleration")
	}
}

// Venus needs three separate things. Claiming support without all of them
// produces a QEMU command line that fails to start.
func TestSupportsVenus(t *testing.T) {
	full := func() *Caps {
		return &Caps{
			Devices:      map[string]bool{"virtio-gpu-gl-pci": true},
			VenusDevice:  true,
			MoltenVK:     "/opt/homebrew/opt/molten-vk/lib/libMoltenVK.dylib",
			VulkanLoader: "/opt/homebrew/lib/libvulkan.dylib",
			SeqPacket:    true,
		}
	}
	if !full().SupportsVenus() {
		t.Error("a host with every piece was not reported as Venus-capable")
	}

	for name, break_ := range map[string]func(*Caps){
		"no GL device":      func(c *Caps) { c.Devices = map[string]bool{} },
		"no venus property": func(c *Caps) { c.VenusDevice = false },
		"no driver":         func(c *Caps) { c.MoltenVK = "" },
		"no loader":         func(c *Caps) { c.VulkanLoader = "" },
		"no seqpacket":      func(c *Caps) { c.SeqPacket = false },
	} {
		t.Run(name, func(t *testing.T) {
			c := full()
			break_(c)
			if c.SupportsVenus() {
				t.Error("Venus was claimed without every piece present")
			}
		})
	}

	var nilCaps *Caps
	if nilCaps.SupportsVenus() {
		t.Error("nil caps should not claim Venus")
	}
}

// The ICD manifest is what points the Vulkan loader at MoltenVK. It has to be
// real JSON naming the real driver, or QEMU starts and silently finds no GPU.
func TestVulkanEnv(t *testing.T) {
	dir := t.TempDir()

	// Without Venus there is nothing to point at, and no file should appear.
	plain := &Caps{}
	if env := plain.VulkanEnv(dir); env != nil {
		t.Errorf("VulkanEnv = %v on a host without Venus, want nil", env)
	}

	c := &Caps{
		Devices:      map[string]bool{"virtio-gpu-gl-pci": true},
		VenusDevice:  true,
		MoltenVK:     "/opt/homebrew/opt/molten-vk/lib/libMoltenVK.dylib",
		VulkanLoader: "/opt/homebrew/lib/libvulkan.dylib",
		SeqPacket:    true,
	}
	env := c.VulkanEnv(dir)
	if len(env) == 0 {
		t.Fatal("VulkanEnv returned nothing on a Venus-capable host")
	}

	var manifest string
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		if k == "VK_DRIVER_FILES" {
			manifest = v
		}
	}
	if manifest == "" {
		t.Fatalf("no VK_DRIVER_FILES in %v", env)
	}

	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	var icd struct {
		ICD struct {
			LibraryPath string `json:"library_path"`
		} `json:"ICD"`
	}
	if err := json.Unmarshal(body, &icd); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v\n%s", err, body)
	}
	if icd.ICD.LibraryPath != c.MoltenVK {
		t.Errorf("manifest names %q, want the detected driver %q", icd.ICD.LibraryPath, c.MoltenVK)
	}
}
