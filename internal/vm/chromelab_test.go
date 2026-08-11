package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/console"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/image"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/qemu"
)

// TestChromeGPULab is scaffolding, not a test: it installs one accelerated
// guest, keeps it, and runs whatever script MARCH_CHROME_PROBE points at inside
// the live session, printing everything it says. That is what makes the flag
// matrix iterable — the install is paid for once and each new probe is a rerun.
//
//	MARCH_CHROME_LAB=1 MARCH_CHROME_PROBE=/path/to/probe.sh \
//	  go test ./internal/vm -run TestChromeGPULab -timeout 90m -v
func TestChromeGPULab(t *testing.T) {
	if os.Getenv("MARCH_CHROME_LAB") != "1" {
		t.Skip("set MARCH_CHROME_LAB=1 to run the Chrome GPU lab")
	}
	probePath := os.Getenv("MARCH_CHROME_PROBE")
	if probePath == "" {
		t.Fatal("set MARCH_CHROME_PROBE to the probe script to run in the guest")
	}
	probe, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}

	ctx := context.Background()
	caps, err := host.Probe(ctx)
	if err != nil || !caps.Ready() {
		t.Skipf("a complete QEMU installation is required: %v", err)
	}
	if !caps.SupportsGPUAccel() {
		t.Skip("this QEMU has no virtio-gpu-gl; install melvinsh/march/qemu-march")
	}
	t.Logf("qemu %s at %s", caps.Version, caps.QemuSystem)

	// Its own store, not the one the e2e tests share: this guest is meant to
	// outlive a run, and those tests delete their VMs by name on the way in.
	store, err := config.NewStore(filepath.Join(os.TempDir(), "march-chrome-lab"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, caps)

	const name = "chrome-lab"
	const user, password = "arch", "marchtest"

	// Reuse the guest if it is already installed; the install is the expensive
	// part and the probe is the part that changes.
	fresh := true
	if store.Exists(name) {
		if v, err := store.Load(name); err == nil && v.Installed {
			fresh = false
			t.Logf("reusing the installed guest %q", name)
		} else {
			_ = mgr.Delete(ctx, name)
		}
	}

	if fresh {
		rel, err := image.Resolve(ctx, nil)
		if err != nil {
			t.Skipf("cannot reach the Archboot index: %v", err)
		}
		iso, err := image.NewDownloader(store.ImagesDir()).Fetch(ctx, rel, nil)
		if err != nil {
			t.Fatalf("downloading the installer: %v", err)
		}

		spec := config.Defaults(name, caps)
		spec.CPUs, spec.MemoryMiB, spec.DiskGiB = min(caps.HostCPUs, 4), 6144, 24
		// A window is required: the GL device is only valid alongside a display
		// backend that has GL enabled, and the windowed rows need a real
		// Wayland session to map into.
		spec.Display, spec.GPU = config.DisplayCocoa, true
		if !spec.GPUAccel {
			t.Fatal("defaults did not enable GPU acceleration on a capable host")
		}
		if _, err := mgr.Create(ctx, CreateOptions{Spec: spec, ISOPath: iso}); err != nil {
			t.Fatalf("Create: %v", err)
		}

		profile := install.DefaultProfile(name)
		profile.Username, profile.Password, profile.Autologin = user, password, true

		start := time.Now()
		hooks := install.Hooks{OnProgress: func(p install.Progress) {
			t.Logf("[%6s] %s", time.Since(start).Round(time.Second), p.Message)
		}}
		if err := mgr.Install(ctx, name, profile, hooks); err != nil {
			t.Fatalf("install failed after %s: %v", time.Since(start).Round(time.Second), err)
		}
		t.Logf("installed in %s", time.Since(start).Round(time.Second))
	}

	if st, err := mgr.Status(ctx, name); err != nil || st.State != StateRunning {
		if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
			t.Fatalf("boot: %v", err)
		}
	}

	c, err := console.Dial(ctx, store.Paths(name).SerialSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	bctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// A guest left running from the last probe is already logged in, and its
	// login prompt scrolled past long ago. Ask the console what it is looking
	// at rather than assuming.
	_ = c.SendLine("")
	pctx, pcancel := context.WithTimeout(bctx, 20*time.Second)
	_, err = c.Expect(pctx, "]$")
	pcancel()
	if err != nil {
		if _, err := c.Expect(bctx, "login:"); err != nil {
			t.Fatalf("no login prompt: %v\n%s", err, c.Tail(2000))
		}
		_ = c.SendLine(user)
		if _, err := c.Expect(bctx, "assword"); err != nil {
			t.Fatal(err)
		}
		_ = c.SendLine(password)
		if _, err := c.Expect(bctx, "]$"); err != nil {
			t.Fatalf("could not log in: %v\n%s", err, c.Tail(2000))
		}
		time.Sleep(20 * time.Second) // let the desktop settle
	}

	ask := func(cmd string, d time.Duration) string {
		t.Helper()
		_ = c.SendLine(`printf '%s\n' "M-BE""GIN"; ` + cmd + ` 2>&1; printf '%s\n' "M-E""ND"`)
		qc, qcancel := context.WithTimeout(ctx, d)
		defer qcancel()
		if _, err := c.Expect(qc, "M-BEGIN"); err != nil {
			t.Fatalf("no response to %q: %v", cmd, err)
		}
		_, out, err := c.ExpectCapture(qc, "M-END")
		if err != nil {
			t.Fatalf("%q did not finish: %v\n%s", cmd, err, c.Tail(3000))
		}
		return strings.TrimSpace(out)
	}

	// The probe reads the guest's own GL through eglinfo and parses hyprctl
	// with jq; neither is part of a march guest.
	t.Log(ask("echo "+password+" | sudo -S pacman -Sy --noconfirm --needed mesa-utils jq 2>&1 | tail -3", 8*time.Minute))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, string(probe))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	cmd := fmt.Sprintf("curl -fsS -o /tmp/probe.sh http://10.0.2.2:%d/probe.sh && bash /tmp/probe.sh",
		ln.Addr().(*net.TCPAddr).Port)
	out := ask(cmd, 40*time.Minute)
	for _, line := range strings.Split(out, "\n") {
		t.Logf("guest| %s", strings.TrimRight(line, "\r"))
	}
	// Probes end with PROBE-DONE; the in-guest self-test, which is also worth
	// pointing this at, ends with its own marker.
	if !strings.Contains(out, "PROBE-DONE") && !strings.Contains(out, "SELFTEST-DONE") {
		t.Errorf("the probe did not finish")
	}
	t.Logf("guest %q left running; rerun with a new MARCH_CHROME_PROBE to iterate", name)
}
