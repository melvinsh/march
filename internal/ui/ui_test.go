package ui

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/image"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/vm"
)

func testCaps() *host.Caps {
	return &host.Caps{
		QemuSystem: "/opt/homebrew/bin/qemu-system-aarch64",
		QemuImg:    "/opt/homebrew/bin/qemu-img",
		Firmware:   "/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		Version:    "11.0.3",
		Accels:     []string{"hvf", "tcg"},
		AIOModes:   []string{"threads"},
		Displays:   []string{"none", "cocoa", "curses"},
		Devices: map[string]bool{
			"virtio-blk-pci": true, "virtio-net-pci": true, "virtio-rng-pci": true,
			"virtio-balloon-pci": true, "virtio-scsi-pci": true, "scsi-cd": true,
			"virtio-gpu-pci": true, "qemu-xhci": true, "usb-kbd": true, "usb-tablet": true,
		},
		HostCPUs:   10,
		HostMemMiB: 32768,
		Arch:       "arm64",
		OS:         "darwin",
	}
}

// keyPress builds the message Bubble Tea delivers for a plain character key.
func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// newTestModel returns a model wound forward to the list screen with the
// supplied VMs, which is where most interaction happens.
func newTestModel(t *testing.T, vms ...config.VM) *Model {
	t.Helper()
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vms {
		if err := store.Save(v); err != nil {
			t.Fatal(err)
		}
	}

	m := New(store)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(capsMsg{caps: testCaps()})

	status := map[string]vm.Status{}
	for _, v := range vms {
		status[v.Name] = vm.Status{Name: v.Name, State: vm.StateStopped}
	}
	m.Update(vmsMsg{vms: vms, status: status})
	return m
}

func TestModelStartsOnListAfterProbe(t *testing.T) {
	m := newTestModel(t)
	if m.screen != screenList {
		t.Errorf("screen = %v, want the list after probing", m.screen)
	}

	content := m.View().Content
	if !strings.Contains(content, "march") {
		t.Error("the header is missing from the list view")
	}
	if !strings.Contains(content, "No machines yet") {
		t.Error("an empty list should tell the user how to start")
	}
}

func TestViewUsesAltScreen(t *testing.T) {
	m := newTestModel(t)
	v := m.View()
	if !v.AltScreen {
		t.Error("the TUI should run in the alternate screen buffer")
	}
	if v.WindowTitle == "" {
		t.Error("the window title should be set")
	}
}

func TestHostLineShowsAcceleration(t *testing.T) {
	m := newTestModel(t)
	content := m.View().Content

	if !strings.Contains(content, "11.0.3") {
		t.Error("the host line should show the QEMU version")
	}
	if !strings.Contains(content, "HVF") {
		t.Error("the host line should show the accelerator in use")
	}
}

// A host without acceleration must be called out, because it changes what the
// user should expect from performance.
func TestHostLineWarnsAboutTCG(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	caps := testCaps()
	caps.Accels = []string{"tcg"}

	m := New(store)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(capsMsg{caps: caps})

	content := m.View().Content
	if !strings.Contains(strings.ToLower(content), "emulated") {
		t.Errorf("a TCG-only host should be flagged as emulated; got:\n%s", content)
	}
}

func TestMissingQemuIsReported(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(store)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(capsMsg{caps: &host.Caps{HostCPUs: 8, HostMemMiB: 16384, Arch: "arm64"}})

	content := m.View().Content
	if !strings.Contains(content, "brew install qemu") {
		t.Errorf("a host without QEMU should be told how to install it; got:\n%s", content)
	}
}

func TestListRendersVMs(t *testing.T) {
	v1 := config.Defaults("alpha", testCaps())
	v1.SSHPort = 2222
	v2 := config.Defaults("beta", testCaps())
	v2.SSHPort = 2223
	v2.Installed = true

	m := newTestModel(t, v1, v2)
	content := m.View().Content

	for _, want := range []string{"alpha", "beta", "NAME", "STATE", ":2222", "installed"} {
		if !strings.Contains(content, want) {
			t.Errorf("the list view is missing %q; got:\n%s", want, content)
		}
	}
}

func TestCursorMovement(t *testing.T) {
	m := newTestModel(t,
		config.Defaults("a", testCaps()),
		config.Defaults("b", testCaps()),
		config.Defaults("c", testCaps()),
	)

	if m.cursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.cursor)
	}

	m.Update(keyPress('j'))
	m.Update(keyPress('j'))
	if m.cursor != 2 {
		t.Errorf("cursor = %d after two downs, want 2", m.cursor)
	}

	// The cursor must not run off the end of the list.
	m.Update(keyPress('j'))
	if m.cursor != 2 {
		t.Errorf("cursor = %d, want it clamped at the last row", m.cursor)
	}

	m.Update(keyPress('k'))
	if m.cursor != 1 {
		t.Errorf("cursor = %d after an up, want 1", m.cursor)
	}

	m.Update(keyPress('k'))
	m.Update(keyPress('k'))
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want it clamped at the first row", m.cursor)
	}
}

// Deleting the last VM must not leave the cursor pointing past the end.
func TestCursorClampsWhenListShrinks(t *testing.T) {
	m := newTestModel(t,
		config.Defaults("a", testCaps()),
		config.Defaults("b", testCaps()),
	)
	m.Update(keyPress('j'))
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	m.Update(vmsMsg{vms: []config.VM{config.Defaults("a", testCaps())}})
	if m.cursor != 0 {
		t.Errorf("cursor = %d after the list shrank, want 0", m.cursor)
	}

	// Rendering must not panic with an out-of-range cursor.
	_ = m.View().Content
}

func TestEnterOpensDetail(t *testing.T) {
	v := config.Defaults("alpha", testCaps())
	v.SSHPort = 2222
	m := newTestModel(t, v)

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.screen != screenDetail {
		t.Fatalf("screen = %v, want the detail view", m.screen)
	}

	content := m.View().Content
	for _, want := range []string{"alpha", "virtio-blk", "iothread"} {
		if !strings.Contains(content, want) {
			t.Errorf("the detail view is missing %q", want)
		}
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.screen != screenList {
		t.Errorf("escape did not return to the list, screen = %v", m.screen)
	}
}

func TestShowQemuCommand(t *testing.T) {
	v := config.Defaults("alpha", testCaps())
	v.Installed = true
	m := newTestModel(t, v)

	m.Update(keyPress('y'))
	if m.screen != screenDetail {
		t.Fatalf("screen = %v, want the detail view", m.screen)
	}

	content := m.View().Content
	for _, want := range []string{"qemu-system-aarch64", "-machine", "accel=hvf", "virtio-blk-pci"} {
		if !strings.Contains(content, want) {
			t.Errorf("the command view is missing %q; got:\n%s", want, content)
		}
	}
}

func TestHelpToggles(t *testing.T) {
	m := newTestModel(t)

	m.Update(keyPress('?'))
	if m.screen != screenHelp {
		t.Fatalf("screen = %v, want help", m.screen)
	}
	if !strings.Contains(m.View().Content, "new VM") {
		t.Error("the help view should list the bindings")
	}

	m.Update(keyPress('?'))
	if m.screen != screenList {
		t.Errorf("? did not close help, screen = %v", m.screen)
	}
}

func TestDeleteAsksForConfirmation(t *testing.T) {
	m := newTestModel(t, config.Defaults("doomed", testCaps()))

	m.Update(keyPress('d'))
	if m.screen != screenConfirmDelete {
		t.Fatalf("screen = %v, want the confirmation prompt", m.screen)
	}

	content := m.View().Content
	if !strings.Contains(content, "doomed") {
		t.Error("the prompt should name the VM being deleted")
	}
	if !strings.Contains(strings.ToLower(content), "erases the virtual disk") {
		t.Error("the prompt should say the disk will be destroyed")
	}

	// Declining must leave the VM alone.
	m.Update(keyPress('n'))
	if m.screen != screenList {
		t.Errorf("declining did not return to the list, screen = %v", m.screen)
	}
	if !m.store.Exists("doomed") {
		t.Error("the VM was deleted despite the user declining")
	}
}

// Deleting with nothing selected must not fall through to a confirmation.
func TestDeleteOnEmptyListDoesNothing(t *testing.T) {
	m := newTestModel(t)
	m.Update(keyPress('d'))
	if m.screen != screenList {
		t.Errorf("screen = %v, want to stay on the empty list", m.screen)
	}
}

func TestStartAndStopGuards(t *testing.T) {
	v := config.Defaults("alpha", testCaps())
	m := newTestModel(t, v)

	// The VM is stopped, so stopping it should flash an error rather than run.
	m.Update(keyPress('x'))
	if !m.flashErr || !strings.Contains(m.flash, "not running") {
		t.Errorf("stopping a stopped VM gave flash %q (err=%v)", m.flash, m.flashErr)
	}

	// Now mark it running and check the reverse guard.
	m.Update(vmsMsg{
		vms:    []config.VM{v},
		status: map[string]vm.Status{"alpha": {Name: "alpha", State: vm.StateRunning, PID: 123}},
	})
	m.Update(keyPress('s'))
	if !m.flashErr || !strings.Contains(m.flash, "already running") {
		t.Errorf("starting a running VM gave flash %q (err=%v)", m.flash, m.flashErr)
	}
}

func TestFlashExpires(t *testing.T) {
	m := newTestModel(t)
	m.flashInfo("hello")
	if m.flash != "hello" {
		t.Fatalf("flash = %q", m.flash)
	}

	// A stale expiry from an earlier message must not clear a newer one.
	staleID := m.flashID
	m.flashInfo("newer")
	m.Update(flashExpiredMsg{id: staleID})
	if m.flash != "newer" {
		t.Errorf("flash = %q, a stale expiry cleared the current message", m.flash)
	}

	m.Update(flashExpiredMsg{id: m.flashID})
	if m.flash != "" {
		t.Errorf("flash = %q, want it cleared", m.flash)
	}
}

func TestNewVMBlockedWithoutQemu(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(store)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(capsMsg{caps: &host.Caps{HostCPUs: 8, HostMemMiB: 16384, Arch: "arm64"}})

	m.Update(keyPress('n'))
	if m.screen == screenCreate {
		t.Error("the create wizard opened despite QEMU being unavailable")
	}
	if !m.flashErr {
		t.Error("expected an error explaining why a VM cannot be created")
	}
}

func TestNewVMOpensWizard(t *testing.T) {
	m := newTestModel(t)
	m.Update(keyPress('n'))

	if m.screen != screenCreate {
		t.Fatalf("screen = %v, want the create wizard", m.screen)
	}
	if m.create == nil {
		t.Fatal("the wizard model was not built")
	}
	content := m.View().Content
	for _, want := range []string{"Machine name"} {
		if !strings.Contains(content, want) {
			t.Errorf("the wizard is missing the %q field; got:\n%s", want, content)
		}
	}
}

func TestDownloadProgressView(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenDownload
	m.dlRelease = image.Release{Filename: "archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso"}
	m.Update(downloadProgressMsg(image.Progress{
		Downloaded: 100 << 20, Total: 400 << 20, BytesPerSec: 10 << 20,
	}))

	content := m.View().Content
	for _, want := range []string{"Downloading", "100.0 MiB", "400.0 MiB", "MiB/s", "remaining"} {
		if !strings.Contains(content, want) {
			t.Errorf("the download view is missing %q; got:\n%s", want, content)
		}
	}
}

func TestQuitKey(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(keyPress('q'))
	if cmd == nil {
		t.Fatal("q produced no command; it should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not produce a quit message")
	}
}

// Rendering must be safe at every screen and size, including before the first
// window-size message arrives.
func TestViewNeverPanics(t *testing.T) {
	v := config.Defaults("alpha", testCaps())
	v.ISOPath = "/images/x.iso"

	for _, size := range []tea.WindowSizeMsg{
		{Width: 100, Height: 30},
		{Width: 40, Height: 10},
		{Width: 10, Height: 3},
	} {
		m := newTestModel(t, v)
		m.Update(size)
		for _, s := range []screen{
			screenProbing, screenList, screenCreate, screenDownload,
			screenDetail, screenConfirmDelete, screenHelp,
		} {
			m.screen = s
			if s == screenCreate && m.create == nil {
				m.create = newCreateModel(testCaps(), nil)
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("View panicked on screen %v at %dx%d: %v",
							s, size.Width, size.Height, r)
					}
				}()
				_ = m.View().Content
			}()
		}
	}
}

func TestViewBeforeWindowSize(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(store)
	// No WindowSizeMsg yet; View must still produce something.
	if content := m.View().Content; content == "" {
		t.Error("View before the first size message returned nothing")
	}
}

func TestStatusPollingOnlyOnLiveScreens(t *testing.T) {
	m := newTestModel(t)

	m.screen = screenList
	_, cmd := m.Update(statusTickMsg(time.Now()))
	if cmd == nil {
		t.Error("the list screen should keep polling for status")
	}

	// The wizard must not be interrupted by list refreshes, but the tick has
	// to keep rescheduling itself or polling would stop forever.
	m.screen = screenCreate
	_, cmd = m.Update(statusTickMsg(time.Now()))
	if cmd == nil {
		t.Error("the tick must reschedule itself even off the list screen")
	}
}

// The status dot occupies its own column; if it is not accounted for in the
// header, every data row sits offset from its column titles.
func TestTableColumnsAlign(t *testing.T) {
	v1 := config.Defaults("alpha", testCaps())
	v1.SSHPort = 2222
	v2 := config.Defaults("a-much-longer-name", testCaps())
	v2.SSHPort = 2223

	m := newTestModel(t, v1, v2)
	m.status = map[string]vm.Status{
		"alpha":              {Name: "alpha", State: vm.StateRunning},
		"a-much-longer-name": {Name: "a-much-longer-name", State: vm.StateStopped},
	}

	lines := strings.Split(m.renderTable(), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected a header and two rows, got %d lines", len(lines))
	}

	// Columns are display positions, so everything is measured in runes rather
	// than bytes — the status glyph is multi-byte.
	header := []rune(stripANSI(lines[0]))
	nameCol := runeIndex(header, "NAME")
	stateCol := runeIndex(header, "STATE")
	if nameCol < 0 || stateCol < 0 {
		t.Fatalf("header is missing columns: %q", string(header))
	}

	for _, raw := range lines[1:] {
		row := []rune(stripANSI(raw))

		if got := fieldStart(row, nameCol); got != nameCol {
			t.Errorf("row %q starts its name at column %d, want %d\nheader %q",
				string(row), got, nameCol, string(header))
		}
		if got := fieldStart(row, stateCol); got != stateCol {
			t.Errorf("row %q starts its state at column %d, want %d\nheader %q",
				string(row), got, stateCol, string(header))
		}
		if state := fieldAt(row, stateCol); !isState(state) {
			t.Errorf("row %q has %q under the STATE column", string(row), state)
		}
	}
}

func isState(s string) bool {
	switch s {
	case "running", "stopped", "paused", "unknown":
		return true
	}
	return false
}

// runeIndex reports the rune offset of sub within r, or -1.
func runeIndex(r []rune, sub string) int {
	b := strings.Index(string(r), sub)
	if b < 0 {
		return -1
	}
	return len([]rune(string(r)[:b]))
}

// fieldStart returns the column where the field containing col begins, so a
// row shifted left or right of its heading is detected.
func fieldStart(row []rune, col int) int {
	if col >= len(row) {
		return -1
	}
	i := col
	for i > 0 && row[i-1] != ' ' {
		i--
	}
	for i < len(row) && row[i] == ' ' {
		i++
	}
	return i
}

// fieldAt returns the whitespace-delimited token starting at col.
func fieldAt(row []rune, col int) string {
	if col >= len(row) {
		return ""
	}
	end := col
	for end < len(row) && row[end] != ' ' {
		end++
	}
	return strings.TrimSpace(string(row[col:end]))
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestInstallingScreenShowsPhases(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenInstalling
	m.pendSpec = config.Defaults("archbox", testCaps())
	m.pendProfile = install.DefaultProfile("archbox")
	m.installStart = time.Now()
	m.Update(installProgressMsg(install.Progress{
		Phase: install.PhaseDesktop,
		Index: 4, Total: len(install.Phases),
		Message: install.PhaseDesktop.Label(),
	}))
	m.Update(installOutputMsg("installing xfce4-session...\ninstalling mesa...\n"))

	content := m.View().Content

	// Every phase is listed so the user can see what is left to do.
	for _, ph := range install.Phases {
		if !strings.Contains(content, ph.Label()) {
			t.Errorf("the install screen omits phase %q", ph.Label())
		}
	}
	if !strings.Contains(content, "archbox") {
		t.Error("the install screen does not name the machine")
	}
	if !strings.Contains(content, "unattended") {
		t.Error("the install screen should say the install is unattended")
	}
	// Completed phases are ticked, so progress is legible at a glance.
	if !strings.Contains(content, "✓") {
		t.Error("earlier phases are not marked complete")
	}
	// The live console tail is what shows a long install is not stuck.
	if !strings.Contains(content, "installing mesa") {
		t.Errorf("the install screen does not show console output; got:\n%s", content)
	}
}

// The log tail must stay bounded, or a long install would grow the model
// without limit.
func TestInstallLogIsBounded(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 2000; i++ {
		m.appendInstallLog("line " + strconv.Itoa(i) + "\n")
	}
	if len(m.installLog) > 500 {
		t.Errorf("install log grew to %d lines; it should be capped", len(m.installLog))
	}
	// The most recent output must survive.
	last := m.installLog[len(m.installLog)-1]
	if !strings.Contains(last, "1999") {
		t.Errorf("the newest line was dropped; last = %q", last)
	}
}

// A failed install must return the user to the list with the reason shown,
// rather than leaving them on a frozen progress screen.
func TestInstallFailureSurfaces(t *testing.T) {
	m := newTestModel(t)
	m.screen = screenInstalling
	m.Update(installDoneMsg{name: "archbox", err: errors.New("pacstrap exploded")})

	if m.screen != screenList {
		t.Errorf("screen = %v after a failed install, want the list", m.screen)
	}
	if !m.flashErr || !strings.Contains(m.flash, "pacstrap exploded") {
		t.Errorf("the failure was not reported: flash=%q err=%v", m.flash, m.flashErr)
	}
}

// Console output arrives in socket-sized chunks that cut lines at arbitrary
// points. Splitting each chunk on newlines invents line breaks mid-word, which
// is what made the live install log unreadable.
func TestInstallLogJoinsSplitChunks(t *testing.T) {
	m := newTestModel(t)
	m.appendInstallLog("installing xfce4-ses")
	m.appendInstallLog("sion...\n")

	lines := m.installLines()
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want the chunks joined into one: %q", len(lines), lines)
	}
	if lines[0] != "installing xfce4-session..." {
		t.Errorf("line = %q, want the two chunks joined", lines[0])
	}
}

// pacman animates progress by returning to the start of the line with a bare
// carriage return. Each redraw must overwrite the line, not add another one.
func TestInstallLogCollapsesProgressRedraws(t *testing.T) {
	m := newTestModel(t)
	m.appendInstallLog("Total (  0/264)   0%\r")
	m.appendInstallLog("Total (130/264)  49%\r")
	m.appendInstallLog("Total (264/264) 100%\n")

	lines := m.installLines()
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want one line redrawn in place: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "100%") {
		t.Errorf("line = %q, want the final redraw", lines[0])
	}
}

// A serial console sends CRLF; that is an ordinary line ending, not a redraw,
// so the text before it must survive.
func TestInstallLogHandlesCRLF(t *testing.T) {
	m := newTestModel(t)
	m.appendInstallLog("first line\r\nsecond line\r\n")

	lines := m.installLines()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), lines)
	}
	if lines[0] != "first line" || lines[1] != "second line" {
		t.Errorf("lines = %q, want both intact", lines)
	}
}

// A line still being drawn should be visible, so a slow phase does not look
// frozen.
func TestInstallLogShowsPendingLine(t *testing.T) {
	m := newTestModel(t)
	m.appendInstallLog("done\n")
	m.appendInstallLog("downloading mesa")

	lines := m.installLines()
	if len(lines) != 2 || lines[1] != "downloading mesa" {
		t.Errorf("lines = %q, want the in-progress line shown", lines)
	}
	// It must not be committed twice once it finishes.
	m.appendInstallLog("\n")
	if got := m.installLines(); len(got) != 2 {
		t.Errorf("lines = %q, want the pending line committed once", got)
	}
}

// A guest that never emits a newline must not grow the buffer without bound.
func TestInstallLogBoundsPendingLine(t *testing.T) {
	m := newTestModel(t)
	m.appendInstallLog(strings.Repeat("x", 10000))
	if got := m.installPending.Len(); got > installLogLineMax {
		t.Errorf("pending line grew to %d bytes, want it capped at %d", got, installLogLineMax)
	}
}

// The log panel must never render wider than the terminal, or the box breaks
// apart across lines.
func TestInstallLogViewFitsWidth(t *testing.T) {
	for _, width := range []int{40, 80, 100, 200} {
		m := newTestModel(t)
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m.appendInstallLog(strings.Repeat("long-package-name ", 40) + "\n")

		for _, line := range strings.Split(m.installLogView(), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("at terminal width %d a log line rendered %d wide", width, w)
				break
			}
		}
	}
}

// huh completes asynchronously: the final enter returns a command whose
// message sets the completed state. If that state is only checked on key
// presses, the app sits on a finished form — which renders as nothing, so the
// screen goes blank until the user happens to press another key.
func TestWizardCompletesOnNonKeyMessage(t *testing.T) {
	m := newTestModel(t)
	m.release = image.Release{Filename: "archboot-test.iso", URL: "http://example.invalid/x.iso"}

	// The wizard's Init command focuses the first field; without running it the
	// form never accepts input.
	_, cmd := m.Update(keyPress('n'))
	pump(t, m, cmd)
	if m.screen != screenCreate {
		t.Fatalf("screen = %v, want the wizard", m.screen)
	}

	// Advance with huh's own navigation message — the same one the final enter
	// produces — rather than with key presses, which is the path that was
	// missing its completion check.
	next := func() {
		_, c := m.Update(huh.NextField())
		pump(t, m, c)
	}
	typeText := func(s string) {
		for _, r := range s {
			_, c := m.Update(keyPress(r))
			pump(t, m, c)
		}
	}

	next()                          // name -> username
	next()                          // -> password
	typeText("hunter2")             // the only field with no usable default
	next()                          // -> vCPUs
	next()                          // -> memory
	next()                          // -> disk
	next()                          // -> confirm
	_, c := m.Update(keyPress('y')) // affirm
	pump(t, m, c)
	next() // submit

	if m.screen == screenCreate {
		t.Fatalf("the wizard never finished; form state = %v", m.create.State())
	}
	// With no cached ISO the next step is the download, not a blank screen.
	if m.screen != screenDownload {
		t.Errorf("screen = %v after the wizard finished, want the download view", m.screen)
	}
	if content := m.View().Content; strings.TrimSpace(content) == "" {
		t.Error("the view is blank after the wizard finished")
	}
}

// pump runs a command and feeds the resulting messages back into the model, the
// way the Bubble Tea runtime does. Components like huh advance through
// commands, so a test that ignores them never sees the form progress.
//
// Commands that would block or reach the outside world (downloads, installs,
// timers) are not executed; only the in-process messages that drive UI state.
func pump(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	pumpDepth(t, m, cmd, 0)
}

func pumpDepth(t *testing.T, m *Model, cmd tea.Cmd, depth int) {
	t.Helper()
	if cmd == nil || depth > 12 {
		return
	}
	// Many commands block: timers sleep for their whole interval and real work
	// reaches the network. Give each a short deadline and abandon the ones that
	// do not resolve promptly, keeping only the messages that drive UI state.
	done := make(chan tea.Msg, 1)
	go func() {
		defer func() { _ = recover() }()
		done <- cmd()
	}()

	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(150 * time.Millisecond):
		return
	}

	switch msg := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, sub := range msg {
			pumpDepth(t, m, sub, depth+1)
		}
		return
	case downloadDoneMsg, installDoneMsg, vmCreatedMsg, opDoneMsg, vmsMsg, capsMsg:
		// Outcomes of real work; a unit test drives these explicitly.
		return
	}
	_, next := m.Update(msg)
	pumpDepth(t, m, next, depth+1)
}
