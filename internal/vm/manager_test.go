package vm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/qemu"
)

// shortTempDir keeps paths short enough for unix sockets, which the default
// macOS temp directory is not.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "march-vm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func fakeCaps() *host.Caps {
	return &host.Caps{
		QemuSystem: "/opt/homebrew/bin/qemu-system-aarch64",
		QemuImg:    "/opt/homebrew/bin/qemu-img",
		Firmware:   "/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
		Accels:     []string{"hvf", "tcg"},
		AIOModes:   []string{"threads"},
		Displays:   []string{"none"},
		Devices:    map[string]bool{"virtio-blk-pci": true},
		HostCPUs:   8,
		HostMemMiB: 16384,
		Arch:       "arm64",
		OS:         "darwin",
	}
}

func newTestManager(t *testing.T, caps *host.Caps) *Manager {
	t.Helper()
	store, err := config.NewStore(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return New(store, caps)
}

// realCaps probes the actual host, skipping when QEMU is not usable.
func realCaps(t *testing.T) *host.Caps {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping a test that runs QEMU")
	}
	caps, err := host.Probe(context.Background())
	if err != nil || caps == nil || !caps.Ready() {
		t.Skip("a complete QEMU installation with aarch64 firmware is required")
	}
	return caps
}

func TestStatusOfMissingVM(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	if _, err := m.Status(context.Background(), "ghost"); err == nil {
		t.Error("expected an error for a VM that does not exist")
	}
}

func TestStatusStoppedWithoutPidfile(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	v := config.Defaults("arch", fakeCaps())
	if err := m.Store.Save(v); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(context.Background(), "arch")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateStopped {
		t.Errorf("State = %q, want stopped", st.State)
	}
	if st.PID != 0 {
		t.Errorf("PID = %d, want 0", st.PID)
	}
}

// A pidfile left behind by a host crash must not make a dead VM look alive.
func TestStatusIgnoresStalePidfile(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	v := config.Defaults("arch", fakeCaps())
	if err := m.Store.Save(v); err != nil {
		t.Fatal(err)
	}

	p := m.Store.Paths("arch")
	// PID 2^22 is above the usual maximum, so it cannot be a live process.
	if err := os.WriteFile(p.PIDFile, []byte("4194304\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(context.Background(), "arch")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateStopped {
		t.Errorf("State = %q, want stopped for a stale pidfile", st.State)
	}
}

func TestStatusHandlesGarbagePidfile(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	if err := m.Store.Save(config.Defaults("arch", fakeCaps())); err != nil {
		t.Fatal(err)
	}
	p := m.Store.Paths("arch")
	if err := os.WriteFile(p.PIDFile, []byte("not a pid"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(context.Background(), "arch")
	if err != nil {
		t.Fatalf("a corrupt pidfile should not error: %v", err)
	}
	if st.State != StateStopped {
		t.Errorf("State = %q, want stopped", st.State)
	}
}

func TestCreateValidation(t *testing.T) {
	caps := fakeCaps()

	t.Run("rejects a bad spec", func(t *testing.T) {
		m := newTestManager(t, caps)
		spec := config.Defaults("arch", caps)
		spec.CPUs = 0
		if _, err := m.Create(context.Background(), CreateOptions{Spec: spec}); err == nil {
			t.Error("expected an error for an invalid spec")
		}
	})

	t.Run("rejects an unreadable ISO", func(t *testing.T) {
		m := newTestManager(t, caps)
		spec := config.Defaults("arch", caps)
		_, err := m.Create(context.Background(), CreateOptions{
			Spec: spec, ISOPath: "/nonexistent/installer.iso",
		})
		if err == nil {
			t.Fatal("expected an error for a missing installer")
		}
		if !strings.Contains(err.Error(), "not readable") {
			t.Errorf("error %q should name the unreadable installer", err)
		}
	})

	t.Run("rejects an incomplete QEMU", func(t *testing.T) {
		broken := fakeCaps()
		broken.Firmware = ""
		m := newTestManager(t, broken)
		if _, err := m.Create(context.Background(), CreateOptions{Spec: config.Defaults("arch", broken)}); err == nil {
			t.Error("expected an error when firmware is missing")
		}
	})
}

func TestCreateRealDisk(t *testing.T) {
	caps := realCaps(t)
	m := newTestManager(t, caps)

	spec := config.Defaults("arch", caps)
	spec.DiskGiB = 8

	v, err := m.Create(context.Background(), CreateOptions{Spec: spec})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if v.SSHPort == 0 {
		t.Error("Create should have allocated an SSH port")
	}
	if v.MAC == "" {
		t.Error("Create should have assigned a MAC")
	}

	p := m.Store.Paths("arch")
	for _, f := range []string{p.Config, p.Disk, p.EFIVars} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("Create did not produce %s: %v", filepath.Base(f), err)
		}
	}

	// A second create with the same name must be refused, not silently
	// overwrite the existing disk.
	if _, err := m.Create(context.Background(), CreateOptions{Spec: spec}); err == nil {
		t.Error("Create overwrote an existing VM")
	}
}

// A failure partway through creation must not leave a half-built VM behind.
func TestCreateCleansUpOnFailure(t *testing.T) {
	caps := realCaps(t)
	m := newTestManager(t, caps)

	// An absurd disk size makes qemu-img fail after the config is written.
	spec := config.Defaults("arch", caps)
	spec.DiskGiB = 1 << 30 // 1 EiB

	if _, err := m.Create(context.Background(), CreateOptions{Spec: spec}); err == nil {
		t.Skip("qemu-img accepted an exabyte disk; cannot exercise the failure path")
	}

	if m.Store.Exists("arch") {
		t.Error("a failed Create left the VM registered")
	}
	if _, err := os.Stat(m.Store.VMDir("arch")); !os.IsNotExist(err) {
		t.Error("a failed Create left its directory behind")
	}
}

func TestStartRejectsUnknownVM(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	if err := m.Start(context.Background(), "ghost", qemu.BuildOptions{}); err == nil {
		t.Error("expected an error starting a VM that does not exist")
	}
}

func TestStopRejectsStoppedVM(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	if err := m.Store.Save(config.Defaults("arch", fakeCaps())); err != nil {
		t.Fatal(err)
	}
	err := m.Stop(context.Background(), "arch", time.Second)
	if err == nil {
		t.Fatal("expected an error stopping a VM that is not running")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error %q should say the VM is not running", err)
	}
}

func TestMarkInstalled(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	v := config.Defaults("arch", fakeCaps())
	v.ISOPath = "/images/x.iso"
	if err := m.Store.Save(v); err != nil {
		t.Fatal(err)
	}

	if err := m.MarkInstalled("arch", true); err != nil {
		t.Fatal(err)
	}
	got, err := m.Store.Load("arch")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Installed {
		t.Error("Installed was not persisted")
	}

	if err := m.MarkInstalled("arch", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Store.Load("arch"); got.Installed {
		t.Error("Installed was not cleared")
	}
}

func TestSSHAndConsoleCommands(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	v := config.Defaults("arch", fakeCaps())
	v.SSHPort = 2222

	ssh := m.SSHCommand(v, "")
	if !strings.Contains(ssh, "-p 2222") {
		t.Errorf("SSH command %q does not use the forwarded port", ssh)
	}
	if !strings.Contains(ssh, "root@127.0.0.1") {
		t.Errorf("SSH command %q should default to root on loopback", ssh)
	}
	if !strings.Contains(m.SSHCommand(v, "alice"), "alice@") {
		t.Error("SSHCommand ignored the user argument")
	}

	console := m.ConsoleCommand("arch")
	if !strings.Contains(console, m.Store.Paths("arch").SerialSocket) {
		t.Errorf("console command %q does not reference the serial socket", console)
	}
}

func TestStatusAll(t *testing.T) {
	m := newTestManager(t, fakeCaps())
	for _, n := range []string{"a", "b", "c"} {
		if err := m.Store.Save(config.Defaults(n, fakeCaps())); err != nil {
			t.Fatal(err)
		}
	}

	all, err := m.StatusAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("StatusAll returned %d entries, want 3", len(all))
	}
	for _, n := range []string{"a", "b", "c"} {
		if all[n].State != StateStopped {
			t.Errorf("%s: State = %q, want stopped", n, all[n].State)
		}
	}
}

func TestDeleteStoppedVM(t *testing.T) {
	caps := realCaps(t)
	m := newTestManager(t, caps)

	spec := config.Defaults("arch", caps)
	spec.DiskGiB = 8
	if _, err := m.Create(context.Background(), CreateOptions{Spec: spec}); err != nil {
		t.Fatal(err)
	}

	if err := m.Delete(context.Background(), "arch"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.Store.Exists("arch") {
		t.Error("the VM still exists after Delete")
	}
	if _, err := os.Stat(m.Store.VMDir("arch")); !os.IsNotExist(err) {
		t.Error("the VM directory survived Delete")
	}
}

// TestLifecycleEndToEnd drives a real VM through create, start, status, stop
// and delete — the whole path a user takes.
func TestLifecycleEndToEnd(t *testing.T) {
	caps := realCaps(t)
	m := newTestManager(t, caps)
	ctx := context.Background()

	spec := config.Defaults("e2e", caps)
	spec.CPUs = 2
	spec.MemoryMiB = 1024
	spec.DiskGiB = 8
	// No installer: UEFI boots, finds nothing, and idles. That is enough to
	// exercise every control path without a 500 MB download.
	spec.Installed = true

	if _, err := m.Create(ctx, CreateOptions{Spec: spec}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := m.Start(ctx, "e2e", qemu.BuildOptions{}); err != nil {
		b, _ := os.ReadFile(filepath.Join(m.Store.VMDir("e2e"), "qemu.log"))
		t.Fatalf("Start: %v\nQEMU log:\n%s", err, b)
	}
	// Whatever happens next, do not leak a running VM.
	defer func() { _ = m.Kill(context.Background(), "e2e") }()

	st, err := m.Status(ctx, "e2e")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != StateRunning {
		t.Errorf("State = %q, want running", st.State)
	}
	if st.PID <= 0 {
		t.Error("a running VM should report a PID")
	}
	if !processAlive(st.PID) {
		t.Errorf("PID %d is not alive", st.PID)
	}

	// Starting an already-running VM must be refused.
	if err := m.Start(ctx, "e2e", qemu.BuildOptions{}); err == nil {
		t.Error("Start on a running VM should fail")
	}

	// The guest has no OS to honour ACPI, so Stop falls through its grace
	// period and terminates the machine. Either way it must end up stopped.
	if err := m.Stop(ctx, "e2e", 2*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	st, err = m.Status(ctx, "e2e")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateStopped {
		t.Errorf("State = %q after Stop, want stopped", st.State)
	}

	// Runtime files must be cleaned up so the next start can bind its sockets.
	p := m.Store.Paths("e2e")
	if _, err := os.Stat(p.QMPSocket); err == nil {
		t.Error("the QMP socket survived shutdown and would block a restart")
	}

	// A stopped VM must be startable again.
	if err := m.Start(ctx, "e2e", qemu.BuildOptions{}); err != nil {
		t.Fatalf("restarting after a clean stop: %v", err)
	}
	if st, _ := m.Status(ctx, "e2e"); st.State != StateRunning {
		t.Errorf("State = %q after the second Start, want running", st.State)
	}

	// Delete must stop a running VM first.
	if err := m.Delete(ctx, "e2e"); err != nil {
		t.Fatalf("Delete on a running VM: %v", err)
	}
	if m.Store.Exists("e2e") {
		t.Error("the VM survived Delete")
	}
}

// A VM that will not respond to a graceful shutdown must still be killable.
func TestKillUnresponsiveVM(t *testing.T) {
	caps := realCaps(t)
	m := newTestManager(t, caps)
	ctx := context.Background()

	spec := config.Defaults("killme", caps)
	spec.CPUs = 1
	spec.MemoryMiB = 512
	spec.DiskGiB = 8
	spec.Installed = true

	if _, err := m.Create(ctx, CreateOptions{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx, "killme", qemu.BuildOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := m.Kill(ctx, "killme"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if st, _ := m.Status(ctx, "killme"); st.State != StateStopped {
		t.Errorf("State = %q after Kill, want stopped", st.State)
	}

	// Killing an already-dead VM is a no-op, not an error.
	if err := m.Kill(ctx, "killme"); err != nil {
		t.Errorf("Kill on a stopped VM should succeed, got %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("the test process should be alive")
	}
	if processAlive(0) || processAlive(-1) {
		t.Error("invalid PIDs must not be reported as alive")
	}
	if processAlive(4194304) {
		t.Error("an out-of-range PID must not be reported as alive")
	}
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()

	if got := tailLog(filepath.Join(dir, "missing")); got != "(no output)" {
		t.Errorf("tailLog on a missing file = %q", got)
	}

	f := filepath.Join(dir, "log")
	if err := os.WriteFile(f, []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailLog(f)
	if strings.Contains(got, "l1") {
		t.Errorf("tailLog = %q, should keep only the last lines", got)
	}
	if !strings.Contains(got, "l7") {
		t.Errorf("tailLog = %q, should include the final line", got)
	}
}
