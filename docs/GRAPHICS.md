# Graphics

How march gets a guest onto the host GPU on macOS, where the ceiling is, and
what it costs. The short version lives in the
[README](../README.md#hardware-acceleration); this is the long one.

## Hardware acceleration

Homebrew's QEMU is built without OpenGL, and the community taps that add it
omit `libslirp`, so their guests have no network, which march's installer
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

Two things follow automatically: the desktop turns its blur, shadows and
animations on, and the guest reports `+virgl` at the kernel level. A headless VM
still renders in software, because the GL device is only valid alongside a
display that has GL enabled, so its effects stay off.

The formula carries an out-of-tree patch that teaches QEMU's Cocoa display to
render through OpenGL. Upstream has not merged this work, so the patch is
rebased per QEMU release; provenance is in
[`Formula/patches/NOTICE`](../Formula/patches/NOTICE).

## Chrome is the exception

The desktop is accelerated; **the browser is not**. Chrome's GPU process does
not start at all on a march guest, and it says why on the way out:

```
eglCreateContext ES 3.0 failed with error EGL_BAD_ATTRIBUTE.
  ES version fallback is disabled.
Exiting GPU process due to errors during initialization
```

Chrome asks EGL for an **ES 3.0 context** before it collects any GPU
information at all, and virgl refuses it: *"Requested version is not
supported"*. Chromium would otherwise step down to ES 2.0, but
`ShouldFallbackToSWIfGLES3NotSupported()` in `ui/gl/gl_features.cc` returns
`true` unconditionally off Windows and ChromeOS, so on Linux that retry is
compiled out. The GPU process exits instead.

The peculiar part is that the context is not unavailable. On the same device in
the same session, `eglinfo` is granted ES 3.0 on the Wayland, surfaceless and
GBM platforms, and Hyprland is *running* on one — the 3.2-to-3.0 retry recorded
further down this page is how it got there:

```
OpenGL ES profile renderer: virgl (Apple M5 Pro)
OpenGL ES profile version:  OpenGL ES 3.0 Mesa 26.1.6
```

So what is refused is the set of attributes Chrome asks with, not the version.
That refusal happens before ANGLE chooses a backend, which is why no flag
reaches past it. Every route was measured in a guest, windowed under Hyprland
and again headless, and each produces the same two lines:

| Route | Result |
| --- | --- |
| default, no flag | ES 3.0 refused, GPU process exits |
| `--use-angle=gl-egl` | ANGLE's desktop-GL backend over EGL, so no X server is involved; refused identically |
| `--use-angle=gles-egl`, `--use-angle=gles` | as above |
| `--use-angle=gl` | ANGLE's GL backend picks GLX instead: *"Could not open the default X display"* |
| `--use-angle=vulkan` | the default written out; the guest has no Vulkan driver, for the reason below |
| `--ignore-gpu-blocklist`, `--enable-gpu-rasterization` | nothing to unblock — the failure precedes GPU info collection |
| `--use-cmd-decoder=validating` | *"Ignoring request for the validating command decoder. It is not supported on this platform."* |
| `MESA_GLES_VERSION_OVERRIDE`, `MESA_GL_VERSION_OVERRIDE`, `MESA_EXTENSION_OVERRIDE=+GL_KHR_robustness` | the version Mesa reports is not the thing being refused |
| `vulkan-virtio` (Mesa's Venus) | everything is in place except one kernel feature — see below |

`internal/vm/chromelab_test.go` is how that table was produced: it installs one
accelerated guest, leaves it running, and runs any probe script inside the live
session, so measuring a newer Chrome is one command rather than another
afternoon.

### No WebGL, rather than software WebGL

One consequence deserves its own heading, because it is what a user actually
meets: the guest's browser has **no WebGL**. `getContext('webgl')` returns
null. Chrome used to fall back to SwiftShader, its CPU rasterizer, and earlier
versions of this page recorded exactly that; Chrome 151 deprecated the
automatic fallback and mentions it on the way past:

```
Automatic fallback to software WebGL has been deprecated. Please use the
  --enable-unsafe-swiftshader flag to opt in to lower security guarantees
```

march does not pass that flag. SwiftShader JIT-compiles shaders inside the GPU
process, which is precisely why Chrome stopped reaching for it unprompted, and
a browser exists to run untrusted code from the network. Trading that surface
away would buy back software rendering — not the GPU — so the guest goes
without WebGL instead. A guest that needs it can say so itself:
`/usr/local/bin/march-chrome` is the one place Chrome's flags live.

Everything else in the desktop (the compositor, its effects, and every native
application) is accelerated.

### Venus, and why it cannot run on macOS

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
  descriptors do not accept those flags, and the flag is unnecessary anyway:
  the only `kevent()` wait already passes an explicit timeout.

With those applied Venus starts: the render server spawns, the
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

## The ceiling, and the log lines it produces

On macOS the guest tops out at **GLES 3.0**. virgl's capabilities come from the
host GL context, which is ANGLE on Metal, and Metal has no geometry-shader
stage. ANGLE refuses a GLES 3.1 or 3.2 context outright and exposes neither
`geometry_shader` nor `tessellation_shader`. aquamarine always asks for GLES 3.2
first and falls back to 3.0, so a healthy guest logs the refusal on the way to
succeeding.

A handful of `ERR` lines therefore appear in `hyprland.log` on every march
guest. None indicates a fault. The counts vary between runs, since the renderer
lines repeat once per renderer created and the page-flip line depends on how
much the window is resized. What is fixed is the *set*, not the tally:

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
