# Design

Why march works the way it does: how an unattended Arch install is possible at
all, why the installer image is Archboot, what the QEMU command line is tuned
for, and how the guest window behaves.

## How the unattended install works

There is no kickstart or preseed for Arch, and Archboot's own installer is
interactive. Two properties of the live environment make automation possible
anyway:

1. **Archboot honours an `autorun=<url>` kernel parameter**, fetching and
   running a script as root once the system is up. This is a supported
   Archboot feature, not a hack.
2. **Its GRUB menu exposes a command line**, reachable over the serial console.

So march starts a throwaway HTTP server bound to loopback, boots the ISO,
drives GRUB over the serial console to boot with its own kernel command line
carrying `autorun=`, and the guest then installs itself. Nothing is patched
and no ISO is repacked.

Taking over the command line also drops Archboot's `nr_cpus=1`, which
otherwise pins the installer to a single core.

The generated script announces each phase on the serial console, so march can
show real progress and stop with a useful error rather than a timeout:

```
✓ Preparing the installer
✓ Partitioning the disk
✓ Installing the base system
⣾ Installing the desktop
· Configuring the system
· Installing the bootloader
```

The full console transcript is kept in `install.log` inside the VM directory.

## Why Archboot

Arch Linux publishes no aarch64 ISO, and Arch Linux ARM ships only rootfs
tarballs that must be unpacked onto an ext4 filesystem as root, which cannot
be done natively on macOS. [Archboot](https://archboot.com) builds bootable
UEFI aarch64 ISOs carrying a full Arch installer, so it is the one approach
that works identically on every host. march resolves the current release from
`release.archboot.com`.

march always uses Archboot's `latest` build (~290 MiB). It is the smallest of
the three they publish, and since installing a desktop needs the network
regardless, the larger offline-capable images buy nothing. There is no choice
to make, so march does not ask.

Downloads are cached, resume after an interruption, and are only promoted to a
usable ISO once complete.

Two quirks shape the installer. Archboot's package database claims tools like
`pacstrap` and `mkfs.ext4` are installed while their binaries are stripped out
of the image, so `pacman -S --needed` skips them and the install fails with
everything "already installed" but nothing present. march reinstalls them
without `--needed`. And Arch Linux ARM rotates its build-server signing keys
faster than the installer image is rebuilt, so packages arrive signed by keys
the live keyring has never seen; march refreshes the keyring first, or pacman
would stop on an import prompt no unattended run can answer.

## The window

QEMU opens VMs at a cramped 1280×800 by default. march sizes the guest from
your actual screen instead, and an installed machine opens **fullscreen** at
your display's native resolution.

Installation itself runs headless. It is driven over the serial console and
march reports progress in the terminal, so opening a window there would only
cover that with a blank screen. The window belongs to the installed system.

Because a guest framebuffer is measured in the host's *physical* pixels, a
Retina display gives a guest at full pixel density: sharp, but with everything
half the size it would be on the host. march therefore sets the desktop's UI
scale to **200%** by default, which is what makes the resolution readable
rather than merely detailed. Wayland scales in the compositor, so it arrives
through Hyprland's `monitor` line; only what XWayland cannot learn from that
(`QT_SCALE_FACTOR` and the cursor size) is set in the environment.

A fresh boot spends its first seconds on firmware and bootloader, then the
kernel's framebuffer console scrolls the boot log for a few more — but from
there until Hyprland's first frame, the window has nothing to draw, and that
hand-off through SDDM's autologin is long enough to read as a hung black
screen. march fills it with one dark wallpaper, painted twice: the SDDM greeter
shows it from the moment the display manager takes the screen, and swaybg
redraws the same image on the compositor's first frame. Whatever the 
display-manager and compositor are doing, the window is showing something
starting rather than a void.

**All keystrokes go to the guest**, including combinations macOS would
otherwise intercept. `Cmd+Space` matters most, since Hyprland binds it to the
launcher. This is QEMU's `full-grab`; macOS asks for accessibility permission
the first time, and without it the grab silently does not happen. Turn it off
with `capture_all_keys: false` in `vm.json`.

There is one trade-off worth knowing about, and it is QEMU's rather than
march's. Its macOS backend offers two mutually exclusive window behaviours:

| `zoom-to-fit` | Window opens at | Draggable |
| --- | --- | --- |
| off (default) | the guest's resolution | no |
| on | a small fixed default, and the guest is forced to match | yes |

march leaves it off, because turning it on makes QEMU ignore the configured
resolution entirely and shrink the guest to a fixed ~640×414. Resolution is
instead changed from inside the guest, through Hyprland's `monitor` line, and
the window follows exactly. Setting `resizable_window: true` in `vm.json` opts
into the draggable behaviour.

## Sound

Guests get an Intel HDA card and a full PipeWire stack, so the volume keys and
`SUPER + CTRL + A` do something. The card is HDA rather than virtio-sound,
which would otherwise fit march's paravirtual hardware better: Arch Linux ARM's
kernel ships no `virtio_snd` module, so a virtio card would enumerate in the
guest with no driver to bind it.

A machine with no window gets the same card on QEMU's null backend. The guest
sees identical hardware either way, and nothing opens a CoreAudio device for a
VM nobody is listening to.

## The optimized settings

march does not ship a fixed command line. It probes what your QEMU build
actually supports and derives the rest, because builds differ in ways that
matter — Homebrew's macOS QEMU has no `io_uring`/`native` AIO and no
virgl-backed GPU, where a typical Linux build has both. Press `y` on any VM to
see the exact invocation.

**CPU and machine**

- `-accel hvf` on Apple Silicon, `kvm` on ARM64 Linux, `tcg` otherwise.
  Hardware acceleration needs matching host and guest architectures, so an x86
  host correctly falls back to emulation.
- `-cpu host` when accelerated (`max` under TCG, where pass-through is
  meaningless).
- `gic-version=3` — required above 8 vCPUs, expected by modern kernels, and
  the only version HVF supports.
- `highmem=on` so guests can use more than ~3 GiB.
- `graphics=off` for headless machines, dropping the unused display pipeline.

**Storage** (usually the bottleneck for an install)

- qcow2 with a 64 KiB cluster, preallocated metadata and lazy refcounts, so
  the guest stops paying for cluster allocation during normal writes.
- Split `-blockdev` file and format layers, with `cache.direct=on` (O_DIRECT)
  so guest pages are not cached twice.
- The fastest AIO backend that this build demonstrably supports.
- `virtio-blk-pci` with one virtqueue per vCPU and a dedicated `iothread`, so
  disk I/O does not serialise behind the main loop.
- `discard=unmap` end to end, so `fstrim` in the guest shrinks the host file.

**Boot and the rest**

- UEFI via split pflash: a shared read-only firmware image plus a per-VM
  writable variable store, so VMs keep their own boot entries and a QEMU
  upgrade updates the firmware for all of them.
- GRUB boots straight through with a zero timeout: an installed VM has one boot
  target and nobody waiting at its menu.
- The installer attaches as a real CD-ROM behind `virtio-scsi`, which needs no
  USB controller and gives installers the removable-media semantics they
  expect. Boot order flips to the disk once installed.
- `virtio-net-pci` on the user-mode stack with an SSH forward bound to
  loopback, so no privileges are required and the guest is not exposed to the
  LAN.
- `virtio-rng-pci`, so boot does not stall waiting for entropy.
- A serial console on a unix socket even for windowed VMs, and a QMP socket
  for clean ACPI shutdown.

Sizing defaults leave the host usable: half the cores and a quarter of RAM
when accelerated, much less under TCG, with floors, ceilings, and warnings
when you overcommit.

## Layout

VMs live under `$XDG_DATA_HOME/march` (override with `-home` or `MARCH_HOME`):

```
march/
  images/                    cached installer ISOs
  vms/<name>/
    vm.json                  the specification
    disk.qcow2               the virtual disk
    efi-vars.fd              this VM's UEFI variables
    install.log              full transcript of the unattended install
    console.sock  qmp.sock   serial console and control socket
    vm.pid  vm.log  qemu.log
```

The account password is never written to disk. It is held in memory only for
the duration of the install, and delivered to the guest over a loopback-bound
HTTP server.
