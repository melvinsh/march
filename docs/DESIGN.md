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

### The boot

A guest reaches its desktop 4.2 seconds after QEMU starts, timed from launch to
the compositor's first frame — the first thing this display path can show at
all. Getting there was four separate waits, none of which was work:

- **GRUB waited ten seconds on every boot.** `grub-mkconfig` writes a
  `load_video` that insmods every video driver GRUB has ever had, including
  several arm64-efi does not build. The first missing one is an error, and
  GRUB answers an error inside a menu entry by printing it and waiting ten
  seconds for a keypress that is never coming. The install now drops the
  insmod lines whose modules are not on disk.
- **The firmware offered a boot menu for five seconds.** EDK2's timeout is a
  UEFI variable, so `efibootmgr -t 0` in the installed system writes it into
  the machine's own varstore, where it survives every later boot.
- **The initramfs had nothing to do.** Arch Linux ARM's kernel has virtio_blk
  and ext4 built in, so a guest can mount its root without one — and the phase
  cost 2.2 seconds of a systemd, a udev and a switch-root finding nothing left
  to load. march boots without it, which means `root=` has to name a device
  rather than a UUID, since resolving a UUID is itself a job for an initramfs.
  What it gives up is the recovery path: a kernel that cannot mount its root
  now stops rather than dropping to an initramfs shell. The end-to-end suite
  installs and boots a real machine, so a kernel that stopped building those
  drivers in would fail there rather than in front of somebody.
- **The EFI partition was on the critical path.** Nothing reads it while the
  machine runs — it is written to when GRUB is reinstalled — but as a plain
  fstab entry `local-fs.target` waited for it to appear and be checked, and
  everything behind that waited too. It is mounted as an automount instead,
  on the first access.

That is 8.5s to 2.7s of guest boot by `systemd-analyze`, and about twenty
seconds to 4.2 from the outside.

Four other things were measured and left alone, which is worth writing down so
they are not tried again: `quiet` plus `systemd.show_status=false` saved
nothing once the waits were gone, so the boot log stays; a zstd initramfs was
worth 70ms before the initramfs went away entirely; trimming mkinitcpio's hooks
changed nothing, because the cost was the phase and not its contents; and
turning off `cache.direct` to let the host cache the guest's reads changed
nothing either, which says the boot is not waiting on the disk.

What remains is roughly a second of firmware, 2.7 seconds of guest, and half a
second for SDDM's autologin to hand over to Hyprland.

There is no splash over the wait, because there is nothing to put it on:
QEMU's macOS display shows only what the guest renders through virgl, so the
firmware logo, GRUB and the kernel log are all invisible in that window, and a
boot splash would be too. Shortening the wait was the only thing left.

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

## Packaging

march ships through Homebrew as two formulae: `march` itself and `qemu-march`,
the patched QEMU that makes `virtio-gpu-gl` work on macOS. Both live in
[`Formula/`](../Formula) in this repository, next to the patch and the NOTICE
files that explain where it came from — the patch is march's own work, and it
belongs with the code it exists for.

Homebrew cannot install from here, though. `brew tap melvinsh/march` resolves
to `github.com/melvinsh/homebrew-march` and nothing else, so the formulae have
to exist in a repository with that name. That repository is a **mirror**: every
file in its `Formula/` is a copy, and editing one there is how the two drift
apart.

[`docs/publish-formulae.sh`](publish-formulae.sh) is what keeps them identical.
It reads the sha256 off the tag's tarball as GitHub serves it rather than off a
local `git archive` — the two differ, and that difference shipped a formula
that could not install until somebody noticed — writes url, sha256 and version
into `Formula/march.rb`, copies the directory into the tap, and refuses to
finish unless `diff -r` between the two comes back empty. It ends by resolving
the formula through `brew fetch`, which is the install path a user takes minus
the build.

It is safe to re-run: a release that is already published reports that nothing
changed.

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
