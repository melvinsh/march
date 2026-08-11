package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/qemu"
)

// createModel is the new-VM wizard. It collects answers as strings because
// that is what huh binds to, then converts them into a validated config.VM.
type createModel struct {
	form *huh.Form
	caps *host.Caps

	// Bound form values.
	name     string
	username string
	password string
	cpus     string
	memory   string
	disk     string
	confirm  bool

	highmem  bool
	ioThread bool
}

// cpuChoices offers a sensible ladder up to the host's core count, always
// including the tuned default so the recommended value is selectable.
func cpuChoices(caps *host.Caps, def int) []huh.Option[string] {
	ladder := []int{1, 2, 4, 6, 8, 12, 16}
	max := 8
	if caps != nil && caps.HostCPUs > 0 {
		max = caps.HostCPUs
	}

	seen := map[int]bool{}
	var opts []huh.Option[string]
	for _, n := range ladder {
		if n > max || seen[n] {
			continue
		}
		seen[n] = true
		label := fmt.Sprintf("%d vCPU", n)
		if n == def {
			label += "  (recommended)"
		}
		opts = append(opts, huh.NewOption(label, strconv.Itoa(n)))
	}
	if !seen[def] && def > 0 {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%d vCPU  (recommended)", def), strconv.Itoa(def)))
	}
	return opts
}

// memChoices offers memory sizes that leave at least 2 GiB for the host.
func memChoices(caps *host.Caps, def int) []huh.Option[string] {
	ladder := []int{1024, 2048, 4096, 8192, 12288, 16384, 24576, 32768}
	max := 8192
	if caps != nil && caps.HostMemMiB > 2048 {
		max = caps.HostMemMiB - 2048
	}

	seen := map[int]bool{}
	var opts []huh.Option[string]
	for _, n := range ladder {
		if n > max || seen[n] {
			continue
		}
		seen[n] = true
		label := formatMiB(n)
		if n == def {
			label += "  (recommended)"
		}
		opts = append(opts, huh.NewOption(label, strconv.Itoa(n)))
	}
	if !seen[def] && def > 0 {
		opts = append(opts, huh.NewOption(formatMiB(def)+"  (recommended)", strconv.Itoa(def)))
	}
	return opts
}

func displayChoices(caps *host.Caps) []huh.Option[string] {
	opts := []huh.Option[string]{
		huh.NewOption("Headless — serial console only (fastest)", config.DisplayNone),
	}
	if windowed := qemu.DefaultDisplay(caps); windowed != config.DisplayNone {
		opts = append(opts, huh.NewOption("Graphical window ("+windowed+")", windowed))
	}
	if caps != nil && caps.HasDisplay(config.DisplayCurses) {
		opts = append(opts, huh.NewOption("In-terminal text console (curses)", config.DisplayCurses))
	}
	return opts
}

func newCreateModel(caps *host.Caps, existing []string) *createModel {
	def := config.Defaults("", caps)

	m := &createModel{
		caps:     caps,
		cpus:     strconv.Itoa(def.CPUs),
		memory:   strconv.Itoa(def.MemoryMiB),
		disk:     strconv.Itoa(def.DiskGiB),
		highmem:  def.Highmem,
		ioThread: def.IOThread,
		name:     suggestName(existing),
		username: "arch",
	}

	taken := map[string]bool{}
	for _, n := range existing {
		taken[n] = true
	}

	accelNote := "TCG software emulation (slow)"
	if caps != nil && caps.Accelerated() {
		accelNote = strings.ToUpper(caps.BestAccel()) + " hardware acceleration"
	}

	m.form = huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("New Arch Linux ARM machine").
				Description(fmt.Sprintf(
					"march installs Arch unattended and boots into a desktop.\nQEMU will use %s.", accelNote)),

			huh.NewInput().
				Key("name").
				Title("Machine name").
				Value(&m.name).
				Validate(func(s string) error {
					if err := config.ValidateName(s); err != nil {
						return err
					}
					if taken[s] {
						return fmt.Errorf("a VM named %q already exists", s)
					}
					return nil
				}),
		),

		huh.NewGroup(
			huh.NewInput().
				Key("username").
				Title("Username").
				Description("Logged in automatically when the desktop starts").
				Value(&m.username).
				Validate(func(s string) error {
					p := install.DefaultProfile("x")
					p.Username = s
					p.Password = "placeholder"
					if err := p.Validate(); err != nil {
						return err
					}
					return nil
				}),

			huh.NewInput().
				Key("password").
				Title("Password").
				Description("For this account and for root").
				EchoMode(huh.EchoModePassword).
				Value(&m.password).
				Validate(func(s string) error {
					if len(s) < 4 {
						return fmt.Errorf("use at least 4 characters")
					}
					if strings.ContainsAny(s, "\n\r") {
						return fmt.Errorf("no newlines")
					}
					return nil
				}),
		),

		huh.NewGroup(
			huh.NewSelect[string]().
				Key("cpus").
				Title("vCPUs").
				Options(cpuChoices(caps, def.CPUs)...).
				Value(&m.cpus),

			huh.NewSelect[string]().
				Key("memory").
				Title("Memory").
				Options(memChoices(caps, def.MemoryMiB)...).
				Value(&m.memory),

			huh.NewInput().
				Key("disk").
				Title("Disk size (GiB)").
				Description("Sparse qcow2 — a desktop install needs roughly 12 GiB").
				Value(&m.disk).
				Validate(func(s string) error {
					n, err := strconv.Atoi(strings.TrimSpace(s))
					if err != nil {
						return fmt.Errorf("enter a whole number of GiB")
					}
					if n < 16 {
						return fmt.Errorf("a desktop install needs at least 16 GiB")
					}
					if n > 4096 {
						return fmt.Errorf("that is unreasonably large")
					}
					return nil
				}),

			huh.NewConfirm().
				Key("confirm").
				Title("Install now?").
				Description("Downloads the installer, then installs Arch unattended").
				Affirmative("Install").
				Negative("Cancel").
				Value(&m.confirm),
		),
	).WithShowHelp(true).WithShowErrors(true)

	return m
}

// Spec converts the collected answers into a VM specification.
func (m *createModel) Spec() config.VM {
	v := config.Defaults(m.name, m.caps)
	if n, err := strconv.Atoi(strings.TrimSpace(m.cpus)); err == nil {
		v.CPUs = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.memory)); err == nil {
		v.MemoryMiB = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.disk)); err == nil {
		v.DiskGiB = n
	}
	// The whole point is to land on a desktop, so the machine gets a window
	// and a GPU. The serial console stays available alongside it.
	v.Display = qemu.DefaultDisplay(m.caps)
	v.GPU = v.Display != config.DisplayNone
	v.Highmem = m.highmem
	v.IOThread = m.ioThread
	v.Username = m.username
	return v
}

// InstallProfile is the unattended installation the wizard described.
func (m *createModel) InstallProfile() install.Profile {
	p := install.DefaultProfile(m.name)
	p.Username = m.username
	p.Password = m.password
	p.Autologin = true
	return p
}

// Confirmed reports whether the user chose to install rather than cancel.
func (m *createModel) Confirmed() bool { return m.confirm }

func (m *createModel) Init() tea.Cmd { return m.form.Init() }

func (m *createModel) Update(msg tea.Msg) tea.Cmd {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.form = m.form.WithWidth(min(sz.Width-4, 80)).WithHeight(max(sz.Height-8, 10))
	}
	next, cmd := m.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		m.form = f
	}
	return cmd
}

func (m *createModel) State() huh.FormState { return m.form.State }

func (m *createModel) View() string { return m.form.View() }

// suggestName proposes an unused default name so the form starts valid.
func suggestName(existing []string) string {
	taken := map[string]bool{}
	for _, n := range existing {
		taken[n] = true
	}
	if !taken["arch"] {
		return "arch"
	}
	for i := 2; i < 1000; i++ {
		candidate := "arch" + strconv.Itoa(i)
		if !taken[candidate] {
			return candidate
		}
	}
	return "arch"
}

// shortenISO trims Archboot's long filenames down to something readable.
func shortenISO(name string) string {
	name = strings.TrimSuffix(name, ".iso")
	name = strings.TrimPrefix(name, "archboot-")
	name = strings.ReplaceAll(name, "-aarch64-ARCH", "")
	name = strings.TrimSuffix(name, "-aarch64")
	if len(name) > 40 {
		name = name[:40] + "…"
	}
	return name
}
