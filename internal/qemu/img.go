package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DiskInfo is the subset of `qemu-img info` march cares about.
type DiskInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	ActualSize  int64  `json:"actual-size"`
}

// CreateDisk makes a sparse qcow2 image of the requested size.
//
// The image uses a 64 KiB cluster (the largest qcow2 supports) and preallocated
// metadata. Both trade a little upfront space for markedly better sustained
// write throughput, because the guest stops paying for cluster allocation and
// L2 table growth during normal use.
func CreateDisk(ctx context.Context, qemuImg, path string, sizeGiB int) error {
	if qemuImg == "" {
		return fmt.Errorf("qemu-img is not available")
	}
	if sizeGiB < 1 {
		return fmt.Errorf("disk size must be at least 1 GiB, got %d", sizeGiB)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("disk already exists at %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating disk directory: %w", err)
	}

	out, err := output(ctx, qemuImg, "create",
		"-f", "qcow2",
		"-o", "cluster_size=65536,preallocation=metadata,lazy_refcounts=on",
		path, strconv.Itoa(sizeGiB)+"G")
	if err != nil {
		return fmt.Errorf("creating disk: %w: %s", err, out)
	}
	return nil
}

// CreateEFIVars creates a VM's private UEFI variable store. pflash devices
// require the backing file to match the flash size exactly, so the store is
// sized from the firmware code image it will be paired with.
func CreateEFIVars(path, firmwareCode string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already provisioned
	}
	size := int64(64 << 20) // the conventional aarch64 flash size
	if fi, err := os.Stat(firmwareCode); err == nil && fi.Size() > 0 {
		size = fi.Size()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating firmware directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("creating EFI variable store: %w", err)
	}
	defer f.Close()
	// A sparse file of zeros is a valid, empty variable store; edk2 initialises
	// it on first boot.
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("sizing EFI variable store: %w", err)
	}
	return nil
}

// Info reports on an existing disk image.
func Info(ctx context.Context, qemuImg, path string) (DiskInfo, error) {
	var di DiskInfo
	out, err := output(ctx, qemuImg, "info", "--output=json", path)
	if err != nil {
		return di, fmt.Errorf("reading disk info: %w: %s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &di); err != nil {
		return di, fmt.Errorf("parsing disk info: %w", err)
	}
	return di, nil
}

// ResizeDisk grows an image. Shrinking is refused: qcow2 cannot know where the
// guest filesystem ends, so truncating silently destroys data.
func ResizeDisk(ctx context.Context, qemuImg, path string, newGiB int) error {
	di, err := Info(ctx, qemuImg, path)
	if err != nil {
		return err
	}
	want := int64(newGiB) << 30
	if want < di.VirtualSize {
		return fmt.Errorf("refusing to shrink disk from %d GiB to %d GiB: this would destroy data",
			di.VirtualSize>>30, newGiB)
	}
	if want == di.VirtualSize {
		return nil
	}
	out, err := output(ctx, qemuImg, "resize", path, strconv.Itoa(newGiB)+"G")
	if err != nil {
		return fmt.Errorf("resizing disk: %w: %s", err, out)
	}
	return nil
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	b, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(b)), err
}
