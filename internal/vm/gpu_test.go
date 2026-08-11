package vm

import (
	"context"
	"encoding/json"
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

// TestHardwareAcceleratedDesktop proves the guest renders on the host GPU
// rather than on its own CPU.
//
// The distinction is invisible from the outside — a software-rendered desktop
// looks the same, just slower — so the test asks Mesa inside the guest what it
// is actually using. It needs a QEMU built with virglrenderer (see
// Formula/qemu-march.rb) and opens a window, so it is opt-in.
func TestHardwareAcceleratedDesktop(t *testing.T) {
	if os.Getenv("MARCH_E2E") != "1" {
		t.Skip("set MARCH_E2E=1 to run the hardware acceleration test")
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

	store, err := config.NewStore(filepath.Join(os.TempDir(), "march-e2e-cache"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(store, caps)

	const name = "e2e-gpu"
	_ = mgr.Delete(ctx, name)

	rel, err := image.Resolve(ctx, nil)
	if err != nil {
		t.Skipf("cannot reach the Archboot index: %v", err)
	}
	iso, err := image.NewDownloader(store.ImagesDir()).Fetch(ctx, rel, nil)
	if err != nil {
		t.Fatalf("downloading the installer: %v", err)
	}

	spec := config.Defaults(name, caps)
	spec.CPUs, spec.MemoryMiB, spec.DiskGiB = 4, 4096, 24
	// A window is required: the GL device is only valid with a display backend
	// that has GL enabled.
	spec.Display, spec.GPU = config.DisplayCocoa, true
	if !spec.GPUAccel {
		t.Fatal("defaults did not enable GPU acceleration on a capable host")
	}

	if _, err := mgr.Create(ctx, CreateOptions{Spec: spec, ISOPath: iso}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(context.Background(), name) })

	profile := install.DefaultProfile(name)
	profile.Username, profile.Password = "arch", "marchtest"

	start := time.Now()
	if err := mgr.Install(ctx, name, profile, install.Hooks{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Logf("installed in %s", time.Since(start).Round(time.Second))

	if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(context.Background(), name) })

	c, err := console.Dial(ctx, store.Paths(name).SerialSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	bctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	if _, err := c.Expect(bctx, "login:"); err != nil {
		t.Fatalf("no login prompt: %v\n%s", err, c.Tail(2000))
	}
	_ = c.SendLine(profile.Username)
	if _, err := c.Expect(bctx, "assword"); err != nil {
		t.Fatal(err)
	}
	_ = c.SendLine(profile.Password)
	if _, err := c.Expect(bctx, "]$"); err != nil {
		t.Fatalf("could not log in: %v\n%s", err, c.Tail(2000))
	}
	time.Sleep(15 * time.Second) // let the session settle

	ask := func(cmd string) string {
		t.Helper()
		_ = c.SendLine(`printf '%s\n' "M-BE""GIN"; ` + cmd + ` 2>&1; printf '%s\n' "M-E""ND"`)
		qc, qcancel := context.WithTimeout(ctx, 3*time.Minute)
		defer qcancel()
		if _, err := c.Expect(qc, "M-BEGIN"); err != nil {
			t.Fatalf("no response to %q: %v", cmd, err)
		}
		_, out, err := c.ExpectCapture(qc, "M-END")
		if err != nil {
			t.Fatalf("%q did not finish: %v", cmd, err)
		}
		return strings.TrimSpace(out)
	}

	// The kernel half: virtio-gpu only advertises virgl when QEMU offers it.
	if drm := ask("dmesg 2>/dev/null | grep -i 'virgl' | head -2"); !strings.Contains(drm, "+virgl") {
		t.Errorf("the guest kernel does not report virgl: %q", drm)
	}

	// The userspace half, and the one that decides whether frames are drawn on
	// the GPU: Mesa falls back to llvmpipe silently if virgl is unusable.
	renderer := ask("eglinfo -B 2>/dev/null | grep -i 'core profile renderer' | head -1")
	if renderer == "" {
		_ = ask("echo " + profile.Password + " | sudo -S pacman -Sy --noconfirm --needed mesa-utils 2>&1 | tail -1")
		renderer = ask("eglinfo -B 2>/dev/null | grep -i 'core profile renderer' | head -1")
	}
	switch {
	case strings.Contains(renderer, "virgl"):
		t.Logf("hardware rendering confirmed: %s", renderer)
	case strings.Contains(strings.ToLower(renderer), "llvmpipe"):
		t.Errorf("guest fell back to software rendering: %s", renderer)
	default:
		t.Errorf("could not determine the renderer: %q", renderer)
	}

	if failed := ask("systemctl --failed --no-legend --no-pager | head -5"); strings.TrimSpace(failed) != "" {
		t.Errorf("the guest has failed units:\n%s", failed)
	}

	// Venus forwards the guest's Vulkan to the host GPU. Without it Chromium,
	// which renders through ANGLE's Vulkan backend, falls back to SwiftShader
	// on a guest whose desktop is otherwise fully accelerated.
	venus := caps.SupportsVenus()
	t.Logf("host Venus support: %v (device=%v driver=%q loader=%q)",
		venus, caps.VenusDevice, caps.MoltenVK, caps.VulkanLoader)
	if venus {
		vk := ask("vulkaninfo --summary 2>&1 | grep -E 'deviceName|driverName|apiVersion' | head -4")
		switch {
		case strings.Contains(vk, "virtio") || strings.Contains(strings.ToLower(vk), "venus"):
			t.Logf("guest Vulkan reaches the host GPU: %s", vk)
		default:
			t.Errorf("Venus is enabled but the guest has no virtio Vulkan device:\n%s", vk)
		}
	}

	_ = ask(`cat > /tmp/webgl.html <<'HTML'
<html><body><canvas id=c1></canvas><script>
var g=document.getElementById('c1').getContext('webgl2')||document.getElementById('c1').getContext('webgl');
var d=g&&g.getExtension('WEBGL_debug_renderer_info');
document.body.setAttribute('data-r',g?(g.getParameter(g.VERSION)+' :: '+(d?g.getParameter(d.UNMASKED_RENDERER_WEBGL):'masked')):'NONE');
</script></body></html>
HTML
echo ok`)
	webgl := ask(`timeout 90 chromium --headless=new --no-sandbox --disable-gpu-sandbox ` +
		`--dump-dom file:///tmp/webgl.html 2>/dev/null | grep --color=never -o 'data-r="[^"]*"'`)
	software := strings.Contains(strings.ToLower(webgl), "swiftshader") ||
		strings.Contains(strings.ToLower(webgl), "llvmpipe")
	switch {
	case strings.Contains(webgl, "NONE") || strings.TrimSpace(webgl) == "":
		t.Errorf("the browser has no WebGL at all: %q", webgl)
	case venus && software:
		t.Errorf("Venus is enabled but the browser still renders in software: %s", webgl)
	case software:
		t.Logf("browser WebGL runs on the CPU, as expected without Venus: %s", webgl)
	default:
		t.Logf("browser WebGL is hardware accelerated: %s", webgl)
	}

	// Record what the guest actually negotiated. GLES 3.0 is the ceiling on
	// macOS (see knownBenignErrors), so this is a log line, not an assertion.
	t.Logf("guest GL: %s", ask("eglinfo -B 2>/dev/null | grep -iE 'opengl es profile version' | head -1"))

	// Locate the running compositor through its log, and take the instance
	// signature from that same directory. Deriving it any other way risks
	// pointing hyprctl at a stale instance whose socket is gone, which answers
	// nothing and reads exactly like a clean desktop.
	//
	// A wrong path yields no lines to inspect, which is indistinguishable from
	// a spotless guest, so an unreadable log is a failure rather than a pass.
	logPath := ask(`ls /run/user/1000/hypr/*/hyprland.log 2>/dev/null | head -1`)
	if !strings.HasSuffix(logPath, "hyprland.log") {
		t.Fatalf("could not locate Hyprland's log, so the error check below would prove nothing: %q", logPath)
	}
	total := ask("grep -c '' " + logPath)
	if total == "" || total == "0" {
		t.Fatalf("Hyprland's log at %s is empty; the error check below would prove nothing", logPath)
	}
	sig := filepath.Base(filepath.Dir(logPath))
	t.Logf("scanning %s (%s lines), instance %s", logPath, total, sig)

	// A desktop that starts with a config-error banner is not "working".
	//
	// The plain form of this command prints one line per error and nothing at
	// all when the config is clean, so an empty answer is indistinguishable
	// from an hyprctl that never reached the compositor. The JSON form always
	// returns an array, so decoding it successfully is positive proof that
	// hyprctl was answered.
	//
	// A clean config yields [""] rather than []: Hyprland splits its (empty)
	// error string on newlines, which produces one empty element. So the test
	// is that no element has any content, not that the array is empty.
	hc := "XDG_RUNTIME_DIR=/run/user/1000 HYPRLAND_INSTANCE_SIGNATURE=" + sig + " "
	raw := strings.TrimSpace(ask(hc + "hyprctl -j configerrors 2>&1"))
	var reported []string
	if err := json.Unmarshal([]byte(raw), &reported); err != nil {
		t.Errorf("hyprctl never answered, so the configuration was not actually checked: %q (%v)\n  hyprctl: %s\n  instance dir:\n%s",
			raw, err,
			ask("command -v hyprctl || echo MISSING"),
			ask("ls -la /run/user/1000/hypr/"+sig+" 2>&1 | head -10"))
	} else {
		var real []string
		for _, e := range reported {
			if strings.TrimSpace(e) != "" {
				real = append(real, e)
			}
		}
		if len(real) > 0 {
			t.Errorf("Hyprland reports configuration errors:\n  %s", strings.Join(real, "\n  "))
		} else {
			t.Log("Hyprland reports a clean configuration")
		}
	}

	// Warnings matter too: the GLES 3.2 fallback is logged at WARN, so this is
	// where an unexpected one would show up.
	if warns := ask(logLines(logPath, "WARN") + " | head -20"); strings.TrimSpace(warns) != "" {
		for _, line := range strings.Split(warns, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				t.Logf("warning: %s", line)
			}
		}
	}

	// Every remaining ERR line in Hyprland's log must be one of the upstream
	// messages that cannot be configured away. Anything else is ours to fix.
	errLines := ask(logLines(logPath, "ERR"))
	for _, line := range strings.Split(errLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if benign := knownBenignError(line); benign != "" {
			t.Logf("known-unavoidable error (%s): %s", benign, line)
			continue
		}
		t.Errorf("unexpected error in Hyprland's log: %s", line)
	}

	// Surface the severity census so a passing run still shows what was
	// actually examined, rather than leaving "no errors found" ambiguous
	// between a clean desktop and a matcher that matches nothing.
	census := ask(stripANSI(logPath) + ` | grep --color=never -oE '^(TRACE|DEBUG|WARN|ERR|CRIT) ' | sort | uniq -c | tr '\n' ' '`)
	if strings.TrimSpace(census) == "" {
		t.Errorf("no log line carried a severity prefix, so the checks above proved nothing")
	}
	t.Logf("severity census: %s", strings.TrimSpace(census))
}

// Hyprland 0.56 logs through Hyprutils' CLI logger, which writes the severity
// as a bare word at the start of the line — "ERR @ 12:00:00 from aquamarine ]:
// ..." — not as a bracketed "[ERR]" tag. Colour escapes are stripped first
// because the same prefix is written with ANSI attributes when a terminal is
// attached.
func stripANSI(path string) string {
	return `sed $'s/\033\\[[0-9;]*m//g' ` + path
}

func logLines(path, level string) string {
	// --color=never matters: the guest's grep is aliased to colourise, which
	// would re-introduce escape sequences into lines sed just cleaned up.
	return stripANSI(path) + " | grep --color=never -E '^" + level + " '"
}

// knownBenignErrors are the errors Hyprland logs on a healthy march guest.
// Each is emitted unconditionally by upstream code with no configuration,
// environment variable or build flag that suppresses it, so the only way to
// reach a literally empty log would be to fork Hyprland and aquamarine.
//
// They are all non-fatal: in each case the code that logs them goes on to
// succeed by another route. The value maps a substring to why it is stuck.
var knownBenignErrors = map[string]string{
	// Hyprland registers the nested-Wayland backend unconditionally
	// (Compositor.cpp: AQ_BACKEND_WAYLAND), and aquamarine constructs and
	// start()s every registered backend before consulting its request mode
	// (Backend.cpp CBackend::create/start). Starting from a TTY there is no
	// outer compositor to connect to, so the probe fails and says so. This
	// happens on every TTY-launched Hyprland on any machine.
	"wl_display_connect failed":              "aquamarine probes for an outer compositor that a TTY session cannot have",
	"backend (wayland) could not start":      "aquamarine logs the failed probe before honouring AQ_BACKEND_REQUEST_FALLBACK",
	"Implementation wayland failed, erasing": "aquamarine logs the cleanup of the backend it just probed",

	// aquamarine's DRM renderer asks for a GLES 3.2 context first and falls
	// back to 3.0 (drm/Renderer.cpp, which logs the retry at AQ_LOG_ERROR
	// rather than at warning level). On macOS the 3.2 request cannot be granted
	// at any layer: virgl's capabilities come from the host GL context, which
	// is ANGLE on Metal, and Metal has no geometry-shader stage. Probing ANGLE
	// directly on the host confirms it refuses both 3.2 and 3.1 and exposes
	// neither geometry_shader nor tessellation_shader — so this is the ceiling,
	// not a misconfiguration. The pair of lines is the refusal as reported by
	// Mesa's EGL debug callback, and aquamarine's own note that it is retrying.
	"eglCreateContext errored out with EGL_BAD_MATCH":      "GLES 3.2 is unreachable through ANGLE on Metal, so the probe is refused",
	"eglCreateContext failed with GLES 3.2, retrying GLES": "aquamarine falls back to GLES 3.0, which is the macOS ceiling",

	// virtio-gpu has no gamma LUT, so its CRTC exposes no GAMMA_LUT_SIZE
	// property. aquamarine asks anyway when enumerating the connector and logs
	// the missing property at error level (drm/DRM.cpp). The query returns 0
	// and the connector is used normally; the only consequence is that
	// gamma/night-light adjustment is unavailable, which is a property of the
	// virtual GPU rather than of march's configuration.
	"Couldn't get the gamma_size prop": "virtio-gpu exposes no gamma LUT for aquamarine to size",

	// aquamarine refuses to commit a new buffer while a page-flip it already
	// submitted has not yet completed, and logs the refusal at error level
	// (drm/DRM.cpp). Behind virtio-gpu a flip completes when the *host*
	// compositor presents the frame, so the guest can outrun it — most visibly
	// when the window is resized and each resize forces a modeset. The commit
	// is dropped and retried on the next frame, which is the guard working.
	"Cannot commit when a page-flip is awaiting": "the guest can outrun host-side page-flip completion; aquamarine drops the frame and retries",
}

func knownBenignError(line string) string {
	for substr, why := range knownBenignErrors {
		if strings.Contains(line, substr) {
			return why
		}
	}
	return ""
}

// The classifier is only useful if it matches the strings upstream actually
// emits. These are copied verbatim from aquamarine's Backend.cpp and
// Wayland.cpp and Hyprland's OpenGL.cpp, so this test fails if a rename
// upstream silently turns a known-benign line into a reported failure.
func TestKnownBenignErrorsMatchUpstreamStrings(t *testing.T) {
	upstream := []string{
		"ERR from aquamarine ]: Wayland backend cannot start: wl_display_connect failed (is a wayland compositor running?)",
		"ERR from aquamarine ]: Requested backend (wayland) could not start, enabling fallbacks",
		"ERR from aquamarine ]: Implementation wayland failed, erasing.",
		"ERR from aquamarine ]: CDRMRenderer: eglCreateContext failed with GLES 3.2, retrying GLES 3.0",
		"ERR from aquamarine ]: Couldn't get the gamma_size prop",
		"ERR from aquamarine ]: drm: Cannot commit when a page-flip is awaiting",
		"ERR from aquamarine ]: [EGL] Command eglCreateContext errored out with EGL_BAD_MATCH (0x12297): dri2_create_context",
	}
	for _, line := range upstream {
		if knownBenignError(line) == "" {
			t.Errorf("upstream line is no longer recognised as benign: %s", line)
		}
	}

	// It must not swallow real problems.
	for _, line := range []string{
		"ERR ]: Config error: invalid dispatcher",
		"ERR from aquamarine ]: DRM Backend failed",
		"ERR ]: EGL: Failed to obtain a high priority context",
	} {
		if why := knownBenignError(line); why != "" {
			t.Errorf("a real error was classified as benign (%s): %s", why, line)
		}
	}
}
