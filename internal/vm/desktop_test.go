package vm

import (
	"context"
	"fmt"
	"net"
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

// TestUnattendedDesktopInstall is the product's whole promise in one test: from
// nothing but a QEMU installation, download the installer, install Arch Linux
// ARM completely unattended, and boot into a graphical desktop.
//
// It downloads several hundred megabytes and takes tens of minutes, so it runs
// only when MARCH_E2E=1. The ISO is cached between runs.
func TestUnattendedDesktopInstall(t *testing.T) {
	if os.Getenv("MARCH_E2E") != "1" {
		t.Skip("set MARCH_E2E=1 to run the full unattended desktop install")
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

	rel, err := image.Resolve(ctx, nil)
	if err != nil {
		t.Skipf("cannot reach the Archboot index: %v", err)
	}
	iso, err := image.NewDownloader(store.ImagesDir()).Fetch(ctx, rel, nil)
	if err != nil {
		t.Fatalf("downloading the installer: %v", err)
	}
	t.Logf("installer: %s", rel.Filename)

	const name = "e2e-desktop"
	_ = mgr.Delete(ctx, name)

	spec := config.Defaults(name, caps)
	spec.CPUs = min(caps.HostCPUs, 4)
	spec.MemoryMiB = 4096
	spec.DiskGiB = 24
	// Headless, but with a GPU: X still needs a framebuffer to render into,
	// while a test should not pop a window open. A headless guest takes its
	// resolution straight from the configured xres/yres.
	spec.Display = config.DisplayNone
	spec.GPU = true

	if _, err := mgr.Create(ctx, CreateOptions{Spec: spec, ISOPath: iso}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(context.Background(), name) })

	profile := install.DefaultProfile(name)
	profile.Username = "arch"
	profile.Password = "marchtest"
	profile.Autologin = true
	if d := os.Getenv("MARCH_E2E_DESKTOP"); d != "" {
		profile.Desktop = install.Desktop(d)
	}
	t.Logf("desktop: %s", profile.Desktop)

	// --- install -----------------------------------------------------------
	start := time.Now()
	var phases []install.Phase
	hooks := install.Hooks{
		OnProgress: func(p install.Progress) {
			if p.Phase != "" {
				phases = append(phases, p.Phase)
			}
			t.Logf("[%6s] %s", time.Since(start).Round(time.Second), p.Message)
		},
	}

	if err := mgr.Install(ctx, name, profile, hooks); err != nil {
		t.Fatalf("unattended install failed after %s: %v", time.Since(start).Round(time.Second), err)
	}
	t.Logf("installed in %s", time.Since(start).Round(time.Second))

	// Every phase should have been reported, in order.
	if len(phases) != len(install.Phases) {
		t.Errorf("saw phases %v, want all of %v", phases, install.Phases)
	}
	for i, p := range phases {
		if i < len(install.Phases) && p != install.Phases[i] {
			t.Errorf("phase %d was %q, want %q", i, p, install.Phases[i])
		}
	}

	// The VM must now be marked installed and boot from disk.
	v, err := store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Installed {
		t.Error("the VM was not marked installed")
	}
	if v.Username != profile.Username || v.Desktop != string(profile.Desktop) {
		t.Errorf("install details were not recorded: user=%q desktop=%q, want %q/%q",
			v.Username, v.Desktop, profile.Username, profile.Desktop)
	}

	// --- boot into the desktop ---------------------------------------------
	if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
		t.Fatalf("booting the installed system: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(context.Background(), name) })

	c, err := console.Dial(ctx, store.Paths(name).SerialSocket)
	if err != nil {
		t.Fatalf("attaching to the console: %v", err)
	}
	defer c.Close()

	bootCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	// systemd announces the graphical target once the display manager is up.
	// Reaching it from a disk boot proves the bootloader, kernel, initramfs,
	// fstab and display manager were all configured correctly.
	if _, err := c.Expect(bootCtx, "Reached target Graphical Interface", "login:"); err != nil {
		t.Fatalf("the installed system never finished booting: %v\nconsole tail:\n%s",
			err, c.Tail(4000))
	}
	t.Logf("graphical target reached %s after power-on", time.Since(start).Round(time.Second))

	// --- confirm the desktop session is really running ----------------------
	// The installed system keeps a getty on the serial console, so it can be
	// logged into and inspected without needing SSH or any host-side tooling.
	loginCtx, loginCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer loginCancel()

	_ = c.SendLine("")
	if _, err := c.Expect(loginCtx, "login:"); err != nil {
		t.Fatalf("no serial login prompt appeared: %v\nconsole tail:\n%s", err, c.Tail(3000))
	}
	if err := c.SendLine(profile.Username); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Expect(loginCtx, "Password:", "password:"); err != nil {
		t.Fatalf("no password prompt: %v\nconsole tail:\n%s", err, c.Tail(2000))
	}
	if err := c.SendLine(profile.Password); err != nil {
		t.Fatal(err)
	}

	// login takes a moment to hand over to the shell, and anything typed during
	// that window is discarded, so wait for the prompt itself.
	if _, err := c.Expect(loginCtx, "]$"); err != nil {
		t.Fatalf("could not log in as %q with the configured password: %v\nconsole tail:\n%s",
			profile.Username, err, c.Tail(2000))
	}

	// ask runs a command in the guest and returns its output.
	//
	// The markers are written split ("M-BE" "GIN") so that the terminal's echo
	// of the command line cannot match them — only the shell's actual output
	// reassembles them. Without this, Expect matches the echoed command and
	// returns the rest of the command line instead of its result.
	ask := func(cmd string) string {
		t.Helper()
		if err := c.SendLine(`printf '%s\n' "M-BE""GIN"; ` + cmd + ` 2>&1; printf '%s\n' "M-E""ND"`); err != nil {
			t.Fatal(err)
		}
		qctx, qcancel := context.WithTimeout(ctx, 90*time.Second)
		defer qcancel()
		if _, err := c.Expect(qctx, "M-BEGIN"); err != nil {
			t.Fatalf("no response to %q: %v", cmd, err)
		}
		// Everything between the two markers is the command's output.
		_, out, err := c.ExpectCapture(qctx, "M-END")
		if err != nil {
			t.Fatalf("command %q did not finish: %v", cmd, err)
		}
		return strings.TrimSpace(out)
	}

	// Each desktop is verified through its own display manager and session
	// process; the rest of the checks are common.
	displayManager, sessionProc := "lightdm", "xfce4-session"
	if profile.Desktop == install.DesktopHyprland {
		displayManager, sessionProc = "sddm", "Hyprland"
	}

	checks := []struct {
		cmd, want, describe string
	}{
		{"systemctl is-active " + displayManager, "active", "the display manager is running"},
		{"systemctl is-active NetworkManager", "active", "networking is managed"},
		{"systemctl get-default", "graphical.target", "the system boots to a graphical target"},
		{"id -un", profile.Username, "the configured account exists"},
	}
	for _, check := range checks {
		got := ask(check.cmd)
		if !strings.Contains(got, check.want) {
			t.Errorf("expected that %s: %q returned %q, want it to contain %q",
				check.describe, check.cmd, got, check.want)
		}
	}

	// The resolution march configured, used by several checks below.
	want := fmt.Sprintf("%dx%d", v.DisplayWidth, v.DisplayHeight)

	// The heart of the feature: an actual desktop session must be running for
	// the autologin user, not merely a display manager sitting on a greeter.
	session := ask("pgrep -u " + profile.Username + " -c " + sessionProc + " || echo 0")
	if strings.TrimSpace(session) == "0" {
		t.Errorf("no %s session is running for %s — the desktop did not auto-start.\n"+
			"display manager log: %q",
			sessionProc, profile.Username,
			ask("journalctl -b -u "+displayManager+" --no-pager | tail -6"))
	} else {
		t.Logf("%s session running for %s", sessionProc, profile.Username)
	}

	// Hyprland brings a bar, a launcher and a compositor that must all be
	// present for the desktop to be usable rather than merely running.
	if profile.Desktop == install.DesktopHyprland {
		if bar := ask("pgrep -u " + profile.Username + " -c waybar || echo 0"); strings.TrimSpace(bar) == "0" {
			t.Errorf("waybar is not running; the top bar is missing")
		}
		// hyprctl needs the instance signature, which names the runtime directory.
		hc := "XDG_RUNTIME_DIR=/run/user/1000 HYPRLAND_INSTANCE_SIGNATURE=$(ls /run/user/1000/hypr | head -1) "
		if mon := ask("sudo -u " + profile.Username + " " + hc + "hyprctl monitors | head -2"); !strings.Contains(mon, want) {
			t.Errorf("hyprctl reports %q, want a monitor at the configured %s", mon, want)
		}
		// The launcher and the helper scripts the bindings call must exist.
		if out := ask("command -v fuzzel march-keybindings march-powermenu"); strings.Count(out, "/") < 3 {
			t.Errorf("launcher or helper scripts are missing: %q", out)
		}
		// The keybindings helper reads from Hyprland itself; running it proves
		// both that the bindings loaded and that the helper's jq actually works.
		// The list is long and sorted, so each binding is looked up directly
		// rather than by truncating the output.
		// hyprctl reports the key as it was written in the config, so these are
		// the XKB keysym spellings the Lua bindings use.
		for _, want := range []string{"SUPER + space", "SUPER + Return", "SUPER + W", "ALT + Tab"} {
			got := ask("sudo -u " + profile.Username + " " + hc +
				"march-keybindings --list | grep -F " + shellQuote(want) + " | head -1")
			if !strings.Contains(got, want) {
				t.Errorf("binding %q is not active in the running compositor (got %q)", want, got)
			}
		}
		// The config must have been copied out of /etc/skel into the account.
		if out := ask("ls /home/" + profile.Username + "/.config/hypr/hyprland.lua 2>&1"); strings.Contains(out, "No such file") {
			t.Errorf("the Hyprland config never reached the account: %s", out)
		}
	}

	// The guest should boot at the resolution march configured, not QEMU's
	// cramped 1280x800 default. The DRM connector lists its preferred mode
	// first, which is readable without a display connection.
	if mode := ask("cat /sys/class/drm/*/modes 2>/dev/null | head -1"); !strings.Contains(mode, want) {
		t.Errorf("guest display mode is %q, want the configured %s", mode, want)
	}

	// The X desktops must actually be using that mode, not merely offering it.
	// Hyprland is checked through hyprctl above.
	if profile.Desktop != install.DesktopHyprland {
		xenv := "DISPLAY=:0 XAUTHORITY=/home/" + profile.Username + "/.Xauthority "
		if mode := ask(xenv + "xrandr --query | awk '/\\*/{print $1; exit}'"); !strings.Contains(mode, want) {
			t.Errorf("X is running at %q, want the configured %s", mode, want)
		}
	}

	// By default the window is sized from the guest, so the guest owns its
	// resolution and the follow-the-window helper must stay out of the way.
	if out := ask("ls /usr/local/bin/march-autoresize 2>&1"); !strings.Contains(out, "No such file") {
		t.Errorf("the resize helper was installed for a guest that owns its resolution: %s", out)
	}

	// The standard toolset. A package that vanished from the repos aborts the
	// install long before this, so what is being checked here is that the
	// binaries carry the names the bindings and the docs assume.
	for _, bin := range []string{"git", "rg", "bat", "eza", "fd", "fzf", "nvim", "mpv", "docker", "tmux", "unzip"} {
		if out := ask("command -v " + bin + " || echo MISSING"); strings.Contains(out, "MISSING") {
			t.Errorf("%s is not on PATH in the installed system", bin)
		}
	}
	// Group membership is what separates a usable docker from one that needs
	// sudo for every command.
	if groups := ask("id -nG " + profile.Username); !strings.Contains(groups, "docker") {
		t.Errorf("%s is not in the docker group: %q", profile.Username, groups)
	}

	// Sound. The guest has an HDA card, but a card alone is silent: pipewire
	// needs wireplumber to bind it, and march ships volume keys and a mixer
	// that do nothing without a sink.
	sinks := ask("sudo -u " + profile.Username + " XDG_RUNTIME_DIR=/run/user/1000 " +
		"wpctl status 2>&1 | sed -n '/Sinks:/,/^$/p'")
	if !strings.Contains(sinks, "hda") && !strings.Contains(sinks, "HDA") &&
		!strings.Contains(sinks, "Built-in") && !strings.Contains(sinks, "Audio") {
		t.Errorf("pipewire reports no sink, so the guest has no working audio:\n%s", sinks)
	}

	// The firewall must be up — `ufw status` needs root, which this console
	// session does not have, so ask systemd instead.
	if state := ask("systemctl is-enabled ufw 2>&1"); !strings.Contains(state, "enabled") {
		t.Errorf("the firewall is not enabled: %s", state)
	}

	// And it must not have closed the one port march forwards. This is checked
	// from the host rather than in the guest, because reaching sshd on the
	// forwarded port is exactly the thing march advertises on its detail
	// screen — a rule that only looks right in `ufw status` is not the claim.
	if v.SSHPort > 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", v.SSHPort)
		conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			t.Errorf("ssh on %s is unreachable, so the firewall locked march out of its own VM: %v", addr, err)
		} else {
			_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			banner := make([]byte, 64)
			n, err := conn.Read(banner)
			if err != nil || !strings.HasPrefix(string(banner[:n]), "SSH-") {
				t.Errorf("nothing answering ssh on %s: read %q (%v)", addr, banner[:n], err)
			}
			_ = conn.Close()
		}
	}

	if failed := ask("systemctl --failed --no-legend --no-pager | head -5"); strings.TrimSpace(failed) != "" {
		t.Errorf("the installed system has failed units:\n%s", failed)
	}

	t.Logf("unattended install to a working %s desktop took %s",
		strings.ToUpper(string(profile.Desktop)), time.Since(start).Round(time.Second))
}

// shellQuote renders a value as a single-quoted shell word, so a pattern can be
// passed to the guest's shell without reinterpretation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
