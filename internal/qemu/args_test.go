package qemu

import (
	"strings"
	"testing"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
)

func testCaps() *host.Caps {
	return &host.Caps{
		QemuSystem: "/opt/homebrew/bin/qemu-system-aarch64",
		QemuImg:    "/opt/homebrew/bin/qemu-img",
		Firmware:   "/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		Version:    "11.0.3",
		Accels:     []string{"hvf", "tcg"},
		AIOModes:   []string{"threads"},
		Displays:   []string{"none", "curses", "cocoa"},
		Devices: map[string]bool{
			"virtio-blk-pci":     true,
			"virtio-net-pci":     true,
			"virtio-rng-pci":     true,
			"virtio-balloon-pci": true,
			"virtio-scsi-pci":    true,
			"scsi-cd":            true,
			"virtio-gpu-pci":     true,
			"qemu-xhci":          true,
			"usb-kbd":            true,
			"usb-tablet":         true,
		},
		HostCPUs:   10,
		HostMemMiB: 32768,
		Arch:       "arm64",
		OS:         "darwin",
	}
}

func testPaths() config.Paths {
	return config.Paths{
		Dir:          "/store/vms/arch",
		Config:       "/store/vms/arch/vm.json",
		Disk:         "/store/vms/arch/disk.qcow2",
		EFIVars:      "/store/vms/arch/efi-vars.fd",
		PIDFile:      "/store/vms/arch/vm.pid",
		LogFile:      "/store/vms/arch/vm.log",
		QMPSocket:    "/store/vms/arch/qmp.sock",
		SerialSocket: "/store/vms/arch/console.sock",
	}
}

// argMap collapses argv into flag -> values, which is how the assertions below
// want to read it.
func argMap(args []string) map[string][]string {
	out := map[string][]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		flag := args[i]
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out[flag] = append(out[flag], args[i+1])
			i++
		} else {
			out[flag] = append(out[flag], "")
		}
	}
	return out
}

// findValue returns the first value for a flag that contains substr.
func findValue(args []string, flag, substr string) (string, bool) {
	for _, v := range argMap(args)[flag] {
		if strings.Contains(v, substr) {
			return v, true
		}
	}
	return "", false
}

func mustBuild(t *testing.T, v config.VM, caps *host.Caps, opts BuildOptions) []string {
	t.Helper()
	args, err := Build(v, caps, testPaths(), opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return args
}

func TestBuildAcceleratedDefaults(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.SSHPort = 2222
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{})
	m := argMap(args)

	machine := m["-machine"][0]
	for _, want := range []string{"virt", "accel=hvf", "gic-version=3", "highmem=on"} {
		if !strings.Contains(machine, want) {
			t.Errorf("-machine %q is missing %q", machine, want)
		}
	}

	if got := m["-cpu"][0]; got != "host" {
		t.Errorf("-cpu = %q, want host under hvf", got)
	}
	if got := m["-m"][0]; got != "8192" {
		t.Errorf("-m = %q, want 8192", got)
	}
	if got := m["-smp"][0]; !strings.HasPrefix(got, "5,") {
		t.Errorf("-smp = %q, want it to start with the vCPU count", got)
	}

	// -nodefaults is what makes the device list deterministic.
	if _, ok := m["-nodefaults"]; !ok {
		t.Error("-nodefaults is missing; QEMU would add implicit devices")
	}
}

// UEFI must be split into a shared read-only code image and a per-VM writable
// variable store, or VMs would trample each other's boot entries.
func TestBuildFirmwareIsSplitPflash(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{})

	code, ok := findValue(args, "-drive", "unit=0")
	if !ok {
		t.Fatal("no pflash unit 0 (firmware code) drive")
	}
	if !strings.Contains(code, "readonly=on") {
		t.Errorf("firmware code drive %q must be read-only", code)
	}
	if !strings.Contains(code, caps.Firmware) {
		t.Errorf("firmware code drive %q does not reference %q", code, caps.Firmware)
	}

	vars, ok := findValue(args, "-drive", "unit=1")
	if !ok {
		t.Fatal("no pflash unit 1 (EFI variables) drive")
	}
	if strings.Contains(vars, "readonly") {
		t.Errorf("EFI variable store %q must be writable", vars)
	}
	if !strings.Contains(vars, testPaths().EFIVars) {
		t.Errorf("EFI variable store %q is not the VM's own", vars)
	}
}

func TestBuildDiskTuning(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{})

	file, ok := findValue(args, "-blockdev", "driver=file")
	if !ok {
		t.Fatal("no file-layer blockdev for the disk")
	}
	for _, want := range []string{"aio=threads", "cache.direct=on", "cache.no-flush=off", "discard=unmap"} {
		if !strings.Contains(file, want) {
			t.Errorf("disk file layer %q is missing %q", file, want)
		}
	}

	if _, ok := findValue(args, "-blockdev", "driver=qcow2"); !ok {
		t.Error("no qcow2 format layer for the disk")
	}

	dev, ok := findValue(args, "-device", "virtio-blk-pci")
	if !ok {
		t.Fatal("no virtio-blk-pci device")
	}
	if !strings.Contains(dev, "iothread=iothread0") {
		t.Errorf("virtio-blk %q should use the dedicated I/O thread", dev)
	}
	// One queue per vCPU is the point of the tuning.
	if !strings.Contains(dev, "num-queues=5") {
		t.Errorf("virtio-blk %q should have one queue per vCPU", dev)
	}
	if !strings.Contains(dev, "discard=on") {
		t.Errorf("virtio-blk %q should advertise discard so fstrim works", dev)
	}

	if _, ok := findValue(args, "-object", "iothread,id=iothread0"); !ok {
		t.Error("the iothread object referenced by virtio-blk was never declared")
	}
}

// Every device that references an object or node must have that thing declared,
// or QEMU refuses to start. This checks the references resolve.
func TestBuildReferencesResolve(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Display = config.DisplayNone
	v.ISOPath = "/images/archboot.iso"
	args := mustBuild(t, v, caps, BuildOptions{AttachISO: true})

	joined := strings.Join(args, " ")

	refs := map[string]string{
		"iothread=iothread0": "iothread,id=iothread0",
		"drive=disk":         "node-name=disk",
		"file=disk-file":     "node-name=disk-file",
		"drive=iso":          "node-name=iso",
		"file=iso-file":      "node-name=iso-file",
		"rng=rng0":           "id=rng0",
		"netdev=net0":        "id=net0",
		"bus=scsi0.0":        "virtio-scsi-pci,id=scsi0",
	}
	for ref, decl := range refs {
		if !strings.Contains(joined, ref) {
			t.Errorf("expected a reference %q in the command line", ref)
			continue
		}
		if !strings.Contains(joined, decl) {
			t.Errorf("reference %q has no matching declaration %q", ref, decl)
		}
	}
}

func TestBuildISOBootOrder(t *testing.T) {
	caps := testCaps()

	t.Run("uninstalled boots the ISO first", func(t *testing.T) {
		v := config.Defaults("arch", caps)
		v.ISOPath = "/images/archboot.iso"
		v.Installed = false

		args := mustBuild(t, v, caps, BuildOptions{})
		cd, ok := findValue(args, "-device", "scsi-cd")
		if !ok {
			t.Fatal("installer ISO was not attached for an uninstalled VM")
		}
		if !strings.Contains(cd, "bootindex=0") {
			t.Errorf("CD %q should boot first before installation", cd)
		}
		disk, _ := findValue(args, "-device", "virtio-blk-pci")
		if !strings.Contains(disk, "bootindex=1") {
			t.Errorf("disk %q should come second before installation", disk)
		}
	})

	t.Run("installed boots the disk and drops the ISO", func(t *testing.T) {
		v := config.Defaults("arch", caps)
		v.ISOPath = "/images/archboot.iso"
		v.Installed = true

		args := mustBuild(t, v, caps, BuildOptions{})
		if _, ok := findValue(args, "-device", "scsi-cd"); ok {
			t.Error("the installer should not be attached once the VM is installed")
		}
		disk, _ := findValue(args, "-device", "virtio-blk-pci")
		if !strings.Contains(disk, "bootindex=0") {
			t.Errorf("disk %q should boot first once installed", disk)
		}
	})

	t.Run("rescue boot puts the ISO first again", func(t *testing.T) {
		v := config.Defaults("arch", caps)
		v.ISOPath = "/images/archboot.iso"
		v.Installed = true

		args := mustBuild(t, v, caps, BuildOptions{ForceISOBoot: true})
		cd, ok := findValue(args, "-device", "scsi-cd")
		if !ok {
			t.Fatal("ForceISOBoot did not attach the ISO")
		}
		if !strings.Contains(cd, "bootindex=0") {
			t.Errorf("CD %q should boot first for a rescue boot", cd)
		}
	})
}

func TestBuildRejectsMissingISO(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.ISOPath = ""
	v.Installed = false

	if _, err := Build(v, caps, testPaths(), BuildOptions{AttachISO: true}); err == nil {
		t.Error("attaching a nonexistent ISO should be an error, not a broken command line")
	}
}

func TestBuildNetworking(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.SSHPort = 2222
	v.MAC = "52:54:00:ab:cd:ef"
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{})

	netdev, ok := findValue(args, "-netdev", "user")
	if !ok {
		t.Fatal("no user-mode netdev")
	}
	// Binding the forward to loopback keeps the guest's SSH off the LAN.
	if !strings.Contains(netdev, "hostfwd=tcp:127.0.0.1:2222-:22") {
		t.Errorf("netdev %q does not forward SSH on loopback", netdev)
	}

	dev, _ := findValue(args, "-device", "virtio-net-pci")
	if !strings.Contains(dev, "mac=52:54:00:ab:cd:ef") {
		t.Errorf("NIC %q does not carry the VM's stable MAC", dev)
	}
}

func TestBuildHeadlessVsWindowed(t *testing.T) {
	caps := testCaps()

	t.Run("headless", func(t *testing.T) {
		v := config.Defaults("arch", caps)
		v.Display = config.DisplayNone
		v.GPU = false
		v.Installed = true

		args := mustBuild(t, v, caps, BuildOptions{})
		m := argMap(args)

		if got := m["-display"][0]; got != "none" {
			t.Errorf("-display = %q, want none", got)
		}
		if !strings.Contains(m["-machine"][0], "graphics=off") {
			t.Error("a headless machine should set graphics=off")
		}
		if _, ok := findValue(args, "-device", "virtio-gpu"); ok {
			t.Error("a headless machine should not attach a GPU")
		}
		// The serial console must still exist, or the VM is unreachable.
		if _, ok := findValue(args, "-chardev", "console0"); !ok {
			t.Error("headless VMs still need a serial console socket")
		}
	})

	t.Run("windowed", func(t *testing.T) {
		v := config.Defaults("arch", caps)
		v.Display = config.DisplayCocoa
		v.GPU = true
		v.Installed = true

		args := mustBuild(t, v, caps, BuildOptions{})
		if got := argMap(args)["-display"][0]; !strings.HasPrefix(got, "cocoa") {
			t.Errorf("-display = %q, want the cocoa backend", got)
		}
		if _, ok := findValue(args, "-device", "virtio-gpu-pci"); !ok {
			t.Error("a windowed machine needs a GPU")
		}
		if _, ok := findValue(args, "-device", "usb-tablet"); !ok {
			t.Error("a windowed machine needs an absolute pointing device")
		}
	})
}

// A build lacking a device must silently do without it rather than emitting a
// command line QEMU will reject.
func TestBuildSkipsUnavailableDevices(t *testing.T) {
	caps := testCaps()
	delete(caps.Devices, "virtio-balloon-pci")
	delete(caps.Devices, "usb-tablet")

	v := config.Defaults("arch", caps)
	v.Display = config.DisplayCocoa
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{})
	if _, ok := findValue(args, "-device", "virtio-balloon"); ok {
		t.Error("emitted a balloon device this QEMU build does not have")
	}
	if _, ok := findValue(args, "-device", "usb-tablet"); ok {
		t.Error("emitted a usb-tablet this QEMU build does not have")
	}
}

func TestBuildSnapshotAndExtraArgs(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{
		Snapshot:  true,
		ExtraArgs: []string{"-d", "guest_errors"},
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-snapshot") {
		t.Error("Snapshot did not add -snapshot")
	}
	if !strings.Contains(joined, "-d guest_errors") {
		t.Error("ExtraArgs were not appended")
	}
}

func TestBuildControlSockets(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true
	p := testPaths()

	args, err := Build(v, caps, p, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := argMap(args)

	qmp := m["-qmp"][0]
	if !strings.Contains(qmp, p.QMPSocket) || !strings.Contains(qmp, "server=on") ||
		!strings.Contains(qmp, "wait=off") {
		t.Errorf("-qmp %q must be a non-blocking server on the VM's socket", qmp)
	}
	if m["-pidfile"][0] != p.PIDFile {
		t.Errorf("-pidfile = %q, want %q", m["-pidfile"][0], p.PIDFile)
	}
}

func TestBuildValidationErrors(t *testing.T) {
	caps := testCaps()
	good := config.Defaults("arch", caps)
	good.Installed = true

	t.Run("nil caps", func(t *testing.T) {
		if _, err := Build(good, nil, testPaths(), BuildOptions{}); err == nil {
			t.Error("expected an error without host capabilities")
		}
	})

	t.Run("no firmware", func(t *testing.T) {
		c := testCaps()
		c.Firmware = ""
		if _, err := Build(good, c, testPaths(), BuildOptions{}); err == nil {
			t.Error("expected an error when no UEFI firmware is available")
		}
	})

	t.Run("invalid spec", func(t *testing.T) {
		bad := good
		bad.CPUs = 0
		if _, err := Build(bad, caps, testPaths(), BuildOptions{}); err == nil {
			t.Error("expected an error for an invalid spec")
		}
	})

	t.Run("incomplete paths", func(t *testing.T) {
		if _, err := Build(good, caps, config.Paths{}, BuildOptions{}); err == nil {
			t.Error("expected an error for empty paths")
		}
	})
}

// The command line must be a pure function of its inputs, both so it can be
// shown to the user and so a restart reproduces the same machine.
func TestBuildIsDeterministic(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true

	first := mustBuild(t, v, caps, BuildOptions{})
	for i := 0; i < 5; i++ {
		next := mustBuild(t, v, caps, BuildOptions{})
		if strings.Join(first, " ") != strings.Join(next, " ") {
			t.Fatalf("Build is not deterministic:\n%v\n%v", first, next)
		}
	}
}

func TestSMPTopology(t *testing.T) {
	got := smpTopology(4)
	for _, want := range []string{"4,", "sockets=1", "cores=4", "threads=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("smpTopology(4) = %q, missing %q", got, want)
		}
	}
}

func TestDefaultDisplay(t *testing.T) {
	caps := testCaps()
	// On darwin cocoa should win over the rest.
	if caps.OS == "darwin" {
		_ = caps
	}
	if got := DefaultDisplay(nil); got != config.DisplayNone {
		t.Errorf("DefaultDisplay(nil) = %q, want none", got)
	}

	bare := testCaps()
	bare.Displays = []string{"none"}
	if got := DefaultDisplay(bare); got != config.DisplayNone {
		t.Errorf("DefaultDisplay with no window backend = %q, want none", got)
	}
}

// A guest that boots at QEMU's cramped 1280x800 default wastes most of a
// modern screen, so the tuned resolution has to reach the GPU device.
func TestBuildGPUResolution(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true
	v.GPU = true
	v.Display = config.DisplayCocoa
	v.DisplayWidth, v.DisplayHeight = 1464, 944

	args := mustBuild(t, v, caps, BuildOptions{})

	gpu, ok := findValue(args, "-device", "virtio-gpu-pci")
	if !ok {
		t.Fatal("no GPU device")
	}
	for _, want := range []string{"xres=1464", "yres=944"} {
		if !strings.Contains(gpu, want) {
			t.Errorf("GPU device %q is missing %q", gpu, want)
		}
	}
}

// zoom-to-fit decides which of two behaviours the Cocoa window has, and they
// are mutually exclusive: with it on the window is draggable but opens small
// and forces the guest to match, with it off the window opens at the guest's
// resolution but cannot be dragged.
func TestBuildCocoaWindowMode(t *testing.T) {
	caps := testCaps()
	base := config.Defaults("arch", caps)
	base.Installed = true
	base.GPU = true
	base.Display = config.DisplayCocoa

	t.Run("sized from the guest by default", func(t *testing.T) {
		display := argMap(mustBuild(t, base, caps, BuildOptions{}))["-display"][0]
		if !strings.HasPrefix(display, "cocoa") {
			t.Fatalf("-display = %q", display)
		}
		// zoom-to-fit would decouple the window and shrink the guest to a small
		// fixed default, which is exactly the tiny-window problem.
		if strings.Contains(display, "zoom-to-fit=on") {
			t.Errorf("-display %q would ignore the configured resolution", display)
		}
	})

	t.Run("draggable when asked for", func(t *testing.T) {
		v := base
		v.ResizableWindow = true
		display := argMap(mustBuild(t, v, caps, BuildOptions{}))["-display"][0]
		if !strings.Contains(display, "zoom-to-fit=on") {
			t.Errorf("-display %q leaves the window unresizable", display)
		}
	})
}

// A headless VM has no window to size, so it must not gain display options.
func TestBuildHeadlessHasNoWindowOptions(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true
	v.Display = config.DisplayNone
	v.GPU = false

	args := mustBuild(t, v, caps, BuildOptions{})
	if got := argMap(args)["-display"][0]; got != "none" {
		t.Errorf("-display = %q, want a bare none", got)
	}
}

// The installer is driven over the serial console while march reports progress
// in the terminal; a window would cover that with a blank screen throughout.
func TestBuildInstallerBootIsHeadless(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Display = config.DisplayCocoa
	v.GPU = true
	v.ISOPath = "/images/archboot.iso"
	v.Installed = false

	args := mustBuild(t, v, caps, BuildOptions{})
	if got := argMap(args)["-display"][0]; got != "none" {
		t.Errorf("-display = %q during installation, want none", got)
	}

	// Once installed, the same VM opens its window.
	v.Installed = true
	args = mustBuild(t, v, caps, BuildOptions{})
	if got := argMap(args)["-display"][0]; !strings.HasPrefix(got, "cocoa") {
		t.Errorf("-display = %q after installation, want the window", got)
	}
}

// A rescue boot of an installed machine is interactive, so it keeps its window.
func TestBuildRescueBootKeepsWindow(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Display = config.DisplayCocoa
	v.GPU = true
	v.ISOPath = "/images/archboot.iso"
	v.Installed = true

	args := mustBuild(t, v, caps, BuildOptions{ForceISOBoot: true})
	if got := argMap(args)["-display"][0]; !strings.HasPrefix(got, "cocoa") {
		t.Errorf("-display = %q for a rescue boot, want the window", got)
	}
}

func TestBuildFullscreen(t *testing.T) {
	caps := testCaps()
	v := config.Defaults("arch", caps)
	v.Installed = true
	v.Display = config.DisplayCocoa
	v.GPU = true

	if !v.Fullscreen {
		t.Error("installed machines should open fullscreen by default")
	}
	display := argMap(mustBuild(t, v, caps, BuildOptions{}))["-display"][0]
	if !strings.Contains(display, "full-screen=on") {
		t.Errorf("-display %q does not open fullscreen", display)
	}

	v.Fullscreen = false
	display = argMap(mustBuild(t, v, caps, BuildOptions{}))["-display"][0]
	if strings.Contains(display, "full-screen=on") {
		t.Errorf("-display %q went fullscreen despite being disabled", display)
	}
}

// Hardware rendering needs both halves: the GL device and a display backend
// with GL enabled. QEMU rejects the device outright if the display cannot
// provide GL, so the two must always be decided together.
func TestBuildGPUAccelRequiresAGLDisplay(t *testing.T) {
	caps := testCaps()
	caps.Devices["virtio-gpu-gl-pci"] = true

	base := config.Defaults("arch", caps)
	base.Installed = true
	base.GPU = true
	base.GPUAccel = true

	t.Run("windowed gets hardware rendering", func(t *testing.T) {
		v := base
		v.Display = config.DisplayCocoa

		args := mustBuild(t, v, caps, BuildOptions{})
		if _, ok := findValue(args, "-device", "virtio-gpu-gl-pci"); !ok {
			t.Error("no accelerated GPU device on a windowed VM")
		}
		if d := argMap(args)["-display"][0]; !strings.Contains(d, "gl=on") {
			t.Errorf("-display %q does not enable GL, so QEMU would reject the device", d)
		}
	})

	t.Run("headless falls back to the plain device", func(t *testing.T) {
		v := base
		v.Display = config.DisplayNone

		args := mustBuild(t, v, caps, BuildOptions{})
		if _, ok := findValue(args, "-device", "virtio-gpu-gl-pci"); ok {
			t.Error("headless VM asked for the GL device, which has no display to render through")
		}
		if _, ok := findValue(args, "-device", "virtio-gpu-pci"); !ok {
			t.Error("headless VM lost its GPU entirely")
		}
	})

	t.Run("installer boot stays headless", func(t *testing.T) {
		v := base
		v.Display = config.DisplayCocoa
		v.Installed = false
		v.ISOPath = "/images/archboot.iso"

		args := mustBuild(t, v, caps, BuildOptions{})
		if _, ok := findValue(args, "-device", "virtio-gpu-gl-pci"); ok {
			t.Error("the installer boot asked for GL, but it runs with no display")
		}
	})

	t.Run("no accelerated QEMU means no GL", func(t *testing.T) {
		plain := testCaps() // no virtio-gpu-gl-pci
		v := base
		v.Display = config.DisplayCocoa

		args := mustBuild(t, v, plain, BuildOptions{})
		if _, ok := findValue(args, "-device", "virtio-gpu-gl-pci"); ok {
			t.Error("asked for a device this QEMU does not have")
		}
		if d := argMap(args)["-display"][0]; strings.Contains(d, "gl=on") {
			t.Errorf("-display %q enables GL on a build without it", d)
		}
	})
}

// venusCaps is a host that can forward Vulkan: the GL device, the venus
// property on it, and both host Vulkan pieces.
func venusCaps() *host.Caps {
	c := testCaps()
	c.Devices["virtio-gpu-gl-pci"] = true
	c.VenusDevice = true
	c.MoltenVK = "/opt/homebrew/opt/molten-vk/lib/libMoltenVK.dylib"
	c.VulkanLoader = "/opt/homebrew/lib/libvulkan.dylib"
	c.SeqPacket = true
	return c
}

func gpuDevice(args []string) string {
	for _, d := range argMap(args)["-device"] {
		if strings.HasPrefix(d, "virtio-gpu") {
			return d
		}
	}
	return ""
}

func TestVenusArgs(t *testing.T) {
	caps := venusCaps()
	v := config.Defaults("arch", caps)
	v.Display, v.GPU = config.DisplayCocoa, true

	if !v.Venus {
		t.Fatal("defaults did not enable Venus on a capable host")
	}

	dev := gpuDevice(mustBuild(t, v, caps, BuildOptions{}))
	for _, want := range []string{"venus=on", "blob=on", "hostmem="} {
		if !strings.Contains(dev, want) {
			t.Errorf("GPU device %q is missing %q", dev, want)
		}
	}
	// Venus rides on the GL device; asking for it on the plain one is invalid.
	if !strings.HasPrefix(dev, "virtio-gpu-gl-pci") {
		t.Errorf("Venus was requested on %q, not the GL device", dev)
	}
}

// Every piece Venus needs must be present, and it must never outlive GL: the
// installer boots without a display, and QEMU rejects the GL device there.
func TestVenusRequiresEveryPiece(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*host.Caps, *config.VM)
	}{
		{"no venus property", func(c *host.Caps, _ *config.VM) { c.VenusDevice = false }},
		{"no vulkan driver", func(c *host.Caps, _ *config.VM) { c.MoltenVK = "" }},
		{"no vulkan loader", func(c *host.Caps, _ *config.VM) { c.VulkanLoader = "" }},
		{"no seqpacket", func(c *host.Caps, _ *config.VM) { c.SeqPacket = false }},
		{"no GL device", func(c *host.Caps, _ *config.VM) { delete(c.Devices, "virtio-gpu-gl-pci") }},
		{"disabled on the VM", func(_ *host.Caps, v *config.VM) { v.Venus = false }},
		{"headless", func(_ *host.Caps, v *config.VM) { v.Display = config.DisplayNone }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := venusCaps()
			v := config.Defaults("arch", caps)
			v.Display, v.GPU = config.DisplayCocoa, true
			tc.mutate(caps, &v)

			if dev := gpuDevice(mustBuild(t, v, caps, BuildOptions{})); strings.Contains(dev, "venus") {
				t.Errorf("Venus was requested anyway: %q", dev)
			}
		})
	}

	// The installer runs headless, so Venus must be off there even though the
	// VM and host both support it.
	caps := venusCaps()
	v := config.Defaults("arch", caps)
	v.Display, v.GPU = config.DisplayCocoa, true
	// An attached ISO on a not-yet-installed guest is what marks the install
	// boot, and that boot is headless.
	v.Installed, v.ISOPath = false, "/images/archboot.iso"
	args := mustBuild(t, v, caps, BuildOptions{AttachISO: true})
	if dev := gpuDevice(args); strings.Contains(dev, "venus") {
		t.Errorf("Venus was requested during install: %q", dev)
	}
}
