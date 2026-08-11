package qemu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melvinsh/march/internal/host"
)

// qemuImgPath finds the real qemu-img, skipping the test when absent.
func qemuImgPath(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping a test that shells out to qemu-img")
	}
	caps, _ := host.Probe(context.Background())
	if caps == nil || caps.QemuImg == "" {
		t.Skip("qemu-img is not installed")
	}
	return caps.QemuImg
}

func TestCreateDisk(t *testing.T) {
	img := qemuImgPath(t)
	path := filepath.Join(t.TempDir(), "disk.qcow2")

	if err := CreateDisk(context.Background(), img, path, 8); err != nil {
		t.Fatalf("CreateDisk: %v", err)
	}

	info, err := Info(context.Background(), img, path)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Format != "qcow2" {
		t.Errorf("Format = %q, want qcow2", info.Format)
	}
	if want := int64(8) << 30; info.VirtualSize != want {
		t.Errorf("VirtualSize = %d, want %d", info.VirtualSize, want)
	}
	// A sparse image must not actually occupy its full virtual size.
	if info.ActualSize >= info.VirtualSize {
		t.Errorf("ActualSize %d is not sparse relative to VirtualSize %d",
			info.ActualSize, info.VirtualSize)
	}
}

func TestCreateDiskRefusesOverwrite(t *testing.T) {
	img := qemuImgPath(t)
	path := filepath.Join(t.TempDir(), "disk.qcow2")

	if err := CreateDisk(context.Background(), img, path, 8); err != nil {
		t.Fatal(err)
	}
	// Silently recreating a disk would destroy a guest's data.
	if err := CreateDisk(context.Background(), img, path, 8); err == nil {
		t.Error("CreateDisk overwrote an existing disk")
	}
}

func TestCreateDiskValidation(t *testing.T) {
	dir := t.TempDir()
	if err := CreateDisk(context.Background(), "", filepath.Join(dir, "a.qcow2"), 8); err == nil {
		t.Error("expected an error without qemu-img")
	}
	if err := CreateDisk(context.Background(), "qemu-img", filepath.Join(dir, "b.qcow2"), 0); err == nil {
		t.Error("expected an error for a zero-sized disk")
	}
}

func TestResizeDisk(t *testing.T) {
	img := qemuImgPath(t)
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	ctx := context.Background()

	if err := CreateDisk(ctx, img, path, 8); err != nil {
		t.Fatal(err)
	}

	if err := ResizeDisk(ctx, img, path, 16); err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}
	info, err := Info(ctx, img, path)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(16) << 30; info.VirtualSize != want {
		t.Errorf("VirtualSize after grow = %d, want %d", info.VirtualSize, want)
	}

	// Shrinking silently discards whatever the guest wrote past the new end.
	err = ResizeDisk(ctx, img, path, 4)
	if err == nil {
		t.Fatal("ResizeDisk shrank the disk; that destroys data")
	}
	if !strings.Contains(err.Error(), "shrink") {
		t.Errorf("error %q should explain the refusal to shrink", err)
	}

	// Resizing to the current size is a no-op, not an error.
	if err := ResizeDisk(ctx, img, path, 16); err != nil {
		t.Errorf("resizing to the same size should succeed, got %v", err)
	}
}

func TestCreateEFIVars(t *testing.T) {
	dir := t.TempDir()

	// The variable store must exactly match the firmware flash size, or the
	// pflash device refuses to attach.
	code := filepath.Join(dir, "code.fd")
	const codeSize = 64 << 20
	f, err := os.Create(code)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(codeSize); err != nil {
		t.Fatal(err)
	}
	f.Close()

	vars := filepath.Join(dir, "vars.fd")
	if err := CreateEFIVars(vars, code); err != nil {
		t.Fatalf("CreateEFIVars: %v", err)
	}

	fi, err := os.Stat(vars)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != codeSize {
		t.Errorf("variable store is %d bytes, want %d to match the firmware", fi.Size(), codeSize)
	}

	// A second call must not clobber a store that already holds boot entries.
	if err := os.WriteFile(vars, []byte("existing boot entries"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateEFIVars(vars, code); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(vars)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing boot entries" {
		t.Error("CreateEFIVars overwrote an existing variable store")
	}
}

func TestCreateEFIVarsWithoutFirmware(t *testing.T) {
	vars := filepath.Join(t.TempDir(), "vars.fd")
	// With no firmware to measure, the conventional 64 MiB is used.
	if err := CreateEFIVars(vars, "/nonexistent/code.fd"); err != nil {
		t.Fatalf("CreateEFIVars: %v", err)
	}
	fi, err := os.Stat(vars)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 64<<20 {
		t.Errorf("variable store is %d bytes, want the conventional 64 MiB", fi.Size())
	}
}

// The generated disk must be one QEMU will actually accept with the exact
// blockdev options the arg builder emits.
func TestCreatedDiskWorksWithBuiltArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping a test that runs QEMU")
	}
	caps, err := host.Probe(context.Background())
	if err != nil || !caps.Ready() {
		t.Skip("a complete QEMU installation is required")
	}

	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if err := CreateDisk(context.Background(), caps.QemuImg, disk, 4); err != nil {
		t.Fatal(err)
	}

	blockdev := "driver=file,node-name=d0,filename=" + disk +
		",aio=" + caps.BestAIO() + ",cache.direct=on,cache.no-flush=off,discard=unmap"

	out, err := runQEMUAndQuit(t, caps.QemuSystem,
		"-machine", "virt", "-display", "none", "-nodefaults",
		"-blockdev", blockdev,
		"-blockdev", "driver=qcow2,node-name=d1,file=d0,discard=unmap",
		"-S", "-monitor", "none", "-qmp", "stdio")
	if err != nil {
		t.Fatalf("QEMU rejected the generated disk options: %v\n%s", err, out)
	}
}
