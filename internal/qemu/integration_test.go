package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
)

// runQEMUAndQuit starts QEMU with a QMP stdio session, immediately quits, and
// returns the combined output. It is the cheapest way to make QEMU parse and
// accept a set of options without booting anything.
func runQEMUAndQuit(t *testing.T, qemu string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, qemu, args...)
	cmd.Stdin = strings.NewReader(`{"execute":"qmp_capabilities"}{"execute":"quit"}`)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shortTempDir returns a short-lived directory with a path short enough for
// unix sockets, which t.TempDir() on macOS is not.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "march-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func integrationCaps(t *testing.T) *host.Caps {
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

// TestBuiltArgsAreAcceptedByQEMU is the test that matters most: it takes the
// exact command line march generates and proves real QEMU accepts every
// option, device and reference in it. Unit tests can only check the strings.
func TestBuiltArgsAreAcceptedByQEMU(t *testing.T) {
	caps := integrationCaps(t)

	root := shortTempDir(t)
	store := &config.Store{Root: root}
	p := store.Paths("it")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CreateDisk(context.Background(), caps.QemuImg, p.Disk, 4); err != nil {
		t.Fatal(err)
	}
	if err := CreateEFIVars(p.EFIVars, caps.Firmware); err != nil {
		t.Fatal(err)
	}

	// A fake ISO exercises the CD-ROM path; QEMU only needs it to be readable.
	iso := filepath.Join(root, "installer.iso")
	if err := os.WriteFile(iso, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		spec  func(*config.VM)
		build BuildOptions
	}{
		{
			name: "headless installed",
			spec: func(v *config.VM) { v.Installed = true },
		},
		{
			name: "headless with installer attached",
			spec: func(v *config.VM) { v.ISOPath = iso; v.Installed = false },
		},
		{
			name:  "rescue boot from ISO",
			spec:  func(v *config.VM) { v.ISOPath = iso; v.Installed = true },
			build: BuildOptions{ForceISOBoot: true},
		},
		{
			name: "snapshot mode",
			spec: func(v *config.VM) { v.Installed = true },
			build: BuildOptions{
				Snapshot: true,
			},
		},
		{
			name: "single vcpu, no iothread, no balloon",
			spec: func(v *config.VM) {
				v.Installed = true
				v.CPUs = 1
				v.IOThread = false
				v.Balloon = false
				v.RNG = false
			},
		},
		{
			name: "gpu with a tuned resolution",
			spec: func(v *config.VM) {
				v.Installed = true
				v.GPU = true
				v.DisplayWidth, v.DisplayHeight = 1464, 944
			},
		},
		{
			name: "highmem off",
			spec: func(v *config.VM) { v.Installed = true; v.Highmem = false; v.MemoryMiB = 2048 },
		},
		{
			name: "tcg emulation",
			spec: func(v *config.VM) {
				v.Installed = true
				v.Accel = host.AccelTCG
				v.CPUModel = "max"
				v.CPUs = 2
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := config.Defaults("it", caps)
			v.SSHPort = 0 // avoid competing for a host port across subtests
			tc.spec(&v)

			args, err := Build(v, caps, p, tc.build)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// Replace the unix QMP server with stdio so the test can drive it,
			// and stop before executing guest code.
			args = replaceQMPWithStdio(args)
			args = append(args, "-S")

			out, err := runQEMUAndQuit(t, caps.QemuSystem, args...)
			if err != nil {
				t.Fatalf("QEMU rejected the generated command line: %v\n%s\nargs: %s",
					err, out, strings.Join(args, " "))
			}
			// QEMU warns on stderr about options it tolerates but dislikes;
			// those are worth failing on because they signal a bad choice.
			if w := extractWarnings(out); len(w) > 0 {
				t.Errorf("QEMU emitted warnings for the generated command line:\n%s",
					strings.Join(w, "\n"))
			}

			// Clean up the sockets QEMU created so the next subtest can bind.
			os.Remove(p.SerialSocket)
			os.Remove(p.PIDFile)
		})
	}
}

// replaceQMPWithStdio swaps the unix-socket QMP server for a stdio one.
func replaceQMPWithStdio(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-qmp" && i+1 < len(args) {
			out = append(out, "-qmp", "stdio")
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func extractWarnings(out string) []string {
	var warns []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "warning:") || strings.Contains(line, "deprecated") {
			warns = append(warns, strings.TrimSpace(line))
		}
	}
	return warns
}

// TestFullBootLifecycle starts a real VM through the generated command line,
// talks to it over its own QMP socket, and shuts it down — proving the socket
// wiring, pidfile and control path all work together.
func TestFullBootLifecycle(t *testing.T) {
	caps := integrationCaps(t)

	root := shortTempDir(t)
	store := &config.Store{Root: root}
	p := store.Paths("boot")
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := CreateDisk(context.Background(), caps.QemuImg, p.Disk, 4); err != nil {
		t.Fatal(err)
	}
	if err := CreateEFIVars(p.EFIVars, caps.Firmware); err != nil {
		t.Fatal(err)
	}

	v := config.Defaults("boot", caps)
	v.Installed = true // no media; UEFI will simply find nothing to boot
	v.CPUs = 2
	v.MemoryMiB = 1024
	v.SSHPort = 0

	args, err := Build(v, caps, p, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(caps.QemuSystem, args...)
	logFile, err := os.Create(filepath.Join(p.Dir, "qemu.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting QEMU: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// The QMP socket appears shortly after launch.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var q *QMP
	deadline := time.Now().Add(20 * time.Second)
	for {
		q, err = DialQMP(ctx, p.QMPSocket)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(filepath.Join(p.Dir, "qemu.log"))
			t.Fatalf("QMP socket never appeared: %v\nQEMU output:\n%s", err, b)
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer q.Close()

	rs, err := q.QueryStatus()
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if !rs.Running {
		t.Errorf("guest status = %q, want it running", rs.Status)
	}

	// The pidfile QEMU wrote must name the process we started.
	pidBytes, err := os.ReadFile(p.PIDFile)
	if err != nil {
		t.Errorf("reading the pidfile: %v", err)
	} else {
		var pid int
		fmt.Sscanf(strings.TrimSpace(string(pidBytes)), "%d", &pid)
		if pid != cmd.Process.Pid {
			t.Errorf("pidfile holds %d, want %d", pid, cmd.Process.Pid)
		}
	}

	// The serial console socket must exist so a user can attach to it.
	if _, err := os.Stat(p.SerialSocket); err != nil {
		t.Errorf("the serial console socket was not created: %v", err)
	}

	if err := q.Quit(); err != nil {
		t.Errorf("Quit: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("QEMU did not exit after a QMP quit")
	}
}
