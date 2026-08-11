// Package ui implements march's terminal interface: a VM list, a creation
// wizard, an installer download view, and a per-VM detail screen.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/image"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/qemu"
	"github.com/melvinsh/march/internal/vm"
)

type screen int

const (
	screenProbing screen = iota
	screenList
	screenCreate
	screenDownload
	screenDetail
	screenConfirmDelete
	screenHelp
	screenInstalling
)

// Model is the root Bubble Tea model.
type Model struct {
	screen screen
	keys   KeyMap
	styles Styles
	help   help.Model

	store *config.Store
	mgr   *vm.Manager
	caps  *host.Caps
	dl    *image.Downloader

	// program is needed so background downloads can push progress messages.
	// It is injected after tea.NewProgram, which is why it is settable.
	program *tea.Program

	vms    []config.VM
	status map[string]vm.Status
	cursor int

	create      *createModel
	pendSpec    config.VM
	pendProfile install.Profile

	release image.Release

	installPhase install.Progress
	installLog   []string
	// installPending holds the line the guest is still drawing, so a chunk
	// that cuts a line in half does not become two lines.
	installPending strings.Builder
	installStart   time.Time
	installCancel  context.CancelFunc

	spinner  spinner.Model
	progress progress.Model
	viewport viewport.Model

	dlProgress image.Progress
	dlRelease  image.Release
	dlCancel   context.CancelFunc

	busy     bool
	busyWhat string

	flash    string
	flashErr bool
	flashID  int

	width  int
	height int
	ready  bool
}

// New builds the root model. The program pointer is attached later with
// SetProgram.
func New(store *config.Store) *Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	pr := progress.New(progress.WithDefaultBlend())

	styles := NewStyles(true)
	h := help.New()
	h.Styles = styles.HelpStyles()

	return &Model{
		screen:   screenProbing,
		keys:     DefaultKeyMap(),
		styles:   styles,
		help:     h,
		store:    store,
		dl:       image.NewDownloader(store.ImagesDir()),
		spinner:  sp,
		progress: pr,
		viewport: viewport.New(),
		status:   map[string]vm.Status{},
	}
}

// SetProgram attaches the running program so background work can send messages.
func (m *Model) SetProgram(p *tea.Program) { m.program = p }

// setDark rebuilds the palette for the terminal's actual background. The help
// component holds its own copy of the styles, so it is refreshed too.
func (m *Model) setDark(isDark bool) {
	m.styles = NewStyles(isDark)
	m.help.Styles = m.styles.HelpStyles()
}

// Init starts the host probe and the spinner.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, probeHostCmd())
}

// Update handles all messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.help.SetWidth(msg.Width)
		m.progress.SetWidth(min(msg.Width-8, 60))
		m.viewport.SetWidth(max(msg.Width-4, 20))
		m.viewport.SetHeight(max(msg.Height-8, 5))
		if m.create != nil {
			m.create.Update(msg)
		}
		return m, nil

	case tea.BackgroundColorMsg:
		// Lip Gloss v2 does not probe the terminal; Bubble Tea reports the
		// background once at startup so the palette can be chosen for real
		// rather than assuming a dark theme.
		m.setDark(msg.IsDark())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case capsMsg:
		// A probe error means QEMU is missing or unusable; Problems() renders
		// that on the list screen, so nothing extra is needed here.
		m.caps = msg.caps
		m.mgr = vm.New(m.store, m.caps)
		m.screen = screenList
		// The release index is fetched eagerly so the create wizard can show
		// download sizes without a stall.
		return m, tea.Batch(loadVMsCmd(m.mgr), fetchReleaseCmd(), statusTickCmd())

	case vmsMsg:
		m.vms = msg.vms
		if msg.status != nil {
			m.status = msg.status
		}
		if m.cursor >= len(m.vms) {
			m.cursor = max(len(m.vms)-1, 0)
		}
		if msg.err != nil {
			return m, m.flashError(msg.err)
		}
		return m, nil

	case releaseMsg:
		// A failed index fetch is not fatal here; it is reported when the user
		// actually starts an install.
		m.release = msg.release
		return m, nil

	case installProgressMsg:
		m.installPhase = install.Progress(msg)
		return m, nil

	case installOutputMsg:
		m.appendInstallLog(string(msg))
		return m, nil

	case installDoneMsg:
		return m.onInstallDone(msg)

	case statusTickMsg:
		if m.screen == screenList || m.screen == screenDetail {
			return m, tea.Batch(loadVMsCmd(m.mgr), statusTickCmd())
		}
		return m, statusTickCmd()

	case downloadProgressMsg:
		m.dlProgress = image.Progress(msg)
		return m, m.progress.SetPercent(m.dlProgress.Fraction())

	case downloadDoneMsg:
		return m.onDownloadDone(msg)

	case vmCreatedMsg:
		if msg.err != nil {
			m.busy = false
			m.screen = screenList
			return m, m.flashError(msg.err)
		}
		// The machine exists; hand straight over to the unattended install.
		ctx, cancel := context.WithCancel(context.Background())
		m.installCancel = cancel
		m.installPhase = install.Progress{Message: "Starting the installer"}
		return m, tea.Batch(
			m.spinner.Tick,
			installCmd(m.program, m.mgr, msg.name, m.pendProfile, ctx),
		)

	case opDoneMsg:
		m.busy = false
		if msg.err != nil {
			return m, m.flashError(msg.err)
		}
		return m, tea.Batch(
			loadVMsCmd(m.mgr),
			m.flashInfo(fmt.Sprintf("%s %s", msg.name, msg.verb)),
		)

	case flashExpiredMsg:
		if msg.id == m.flashID {
			m.flash, m.flashErr = "", false
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd
	}

	// Anything unclaimed goes to the active sub-component.
	return m.forwardToScreen(msg)
}

func (m *Model) forwardToScreen(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenCreate:
		if m.create != nil {
			return m.advanceCreate(m.create.Update(msg))
		}
	case screenDetail:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The create wizard owns the keyboard while it is up, apart from a hard
	// escape, so typing a VM name cannot trigger global shortcuts.
	if m.screen == screenCreate && m.create != nil {
		return m.advanceCreate(m.create.Update(msg))
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.screen == screenDownload && m.dlCancel != nil {
			m.dlCancel()
		}
		if m.screen == screenInstalling && m.installCancel != nil {
			m.installCancel()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		if m.screen == screenHelp {
			m.screen = screenList
		} else if m.screen == screenList {
			m.screen = screenHelp
		}
		return m, nil

	case key.Matches(msg, m.keys.Back):
		switch m.screen {
		case screenDetail, screenHelp, screenConfirmDelete:
			m.screen = screenList
		case screenDownload:
			if m.dlCancel != nil {
				m.dlCancel()
			}
			m.screen = screenList
		case screenInstalling:
			if m.installCancel != nil {
				m.installCancel()
			}
			m.screen = screenList
		}
		return m, nil
	}

	switch m.screen {
	case screenList:
		return m.handleListKey(msg)
	case screenConfirmDelete:
		return m.handleConfirmKey(msg)
	case screenDetail:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.cursor < len(m.vms)-1 {
			m.cursor++
		}
	case key.Matches(msg, m.keys.Refresh):
		return m, loadVMsCmd(m.mgr)

	case key.Matches(msg, m.keys.New):
		if m.caps == nil || !m.caps.Ready() {
			return m, m.flashErrorText("Cannot create a VM: " + strings.Join(m.caps.Problems(), "; "))
		}
		m.create = newCreateModel(m.caps, m.vmNames())
		m.screen = screenCreate
		cmd := m.create.Init()
		if m.width > 0 {
			m.create.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
		return m, cmd

	case key.Matches(msg, m.keys.Enter):
		if v, ok := m.selected(); ok {
			m.screen = screenDetail
			m.viewport.SetContent(m.detailContent(v))
			return m, nil
		}

	case key.Matches(msg, m.keys.Start):
		if v, ok := m.selected(); ok {
			if m.status[v.Name].State == vm.StateRunning {
				return m, m.flashErrorText(v.Name + " is already running")
			}
			m.busy, m.busyWhat = true, "Starting "+v.Name
			return m, tea.Batch(m.spinner.Tick, startVMCmd(m.mgr, v.Name))
		}

	case key.Matches(msg, m.keys.Stop):
		if v, ok := m.selected(); ok {
			if m.status[v.Name].State == vm.StateStopped {
				return m, m.flashErrorText(v.Name + " is not running")
			}
			m.busy, m.busyWhat = true, "Shutting down "+v.Name
			return m, tea.Batch(m.spinner.Tick, stopVMCmd(m.mgr, v.Name))
		}

	case key.Matches(msg, m.keys.Restart):
		if v, ok := m.selected(); ok {
			m.busy, m.busyWhat = true, "Restarting "+v.Name
			return m, tea.Batch(m.spinner.Tick, restartVMCmd(m.mgr, v.Name))
		}

	case key.Matches(msg, m.keys.Delete):
		if _, ok := m.selected(); ok {
			m.screen = screenConfirmDelete
		}

	case key.Matches(msg, m.keys.Console):
		if v, ok := m.selected(); ok {
			return m, m.flashInfo(m.mgr.ConsoleCommand(v.Name))
		}

	case key.Matches(msg, m.keys.Command):
		if v, ok := m.selected(); ok {
			m.screen = screenDetail
			m.viewport.SetContent(m.commandContent(v))
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) handleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		v, ok := m.selected()
		if !ok {
			m.screen = screenList
			return m, nil
		}
		m.screen = screenList
		m.busy, m.busyWhat = true, "Deleting "+v.Name
		return m, tea.Batch(m.spinner.Tick, deleteVMCmd(m.mgr, v.Name))
	case "n", "N", "esc":
		m.screen = screenList
	}
	return m, nil
}

// advanceCreate passes a command back from the wizard and reacts if the form
// has finished.
//
// huh finishes asynchronously: the final enter returns a command whose message
// is what actually sets the completed state. The state therefore has to be
// re-checked after every message the wizard sees, not only after key presses —
// otherwise the app sits on a finished form, which renders as nothing at all.
func (m *Model) advanceCreate(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.create == nil {
		return m, cmd
	}
	switch m.create.State() {
	case huh.StateCompleted:
		return m.onCreateSubmitted()
	case huh.StateAborted:
		m.create = nil
		m.screen = screenList
		return m, nil
	}
	return m, cmd
}

// onCreateSubmitted starts the pipeline the wizard described: fetch the
// installer if it is not cached, create the machine, install it unattended,
// then boot it into its desktop.
func (m *Model) onCreateSubmitted() (tea.Model, tea.Cmd) {
	c := m.create
	m.create = nil
	if c == nil || !c.Confirmed() {
		m.screen = screenList
		return m, nil
	}

	m.pendSpec = c.Spec()
	m.pendProfile = c.InstallProfile()

	if m.release.Filename == "" {
		m.screen = screenList
		return m, m.flashErrorText(
			"Could not reach the Archboot release index; check your network and try again")
	}

	// A cached installer skips straight to creating the machine.
	if m.dl.Cached(m.release) {
		return m.beginCreate(m.dl.Path(m.release))
	}

	m.dlProgress = image.Progress{Total: m.release.SizeHint}
	m.screen = screenDownload

	ctx, cancel := context.WithCancel(context.Background())
	m.dlCancel = cancel
	return m, tea.Batch(
		m.spinner.Tick,
		m.progress.SetPercent(0),
		downloadCmd(m.program, m.dl, m.release, ctx),
	)
}

func (m *Model) beginCreate(iso string) (tea.Model, tea.Cmd) {
	m.screen = screenInstalling
	m.installLog = nil
	m.installStart = time.Now()
	m.installPhase = install.Progress{Message: "Creating the virtual machine"}
	return m, tea.Batch(m.spinner.Tick, createVMCmd(m.mgr, m.pendSpec, iso))
}

func (m *Model) onDownloadDone(msg downloadDoneMsg) (tea.Model, tea.Cmd) {
	if m.dlCancel != nil {
		m.dlCancel()
		m.dlCancel = nil
	}
	if msg.err != nil {
		m.screen = screenList
		return m, m.flashError(msg.err)
	}
	return m.beginCreate(msg.path)
}

// onInstallDone finishes the pipeline by booting the freshly installed system.
func (m *Model) onInstallDone(msg installDoneMsg) (tea.Model, tea.Cmd) {
	if m.installCancel != nil {
		m.installCancel()
		m.installCancel = nil
	}
	if msg.err != nil {
		m.screen = screenList
		return m, tea.Batch(loadVMsCmd(m.mgr), m.flashError(msg.err))
	}

	m.screen = screenList
	m.busy, m.busyWhat = true, "Booting "+msg.name+" into its desktop"
	return m, tea.Batch(
		m.spinner.Tick,
		loadVMsCmd(m.mgr),
		startVMCmd(m.mgr, msg.name),
	)
}

// Bounds for the live install log.
const (
	installLogLines   = 400  // scrollback kept in memory
	installLogLineMax = 2048 // bytes per line, before display truncation
)

// appendInstallLog folds a chunk of console output into the live log.
//
// It cannot simply split on newlines. Output arrives in socket-sized chunks
// that cut lines at arbitrary points, so a naive split invents line breaks
// mid-word. And pacman animates progress bars by returning to the start of the
// line with a bare carriage return, which would otherwise pile up hundreds of
// near-identical lines. So a partial line is held back until it is terminated,
// and a lone CR discards the partial redraw it replaces.
func (m *Model) appendInstallLog(chunk string) {
	for i := 0; i < len(chunk); i++ {
		switch chunk[i] {
		case '\n':
			m.flushInstallLine()
		case '\r':
			// CRLF is an ordinary line ending; only a lone CR is a redraw.
			if i+1 < len(chunk) && chunk[i+1] == '\n' {
				continue
			}
			m.installPending.Reset()
		default:
			// Bounding the pending line stops a guest that never emits a
			// newline from growing the buffer without limit.
			if m.installPending.Len() < installLogLineMax {
				m.installPending.WriteByte(chunk[i])
			}
		}
	}
}

// flushInstallLine commits the pending line to the log.
func (m *Model) flushInstallLine() {
	line := strings.TrimRight(m.installPending.String(), " \t")
	m.installPending.Reset()
	if strings.TrimSpace(line) == "" {
		return
	}
	// A byte-wise cap can land mid-rune; drop any resulting invalid bytes.
	m.installLog = append(m.installLog, strings.ToValidUTF8(line, ""))
	if len(m.installLog) > installLogLines {
		m.installLog = m.installLog[len(m.installLog)-installLogLines:]
	}
}

// installLines returns the log plus whatever is currently being drawn, so a
// progress bar mid-update is still visible.
func (m *Model) installLines() []string {
	pending := strings.TrimRight(m.installPending.String(), " \t")
	if strings.TrimSpace(pending) == "" {
		return m.installLog
	}
	return append(append([]string{}, m.installLog...), strings.ToValidUTF8(pending, ""))
}

func (m *Model) selected() (config.VM, bool) {
	if m.cursor < 0 || m.cursor >= len(m.vms) {
		return config.VM{}, false
	}
	return m.vms[m.cursor], true
}

func (m *Model) vmNames() []string {
	out := make([]string, 0, len(m.vms))
	for _, v := range m.vms {
		out = append(out, v.Name)
	}
	return out
}

func (m *Model) flashInfo(s string) tea.Cmd {
	m.flashID++
	m.flash, m.flashErr = s, false
	return flashExpireCmd(m.flashID)
}

func (m *Model) flashError(err error) tea.Cmd { return m.flashErrorText(err.Error()) }

func (m *Model) flashErrorText(s string) tea.Cmd {
	m.flashID++
	m.flash, m.flashErr = s, true
	return flashExpireCmd(m.flashID)
}

// View renders the active screen.
func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.WindowTitle = "march — Arch ARM on QEMU"

	if !m.ready {
		v.Content = "\n  Starting march…\n"
		return v
	}

	var body string
	switch m.screen {
	case screenProbing:
		body = m.viewProbing()
	case screenCreate:
		body = m.viewCreate()
	case screenDownload:
		body = m.viewDownload()
	case screenInstalling:
		body = m.viewInstalling()
	case screenDetail:
		body = m.viewDetail()
	case screenConfirmDelete:
		body = m.viewConfirmDelete()
	case screenHelp:
		body = m.viewHelp()
	default:
		body = m.viewList()
	}

	v.Content = body
	return v
}

func (m *Model) viewProbing() string {
	return "\n  " + m.spinner.View() + m.styles.Body.Render(" Inspecting QEMU and this host…") + "\n"
}

func (m *Model) viewCreate() string {
	if m.create == nil {
		return ""
	}
	return "\n" + m.create.View()
}

func (m *Model) header() string {
	title := m.styles.Title.Render("march")
	sub := m.styles.Subtitle.Render("Arch ARM on QEMU")
	return "  " + title + "  " + sub
}

// hostLine summarises the host so the user can see what they are working with
// without opening a detail screen.
func (m *Model) hostLine() string {
	if m.caps == nil {
		return ""
	}
	if m.caps.QemuSystem == "" {
		return m.styles.Danger.Render("  QEMU not found — install it with: brew install qemu")
	}
	accel := strings.ToUpper(m.caps.BestAccel())
	if !m.caps.Accelerated() {
		accel = m.styles.Warning.Render("TCG (emulated, slow)")
	} else {
		accel = m.styles.Success.Render(accel)
	}
	parts := []string{
		"qemu " + m.caps.Version,
		accel,
		fmt.Sprintf("%d cores", m.caps.HostCPUs),
		formatMiB(m.caps.HostMemMiB),
		"aio " + m.caps.BestAIO(),
	}
	return m.styles.StatusBar.Render("  " + strings.Join(parts, "  ·  "))
}

func (m *Model) viewList() string {
	var b strings.Builder
	b.WriteString("\n" + m.header() + "\n")
	b.WriteString(m.hostLine() + "\n\n")

	if m.caps != nil {
		for _, p := range m.caps.Problems() {
			if strings.Contains(p, "acceleration") {
				continue // already shown in the host line
			}
			b.WriteString(m.styles.Warning.Render("  ! "+p) + "\n")
		}
	}

	if len(m.vms) == 0 {
		b.WriteString(m.styles.Muted.Render("  No machines yet.") + "\n")
		b.WriteString(m.styles.Body.Render("  Press ") + m.styles.Key.Render("n") +
			m.styles.Body.Render(" to install Arch Linux ARM.") + "\n")
	} else {
		b.WriteString(m.renderTable() + "\n")
	}

	if m.busy {
		b.WriteString("  " + m.spinner.View() + m.styles.Body.Render(" "+m.busyWhat+"…") + "\n")
	} else if m.flash != "" {
		style := m.styles.Success
		if m.flashErr {
			style = m.styles.Danger
		}
		b.WriteString("  " + style.Render(m.flash) + "\n")
	} else {
		b.WriteString("\n")
	}

	b.WriteString("\n  " + m.help.ShortHelpView(m.keys.ShortHelp()) + "\n")
	return b.String()
}

// renderTable lays out the VM list in aligned columns.
func (m *Model) renderTable() string {
	cols := []struct {
		title string
		width int
	}{
		{"NAME", 16}, {"STATE", 10}, {"CPU", 5}, {"MEMORY", 9},
		{"DISK", 7}, {"SSH", 7}, {"INSTALLER", 12},
	}

	// The status indicator is a column in its own right. Both the header and
	// the rows reserve it, or every row sits offset from its column titles.
	const dotWidth = 2

	var head strings.Builder
	head.WriteString(pad("", dotWidth))
	for _, c := range cols {
		head.WriteString(pad(c.title, c.width))
	}
	out := []string{m.styles.Header.Render(head.String())}

	for i, v := range m.vms {
		st := m.status[v.Name]
		cells := []string{
			pad(truncate(v.Name, cols[0].width-1), cols[0].width),
			pad(stateLabel(st.State), cols[1].width),
			pad(fmt.Sprintf("%d", v.CPUs), cols[2].width),
			pad(formatMiB(v.MemoryMiB), cols[3].width),
			pad(fmt.Sprintf("%dG", v.DiskGiB), cols[4].width),
			pad(sshCell(v), cols[5].width),
			pad(installerCell(v), cols[6].width),
		}
		line := pad(stateDot(m.styles, st.State), dotWidth) + strings.Join(cells, "")

		style := m.styles.Row
		if i == m.cursor {
			style = m.styles.RowFocused
		}
		out = append(out, style.Render(line))
	}
	return strings.Join(out, "\n")
}

func (m *Model) viewDownload() string {
	var b strings.Builder
	b.WriteString("\n" + m.header() + "\n\n")
	b.WriteString("  " + m.styles.Bold.Render("Downloading installer") + "\n")
	b.WriteString("  " + m.styles.Muted.Render(shortenISO(m.dlRelease.Filename)) + "\n\n")
	b.WriteString("  " + m.progress.View() + "\n\n")

	p := m.dlProgress
	line := fmt.Sprintf("%s of %s", formatBytes(p.Downloaded), formatBytes(p.Total))
	if p.BytesPerSec > 0 {
		line += fmt.Sprintf("  ·  %s/s", formatBytes(int64(p.BytesPerSec)))
	}
	if eta := p.ETA(); eta > 0 {
		line += fmt.Sprintf("  ·  %s remaining", eta.Round(1e9))
	}
	b.WriteString("  " + m.styles.Muted.Render(line) + "\n\n")
	b.WriteString("  " + m.styles.Muted.Render("esc cancel") + "\n")
	return b.String()
}

// viewInstalling shows the phase checklist and a live tail of the guest's
// console, so a long unattended run never looks like it has hung.
func (m *Model) viewInstalling() string {
	var b strings.Builder
	b.WriteString("\n" + m.header() + "\n\n")
	b.WriteString("  " + m.styles.Bold.Render("Installing "+m.pendSpec.Name) + "  " +
		m.styles.Muted.Render(strings.ToUpper(string(m.pendProfile.Desktop))) + "\n")
	b.WriteString("  " + m.styles.Muted.Render(
		"unattended · "+time.Since(m.installStart).Round(time.Second).String()+" elapsed") + "\n\n")

	current := m.installPhase.Phase
	currentIdx := m.installPhase.Index
	for i, ph := range install.Phases {
		var mark, text string
		switch {
		case currentIdx > i+1 || (m.installPhase.Index == len(install.Phases) && current == ""):
			mark = m.styles.Success.Render("✓")
			text = m.styles.Muted.Render(ph.Label())
		case ph == current:
			mark = m.styles.Accent.Render(m.spinner.View())
			text = m.styles.Bold.Render(ph.Label())
		default:
			mark = m.styles.Muted.Render("·")
			text = m.styles.Muted.Render(ph.Label())
		}
		b.WriteString("  " + mark + " " + text + "\n")
	}

	if current == "" && m.installPhase.Message != "" {
		b.WriteString("\n  " + m.spinner.View() + " " +
			m.styles.Body.Render(m.installPhase.Message) + "\n")
	}

	b.WriteString("\n" + m.installLogView() + "\n")
	b.WriteString("  " + m.styles.Muted.Render("esc cancel") + "\n")
	return b.String()
}

// installLogView renders the last few console lines inside a panel.
func (m *Model) installLogView() string {
	rows := 8
	if m.height > 0 {
		rows = max(min(m.height-20, 14), 4)
	}
	log := m.installLines()
	start := max(len(log)-rows, 0)

	// The panel's Width covers its own padding, so text has two columns less.
	// Truncating here is what keeps a long pacman progress line from wrapping
	// and pushing the box apart.
	width := max(min(m.width-6, 96), 20)
	var lines []string
	for _, l := range log[start:] {
		lines = append(lines, truncate(l, width-2))
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	body := m.styles.Muted.Render(strings.Join(lines, "\n"))
	return indent(m.styles.Panel.Width(width).Render(body), 2)
}

func (m *Model) viewDetail() string {
	var b strings.Builder
	b.WriteString("\n" + m.header() + "\n\n")
	b.WriteString(m.viewport.View() + "\n")
	b.WriteString("  " + m.styles.Muted.Render("↑/↓ scroll  ·  esc back") + "\n")
	return b.String()
}

func (m *Model) viewConfirmDelete() string {
	v, ok := m.selected()
	if !ok {
		return m.viewList()
	}
	box := m.styles.Panel.Render(strings.Join([]string{
		m.styles.Danger.Render("Delete " + v.Name + "?"),
		"",
		m.styles.Body.Render("This erases the virtual disk and everything on it."),
		m.styles.Muted.Render(m.store.VMDir(v.Name)),
		"",
		m.styles.Body.Render("y  delete      n  cancel"),
	}, "\n"))
	return "\n" + m.header() + "\n\n" + indent(box, 2) + "\n"
}

func (m *Model) viewHelp() string {
	return "\n" + m.header() + "\n\n" +
		indent(m.help.FullHelpView(m.keys.FullHelp()), 2) + "\n\n" +
		m.styles.Muted.Render("  ? or esc to close") + "\n"
}

// detailContent renders everything known about a VM.
func (m *Model) detailContent(v config.VM) string {
	st := m.status[v.Name]
	p := m.store.Paths(v.Name)

	rows := [][2]string{
		{"State", string(st.State) + detailSuffix(st)},
		{"vCPUs", fmt.Sprintf("%d", v.CPUs)},
		{"Memory", formatMiB(v.MemoryMiB)},
		{"Disk", fmt.Sprintf("%d GiB  (%s)", v.DiskGiB, p.Disk)},
		{"Accelerator", v.Accel + "  ·  cpu " + v.CPUModel},
		{"Machine", v.Machine + "  ·  GIC v" + v.GICVersion + "  ·  highmem " + onOff(v.Highmem)},
		{"Storage", "virtio-blk  ·  aio " + v.AIO + "  ·  O_DIRECT " + onOff(v.CacheDirect) +
			"  ·  iothread " + onOff(v.IOThread)},
		{"Console", v.Display},
		{"Network", "user-mode  ·  mac " + v.MAC},
		{"SSH", m.mgr.SSHCommand(v, "root")},
		{"Serial", m.mgr.ConsoleCommand(v.Name)},
		{"Installed", yesNo(v.Installed)},
		{"Created", v.Created.Local().Format("2006-01-02 15:04")},
	}
	if v.Desktop != "" {
		rows = append(rows, [2]string{"Desktop", strings.ToUpper(v.Desktop)})
	}
	if v.Username != "" {
		rows = append(rows, [2]string{"Account", v.Username + "  (autologin)"})
	}
	if v.ISOPath != "" {
		rows = append(rows, [2]string{"Installer", v.ISOPath})
	}
	if st.PID > 0 {
		rows = append(rows, [2]string{"PID", fmt.Sprintf("%d", st.PID)})
	}

	var b strings.Builder
	b.WriteString(m.styles.Bold.Render("  "+v.Name) + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + m.styles.Muted.Render(pad(r[0], 14)) + m.styles.Body.Render(r[1]) + "\n")
	}

	if warns := v.CheckAgainstHost(m.caps); len(warns) > 0 {
		b.WriteString("\n" + m.styles.Warning.Render("  Warnings") + "\n")
		for _, w := range warns {
			b.WriteString(m.styles.Warning.Render("  ! "+w) + "\n")
		}
	}

	b.WriteString("\n" + m.styles.Muted.Render("  Press y on the list to see the full QEMU command line.") + "\n")
	return b.String()
}

// commandContent shows the exact QEMU invocation, which makes the tuning
// visible and lets a user run it by hand.
func (m *Model) commandContent(v config.VM) string {
	p := m.store.Paths(v.Name)
	args, err := qemu.Build(v, m.caps, p, qemu.BuildOptions{})
	if err != nil {
		return m.styles.Danger.Render("  " + err.Error())
	}

	var b strings.Builder
	b.WriteString(m.styles.Bold.Render("  QEMU command for "+v.Name) + "\n\n")
	b.WriteString("  " + m.styles.Accent.Render(m.caps.QemuSystem) + " \\\n")
	for i := 0; i < len(args); i++ {
		line := args[i]
		// Pair each flag with its value so the output reads like a script.
		if strings.HasPrefix(line, "-") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			line += " " + args[i+1]
			i++
		}
		cont := " \\"
		if i >= len(args)-1 {
			cont = ""
		}
		b.WriteString("    " + m.styles.Body.Render(line) + cont + "\n")
	}
	return b.String()
}

func detailSuffix(st vm.Status) string {
	if st.Detail != "" && st.Detail != string(st.State) {
		return "  (" + st.Detail + ")"
	}
	return ""
}

func stateLabel(s vm.State) string {
	if s == "" {
		return string(vm.StateStopped)
	}
	return string(s)
}

// stateDot renders the one-glyph status indicator for a VM.
func stateDot(st Styles, s vm.State) string {
	switch s {
	case vm.StateRunning:
		return st.Success.Render("●")
	case vm.StatePaused:
		return st.Warning.Render("◐")
	case vm.StateUnknown:
		return st.Warning.Render("?")
	default:
		return st.Muted.Render("○")
	}
}

func sshCell(v config.VM) string {
	if v.SSHPort == 0 {
		return "—"
	}
	return fmt.Sprintf(":%d", v.SSHPort)
}

func installerCell(v config.VM) string {
	if v.Installed {
		return "installed"
	}
	if v.ISOPath == "" {
		return "—"
	}
	return "pending"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// indent shifts a block right by n spaces.
func indent(s string, n int) string {
	prefix := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
