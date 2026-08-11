# march

A terminal UI that installs Arch Linux ARM on QEMU **completely unattended**
and boots straight into a Hyprland desktop configured after
[Omarchy](https://github.com/basecamp/omarchy). Answer a handful of questions,
walk away, come back to a working machine. Built with
[Charm](https://github.com/charmbracelet)'s Bubble Tea v2, Bubbles, Lip Gloss
and Huh.

```
  march  Arch ARM on QEMU
  qemu 11.0.3  ·  HVF  ·  10 cores  ·  32 GiB  ·  aio threads

   NAME            STATE     CPU  MEMORY   DISK   SSH    INSTALLER
 ● arch-dev        running   4    8 GiB    32G    :2222  installed
 ○ arch-test       stopped   2    4 GiB    32G    :2223  pending

  n new VM • ↵ details • s start • x stop • d delete • ? help • q quit
```

## Install

On macOS, this brings the whole stack — march itself and the patched QEMU that
gives guests [hardware-accelerated graphics](#hardware-acceleration):

```sh
brew trust melvinsh/march
brew install melvinsh/march/march
march
```

The `brew trust` line is not optional. Homebrew refuses to load formulae from
untrusted third-party taps, and march's QEMU lives in the same tap — so without
it the install stops partway with *"Refusing to load formula
melvinsh/march/qemu-march from untrusted tap"*. Trusting the tap covers both
formulae; it also taps the repository, so no separate `brew tap` is needed.

The GPU stack comes from the `startergo` taps, which Homebrew adds
automatically as dependencies. If it reports those as untrusted too:

```sh
brew trust startergo/angle startergo/libepoxy startergo/virglrenderer
```

Or build it yourself, bringing your own QEMU. aarch64 support and UEFI firmware
are the only requirements:

```sh
go install github.com/melvinsh/march@latest
```

| Platform | Packages |
| --- | --- |
| macOS | `brew install qemu` (software rendering; see [Hardware acceleration](#hardware-acceleration)) |
| Debian/Ubuntu | `apt install qemu-system-arm qemu-utils qemu-efi-aarch64` |
| Arch | `pacman -S qemu-system-aarch64 edk2-aarch64` |
| Fedora | `dnf install qemu-system-aarch64 edk2-aarch64` |

On startup march probes your QEMU build and reports anything missing rather
than failing later with a raw QEMU error.

## Usage

| Key | Action |
| --- | --- |
| `n` | Create and install a VM (guided wizard) |
| `↵` | Show VM details |
| `y` | Show the exact QEMU command line |
| `s` / `x` | Start / shut down |
| `r` | Restart |
| `d` | Delete (asks first) |
| `c` | Print the serial-console command |
| `R` | Refresh |
| `?` | Full help |
| `q` | Quit — running VMs keep running |

Creating a VM asks for a name, username and password, then does
everything else on its own: downloads the installer, allocates a sparse qcow2
disk, partitions it, installs the base system and desktop, configures the
account, installs the bootloader, and boots into the desktop with the user
already logged in. On a fast connection the whole install takes a couple of
minutes.

Reach a running guest with `ssh -p <port> <user>@127.0.0.1` (the port is in the
list), or attach to its serial console with the command `c` prints.

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

### The desktop

There is one, and it is not a choice: **Hyprland on SDDM, configured after
Omarchy**, with the toolset and the menu described below. march used to offer
XFCE, GNOME and Plasma as well, which meant scaling, autologin, window resizing
and the whole toolset each existed in two or three variants that nothing
verified equally. The desktop is the product.

Stock QEMU on macOS has no OpenGL at all, so guests render on the CPU through
llvmpipe. march ships its own QEMU that fixes this — see **Hardware
acceleration** below. Without it Hyprland still runs, with blur, shadows and
animations disabled; with it they are restored automatically.

### The Hyprland desktop

march mirrors Omarchy v3.8.4's setup: its top bar, its window-management
behaviour and its keyboard shortcuts. The bindings are Omarchy's own — the
window-management half is copied verbatim, since those are native Hyprland
dispatchers.

| | |
| --- | --- |
| Compositor | Hyprland, with Omarchy's look and feel |
| Bar | Waybar, with Omarchy's layout |
| Menu | `march-menu` on `SUPER + SPACE` and the bar's Arch button |
| Launcher | fuzzel on `SUPER + ALT + SPACE` |
| Notifications | mako, `SUPER + ,` to dismiss |
| Lock | hyprlock |
| Terminal | Alacritty on `SUPER + RETURN` |
| Keys | `SUPER + K` lists every binding; `SUPER + ESCAPE` opens the System branch |

None of Omarchy's own packages are built for aarch64, so bindings that called
them are repointed at equivalents that are packaged (`grim`/`slurp` for
screenshots, `swayosd-client` for volume, `wiremix`, `nmtui`, `btop`) or
dropped. Nothing is left as a shortcut that silently does nothing — a test
enforces that every binding runs a program the install actually provides.
Omarchy's launcher, walker, has no aarch64 package; fuzzel takes its place.

### The menu

Omarchy's bar is a front end for `omarchy-menu`: a tree of actions covering
capture, clipboard, toggles, config editing, package management and power. That
bar is a Quickshell plugin and quickshell has no aarch64 package, so march
rebuilds the **content** on waybar and fuzzel. Route names are Omarchy's, so
`march-menu capture` opens what `omarchy-menu toggle capture` opens.

| Branch | What is in it |
| --- | --- |
| Apps | the launcher |
| Learn | the key bindings, and the Hyprland, Arch and Bash references |
| Capture | screenshots, annotation, colour picker, OCR, screen recording |
| Clipboard | history, via cliphist |
| Emoji | rofimoji, typed into the focused window |
| Toggle | notification silencing, the bar, window gaps, workspace layout |
| Setup | every config file the desktop reads, in `$EDITOR` |
| Install / Remove | a package picker over pacman, and orphan cleanup |
| Update | system packages, cache cleaning, `fastfetch` |
| System | lock, log out, reboot, shut down |

The bar gained the widgets that go with it: pending updates, a do-not-disturb
indicator, a recording indicator, and a power button. Each is empty until it has
something to say, as Omarchy's are.

`SUPER + CTRL + C` opens Capture directly, `SUPER + CTRL + O` the toggles,
`SUPER + CTRL + V` the clipboard, `SUPER + CTRL + E` the emoji picker —
Omarchy's keys, unchanged.

Two entries Omarchy has are missing on purpose. **Suspend** is not offered: the
guest can enter it — `/sys/power/state` lists `mem` — but only a QMP
`system_wakeup` brings the machine back, and neither march's window nor QEMU's
has anything that sends one, so the entry would strand the VM. And there is **no
idle daemon** — a guest window sits inside a host that already locks itself, so
`hypridle` was removed rather than configured.

Recording is the one place the tooling had to change rather than be repointed:
Omarchy records with `gpu-screen-recorder`, which needs a hardware encoder that
virtio-gpu does not have, so march uses `wf-recorder` and encodes on the CPU.

The configuration lands in `/etc/skel`, so it is copied into the account at
creation and is yours to edit. Provenance and the full list of changes are in
`internal/install/assets/hyprland/NOTICE`.

It is written in **Lua**, not hyprlang: Hyprland deprecated its own config
language in 0.55 and reads `~/.config/hypr/hyprland.lua` in preference to
`hyprland.conf`, which it will stop reading entirely a release or two later.
Omarchy has not migrated, so march's files are a hand translation of its
v3.8.4 `.conf` — same bar, same bindings, same behaviour. Two shortcuts had to
be rewritten rather than re-spelled, because the commands they shelled out to
are gone in the Lua era: zoom now keeps its level in Lua instead of reading it
back with `hyprctl keyword`, and the menu's Log out dispatches `hl.dsp.exit()`.

The chosen user is logged in automatically through an SDDM drop-in, so the
machine really does land on a desktop rather than a login screen.

### The browser

The browser is **Google Chrome**, and it is the one thing march installs that
does not come from an Arch mirror. Google began shipping Linux builds for
aarch64 in 2026, as `.deb` and `.rpm` only; no Arch package carries an aarch64
build and march installs no AUR helper. So the install fetches Google's own
`.deb` and unpacks it, which is what a PKGBUILD would do anyway.

Two consequences worth stating plainly. Google's CDN is now a dependency of
every install — if it is unreachable, the install fails rather than quietly
falling back. And updates do not come from `pacman`: the cron job Google's
package ships to add an apt repository is deliberately left out, since a machine
with no apt would run it daily to no effect.

Chrome is the default browser three ways over: the desktop entry in
`mimeapps.list` for anything that asks `xdg-mime`, `$BROWSER` for anything that
does not, and `apps.lua` for `SUPER + B`. Because Chrome still picks X11 unless
told otherwise, every launch march controls passes `--ozone-platform=wayland`.

Chrome's shared libraries are asked for by name in `chromePackages`, since
pacman knows nothing about a package it did not install; the end-to-end test
runs `ldd` against the unpacked binary so a missing one fails the build rather
than the first click.

### The standard toolset

A guest comes with the tools Omarchy ships, so it is a working machine on first
boot rather than a bare Arch install: `git`, `neovim`, `lazygit`, `ripgrep`,
`fd`, `fzf`, `bat`, `eza`, `zoxide`, `starship`, `tmux`, `docker` (with the
account already in the group), `mpv`, `imv`, `evince`, LibreOffice, GNOME Disks,
and the rest of the list in `internal/install/standard.go`.

**Nothing is installed that this hardware cannot run.** A package whose device
does not exist is a command that fails, a daemon that idles, and download time
paid on every install. So march omits what Omarchy ships for real laptops —
`bluez` (no controller), `ddcutil` (no DDC channel), `power-profiles-daemon` (no
battery), `wireless-regdb` (no radio), `avahi` and `cups-browsed` (multicast
does not cross QEMU's NAT), `gpu-screen-recorder` (no hardware encoder),
`hyprsunset` (no DRM gamma ramp), `gvfs-mtp` (no USB passthrough). Each omission
carries its reason in `omittedForVirtualHardware`, and a test fails if one of
them creeps back in.

One more is omitted for a different reason. `system-config-printer`'s aarch64
package installs the egg-info for its `cupshelpers` module without the module,
so it stops at `No module named 'cupshelpers'` every time it opens — found by
running it in a guest, not by reading a package list. CUPS' own interface on
`localhost:631` adds a printer perfectly well, and `cups-pdf` needs no adding.

Two programs needed a config before they worked at all here, and both now ship
one. **hyprlock** refuses to start without a config file, which quietly made
`SUPER + CTRL + L` and the menu's Lock do nothing. And **mpv**'s default GPU
output cannot create a context behind virtio-gpu without acceleration; it then
fell back to X11 and aborted on an assertion, so march names `wlshm` as the
fallback and turns off hardware decoding that does not exist.

Printing works the one way it can from behind user-mode networking: CUPS with
`cups-pdf`, so **Print** always has somewhere to go.

`linux-firmware` is gone for the same reason — 277 MB of vendor blobs for
hardware that is not there, which was the single largest thing march downloaded.

### Sound

Guests get an Intel HDA card and a full PipeWire stack, so the volume keys and
`SUPER + CTRL + A` do something. The card is HDA rather than virtio-sound,
which would otherwise fit march's paravirtual hardware better: Arch Linux ARM's
kernel ships no `virtio_snd` module, so a virtio card would enumerate in the
guest with no driver to bind it.

A machine with no window gets the same card on QEMU's null backend — the guest
sees identical hardware either way, and nothing opens a CoreAudio device for a
VM nobody is listening to.

### The window

QEMU opens VMs at a cramped 1280×800 by default. march sizes the guest from
your actual screen instead, and an installed machine opens **fullscreen** at
your display's native resolution.

Installation itself runs headless. It is driven over the serial console and
march reports progress in the terminal, so opening a window there would only
cover that with a blank screen. The window belongs to the installed system.

Because a guest framebuffer is measured in the host's *physical* pixels, a
Retina display gives a guest at full pixel density — sharp, but with everything
half the size it would be on the host. march therefore sets the desktop's UI
scale to **200%** by default, which is what makes the resolution readable
rather than merely detailed. Wayland scales in the compositor, so it arrives
through Hyprland's `monitor` line; only what XWayland cannot learn from that —
`QT_SCALE_FACTOR` and the cursor size — is set in the environment.

**All keystrokes go to the guest**, including combinations macOS would
otherwise intercept — `Cmd+Space` above all, which Hyprland binds to its
menu. This is QEMU's `full-grab`; macOS asks for accessibility permission
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

## Hardware acceleration

Homebrew's QEMU is built without OpenGL, and the community taps that add it
omit `libslirp`, so their guests have no network — which march's installer
depends on entirely. Both also take the name `qemu`, so installing one removes
the other.

march therefore ships its own formula combining the two, installed alongside
your existing QEMU rather than replacing it. `brew install melvinsh/march/march`
pulls it in already; to add it to an existing march:

```sh
brew tap melvinsh/march https://github.com/melvinsh/march
brew install melvinsh/march/qemu-march
```

It is keg-only. march prefers it automatically when present and falls back to
the stock binary otherwise, so nothing breaks if you skip it.

With it installed, guests get `virtio-gpu-gl` and a GL-enabled display, and
Mesa inside the guest renders through virgl onto the host GPU:

```
OpenGL core profile renderer: virgl (Apple M5 Pro)
```

Two things follow automatically: Omarchy's blur, shadows and animations are
re-enabled, and the guest reports `+virgl` at the kernel level. A headless VM
still renders in software — the GL device is only valid alongside a display
that has GL enabled — so its effects stay off.

The formula carries an out-of-tree patch that teaches QEMU's Cocoa display to
render through OpenGL. Upstream has not merged this work, so the patch is
rebased per QEMU release; provenance is in `Formula/patches/NOTICE`.

### Chrome is the exception

The desktop is accelerated; **the browser is not**. Chrome's WebGL works, but
on SwiftShader — ANGLE's CPU rasterizer:

```
WebGL 2.0 (OpenGL ES 3.0 Chromium) :: ANGLE (Google, Vulkan 1.3.0
  (SwiftShader Device (LLVM 10.0.0)), SwiftShader driver)
```

Chrome 151 routes all GL through ANGLE and refuses any other implementation
(`--use-gl=egl` fails with *"not found in allowed implementations:
[(gl=egl-angle,angle=default)]"*). ANGLE's default backend is Vulkan, and the
guest has no Vulkan driver for virtio-gpu, so it falls back to software.
Hyprland is unaffected because it drives EGL/virgl directly.

Three routes were measured in the guest, and all are closed:

| Route | Result |
| --- | --- |
| `--use-angle=gl` under Wayland | ANGLE's GL backend requires an X display: *"Could not open the default X display"*. |
| `--use-angle=gl` via Hyprland's XWayland | Connects, then fails with *"GLES3 is unsupported and ES version fallback is disabled"* — no WebGL at all, worse than the default. |
| `vulkan-virtio` (Mesa's Venus) | Everything is in place except one kernel feature — see below. |

Everything else in the desktop — the compositor, its effects, and every native
application — is accelerated.

#### Venus, and why it cannot run on macOS

Venus forwards the guest's Vulkan to the host GPU, which would put the browser
on the GPU too. march implements it: it detects the pieces, writes a MoltenVK
ICD manifest for the Vulkan loader, passes `venus=on,blob=on,hostmem=` to QEMU,
and installs `vulkan-virtio` in the guest. On a Linux host all of that works.

On macOS it does not, and the reason is a host/guest page-size mismatch that
sits below every layer above it.

Getting far enough to see that took three fixes to virglrenderer, kept in
`Formula/patches/virglrenderer-macos-venus-transport.patch`. Venus is served
*only* through virglrenderer's render-server proxy, and that proxy could not
start on macOS at all:

* Its transport is a `SOCK_SEQPACKET` socket pair, which Darwin's `AF_UNIX`
  does not implement. The macOS patch series already contained the
  length-prefixed framing needed for a stream socket, but wrote the selection
  as `#ifdef __APPLE__ SOCK_SEQPACKET #else SOCK_SEQPACKET #endif` — both
  branches identical, so it never took effect. QEMU aborted virgl
  initialization during guest firmware init, meaning a VM with `venus=on`
  never booted at all.
* The render server then died immediately, because it treated a failed
  `fcntl(F_SETFL, O_NONBLOCK)` on a **kqueue** descriptor as fatal. kqueue
  descriptors do not accept those flags, and the flag is unnecessary anyway —
  the only `kevent()` wait already passes an explicit timeout.

With those applied Venus genuinely starts: the render server spawns, the
guest's Venus context is created, blob resources are created, and the guest
kernel reports `+virgl +edid +resource_blob +host_visible`. It then fails on
the last step, mapping a blob into the guest:

```
MARCH-DIAG mmap addr=0x400004000 size=135168 fd=56 -> 0x400004000  ok
MARCH-DIAG mmap addr=0x400025000 size=1048576 fd=57 -> MAP_FAILED  EINVAL
```

The guest is aarch64 Linux with **4 KiB** pages, so it places blob resources at
4 KiB granularity. Apple Silicon has **16 KiB** pages, and `mmap(MAP_FIXED)`
rejects any address that is not 16 KiB-aligned. The first blob happened to land
on a 16 KiB boundary and mapped; the next did not
(`0x400025000 mod 0x4000 = 0x1000`) and failed. Nothing in the configuration
changes this: it is the guest's page size meeting the host's.

Closing it would mean either running a 16 KiB-page guest kernel, so blob
offsets are aligned by construction, or bounce-buffering misaligned blobs in
QEMU. Neither is done here.

march therefore probes for `SOCK_SEQPACKET` and leaves Venus off when it is
absent. The probe tests the kernel rather than `runtime.GOOS`, so Venus enables
itself on Linux, where all of the above is moot.

### The ceiling, and the log lines it produces

On macOS the guest tops out at **GLES 3.0**. virgl's capabilities come from the
host GL context, which is ANGLE on Metal, and Metal has no geometry-shader
stage — ANGLE refuses a GLES 3.1 or 3.2 context outright and exposes neither
`geometry_shader` nor `tessellation_shader`. aquamarine always asks for GLES 3.2
first and falls back to 3.0, so a healthy guest logs the refusal on the way to
succeeding.

A handful of `ERR` lines therefore appear in `hyprland.log` on every march
guest. None indicates a fault. The counts vary between runs — the renderer
lines repeat once per renderer created, and the page-flip line depends on how
much the window is resized — so what is fixed is the *set*, not the tally:

| Line | Why it is unavoidable |
| --- | --- |
| `Wayland backend cannot start: wl_display_connect failed` | Hyprland registers the nested-Wayland backend unconditionally (`Compositor.cpp`), and aquamarine constructs and `start()`s every registered backend before consulting its request mode (`Backend.cpp`). Launched from a TTY there is no outer compositor to connect to. Every TTY-launched Hyprland on any machine logs these three. |
| `Requested backend (wayland) could not start, enabling fallbacks` | ⤶ |
| `Implementation wayland failed, erasing.` | ⤶ |
| `[EGL] Command eglCreateContext errored out with EGL_BAD_MATCH` | Mesa's EGL debug callback reporting the GLES 3.2 refusal described above. |
| `CDRMRenderer: eglCreateContext failed with GLES 3.2, retrying GLES 3.0` | aquamarine noting its own fallback (`drm/Renderer.cpp`), which it logs at error level rather than warning. The retry succeeds. |
| `Couldn't get the gamma_size prop` | virtio-gpu has no gamma LUT, so its CRTC exposes no `GAMMA_LUT_SIZE`. The query returns 0 and the connector is used normally; only gamma/night-light adjustment is unavailable. |
| `drm: Cannot commit when a page-flip is awaiting` | Behind virtio-gpu a page-flip completes when the *host* compositor presents, so the guest can outrun it — most often while the window is being resized. aquamarine drops the frame and retries, which is the guard working. |

Alongside them, `GBM: Using modifier-less allocation` is logged at `WARN`:
virtio-gpu advertises no format modifiers, so allocation falls back to the
linear path.

None has a configuration flag, environment variable or build option that
suppresses it; removing them would mean forking Hyprland and aquamarine.
`TestHardwareAcceleratedDesktop` asserts that these are the *only* errors in the
log and prints a severity census, so anything new fails the test.

## Why Archboot

Arch Linux publishes no aarch64 ISO, and Arch Linux ARM ships only rootfs
tarballs that must be unpacked onto an ext4 filesystem as root — which cannot
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
everything "already installed" but nothing present — march reinstalls them
without `--needed`. And Arch Linux ARM rotates its build-server signing keys
faster than the installer image is rebuilt, so packages arrive signed by keys
the live keyring has never seen; march refreshes the keyring first, or pacman
would stop on an import prompt no unattended run can answer.

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

**Storage** — usually the bottleneck for an install

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
- The installer attaches as a real CD-ROM behind `virtio-scsi`, which needs no
  USB controller and gives installers the removable-media semantics they
  expect. Boot order flips to the disk once installed.
- `virtio-net-pci` on the user-mode stack with an SSH forward bound to
  loopback — no privileges required, and the guest is not exposed to the LAN.
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

## Development

```sh
go test ./...              # includes tests that run real QEMU
go test -short ./...       # unit tests only, no QEMU or network
go test -race ./...

MARCH_E2E=1 go test ./internal/vm/ -run TestUnattendedDesktopInstall -timeout 60m
MARCH_E2E=1 go test ./internal/vm/ -run TestHardwareAcceleratedDesktop -timeout 60m
```

The test suite builds command lines and then hands them to the real
`qemu-system-aarch64` to confirm it accepts every option, device and internal
reference, and boots an actual VM to exercise the QMP, pidfile and shutdown
paths. It also parses the live Archboot index, so a change to their filename
scheme surfaces as a test failure rather than a broken install.

The hardware-acceleration test is the one that cannot be faked: a
software-rendered desktop looks identical from outside, just slower, so the
test asks Mesa inside the guest what it is actually drawing with and fails if
it sees `llvmpipe`.

The install script is exercised two ways. A dry run executes it with every
destructive command stubbed out and asserts the phase markers the runner
watches for are emitted in order — and that a failure aborts with the failure
marker instead of reporting success. The opt-in end-to-end test then performs a
genuine install and logs into the finished system over its serial console to
confirm the display manager, the network and an actual desktop session are all
running.

It does not stop there. `internal/vm/testdata/guest-selftest.sh` is fetched by
the guest over the same loopback HTTP path the installer came from and run
inside the live session, so the desktop is tested by using it: every
application is launched and waited for until it maps a window, every menu route
is opened, every toggle is flipped and read back, every button in the bar is
clicked, every key is read out of the running compositor, a screenshot is taken
and OCR'd, a recording is made and played back, and a tone is pushed through
the sink. The picker and the region selector are answered by stand-ins on
`PATH`, so the code behind them runs for real. Each check prints one line, and
the Go test turns each one into a result.

```sh
MARCH_E2E=1 MARCH_E2E_KEEP=1 go test ./internal/vm/ -run TestUnattendedDesktopInstall -v
MARCH_E2E_RERUN=1 go test ./internal/vm/ -run TestGuestSelftest -v   # against the kept VM
```

The first leaves the machine running; the second re-runs the in-guest suite
against it, which is the way to iterate on the suite without paying for another
install.
