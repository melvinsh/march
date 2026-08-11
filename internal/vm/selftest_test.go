package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/console"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/qemu"
)

// TestGuestSelftest runs the in-guest suite against a machine that is already
// installed, which TestUnattendedDesktopInstall leaves behind when
// MARCH_E2E_KEEP=1.
//
// It exists because the suite is the part of the end-to-end test that changes
// most often, and re-installing a guest to re-run it costs four minutes every
// time. The install is verified by the other test; this one verifies the
// desktop.
func TestGuestSelftest(t *testing.T) {
	if os.Getenv("MARCH_E2E_RERUN") != "1" {
		t.Skip("set MARCH_E2E_RERUN=1 to re-run the in-guest suite against a kept VM")
	}

	ctx := context.Background()
	caps, err := host.Probe(ctx)
	if err != nil || !caps.Ready() {
		t.Skipf("a complete QEMU installation is required: %v", err)
	}

	store, err := config.NewStore(filepath.Join(os.TempDir(), "march-e2e-cache"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, caps)

	const name = "e2e-desktop"
	v, err := store.Load(name)
	if err != nil {
		t.Skipf("no kept VM to test: %v", err)
	}
	if !v.Installed {
		t.Skip("the kept VM was never installed")
	}

	if st, err := mgr.Status(ctx, name); err != nil || st.State != StateRunning {
		if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
			t.Fatalf("starting the kept VM: %v", err)
		}
		t.Cleanup(func() { _ = mgr.Kill(context.Background(), name) })
	}

	c, err := console.Dial(ctx, store.Paths(name).SerialSocket)
	if err != nil {
		t.Fatalf("attaching to the console: %v", err)
	}
	defer c.Close()

	// A machine that booted minutes ago prints nothing more, so there is no
	// banner left to wait for — loginOnConsole prompts it into answering, and
	// takes either a login prompt or a shell that is already there.
	loginOnConsole(t, ctx, c, "arch", "marchtest")
	runGuestSelftest(t, ctx, c, "arch")
}

// TestGuestShell runs a single command in the kept VM and prints what it said.
//
// It is how a failure the in-guest suite reports gets diagnosed: the suite says
// which check failed, and this says why, without another install.
//
//	MARCH_E2E_CMD='tail -20 /tmp/march-selftest/log/chrome.log' \
//	  go test ./internal/vm/ -run TestGuestShell -v
func TestGuestShell(t *testing.T) {
	cmd := os.Getenv("MARCH_E2E_CMD")
	if cmd == "" {
		t.Skip("set MARCH_E2E_CMD")
	}
	ctx := context.Background()
	store, err := config.NewStore(filepath.Join(os.TempDir(), "march-e2e-cache"))
	if err != nil {
		t.Fatal(err)
	}

	c, err := console.Dial(ctx, store.Paths("e2e-desktop").SerialSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	loginOnConsole(t, ctx, c, "arch", "marchtest")

	if err := c.SendLine(`printf '%s\n' "M-BE""GIN"; ` + cmd + ` 2>&1; printf '%s\n' "M-E""ND"`); err != nil {
		t.Fatal(err)
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := c.Expect(qctx, "M-BEGIN"); err != nil {
		t.Fatal(err)
	}
	_, out, err := c.ExpectCapture(qctx, "M-END")
	if err != nil {
		t.Fatalf("%v\ntail:\n%s", err, c.Tail(3000))
	}
	t.Logf("\n%s", out)
}
