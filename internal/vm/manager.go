// Package vm ties the store, QEMU and images together into the operations the
// UI performs: create, start, stop and delete a machine.
package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/qemu"
)

// State is a VM's observable condition.
type State string

const (
	StateStopped State = "stopped"
	StateRunning State = "running"
	StatePaused  State = "paused"
	// StateUnknown covers a VM whose pidfile exists but whose process cannot
	// be interrogated, which usually means a stale file after a host crash.
	StateUnknown State = "unknown"
)

// Status is a snapshot of one VM.
type Status struct {
	Name      string
	State     State
	PID       int
	Installed bool
	// Detail carries the QMP run state ("running", "paused", "prelaunch"...)
	// when available.
	Detail string
}

// Manager performs VM operations against a store.
type Manager struct {
	Store *config.Store
	Caps  *host.Caps
}

// New returns a Manager.
func New(store *config.Store, caps *host.Caps) *Manager {
	return &Manager{Store: store, Caps: caps}
}

// CreateOptions describes a new VM.
type CreateOptions struct {
	Spec config.VM
	// ISOPath is the installer to attach. Required unless the caller intends
	// to boot an already-provisioned disk.
	ISOPath string
}

// Create provisions a new VM: it writes the spec, allocates a disk, and
// creates a private UEFI variable store. On failure it removes the partial VM
// directory so a retry starts clean.
func (m *Manager) Create(ctx context.Context, opts CreateOptions) (config.VM, error) {
	v := opts.Spec
	v.ISOPath = opts.ISOPath
	if v.Created.IsZero() {
		v.Created = time.Now().UTC()
	}
	if v.MAC == "" {
		v.MAC = config.RandomMAC()
	}

	if err := v.Validate(); err != nil {
		return v, err
	}
	if m.Store.Exists(v.Name) {
		return v, fmt.Errorf("a VM named %q already exists", v.Name)
	}
	if !m.Caps.Ready() {
		return v, fmt.Errorf("cannot create a VM: %s", strings.Join(m.Caps.Problems(), "; "))
	}
	if v.ISOPath != "" {
		if _, err := os.Stat(v.ISOPath); err != nil {
			return v, fmt.Errorf("installer image %s is not readable: %w", v.ISOPath, err)
		}
	}

	if v.SSHPort == 0 {
		port, err := config.FreePort(0)
		if err != nil {
			return v, err
		}
		v.SSHPort = port
	}

	p := m.Store.Paths(v.Name)
	cleanup := func() { os.RemoveAll(p.Dir) }

	if err := m.Store.Save(v); err != nil {
		cleanup()
		return v, err
	}
	if err := qemu.CreateDisk(ctx, m.Caps.QemuImg, p.Disk, v.DiskGiB); err != nil {
		cleanup()
		return v, err
	}
	if err := qemu.CreateEFIVars(p.EFIVars, m.Caps.Firmware); err != nil {
		cleanup()
		return v, err
	}
	return v, nil
}

// Start boots a VM as a detached process and waits for its QMP socket to come
// up, so a caller that returns successfully knows the machine is really alive.
func (m *Manager) Start(ctx context.Context, name string, opts qemu.BuildOptions) error {
	v, err := m.Store.Load(name)
	if err != nil {
		return err
	}
	if st, _ := m.Status(ctx, name); st.State == StateRunning || st.State == StatePaused {
		return fmt.Errorf("%q is already running", name)
	}

	p := m.Store.Paths(name)

	// Stale sockets from an unclean shutdown would make QEMU fail to bind.
	for _, sock := range []string{p.QMPSocket, p.SerialSocket} {
		if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clearing stale socket %s: %w", sock, err)
		}
	}
	if err := qemu.CreateEFIVars(p.EFIVars, m.Caps.Firmware); err != nil {
		return err
	}

	args, err := qemu.Build(v, m.Caps, p, opts)
	if err != nil {
		return err
	}

	// QEMU's own stderr is the only place startup failures appear, so it is
	// captured separately from the guest serial log.
	stderr, err := os.OpenFile(filepath.Join(p.Dir, "qemu.log"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening the QEMU log: %w", err)
	}
	defer stderr.Close()

	cmd := exec.Command(m.Caps.QemuSystem, args...)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	// Venus needs the Vulkan loader pointed at a driver; this is empty and
	// harmless when Venus is unavailable.
	if vk := m.Caps.VulkanEnv(p.Dir); len(vk) > 0 {
		cmd.Env = append(os.Environ(), vk...)
	}
	// A new session detaches the VM from the TUI's terminal, so quitting march
	// or pressing ctrl-c does not take running guests down with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting QEMU: %w", err)
	}
	// Reap the child so it does not linger as a zombie for the life of the TUI.
	go func() { _ = cmd.Wait() }()

	if err := m.waitForQMP(ctx, p, cmd.Process.Pid); err != nil {
		return err
	}
	return nil
}

// waitForQMP polls until the machine's QMP socket accepts a connection, or the
// process dies first. Reporting the QEMU log on failure turns an opaque
// "didn't start" into the actual reason.
func (m *Manager) waitForQMP(ctx context.Context, p config.Paths, pid int) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if !processAlive(pid) {
			return fmt.Errorf("QEMU exited during startup: %s", tailLog(filepath.Join(p.Dir, "qemu.log")))
		}
		q, err := qemu.DialQMP(ctx, p.QMPSocket)
		if err == nil {
			q.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("QEMU did not open its control socket within 20s: %s",
				tailLog(filepath.Join(p.Dir, "qemu.log")))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Stop asks the guest to shut down cleanly and waits. If the guest ignores the
// request within the grace period, the machine is terminated: an unresponsive
// guest should not leave the user stuck.
func (m *Manager) Stop(ctx context.Context, name string, grace time.Duration) error {
	p := m.Store.Paths(name)
	st, err := m.Status(ctx, name)
	if err != nil {
		return err
	}
	if st.State == StateStopped {
		return fmt.Errorf("%q is not running", name)
	}

	q, err := qemu.DialQMP(ctx, p.QMPSocket)
	if err != nil {
		// No control socket means the process is beyond talking to.
		return m.Kill(ctx, name)
	}
	if err := q.Powerdown(); err != nil {
		q.Close()
		return m.Kill(ctx, name)
	}
	q.Close()

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if st, _ := m.Status(ctx, name); st.State == StateStopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return m.Kill(ctx, name)
}

// Kill terminates a VM immediately. The guest gets no chance to flush, so this
// is reserved for a machine that will not shut down on request.
func (m *Manager) Kill(ctx context.Context, name string) error {
	p := m.Store.Paths(name)

	if q, err := qemu.DialQMP(ctx, p.QMPSocket); err == nil {
		err := q.Quit()
		q.Close()
		if err == nil {
			if m.waitGone(ctx, name, 5*time.Second) {
				return nil
			}
		}
	}

	pid := readPID(p.PIDFile)
	if pid <= 0 || !processAlive(pid) {
		m.cleanupRuntime(p)
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminating QEMU (pid %d): %w", pid, err)
	}
	if m.waitGone(ctx, name, 5*time.Second) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force-killing QEMU (pid %d): %w", pid, err)
	}
	m.waitGone(ctx, name, 3*time.Second)
	m.cleanupRuntime(p)
	return nil
}

func (m *Manager) waitGone(ctx context.Context, name string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st, _ := m.Status(ctx, name); st.State == StateStopped {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
	return false
}

// cleanupRuntime removes files that only make sense while a VM is running.
func (m *Manager) cleanupRuntime(p config.Paths) {
	for _, f := range []string{p.PIDFile, p.QMPSocket, p.SerialSocket} {
		_ = os.Remove(f)
	}
}

// Restart performs a graceful stop followed by a start.
func (m *Manager) Restart(ctx context.Context, name string, opts qemu.BuildOptions) error {
	if st, _ := m.Status(ctx, name); st.State != StateStopped {
		if err := m.Stop(ctx, name, 30*time.Second); err != nil {
			return err
		}
	}
	return m.Start(ctx, name, opts)
}

// Delete removes a VM, stopping it first if necessary.
func (m *Manager) Delete(ctx context.Context, name string) error {
	if st, _ := m.Status(ctx, name); st.State != StateStopped {
		if err := m.Kill(ctx, name); err != nil {
			return err
		}
	}
	return m.Store.Delete(name)
}

// Install performs an unattended Arch installation onto a freshly created VM
// and leaves it powered off, ready to boot into its desktop.
//
// The VM is booted from its installer ISO, driven through Archboot's autorun
// hook, and watched over the serial console until the guest reports it has
// finished. A failure powers the machine off rather than leaving it running
// against a half-installed disk.
func (m *Manager) Install(ctx context.Context, name string, profile install.Profile, hooks install.Hooks) error {
	v, err := m.Store.Load(name)
	if err != nil {
		return err
	}
	if v.Installed {
		return fmt.Errorf("%q is already installed", name)
	}
	if v.ISOPath == "" {
		return fmt.Errorf("%q has no installer image attached", name)
	}
	// Whether effects are affordable depends on how this machine actually
	// renders. The GL device is only attached alongside a display backend with
	// GL enabled, so a headless VM draws in software however capable the host
	// is, and its effects must stay off.
	profile.GPUAccelerated = v.GPUAccel && m.Caps.SupportsGPUAccel() &&
		v.Display != config.DisplayNone
	profile.VulkanAccelerated = profile.GPUAccelerated && v.Venus && m.Caps.SupportsVenus()

	if err := profile.Validate(); err != nil {
		return err
	}

	// A leftover process from an earlier attempt would hold the disk open.
	if st, _ := m.Status(ctx, name); st.State != StateStopped {
		if err := m.Kill(ctx, name); err != nil {
			return err
		}
	}

	if err := m.Start(ctx, name, qemu.BuildOptions{AttachISO: true, ForceISOBoot: true}); err != nil {
		return err
	}

	// Keep a transcript of the install. QEMU truncates the console log on every
	// boot, so without this the record of a failed install is gone by the time
	// anyone looks for it.
	p := m.Store.Paths(name)
	transcript, err := os.OpenFile(filepath.Join(p.Dir, "install.log"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening the install log: %w", err)
	}
	defer transcript.Close()

	userOutput := hooks.OnOutput
	hooks.OnOutput = func(chunk string) {
		_, _ = transcript.WriteString(chunk)
		if userOutput != nil {
			userOutput(chunk)
		}
	}

	runner := &install.Runner{
		Profile:      profile,
		Hooks:        hooks,
		SerialSocket: p.SerialSocket,
	}
	if err := runner.Run(ctx); err != nil {
		// Use a fresh context: the caller's may already be cancelled, and the
		// machine must still be powered off.
		_ = m.Kill(context.WithoutCancel(ctx), name)
		return fmt.Errorf("%w\n(full transcript: %s)", err, filepath.Join(p.Dir, "install.log"))
	}

	// The guest unmounted and synced before signalling completion, so cutting
	// power here is safe and avoids waiting on the live environment's own
	// shutdown sequence.
	if err := m.Kill(ctx, name); err != nil {
		return err
	}

	v.Installed = true
	v.Username = profile.Username
	return m.Store.Save(v)
}

// MarkInstalled records that a guest has been installed, so subsequent boots
// go to the disk instead of the installer.
func (m *Manager) MarkInstalled(name string, installed bool) error {
	v, err := m.Store.Load(name)
	if err != nil {
		return err
	}
	v.Installed = installed
	return m.Store.Save(v)
}

// Status reports a VM's current state. It prefers QMP, which knows whether a
// machine is paused, and falls back to the pidfile when the socket is gone.
func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	st := Status{Name: name, State: StateStopped}

	v, err := m.Store.Load(name)
	if err != nil {
		return st, err
	}
	st.Installed = v.Installed

	p := m.Store.Paths(name)
	pid := readPID(p.PIDFile)
	if pid <= 0 || !processAlive(pid) {
		return st, nil
	}
	st.PID = pid
	st.State = StateUnknown

	// A short timeout keeps a wedged VM from stalling a whole list refresh.
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	q, err := qemu.DialQMP(qctx, p.QMPSocket)
	if err != nil {
		// The process is alive but not answering; report it as running rather
		// than pretending to know more.
		st.State = StateRunning
		return st, nil
	}
	defer q.Close()

	rs, err := q.QueryStatus()
	if err != nil {
		st.State = StateRunning
		return st, nil
	}
	st.Detail = rs.Status
	if rs.Running {
		st.State = StateRunning
	} else {
		st.State = StatePaused
	}
	return st, nil
}

// StatusAll reports on every defined VM.
func (m *Manager) StatusAll(ctx context.Context) (map[string]Status, error) {
	vms, err := m.Store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Status, len(vms))
	for _, v := range vms {
		st, err := m.Status(ctx, v.Name)
		if err != nil {
			st = Status{Name: v.Name, State: StateUnknown}
		}
		out[v.Name] = st
	}
	return out, nil
}

// SSHCommand returns the command a user runs to reach a VM over the forwarded
// port. The guest must have sshd enabled for this to connect.
func (m *Manager) SSHCommand(v config.VM, user string) string {
	if user == "" {
		user = "root"
	}
	return fmt.Sprintf("ssh -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null %s@127.0.0.1",
		v.SSHPort, user)
}

// ConsoleCommand returns a command that attaches to a VM's serial console.
func (m *Manager) ConsoleCommand(name string) string {
	p := m.Store.Paths(name)
	return "socat -,raw,echo=0,escape=0x1d unix-connect:" + p.SerialSocket
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// processAlive reports whether a pid names a live process. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	// EPERM means the process exists but belongs to someone else.
	return err == nil || errors.Is(err, syscall.EPERM)
}

// tailLog returns the last few lines of a log, for embedding in an error.
func tailLog(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return "(no output)"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, "; ")
}
