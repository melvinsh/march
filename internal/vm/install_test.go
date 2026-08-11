package vm

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/image"
	"github.com/melvinsh/march/internal/qemu"
)

// TestInstallerBootsEndToEnd is the whole product in one test: fetch a current
// Archboot release, create a VM around it, boot it, and read the guest's
// serial console until the Arch installer appears.
//
// It downloads roughly 290 MiB and takes a few minutes, so it runs only when
// MARCH_E2E=1. The ISO is cached between runs.
func TestInstallerBootsEndToEnd(t *testing.T) {
	if os.Getenv("MARCH_E2E") != "1" {
		t.Skip("set MARCH_E2E=1 to run the full download-and-boot test")
	}

	ctx := context.Background()
	caps, err := host.Probe(ctx)
	if err != nil || !caps.Ready() {
		t.Skipf("a complete QEMU installation is required: %v", err)
	}

	// A stable cache directory keeps repeat runs from re-downloading.
	cache := filepath.Join(os.TempDir(), "march-e2e-cache")
	store, err := config.NewStore(cache)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, caps)

	rel, err := image.Resolve(ctx, nil)
	if err != nil {
		t.Skipf("cannot reach the Archboot index: %v", err)
	}
	t.Logf("release %s (%s)", rel.Filename, rel.Date)

	dl := image.NewDownloader(store.ImagesDir())
	iso, err := dl.Fetch(ctx, rel, nil)
	if err != nil {
		t.Fatalf("downloading the installer: %v", err)
	}

	const name = "e2e-install"
	_ = mgr.Delete(ctx, name)

	spec := config.Defaults(name, caps)
	spec.CPUs, spec.MemoryMiB, spec.DiskGiB = 4, 4096, 16

	if _, err := mgr.Create(ctx, CreateOptions{Spec: spec, ISOPath: iso}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(context.Background(), name) })

	if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
		b, _ := os.ReadFile(filepath.Join(store.VMDir(name), "qemu.log"))
		t.Fatalf("Start: %v\nQEMU log:\n%s", err, b)
	}

	conn, err := net.Dial("unix", store.Paths(name).SerialSocket)
	if err != nil {
		t.Fatalf("attaching to the serial console: %v", err)
	}
	defer conn.Close()

	// These markers walk the whole boot chain: firmware hands off to the
	// bootloader, which loads the Arch installer.
	want := map[string]string{
		"EFI":      "UEFI firmware did not run",
		"CD-ROM":   "firmware did not find the installer CD",
		"GRUB":     "the bootloader did not start",
		"archboot": "the Arch installer did not load",
	}
	seen := map[string]bool{}

	deadline := time.Now().Add(4 * time.Minute)
	_ = conn.SetReadDeadline(deadline)

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() && len(seen) < len(want) {
		line := strings.ToLower(sc.Text())
		for marker := range want {
			if strings.Contains(line, strings.ToLower(marker)) {
				seen[marker] = true
			}
		}
	}

	for marker, why := range want {
		if !seen[marker] {
			t.Errorf("%s (no %q on the serial console)", why, marker)
		}
	}
	if !t.Failed() {
		t.Log("Archboot booted to the Arch Linux ARM installer")
	}
}
