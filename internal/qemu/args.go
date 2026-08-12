// Package qemu turns a VM specification into a QEMU command line, creates and
// inspects disk images, and speaks QMP to running machines.
package qemu

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
)

// BuildOptions adjusts a single invocation without changing the stored spec.
type BuildOptions struct {
	// AttachISO forces the installer image to be attached and made first in
	// the boot order. Normally the ISO is attached only until Installed is set.
	AttachISO bool

	// ForceISOBoot puts the CD ahead of the disk in the boot order even for an
	// installed guest, for rescue boots.
	ForceISOBoot bool

	// Snapshot discards all disk writes on shutdown. Useful for trying things
	// out against a known-good image.
	Snapshot bool

	// ExtraArgs are appended verbatim, for escape hatches the UI does not model.
	ExtraArgs []string
}

// Build renders the full argv (excluding argv[0]) for a VM.
//
// The resulting command line is tuned for an aarch64 Linux guest:
//   - hardware acceleration with a pass-through CPU where available,
//   - GICv3, which modern kernels expect and which lifts the 8-vCPU ceiling,
//   - a UEFI boot via split pflash so each VM keeps its own EFI variables,
//   - virtio everywhere, with a dedicated I/O thread and multi-queue virtio-blk
//     so disk I/O does not serialise behind the main loop,
//   - O_DIRECT on the image to avoid caching guest pages twice.
func Build(v config.VM, caps *host.Caps, p config.Paths, opts BuildOptions) ([]string, error) {
	if caps == nil {
		return nil, errors.New("host capabilities are required to build a command line")
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	if caps.Firmware == "" {
		return nil, errors.New("no aarch64 UEFI firmware found; cannot boot a virt machine")
	}
	if p.Disk == "" || p.EFIVars == "" {
		return nil, errors.New("incomplete VM paths")
	}

	var a args

	// process= renames the OS-level process, which makes VMs identifiable in
	// ps and pkill. Only Linux supports it; macOS rejects the whole option.
	name := v.Name
	if caps.OS == "linux" {
		name += ",process=march-" + v.Name
	}
	a.add("-name", name)

	// -nodefaults drops the implicit VGA/NIC/serial/floppy set so the machine
	// contains exactly the devices configured below. -no-user-config keeps a
	// stray /etc/qemu config from perturbing a reproducible command line.
	a.add("-nodefaults", "-no-user-config")

	a.add("-machine", a.machine(v, caps))
	a.add("-cpu", v.CPUModel)
	a.add("-smp", smpTopology(v.CPUs))
	a.add("-m", strconv.Itoa(v.MemoryMiB))

	// Keep the guest clock tied to the host, which matters after the host
	// sleeps. driftfix is deliberately omitted: it is x86-only and the virt
	// machine warns about it.
	a.add("-rtc", "base=utc,clock=host")

	// Without an explicit action QEMU keeps a crashed or rebooting guest
	// half-alive; reset-on-reboot is what a physical machine does.
	a.add("-action", "reboot=reset", "-action", "shutdown=poweroff")

	if v.IOThread {
		a.add("-object", "iothread,id=iothread0")
	}

	a.addAll(firmwareArgs(caps.Firmware, p.EFIVars))

	// An uninstalled VM boots its installer; once installed the ISO stays
	// attached only if asked for, and the disk takes precedence unless the
	// caller explicitly wants a rescue boot.
	isoAttached := opts.AttachISO || opts.ForceISOBoot || (v.ISOPath != "" && !v.Installed)
	isoFirst := isoAttached && (opts.ForceISOBoot || !v.Installed)

	diskBoot, isoBoot := 0, 1
	if isoFirst {
		diskBoot, isoBoot = 1, 0
	}

	a.addAll(diskArgs(v, p, opts, diskBoot))
	if isoAttached {
		if v.ISOPath == "" {
			return nil, errors.New("no installer image is attached to this VM")
		}
		a.addAll(isoArgs(v.ISOPath, isoBoot))
	}

	// The installer runs unattended over the serial console while march reports
	// progress in the terminal. Opening a window for it would put a blank
	// screen over that for the whole install, so the display belongs to the
	// installed system rather than to the installation.
	installing := isoAttached && !v.Installed

	a.addAll(netArgs(v))
	a.addAll(consoleArgs(v, caps, p, installing))
	a.addAll(audioArgs(v, caps, installing))

	if v.RNG {
		// A virtio RNG keeps the guest from stalling on entropy during boot,
		// which is otherwise very visible on a fresh install.
		a.add("-object", "rng-random,filename=/dev/urandom,id=rng0")
		a.add("-device", "virtio-rng-pci,rng=rng0")
	}
	if v.Balloon && caps.HasDevice("virtio-balloon-pci") {
		a.add("-device", "virtio-balloon-pci")
	}

	// QMP is how march queries and shuts down the machine.
	a.add("-qmp", "unix:"+p.QMPSocket+",server=on,wait=off")
	a.add("-pidfile", p.PIDFile)

	a.addAll(opts.ExtraArgs)
	return a.out, nil
}

type args struct{ out []string }

func (a *args) add(vals ...string)   { a.out = append(a.out, vals...) }
func (a *args) addAll(vals []string) { a.out = append(a.out, vals...) }

func (a *args) machine(v config.VM, caps *host.Caps) string {
	parts := []string{v.Machine}
	parts = append(parts, "accel="+v.Accel)
	if v.GICVersion != "" {
		parts = append(parts, "gic-version="+v.GICVersion)
	}
	parts = append(parts, "highmem="+onOff(v.Highmem))

	// The graphics pipeline costs memory and device slots even when unused;
	// headless machines say so explicitly.
	if v.Display == config.DisplayNone && !v.GPU {
		parts = append(parts, "graphics=off")
	}
	_ = caps
	return strings.Join(parts, ",")
}

// smpTopology presents the vCPUs as single-threaded cores on one socket.
// Guest schedulers make placement decisions from this topology, and a flat
// core list avoids implying SMT siblings that do not exist.
func smpTopology(cpus int) string {
	return fmt.Sprintf("%d,sockets=1,cores=%d,threads=1", cpus, cpus)
}

// firmwareArgs wires UEFI as split flash: a read-only code image shared by
// every VM, and a private writable variable store. Sharing the code image
// means a QEMU upgrade updates the firmware for all VMs, while each VM keeps
// its own boot entries.
func firmwareArgs(code, vars string) []string {
	return []string{
		"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=" + code,
		"-drive", "if=pflash,format=raw,unit=1,file=" + vars,
	}
}

// diskArgs builds the qcow2 stack. The file and format layers are declared
// separately with -blockdev so cache and AIO policy can be set on the file
// layer, which is where it actually applies.
func diskArgs(v config.VM, p config.Paths, opts BuildOptions, bootIndex int) []string {
	file := []string{
		"driver=file",
		"node-name=disk-file",
		"filename=" + p.Disk,
		"aio=" + orDefault(v.AIO, "threads"),
		// cache.direct bypasses the host page cache; cache.no-flush stays off
		// so the guest's flushes still reach stable storage.
		"cache.direct=" + onOff(v.CacheDirect),
		"cache.no-flush=off",
		"discard=unmap",
	}
	format := []string{
		"driver=qcow2",
		"node-name=disk",
		"file=disk-file",
		"discard=unmap",
	}

	dev := []string{
		"virtio-blk-pci",
		"drive=disk",
		"id=virtio-disk0",
		// One virtqueue per vCPU lets the guest submit I/O from every core
		// without cross-CPU handoff.
		"num-queues=" + strconv.Itoa(v.CPUs),
		// Report unmap support so fstrim in the guest actually shrinks the
		// qcow2 on the host.
		"discard=on",
		"write-cache=on",
		"bootindex=" + strconv.Itoa(bootIndex),
	}
	if v.IOThread {
		dev = append(dev, "iothread=iothread0")
	}

	out := []string{
		"-blockdev", strings.Join(file, ","),
		"-blockdev", strings.Join(format, ","),
		"-device", strings.Join(dev, ","),
	}
	if opts.Snapshot {
		out = append(out, "-snapshot")
	}
	return out
}

// isoArgs attaches the installer as a real CD-ROM behind virtio-scsi. A SCSI
// CD presents the removable-media semantics installers expect, and unlike
// usb-storage it needs no USB controller, so headless installs work without
// dragging in the xhci stack.
func isoArgs(iso string, bootIndex int) []string {
	return []string{
		"-blockdev", "driver=file,node-name=iso-file,filename=" + iso + ",read-only=on",
		"-blockdev", "driver=raw,node-name=iso,file=iso-file,read-only=on",
		"-device", "virtio-scsi-pci,id=scsi0",
		"-device", "scsi-cd,drive=iso,bus=scsi0.0,id=cdrom0,bootindex=" + strconv.Itoa(bootIndex),
	}
}

// netArgs uses QEMU's user-mode network stack. It needs no privileges, which
// matters because vmnet and tap both require root, and forwards a host port to
// the guest's SSH so the VM is reachable without extra setup.
func netArgs(v config.VM) []string {
	netdev := []string{"user", "id=net0"}
	if v.SSHPort > 0 {
		netdev = append(netdev, fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:22", v.SSHPort))
	}
	dev := []string{"virtio-net-pci", "netdev=net0", "id=net0-dev"}
	if v.MAC != "" {
		dev = append(dev, "mac="+v.MAC)
	}
	return []string{
		"-netdev", strings.Join(netdev, ","),
		"-device", strings.Join(dev, ","),
	}
}

// audioArgs gives the guest a sound card.
//
// The device is Intel HDA rather than virtio-sound, which would otherwise be
// the obvious choice next to the rest of march's paravirtual hardware: Arch
// Linux ARM's kernel package ships no virtio_snd module, so a virtio-sound-pci
// would enumerate in the guest and then sit there with no driver bound to it.
// snd-hda-intel is present, and hda is what QEMU has supported longest.
//
// A machine with no window gets the same card wired to the null backend. The
// guest still sees a complete sound stack — which is what keeps a headless
// install identical to a windowed one — while nothing opens a CoreAudio device
// for a VM nobody is listening to.
func audioArgs(v config.VM, caps *host.Caps, installing bool) []string {
	if !caps.HasDevice("ich9-intel-hda") || !caps.HasDevice("hda-duplex") {
		return nil
	}

	driver := "coreaudio"
	if v.Display == config.DisplayNone || installing {
		driver = "none"
	}
	return []string{
		"-audiodev", driver + ",id=snd0",
		"-device", "ich9-intel-hda",
		"-device", "hda-duplex,audiodev=snd0",
	}
}

// consoleArgs configures the display and the serial console. The serial console
// is always exported over a unix socket so a headless VM remains reachable and
// the TUI can attach to it.
// glEnabled reports whether this boot can use hardware rendering. The GL
// device is only valid alongside a display backend that has GL turned on, so a
// headless boot — including every installer boot — uses the plain device.
func glEnabled(v config.VM, caps *host.Caps, installing bool) bool {
	return v.GPUAccel && caps.SupportsGPUAccel() &&
		v.Display != config.DisplayNone && !installing
}

// venusHostMem is the host memory window the guest maps blob resources
// through. It is address space rather than committed memory, but QEMU caps it
// against max_hostmem, so this stays modest enough not to need that raised.
const venusHostMem = "2G"

// venusEnabled reports whether to ask for Vulkan forwarding. It rides on top
// of GL: Venus is a property of the same device, so everything glEnabled
// requires applies here too.
func venusEnabled(v config.VM, caps *host.Caps, installing bool) bool {
	return v.Venus && caps.SupportsVenus() && glEnabled(v, caps, installing)
}

func consoleArgs(v config.VM, caps *host.Caps, p config.Paths, installing bool) []string {
	out := []string{
		"-chardev", "socket,id=console0,path=" + p.SerialSocket + ",server=on,wait=off,logfile=" + p.LogFile,
		"-serial", "chardev:console0",
	}

	gl := glEnabled(v, caps, installing)

	// A GPU is attached whenever the VM has one, independently of whether a
	// window is open: a desktop guest still needs a framebuffer to render into
	// when it is reached over SSH or VNC rather than on screen.
	if v.GPU && caps.HasDevice("virtio-gpu-pci") {
		// virtio-gpu-gl carries the guest's GL commands out to the host GPU;
		// the plain device makes the guest render on its own CPU.
		device := "virtio-gpu-pci"
		if glEnabled(v, caps, installing) {
			device = "virtio-gpu-gl-pci"
		}
		gpu := []string{device}
		if venusEnabled(v, caps, installing) {
			// Venus forwards the guest's Vulkan to the host GPU, which is what
			// gets the browser off its software rasterizer. It only works on
			// blob resources, and blob resources need a host memory window for
			// the guest to map — hence all three together or none.
			gpu = append(gpu, "venus=on", "blob=on", "hostmem="+venusHostMem)
		}
		if v.DisplayWidth > 0 && v.DisplayHeight > 0 {
			// xres/yres set the mode advertised through EDID, which is what
			// the guest picks at boot. Without them QEMU defaults to a cramped
			// 1280x800 regardless of the screen the window opens on.
			gpu = append(gpu,
				"xres="+strconv.Itoa(v.DisplayWidth),
				"yres="+strconv.Itoa(v.DisplayHeight))
		}
		out = append(out, "-device", strings.Join(gpu, ","))
	}

	if v.Display == config.DisplayNone || installing {
		return append(out, "-display", "none")
	}

	out = append(out, "-display", displaySpec(v, gl))
	// A windowed guest needs input. usb-tablet reports absolute coordinates,
	// which keeps the host and guest pointers from drifting apart.
	if caps.HasDevice("qemu-xhci") {
		out = append(out, "-device", "qemu-xhci,id=xhci")
		if caps.HasDevice("usb-kbd") {
			out = append(out, "-device", "usb-kbd,bus=xhci.0")
		}
		if caps.HasDevice("usb-tablet") {
			out = append(out, "-device", "usb-tablet,bus=xhci.0")
		}
	}
	return out
}

// displaySpec adds the options a windowed backend needs to be usable.
//
// zoom-to-fit is the hinge for how the window behaves on macOS, and QEMU only
// offers one behaviour at a time. Off, the window is sized from the guest
// framebuffer — it opens at the configured resolution and renders pixel for
// pixel, but cannot be dragged to a new size. On, the window is decoupled: it
// becomes draggable, but opens at a small fixed default and reports its size
// back to the guest, which then shrinks to match and ignores xres/yres
// entirely. Sizing correctly therefore means leaving it off by default.
func displaySpec(v config.VM, gl bool) string {
	switch v.Display {
	case config.DisplayCocoa:
		// show-cursor is deliberately absent. It forces QEMU to keep drawing the
		// host's pointer over the window, and the guest draws its own on top of
		// the same absolute coordinates — two cursors, a few pixels apart,
		// moving together. With a usb-tablet the guest's is the accurate one, so
		// the host's is the one to leave out.
		opts := []string{v.Display}
		if gl {
			// The display backend has to be told to accept GL, or QEMU refuses
			// the virtio-gpu-gl device outright.
			opts = append(opts, "gl=on")
		}
		if v.CaptureAllKeys {
			// Without this, macOS swallows system combinations before the guest
			// sees them; Cmd+Space opens Spotlight instead of the guest's
			// launcher. QEMU installs a global key tap, which is why macOS
			// asks for accessibility permission the first time.
			opts = append(opts, "full-grab=on")
		}
		if v.ResizableWindow {
			opts = append(opts, "zoom-to-fit=on")
		}
		if v.Fullscreen {
			opts = append(opts, "full-screen=on")
		}
		return strings.Join(opts, ",")
	default:
		if v.Fullscreen {
			return v.Display + ",full-screen=on"
		}
		return v.Display
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// DefaultDisplay picks the best windowed backend for this platform, or
// DisplayNone when the build has no usable window backend.
func DefaultDisplay(caps *host.Caps) string {
	if caps == nil {
		return config.DisplayNone
	}
	order := []string{config.DisplayGTK, config.DisplaySDL}
	if runtime.GOOS == "darwin" {
		order = []string{config.DisplayCocoa, config.DisplaySDL}
	}
	for _, d := range order {
		if caps.HasDisplay(d) {
			return d
		}
	}
	return config.DisplayNone
}
