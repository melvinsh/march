// Package host probes the machine march is running on, so VM defaults and
// QEMU arguments are derived from what this QEMU build actually supports
// rather than from assumptions. Different builds differ in meaningful ways:
// Homebrew's macOS QEMU has no io_uring/native AIO and no virgl-backed GPU,
// while a typical Linux build has both.
package host

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Accelerators, in descending order of preference.
const (
	AccelHVF = "hvf"
	AccelKVM = "kvm"
	AccelTCG = "tcg"
)

// Caps describes the host and its QEMU installation.
type Caps struct {
	QemuSystem string // absolute path to qemu-system-aarch64
	QemuImg    string // absolute path to qemu-img

	Version      string
	VersionMajor int
	VersionMinor int

	Accels   []string        // accelerators this binary was built with
	Devices  map[string]bool // device model name -> present
	Displays []string        // display backends
	AIOModes []string        // block AIO backends that actually work here

	Firmware string // aarch64 UEFI code image (edk2/AAVMF)

	HostCPUs   int
	HostMemMiB int
	OS         string
	Arch       string

	// ScreenWidth and ScreenHeight are the main display's size in physical
	// pixels, which is the unit a guest framebuffer is measured in: QEMU maps
	// one guest pixel to one screen pixel. Zero when unknown.
	ScreenWidth  int
	ScreenHeight int

	// VenusDevice reports that the GL display device accepts venus=on. Venus
	// forwards the guest's *Vulkan* calls to the host, which is what browsers
	// need: Chromium renders through ANGLE, whose backend is Vulkan, so
	// without it the browser falls back to a software rasterizer even on a
	// guest whose desktop is fully accelerated.
	VenusDevice bool

	// MoltenVK and VulkanLoader are the two host pieces Venus needs.
	// virglrenderer dlopens the loader by name at runtime, and the loader
	// finds MoltenVK through an ICD manifest march generates.
	MoltenVK     string
	VulkanLoader string

	// SeqPacket reports that this kernel can create a SOCK_SEQPACKET socket
	// pair, which decides whether Venus can run at all. See
	// seqpacketSupported.
	SeqPacket bool
}

// Probe inspects the host and its QEMU installation. It never returns a nil
// Caps: when QEMU is missing the caps are still populated with host facts so
// the UI can render a useful diagnostic instead of an empty screen.
func Probe(ctx context.Context) (*Caps, error) {
	c := &Caps{
		Devices:    map[string]bool{},
		HostCPUs:   runtime.NumCPU(),
		HostMemMiB: hostMemMiB(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
	}

	c.ScreenWidth, c.ScreenHeight = probeScreen(ctx)

	c.QemuSystem = lookPath("qemu-system-aarch64")
	c.QemuImg = lookPath("qemu-img")
	if c.QemuSystem == "" {
		return c, fmt.Errorf("qemu-system-aarch64 not found in PATH or the usual install prefixes: %w", ErrQemuMissing)
	}

	c.Version, c.VersionMajor, c.VersionMinor = probeVersion(ctx, c.QemuSystem)
	c.Accels = probeAccels(ctx, c.QemuSystem)
	c.Devices = probeDevices(ctx, c.QemuSystem)
	c.Displays = probeDisplays(ctx, c.QemuSystem)
	c.Firmware = FindFirmware(c.QemuSystem)
	c.AIOModes = probeAIO(ctx, c.QemuSystem)
	c.VenusDevice = probeVenus(ctx, c.QemuSystem)
	c.MoltenVK = findLib("libMoltenVK.dylib")
	c.VulkanLoader = findLib("libvulkan.dylib")
	c.SeqPacket = seqpacketSupported()

	return c, nil
}

// ErrQemuMissing signals that no QEMU system emulator could be located.
var ErrQemuMissing = fmt.Errorf("qemu not installed")

// acceleratedPrefixes are searched *before* PATH. A QEMU built against
// virglrenderer hands the guest's OpenGL to the host GPU instead of rendering
// it on the CPU, which is the difference between a usable desktop and a
// slideshow — so it is preferred whenever it is installed. See
// Formula/qemu-march.rb.
var acceleratedPrefixes = []string{
	"/opt/homebrew/opt/qemu-march/bin",
	"/usr/local/opt/qemu-march/bin",
}

// searchPrefixes are consulted when a binary is not on PATH. TUIs are often
// launched from GUI contexts (Spotlight, an IDE) with a minimal PATH that
// omits Homebrew, so falling back to the known prefixes avoids a spurious
// "QEMU not installed" error on a machine that plainly has it.
var searchPrefixes = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/usr/bin",
	"/opt/local/bin",
}

func lookPath(bin string) string {
	// A GPU-accelerated build wins over whatever is on PATH.
	for _, dir := range acceleratedPrefixes {
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath(bin); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	for _, dir := range searchPrefixes {
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

var versionRe = regexp.MustCompile(`version (\d+)\.(\d+)(?:\.(\d+))?`)

func probeVersion(ctx context.Context, qemu string) (full string, major, minor int) {
	out, err := run(ctx, qemu, "--version")
	if err != nil {
		return "", 0, 0
	}
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return "", 0, 0
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	full = m[1] + "." + m[2]
	if m[3] != "" {
		full += "." + m[3]
	}
	return full, major, minor
}

func probeAccels(ctx context.Context, qemu string) []string {
	out, err := run(ctx, qemu, "-accel", "help")
	if err != nil {
		return nil
	}
	var accels []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Skip the "Accelerators supported in QEMU binary:" header.
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		if fields := strings.Fields(line); len(fields) == 1 {
			accels = append(accels, fields[0])
		}
	}
	return accels
}

// deviceRe matches the quoted device model name in `-device help` output,
// e.g. `name "virtio-blk-pci", bus PCI, desc "..."`.
var deviceRe = regexp.MustCompile(`name "([^"]+)"`)

func probeDevices(ctx context.Context, qemu string) map[string]bool {
	devices := map[string]bool{}
	out, err := run(ctx, qemu, "-device", "help")
	if err != nil {
		return devices
	}
	for _, m := range deviceRe.FindAllStringSubmatch(out, -1) {
		devices[m[1]] = true
	}
	return devices
}

// probeVenus asks the GL device whether it has a venus property. The device
// help text lists the properties this build supports, so a build without Venus
// simply does not mention it.
func probeVenus(ctx context.Context, qemu string) bool {
	out, err := run(ctx, qemu, "-device", "virtio-gpu-gl-pci,help")
	if err != nil {
		return false
	}
	return strings.Contains(out, "venus=")
}

// vulkanLibDirs are searched for the host Vulkan pieces. These are libraries
// rather than binaries, so they live beside the prefixes in searchPrefixes
// rather than in them.
var vulkanLibDirs = []string{
	"/opt/homebrew/lib",
	"/opt/homebrew/opt/molten-vk/lib",
	"/usr/local/lib",
	"/usr/local/opt/molten-vk/lib",
	"/usr/lib",
}

func findLib(name string) string {
	for _, dir := range vulkanLibDirs {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func probeDisplays(ctx context.Context, qemu string) []string {
	out, err := run(ctx, qemu, "-display", "help")
	if err != nil {
		return nil
	}
	var displays []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasSuffix(line, ":") || strings.Contains(line, " ") {
			continue
		}
		displays = append(displays, line)
	}
	return displays
}

// aioCandidates are tried in descending order of throughput. io_uring and
// native are Linux-only; asking for one on a build without it is a hard
// startup error, so each is verified against the real binary.
var aioCandidates = []string{"io_uring", "native", "threads"}

// probeAIO asks QEMU to open a throwaway image with each AIO backend. This is
// cheap (QEMU exits before starting the machine) and is the only reliable
// signal: support depends on build flags, not on version or platform alone.
func probeAIO(ctx context.Context, qemu string) []string {
	tmp, err := os.CreateTemp("", "march-aio-*.raw")
	if err != nil {
		return []string{"threads"}
	}
	defer os.Remove(tmp.Name())
	// O_DIRECT requires a block-aligned file; 1 MiB is safely aligned.
	if err := tmp.Truncate(1 << 20); err != nil {
		tmp.Close()
		return []string{"threads"}
	}
	tmp.Close()

	var ok []string
	for _, mode := range aioCandidates {
		blockdev := fmt.Sprintf(
			"driver=file,node-name=probe0,filename=%s,aio=%s,cache.direct=on",
			tmp.Name(), mode)
		// -S stops before executing guest code and QMP-over-stdio lets us quit
		// as soon as QEMU has parsed and accepted the block layer options,
		// which is the only thing being tested here.
		// A backend that cannot be demonstrated is not one worth defaulting to,
		// so any failure counts as unsupported.
		if _, err := runStdin(ctx,
			`{"execute":"qmp_capabilities"}{"execute":"quit"}`,
			qemu,
			"-machine", "virt", "-display", "none", "-nodefaults",
			"-blockdev", blockdev,
			"-S", "-monitor", "none", "-qmp", "stdio",
		); err == nil {
			ok = append(ok, mode)
		}
	}
	if len(ok) == 0 {
		// threads is mandatory in every QEMU build; if the probe could not
		// prove anything, fall back to it rather than emitting no AIO at all.
		return []string{"threads"}
	}
	return ok
}

// firmwareCandidates covers Homebrew, Debian/Ubuntu, Fedora and Arch layouts.
var firmwareCandidates = []string{
	"share/qemu/edk2-aarch64-code.fd",
	"share/AAVMF/AAVMF_CODE.fd",
	"share/edk2/aarch64/QEMU_EFI.silent.fd",
	"share/edk2/aarch64/QEMU_EFI.fd",
	"share/qemu-efi-aarch64/QEMU_EFI.fd",
	"share/edk2-armvirt/aarch64/QEMU_EFI.fd",
}

// FindFirmware locates the aarch64 UEFI code image. It searches relative to
// the QEMU installation prefix first so a Homebrew QEMU picks up Homebrew's
// firmware even when a system copy exists.
func FindFirmware(qemuSystem string) string {
	var roots []string
	if qemuSystem != "" {
		// .../bin/qemu-system-aarch64 -> prefix is two levels up.
		roots = append(roots, filepath.Dir(filepath.Dir(qemuSystem)))
	}
	roots = append(roots, "/opt/homebrew", "/usr/local", "/usr")

	for _, root := range roots {
		for _, rel := range firmwareCandidates {
			p := filepath.Join(root, rel)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// HasAccel reports whether the named accelerator is available.
//
// This and the other predicates tolerate a nil receiver: the UI renders before
// the host probe finishes, and a nil Caps simply means "nothing detected yet".
func (c *Caps) HasAccel(name string) bool {
	if c == nil {
		return false
	}
	for _, a := range c.Accels {
		if a == name {
			return true
		}
	}
	return false
}

// HasDevice reports whether a device model is available.
func (c *Caps) HasDevice(name string) bool {
	if c == nil {
		return false
	}
	return c.Devices[name]
}

// HasDisplay reports whether a display backend is available.
func (c *Caps) HasDisplay(name string) bool {
	if c == nil {
		return false
	}
	for _, d := range c.Displays {
		if d == name {
			return true
		}
	}
	return false
}

// BestAccel returns the fastest accelerator available. Hardware acceleration
// requires the host and guest architectures to match; on an x86 host running
// an aarch64 guest, only TCG emulation is possible.
func (c *Caps) BestAccel() string {
	if c == nil {
		return AccelTCG
	}
	if c.Arch == "arm64" {
		for _, want := range []string{AccelHVF, AccelKVM} {
			if c.HasAccel(want) {
				return want
			}
		}
	}
	return AccelTCG
}

// BestAIO returns the fastest working AIO backend.
func (c *Caps) BestAIO() string {
	if c == nil {
		return "threads"
	}
	for _, want := range aioCandidates {
		for _, got := range c.AIOModes {
			if want == got {
				return want
			}
		}
	}
	return "threads"
}

// SupportsGPUAccel reports whether this QEMU can hand the guest's OpenGL to
// the host GPU.
//
// The virtio-gpu-gl device is the signal: it only exists in a build linked
// against virglrenderer, and such builds also carry the display-side GL
// support that goes with it. Probing the display directly would mean opening a
// window during startup, which is worse than inferring it from the device.
func (c *Caps) SupportsGPUAccel() bool {
	return c.HasDevice("virtio-gpu-gl-pci")
}

// SupportsVenus reports whether the guest's Vulkan can be forwarded to the
// host GPU. It needs all three pieces: a QEMU whose GL device takes venus=on,
// a Vulkan loader for virglrenderer to dlopen, and a driver behind it.
// Without Venus the desktop is still accelerated through virgl; it is the
// browser, which renders Vulkan-first through ANGLE, that pays the price.
func (c *Caps) SupportsVenus() bool {
	if c == nil {
		return false
	}
	return c.SupportsGPUAccel() && c.VenusDevice &&
		c.MoltenVK != "" && c.VulkanLoader != "" && c.SeqPacket
}

// seqpacketSupported reports whether this kernel can create a SOCK_SEQPACKET
// socket pair.
//
// It is the one thing that decides whether Venus can run at all. virglrenderer
// serves Venus contexts exclusively through its render-server proxy, and that
// proxy's transport is a SOCK_SEQPACKET socket pair — it relies on datagram
// boundaries and refuses any other socket type. Darwin's AF_UNIX has no
// SOCK_SEQPACKET, so the renderer fails to initialize and QEMU exits before
// the guest firmware finishes, taking the whole boot with it.
//
// Probing rather than testing runtime.GOOS means this turns itself on if the
// platform ever gains support, and it is correct on Linux without a special
// case.
func seqpacketSupported() bool {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		return false
	}
	syscall.Close(fds[0])
	syscall.Close(fds[1])
	return true
}

// Accelerated reports whether VMs will run at native speed. Under TCG every
// guest instruction is translated, which is roughly an order of magnitude
// slower and changes what defaults make sense.
func (c *Caps) Accelerated() bool { return c.BestAccel() != AccelTCG }

// Ready reports whether march has everything it needs to create a VM.
func (c *Caps) Ready() bool {
	if c == nil {
		return false
	}
	return c.QemuSystem != "" && c.QemuImg != "" && c.Firmware != ""
}

// Problems lists, in human terms, what is missing. Empty when Ready.
func (c *Caps) Problems() []string {
	if c == nil {
		return []string{"host inspection has not finished yet"}
	}
	var out []string
	if c.QemuSystem == "" {
		out = append(out, "qemu-system-aarch64 not found — install QEMU (brew install qemu)")
	}
	if c.QemuImg == "" {
		out = append(out, "qemu-img not found — install QEMU (brew install qemu)")
	}
	if c.Firmware == "" {
		out = append(out, "aarch64 UEFI firmware (edk2-aarch64-code.fd) not found")
	}
	if c.QemuSystem != "" && !c.Accelerated() {
		out = append(out, "no hardware acceleration available — VMs will run under slow TCG emulation")
	}
	return out
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	return runStdin(ctx, "", name, args...)
}

func runStdin(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// retinaRe pulls the pixel dimensions out of system_profiler's display block.
var retinaRe = regexp.MustCompile(`Resolution: (\d+) ?x ?(\d+)( Retina)?`)

// xrandrRe matches the active mode in xrandr output, e.g. "1920x1080+0+0".
var xrandrRe = regexp.MustCompile(`(\d+)x(\d+)\+\d+\+\d+`)

// probeScreen reports the main display's size in physical pixels, so a guest
// framebuffer can be sized relative to the screen it will appear on. It
// returns zeroes when the size cannot be determined, and callers fall back to
// a fixed default.
func probeScreen(ctx context.Context) (w, h int) {
	switch runtime.GOOS {
	case "darwin":
		out, err := run(ctx, "system_profiler", "SPDisplaysDataType")
		if err != nil {
			return 0, 0
		}
		m := retinaRe.FindStringSubmatch(out)
		if m == nil {
			return 0, 0
		}
		// system_profiler reports physical pixels, which is exactly what a
		// guest framebuffer is measured in. A Retina window then occupies half
		// as many points, which is what makes the desktop render sharply
		// rather than upscaled.
		w, _ = strconv.Atoi(m[1])
		h, _ = strconv.Atoi(m[2])
		return w, h

	case "linux":
		if out, err := run(ctx, "xrandr", "--current"); err == nil {
			if m := xrandrRe.FindStringSubmatch(out); m != nil {
				w, _ = strconv.Atoi(m[1])
				h, _ = strconv.Atoi(m[2])
				return w, h
			}
		}
		// Without an X server, the framebuffer geometry is the best guess.
		if b, err := os.ReadFile("/sys/class/graphics/fb0/virtual_size"); err == nil {
			parts := strings.Split(strings.TrimSpace(string(b)), ",")
			if len(parts) == 2 {
				w, _ = strconv.Atoi(parts[0])
				h, _ = strconv.Atoi(parts[1])
				return w, h
			}
		}
	}
	return 0, 0
}

func hostMemMiB() int {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			if b, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return int(b / (1 << 20))
			}
		}
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err == nil {
			defer f.Close()
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				if kb, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
					kb = strings.TrimSuffix(strings.TrimSpace(kb), " kB")
					if v, err := strconv.Atoi(strings.TrimSpace(kb)); err == nil {
						return v / 1024
					}
				}
			}
		}
	}
	// A conservative floor keeps default sizing sane when detection fails.
	return 8192
}

// VulkanEnv prepares the environment QEMU needs for Venus and returns the
// variables to add to it. It returns nil when Venus is unavailable, so callers
// can add unconditionally.
//
// The Vulkan loader locates drivers through ICD manifests. Homebrew's MoltenVK
// ships none, and dropping one into a shared system directory would affect
// every Vulkan program on the machine — so march writes its own next to the VM
// and points the loader at just that file.
func (c *Caps) VulkanEnv(dir string) []string {
	if !c.SupportsVenus() {
		return nil
	}
	manifest := filepath.Join(dir, "moltenvk_icd.json")
	body := fmt.Sprintf(`{
    "file_format_version": "1.0.0",
    "ICD": {
        "library_path": %q,
        "api_version": "1.2.0",
        "is_portability_driver": true
    }
}
`, c.MoltenVK)
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		return nil
	}
	// VK_DRIVER_FILES is the current name; VK_ICD_FILENAMES is its deprecated
	// predecessor, still read by older loaders. Setting both covers whichever
	// loader happens to be installed.
	return []string{
		"VK_DRIVER_FILES=" + manifest,
		"VK_ICD_FILENAMES=" + manifest,
	}
}
