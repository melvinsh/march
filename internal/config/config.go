// Package config defines the VM specification, its tuned defaults, and the
// on-disk store that persists VMs between runs.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/melvinsh/march/internal/host"
)

// Display backends march exposes.
const (
	DisplayNone   = "none"   // headless; console over the serial socket
	DisplayCocoa  = "cocoa"  // native macOS window
	DisplayGTK    = "gtk"    // native window on Linux
	DisplaySDL    = "sdl"    // portable window
	DisplayCurses = "curses" // in-terminal text console
)

// VM is the full specification of a virtual machine. It is persisted verbatim
// as vm.json, so field names are part of the on-disk format.
type VM struct {
	Name string `json:"name"`

	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memory_mib"`
	DiskGiB   int `json:"disk_gib"`

	Accel      string `json:"accel"`       // hvf | kvm | tcg
	CPUModel   string `json:"cpu_model"`   // host | max | cortex-a76 ...
	Machine    string `json:"machine"`     // virt
	GICVersion string `json:"gic_version"` // 3 | max | host | 2
	Highmem    bool   `json:"highmem"`     // allow guest RAM above 32-bit

	Display string `json:"display"`
	GPU     bool   `json:"gpu"` // attach virtio-gpu (implied by a windowed display)

	// DisplayWidth and DisplayHeight are the guest's resolution in pixels. The
	// window opens showing exactly this many pixels.
	DisplayWidth  int `json:"display_width,omitempty"`
	DisplayHeight int `json:"display_height,omitempty"`

	// Fullscreen opens the installed system filling the screen. It applies only
	// once a machine is installed: during installation march reports progress
	// in the terminal, and a window covering the screen would hide it.
	Fullscreen bool `json:"fullscreen"`

	// GPUAccel routes the guest's OpenGL to the host GPU through virgl instead
	// of rendering it on the CPU. It needs a QEMU built with virglrenderer,
	// which stock macOS builds are not.
	GPUAccel bool `json:"gpu_accel"`

	// Venus additionally forwards the guest's Vulkan to the host GPU. The
	// desktop itself only needs GL, but Chromium renders through ANGLE, whose
	// backend is Vulkan — so without this the browser falls back to a software
	// rasterizer on an otherwise fully accelerated guest.
	Venus bool `json:"venus"`

	// CaptureAllKeys sends every keystroke to the guest, including combinations
	// macOS would otherwise intercept — Cmd+Space above all, which a tiling
	// desktop binds to its launcher. It needs macOS accessibility permission
	// for the terminal running march; without it QEMU simply does not grab.
	CaptureAllKeys bool `json:"capture_all_keys"`

	// ResizableWindow trades resolution for a draggable window. QEMU's macOS
	// backend offers one or the other: with it off the window opens at the
	// configured resolution but cannot be dragged to a new size, and with it
	// on the window is draggable but opens small and forces the guest to
	// match. Changing resolution from inside the guest works either way.
	ResizableWindow bool `json:"resizable_window,omitempty"`

	// Storage tuning. CacheDirect bypasses the host page cache, which avoids
	// double-caching guest pages and is the right default for a qcow2 backed
	// by a fast SSD.
	AIO         string `json:"aio"`
	CacheDirect bool   `json:"cache_direct"`
	IOThread    bool   `json:"iothread"`

	SSHPort int    `json:"ssh_port"`
	MAC     string `json:"mac"`

	Balloon bool `json:"balloon"`
	RNG     bool `json:"rng"`

	// ISOPath is the installer image. It stays attached until the guest is
	// installed, then Installed is set so later boots skip the installer.
	ISOPath   string `json:"iso_path,omitempty"`
	Installed bool   `json:"installed"`

	// Username records who was installed, so the UI can show it. The account
	// password is deliberately never persisted: it is needed only while the
	// installer runs and is held in memory for that long.
	//
	// There is no Desktop field: march installs one desktop. A vm.json written
	// before that was true still carries the key, and it is ignored.
	Username string `json:"username,omitempty"`

	Created time.Time `json:"created"`
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,31}$`)

// ValidateName reports whether a VM name is usable as a directory name.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if !nameRe.MatchString(name) {
		return errors.New("name must be 1-32 chars: letters, digits, dot, dash, underscore; must start alphanumeric")
	}
	return nil
}

// Validate checks a VM specification for internal consistency. It deliberately
// does not check against host capabilities; see CheckAgainstHost.
func (v *VM) Validate() error {
	if err := ValidateName(v.Name); err != nil {
		return err
	}
	if v.CPUs < 1 || v.CPUs > 128 {
		return fmt.Errorf("cpus must be between 1 and 128, got %d", v.CPUs)
	}
	if v.MemoryMiB < 512 {
		return fmt.Errorf("memory must be at least 512 MiB, got %d", v.MemoryMiB)
	}
	if v.DiskGiB < 4 {
		return fmt.Errorf("disk must be at least 4 GiB, got %d", v.DiskGiB)
	}
	if v.DisplayWidth < 0 || v.DisplayHeight < 0 ||
		(v.DisplayWidth == 0) != (v.DisplayHeight == 0) {
		return fmt.Errorf("display size must be a positive width and height, got %dx%d",
			v.DisplayWidth, v.DisplayHeight)
	}
	if v.SSHPort != 0 && (v.SSHPort < 1024 || v.SSHPort > 65535) {
		return fmt.Errorf("ssh port must be between 1024 and 65535, got %d", v.SSHPort)
	}
	if v.MAC != "" {
		if _, err := net.ParseMAC(v.MAC); err != nil {
			return fmt.Errorf("invalid MAC %q: %w", v.MAC, err)
		}
	}
	switch v.Accel {
	case host.AccelHVF, host.AccelKVM, host.AccelTCG:
	default:
		return fmt.Errorf("unknown accelerator %q", v.Accel)
	}
	if v.CPUModel == "" {
		return errors.New("cpu model is required")
	}
	if v.Machine == "" {
		return errors.New("machine type is required")
	}
	return nil
}

// CheckAgainstHost returns warnings where a spec asks for more than the host
// can comfortably provide. These are advisory: overcommitting is legal and
// sometimes intended, so they never block starting a VM.
func (v *VM) CheckAgainstHost(caps *host.Caps) []string {
	var warns []string
	if caps == nil {
		return nil
	}
	if !caps.HasAccel(v.Accel) {
		warns = append(warns, fmt.Sprintf("accelerator %q is unavailable on this host", v.Accel))
	}
	if caps.HostCPUs > 0 && v.CPUs > caps.HostCPUs {
		warns = append(warns, fmt.Sprintf("%d vCPUs exceeds the host's %d cores; expect contention", v.CPUs, caps.HostCPUs))
	}
	// Leaving less than 2 GiB for the host makes the whole machine unhappy.
	if caps.HostMemMiB > 0 && v.MemoryMiB > caps.HostMemMiB-2048 {
		warns = append(warns, fmt.Sprintf("%d MiB leaves too little for the host (%d MiB total)", v.MemoryMiB, caps.HostMemMiB))
	}
	if v.Display != DisplayNone && !caps.HasDisplay(v.Display) {
		warns = append(warns, fmt.Sprintf("display %q is unavailable in this QEMU build", v.Display))
	}
	if !v.Highmem && v.MemoryMiB > 3072 {
		warns = append(warns, "highmem is off, which caps usable guest RAM near 3 GiB")
	}
	return warns
}

// Defaults produces a tuned specification for the given host. The sizing rules
// aim to leave the host responsive: half the cores and a quarter of RAM, with
// sane floors and ceilings.
func Defaults(name string, caps *host.Caps) VM {
	v := VM{
		Name:    name,
		Machine: "virt",
		// GICv3 is what every modern aarch64 guest expects, it is required for
		// more than 8 vCPUs, and it is the only version HVF supports. The virt
		// machine has offered it since QEMU 2.6, so it is safe unconditionally.
		GICVersion:  "3",
		Highmem:     true,
		Display:     DisplayNone,
		GPU:         false,
		CacheDirect: true,
		IOThread:    true,
		Balloon:     true,
		RNG:         true,
		DiskGiB:     32,
		MAC:         RandomMAC(),
		Created:     time.Now().UTC(),
	}
	v.Fullscreen = true
	v.CaptureAllKeys = true
	// Hardware rendering is worth having whenever the host offers it.
	v.GPUAccel = caps.SupportsGPUAccel()
	v.Venus = caps.SupportsVenus()
	v.DisplayWidth, v.DisplayHeight = DefaultResolution(caps, v.Fullscreen)

	if caps == nil {
		v.CPUs, v.MemoryMiB, v.Accel, v.CPUModel, v.AIO = 2, 2048, host.AccelTCG, "max", "threads"
		return v
	}

	v.Accel = caps.BestAccel()
	v.AIO = caps.BestAIO()

	if v.Accel == host.AccelTCG {
		// TCG cannot pass the host CPU through, and its translation does not
		// scale across many vCPUs; keep the machine small so it stays usable.
		v.CPUModel = "max"
		v.CPUs = clamp(caps.HostCPUs/4, 1, 4)
		v.MemoryMiB = clamp(roundToMiB(caps.HostMemMiB/8), 1024, 4096)
	} else {
		v.CPUModel = "host"
		v.CPUs = clamp(caps.HostCPUs/2, 2, 8)
		v.MemoryMiB = clamp(roundToMiB(caps.HostMemMiB/4), 2048, 16384)
	}

	return v
}

// Bounds for the guest's initial resolution.
const (
	minDisplayWidth, minDisplayHeight = 1280, 800
	maxDisplayWidth, maxDisplayHeight = 3840, 2400

	// screenFill leaves the VM window comfortably inside the desktop rather
	// than covering it edge to edge.
	screenFill = 0.85
)

// DefaultResolution sizes the guest display from the host screen. A fullscreen
// machine takes the whole screen so nothing is letterboxed or scaled; a
// windowed one leaves a margin so the window does not swallow the desktop. It
// falls back to a common laptop resolution when the screen cannot be measured.
func DefaultResolution(caps *host.Caps, fullscreen bool) (w, h int) {
	w, h = 1600, 1000
	if caps != nil && caps.ScreenWidth > 0 && caps.ScreenHeight > 0 {
		fill := screenFill
		if fullscreen {
			fill = 1.0
		}
		w = int(float64(caps.ScreenWidth) * fill)
		h = int(float64(caps.ScreenHeight) * fill)
	}
	w = clamp(w, minDisplayWidth, maxDisplayWidth)
	h = clamp(h, minDisplayHeight, maxDisplayHeight)
	if fullscreen {
		// Rounding here would letterbox the desktop inside its own fullscreen
		// window, so a fullscreen guest matches the screen exactly.
		return w, h
	}
	// Graphics stacks are happiest with dimensions on an 8-pixel boundary.
	return w / 8 * 8, h / 8 * 8
}

// roundToMiB snaps memory down to a 256 MiB boundary so sizes read cleanly.
func roundToMiB(mib int) int { return (mib / 256) * 256 }

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// RandomMAC generates a MAC in QEMU's registered 52:54:00 OUI, which keeps
// guest-visible addresses recognisable and avoids clashing with real NICs.
func RandomMAC() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "52:54:00:12:34:56"
	}
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
}

// FreePort asks the kernel for an unused TCP port on loopback. Callers use it
// to pick an SSH forward that will not collide with anything already running.
func FreePort(preferred int) (int, error) {
	if preferred > 0 && portFree(preferred) {
		return preferred, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocating a free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// Store is the on-disk home for VMs and cached installer images.
type Store struct{ Root string }

// DefaultRoot follows the XDG data directory convention on every platform, so
// VM disks live somewhere predictable and backup-able.
func DefaultRoot() string {
	if d := os.Getenv("MARCH_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "march")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".march"
	}
	return filepath.Join(home, ".local", "share", "march")
}

// NewStore returns a Store rooted at dir, creating the layout if needed. An
// empty dir uses DefaultRoot.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = DefaultRoot()
	}
	s := &Store{Root: dir}
	for _, d := range []string{s.VMsDir(), s.ImagesDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", d, err)
		}
	}
	return s, nil
}

func (s *Store) VMsDir() string    { return filepath.Join(s.Root, "vms") }
func (s *Store) ImagesDir() string { return filepath.Join(s.Root, "images") }

// VMDir is the per-VM directory holding its config, disk, firmware vars and
// runtime sockets.
func (s *Store) VMDir(name string) string { return filepath.Join(s.VMsDir(), name) }

func (s *Store) configPath(name string) string { return filepath.Join(s.VMDir(name), "vm.json") }

// Exists reports whether a VM of this name is already defined.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.configPath(name))
	return err == nil
}

// Save writes the VM specification atomically, so an interrupted write cannot
// leave a half-parsed vm.json behind.
func (s *Store) Save(v VM) error {
	if err := v.Validate(); err != nil {
		return err
	}
	dir := s.VMDir(v.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", v.Name, err)
	}
	data = append(data, '\n')

	tmp := s.configPath(v.Name) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.configPath(v.Name)); err != nil {
		return fmt.Errorf("saving %s: %w", v.Name, err)
	}
	return nil
}

// Load reads one VM specification.
func (s *Store) Load(name string) (VM, error) {
	var v VM
	data, err := os.ReadFile(s.configPath(name))
	if err != nil {
		return v, fmt.Errorf("reading VM %q: %w", name, err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("parsing VM %q: %w", name, err)
	}
	return v, nil
}

// List returns every defined VM, sorted by name. Directories that do not parse
// are skipped rather than failing the whole listing, so one corrupt VM cannot
// make the tool unusable.
func (s *Store) List() ([]VM, error) {
	entries, err := os.ReadDir(s.VMsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing VMs: %w", err)
	}
	var vms []VM
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		v, err := s.Load(e.Name())
		if err != nil {
			continue
		}
		vms = append(vms, v)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}

// Paths locates every file belonging to one VM.
type Paths struct {
	Dir     string
	Config  string
	Disk    string
	EFIVars string
	PIDFile string
	LogFile string

	QMPSocket    string
	SerialSocket string
}

// maxSockPath is the portable ceiling for a unix socket path. sockaddr_un.sun_path
// is 104 bytes on macOS and 108 on Linux; exceeding it fails at bind time with a
// confusing error, so long store paths get their sockets relocated to TMPDIR.
const maxSockPath = 100

// Paths returns the file layout for a VM. Sockets are placed alongside the VM
// unless that would overflow the unix socket path limit.
func (s *Store) Paths(name string) Paths {
	dir := s.VMDir(name)
	p := Paths{
		Dir:          dir,
		Config:       filepath.Join(dir, "vm.json"),
		Disk:         filepath.Join(dir, "disk.qcow2"),
		EFIVars:      filepath.Join(dir, "efi-vars.fd"),
		PIDFile:      filepath.Join(dir, "vm.pid"),
		LogFile:      filepath.Join(dir, "vm.log"),
		QMPSocket:    filepath.Join(dir, "qmp.sock"),
		SerialSocket: filepath.Join(dir, "console.sock"),
	}
	if len(p.QMPSocket) > maxSockPath || len(p.SerialSocket) > maxSockPath {
		alt := filepath.Join(os.TempDir(), "march-"+name)
		p.QMPSocket = alt + "-qmp.sock"
		p.SerialSocket = alt + "-con.sock"
	}
	return p
}

// Delete removes a VM and everything in its directory, including the disk.
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !s.Exists(name) {
		return fmt.Errorf("VM %q does not exist", name)
	}
	if err := os.RemoveAll(s.VMDir(name)); err != nil {
		return fmt.Errorf("deleting %q: %w", name, err)
	}
	return nil
}
