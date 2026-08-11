package vm

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"net/http"
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

// guestSelftest is run inside the installed desktop; see the script itself for
// what it covers.
//
//go:embed testdata/guest-selftest.sh
var guestSelftest string

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
	t.Cleanup(func() {
		// Keeping the machine lets the in-guest self-test be re-run against it
		// without paying for another install.
		if os.Getenv("MARCH_E2E_KEEP") == "1" {
			t.Logf("keeping VM %q (MARCH_E2E_KEEP=1)", name)
			return
		}
		_ = mgr.Delete(context.Background(), name)
	})

	profile := install.DefaultProfile(name)
	profile.Username = "arch"
	profile.Password = "marchtest"
	profile.Autologin = true

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
	if v.Username != profile.Username {
		t.Errorf("install details were not recorded: user=%q, want %q",
			v.Username, profile.Username)
	}

	// --- boot into the desktop ---------------------------------------------
	if err := mgr.Start(ctx, name, qemu.BuildOptions{}); err != nil {
		t.Fatalf("booting the installed system: %v", err)
	}
	t.Cleanup(func() {
		if os.Getenv("MARCH_E2E_KEEP") == "1" {
			return
		}
		_ = mgr.Kill(context.Background(), name)
	})

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

	loginOnConsole(t, ctx, c, profile.Username, profile.Password)
	_ = loginCtx

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

	// march installs one desktop: SDDM starts a Hyprland session.
	const displayManager, sessionProc = "sddm", "Hyprland"

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
	{
		if bar := ask("pgrep -u " + profile.Username + " -c waybar || echo 0"); strings.TrimSpace(bar) == "0" {
			t.Errorf("waybar is not running; the top bar is missing")
		}
		// hyprctl needs the instance signature, which names the runtime directory.
		hc := "XDG_RUNTIME_DIR=/run/user/1000 HYPRLAND_INSTANCE_SIGNATURE=$(ls /run/user/1000/hypr | head -1) "
		if mon := ask("sudo -u " + profile.Username + " " + hc + "hyprctl monitors | head -2"); !strings.Contains(mon, want) {
			t.Errorf("hyprctl reports %q, want a monitor at the configured %s", mon, want)
		}
		// The launcher and the helper scripts the bindings and the bar call must
		// exist. This is the whole of march's menu: a missing one is a key or a
		// bar button that silently does nothing.
		helpers := []string{
			"fuzzel", "march-menu", "march-keybindings", "march-term",
			"march-bar", "march-toggle", "march-capture", "march-clipboard",
			"march-pkg",
		}
		// command -v prints one path per program it finds and stays silent about
		// the rest, so the line count is the number that exist.
		if out := ask("command -v " + strings.Join(helpers, " ") + " | wc -l"); strings.TrimSpace(out) != fmt.Sprint(len(helpers)) {
			t.Errorf("only %s of the %d menu helpers are on PATH:\n%s",
				strings.TrimSpace(out), len(helpers),
				ask("command -v "+strings.Join(helpers, " ")))
		}
		// And the programs those helpers reach for, none of which come with the
		// compositor.
		for _, bin := range []string{
			"cliphist", "wl-clip-persist", "rofimoji", "wtype", "wf-recorder",
			"satty", "notify-send", "checkupdates",
		} {
			if out := ask("command -v " + bin + " || echo MISSING"); strings.Contains(out, "MISSING") {
				t.Errorf("%s is missing, so part of the menu does nothing", bin)
			}
		}

		// The menu is a table parsed by awk at runtime; printing it proves both
		// that the table survived the install and that the parse works here.
		if tree := ask("march-menu --list | wc -l"); strings.TrimSpace(tree) == "0" {
			t.Error("march-menu lists no entries, so the menu opens empty")
		}
		for _, route := range []string{"capture", "system", "update", "clipboard"} {
			if out := ask("march-menu --list | grep -c '^" + route + "\\.'"); strings.TrimSpace(out) == "0" {
				t.Errorf("the menu has no rows under %q, so that key opens nothing", route)
			}
		}

		// waybar's custom modules are fed by these, and it logs an error and
		// blanks the module for anything that is not JSON.
		for _, module := range []string{"updates", "dnd", "recording"} {
			out := ask("march-bar " + module + " | jq -e 'has(\"text\")' 2>&1")
			if !strings.Contains(out, "true") {
				t.Errorf("march-bar %s did not print JSON waybar can read: %q", module, out)
			}
		}

		// Clipboard history only exists if something is recording it.
		if out := ask("pgrep -u " + profile.Username + " -fc 'wl-paste' || echo 0"); strings.TrimSpace(out) == "0" {
			t.Error("nothing is watching the clipboard, so the history is always empty")
		}
		// The keybindings helper reads from Hyprland itself; running it proves
		// both that the bindings loaded and that the helper's jq actually works.
		// The list is long and sorted, so each binding is looked up directly
		// rather than by truncating the output.
		// hyprctl reports the key as it was written in the config, so these are
		// the XKB keysym spellings the Lua bindings use.
		for _, want := range []string{
			"SUPER + space", "SUPER + ALT + space", "SUPER + CTRL + C",
			"SUPER + Return", "SUPER + W", "ALT + Tab",
		} {
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

	// The browser. Chrome is the one thing march installs that pacman knows
	// nothing about, so nothing resolved its shared libraries for it and
	// nothing would notice a missing one until a user clicked the icon.
	if out := ask("command -v google-chrome-stable || echo MISSING"); strings.Contains(out, "MISSING") {
		t.Error("Google Chrome is not installed")
	}
	if out := ask("ldd /opt/google/chrome/chrome 2>&1 | grep -c 'not found'"); strings.TrimSpace(out) != "0" {
		t.Errorf("Chrome is missing shared libraries (%s):\n%s", strings.TrimSpace(out),
			ask("ldd /opt/google/chrome/chrome 2>&1 | grep 'not found' | head -5"))
	}
	// Running it is the only proof that the unpacked tree is complete.
	if out := ask("google-chrome-stable --version 2>&1"); !strings.Contains(out, "Google Chrome") {
		t.Errorf("Chrome does not run: %q", out)
	}
	// Installed is half of it; being *the* browser is the other half.
	if out := ask("xdg-settings get default-web-browser 2>&1"); !strings.Contains(out, "google-chrome.desktop") {
		t.Errorf("the default browser is %q, want Google Chrome", strings.TrimSpace(out))
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

	t.Logf("unattended install to a working desktop took %s",
		time.Since(start).Round(time.Second))

	// Everything above is what can be seen from outside the session. The rest
	// of the desktop — every application it installs, every menu route, every
	// toggle, the bar's own buttons — is exercised inside it.
	runGuestSelftest(t, ctx, c, profile.Username)
}

// loginOnConsole logs in on the guest's serial getty and leaves a shell prompt
// waiting. A console that is already logged in — a second run against the same
// machine — is answered by the prompt itself rather than by a login banner.
func loginOnConsole(t *testing.T, ctx context.Context, c *console.Console, user, password string) {
	t.Helper()

	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// A getty that has only just started drops what is typed at it, and the
	// banner it prints looks the same either way, so the username is offered
	// again rather than the whole test failing on a race with the console.
	for attempt := 0; attempt < 3; attempt++ {
		_ = c.SendLine("")
		which, err := c.Expect(loginCtx, "login:", "]$")
		if err != nil {
			t.Fatalf("no serial login prompt appeared: %v\nconsole tail:\n%s", err, c.Tail(3000))
		}
		if which == "]$" {
			return // a shell is already waiting
		}

		if err := c.SendLine(user); err != nil {
			t.Fatal(err)
		}
		promptCtx, promptCancel := context.WithTimeout(loginCtx, 45*time.Second)
		_, err = c.Expect(promptCtx, "Password:", "password:")
		promptCancel()
		if err != nil {
			continue
		}

		if err := c.SendLine(password); err != nil {
			t.Fatal(err)
		}
		// login takes a moment to hand over to the shell, and anything typed
		// during that window is discarded, so wait for the prompt itself.
		if _, err := c.Expect(loginCtx, "]$"); err != nil {
			t.Fatalf("could not log in as %q with the configured password: %v\nconsole tail:\n%s",
				user, err, c.Tail(2000))
		}
		return
	}
	t.Fatalf("the console never offered a password prompt for %q\nconsole tail:\n%s",
		user, c.Tail(3000))
}

// runGuestSelftest hands the guest testdata/guest-selftest.sh over the same
// loopback HTTP path the installer uses, runs it inside the live session, and
// turns each line it prints into a test result.
func runGuestSelftest(t *testing.T, ctx context.Context, c *console.Console, user string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("serving the self-test: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, guestSelftest)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	// 10.0.2.2 is the host as QEMU's user-mode network presents it — the same
	// address the install script was fetched from.
	command := fmt.Sprintf(
		"curl -fsS -o /tmp/selftest.sh http://10.0.2.2:%d/selftest.sh && bash /tmp/selftest.sh", port)

	if err := c.SendLine(`printf '%s\n' "M-SE""LF"; ` + command + ` 2>&1; printf '%s\n' "M-DO""NE"`); err != nil {
		t.Fatalf("starting the self-test: %v", err)
	}
	// The suite launches a dozen applications and records video, so it is given
	// far longer than an ordinary console command.
	runCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	if _, err := c.Expect(runCtx, "M-SELF"); err != nil {
		t.Fatalf("the guest never started the self-test: %v", err)
	}
	_, out, err := c.ExpectCapture(runCtx, "M-DONE")
	if err != nil {
		t.Fatalf("the self-test did not finish: %v\nlast output:\n%s", err, c.Tail(4000))
	}

	var passed, failed, skipped int
	var done bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PASS "):
			passed++
		case strings.HasPrefix(line, "SKIP "):
			skipped++
			t.Logf("skipped: %s", strings.TrimPrefix(line, "SKIP "))
		case strings.HasPrefix(line, "FAIL "):
			failed++
			t.Errorf("in-guest: %s", strings.TrimPrefix(line, "FAIL "))
		case strings.HasPrefix(line, "SELFTEST-DONE"):
			done = true
		}
	}
	if !done {
		t.Fatalf("the self-test stopped partway; captured:\n%s", out)
	}
	t.Logf("in-guest self-test: %d passed, %d failed, %d skipped", passed, failed, skipped)
	if passed == 0 {
		t.Error("the self-test reported nothing at all")
	}
}

// shellQuote renders a value as a single-quoted shell word, so a pattern can be
// passed to the guest's shell without reinterpretation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
