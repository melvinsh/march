package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testProfile() Profile {
	p := DefaultProfile("archbox")
	p.Username = "melvin"
	p.Password = "hunter2"
	return p
}

func mustScript(t *testing.T, p Profile) string {
	t.Helper()
	s, err := Script(p)
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	return s
}

func TestProfileValidate(t *testing.T) {
	base := testProfile()
	if err := base.Validate(); err != nil {
		t.Fatalf("a default profile should validate: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Profile)
		want   string
	}{
		{"no hostname", func(p *Profile) { p.Hostname = "" }, "hostname"},
		{"no password", func(p *Profile) { p.Password = "" }, "password"},
		{"uppercase user", func(p *Profile) { p.Username = "Melvin" }, "username"},
		{"user starts with digit", func(p *Profile) { p.Username = "1melvin" }, "username"},
		{"user with space", func(p *Profile) { p.Username = "mel vin" }, "username"},
		{"root is reserved", func(p *Profile) { p.Username = "root" }, "reserved"},
		{"unknown desktop", func(p *Profile) { p.Desktop = "unity" }, "desktop"},
		{"no disk", func(p *Profile) { p.Disk = "" }, "disk"},
		{"hostname with space", func(p *Profile) { p.Hostname = "my box" }, "whitespace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := testProfile()
			tc.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// A newline in the password would split the generated script in two and could
// inject arbitrary commands, so it must be refused outright.
func TestProfileRejectsNewlineInPassword(t *testing.T) {
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b"} {
		p := testProfile()
		p.Password = bad
		if err := p.Validate(); err == nil {
			t.Errorf("password %q was accepted", bad)
		}
	}
}

// The script is the whole install; if bash cannot parse it nothing else
// matters. This runs the host's bash in syntax-check mode.
func TestScriptIsValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	for _, d := range Desktops {
		t.Run(string(d), func(t *testing.T) {
			p := testProfile()
			p.Desktop = d
			script := mustScript(t, p)

			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash rejected the generated script: %v\n%s\n--- script ---\n%s",
					err, out, script)
			}
		})
	}
}

func TestScriptStructure(t *testing.T) {
	s := mustScript(t, testProfile())

	// Each phase must be announced, in order.
	last := -1
	for _, ph := range Phases {
		at := strings.Index(s, "phase "+string(ph))
		if at < 0 {
			t.Errorf("the script never announces phase %q", ph)
			continue
		}
		if at < last {
			t.Errorf("phase %q is announced out of order", ph)
		}
		last = at
	}

	if !strings.Contains(s, MarkerComplete) {
		t.Error("the script never signals completion")
	}
	if !strings.Contains(s, MarkerFailed) {
		t.Error("the script has no failure marker")
	}
	// Without an ERR trap a failed step would be skipped over and the install
	// would report success on a broken system.
	if !strings.Contains(s, "trap 'fail $LINENO' ERR") {
		t.Error("the script does not abort on error")
	}
	if !strings.Contains(s, "exec >/dev/ttyAMA0 2>&1") {
		t.Error("the script does not redirect output to the serial console")
	}
}

// dryRun executes the generated script with every command that would touch a
// disk or the network replaced by a stub, and returns what the guest would
// have printed to its serial console.
//
// This is the closest thing to running the installer without a VM, and it is
// what proves the script and the runner agree on the marker protocol.
func dryRun(t *testing.T, p Profile, failAt string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Stubs stand in for the real tools. Anything whose output the script
	// inspects gets a plausible answer.
	stubs := map[string]string{
		"pacman":    "exit 0",
		"pacstrap":  "exit 0",
		"sgdisk":    "exit 0",
		"mkfs.fat":  "exit 0",
		"mkfs.ext4": "exit 0",
		"mount":     "exit 0",
		"umount":    "exit 0",
		"udevadm":   "exit 0",
		"genfstab":  "echo '# fstab'",
		// arch-chroot receives the configuration heredoc on stdin.
		"arch-chroot": "cat >/dev/null; exit 0",
		"sync":        "exit 0",
	}
	for name, body := range stubs {
		body := body
		if name == failAt {
			body = "exit 7"
		}
		script := "#!/bin/bash\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script, err := Script(p)
	if err != nil {
		t.Fatal(err)
	}
	// The host has no serial console, no writable /mnt, and stubs that cannot
	// satisfy a block-device check, so those three are redirected.
	out := filepath.Join(dir, "console.log")
	root := filepath.Join(dir, "mnt")
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, "/dev/ttyAMA0", out)
	script = strings.ReplaceAll(script, "/mnt", root)
	script = strings.ReplaceAll(script, `[ -b "${DISK}1" ] && [ -b "${DISK}2" ]`, "true")

	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bash, path)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	runErr := cmd.Run()

	console, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("the script produced no console output: %v", readErr)
	}
	return string(console), runErr
}

// The runner watches for exactly these markers; if the script emitted anything
// else, an install would hang until it timed out.
func TestScriptDryRunEmitsTheMarkerProtocol(t *testing.T) {
	console, err := dryRun(t, testProfile(), "")
	if err != nil {
		t.Fatalf("the dry run failed: %v\nconsole:\n%s", err, console)
	}

	at := -1
	for _, ph := range Phases {
		marker := MarkerPhase + string(ph) + "==="
		idx := strings.Index(console, marker)
		if idx < 0 {
			t.Errorf("the guest never emitted %q\nconsole:\n%s", marker, console)
			continue
		}
		if idx < at {
			t.Errorf("phase %q was emitted out of order", ph)
		}
		at = idx
	}

	if !strings.Contains(console, MarkerComplete) {
		t.Errorf("the guest never emitted the completion marker\nconsole:\n%s", console)
	}
	if strings.Contains(console, MarkerFailed) {
		t.Errorf("a successful run emitted the failure marker\nconsole:\n%s", console)
	}
}

// A failure partway through must stop immediately and say so, rather than
// carrying on and reporting a system that was never installed.
func TestScriptDryRunReportsFailure(t *testing.T) {
	console, err := dryRun(t, testProfile(), "pacstrap")
	if err == nil {
		t.Error("the script exited zero despite pacstrap failing")
	}
	if !strings.Contains(console, MarkerFailed) {
		t.Errorf("no failure marker was emitted\nconsole:\n%s", console)
	}
	if strings.Contains(console, MarkerComplete) {
		t.Errorf("a failed install still reported completion\nconsole:\n%s", console)
	}
	// Everything after the failing step must be skipped.
	if strings.Contains(console, MarkerPhase+string(PhaseBootloader)) {
		t.Error("the script continued past a failed pacstrap")
	}
	if !strings.Contains(console, "failed at line") {
		t.Error("the failure does not identify where it happened")
	}
}

func TestScriptForcesToolReinstall(t *testing.T) {
	s := mustScript(t, testProfile())

	// Find the pacman invocation that restores the tools, ignoring comments.
	var pacmanCmd string
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		// Skip the keyring refresh; the tools line is the one being checked.
		if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "pacman -Sy") ||
			strings.Contains(trimmed, "keyring") {
			continue
		}
		pacmanCmd = trimmed
		for strings.HasSuffix(pacmanCmd, "\\") && i+1 < len(lines) {
			i++
			pacmanCmd = strings.TrimSuffix(pacmanCmd, "\\") + " " + strings.TrimSpace(lines[i])
		}
		break
	}
	if pacmanCmd == "" {
		t.Fatal("the script never runs pacman to restore the install tools")
	}
	if strings.Contains(pacmanCmd, "--needed") {
		t.Errorf("the tool install uses --needed, which skips Archboot's stripped packages: %s", pacmanCmd)
	}
	if !strings.Contains(pacmanCmd, "--noconfirm") {
		t.Errorf("the tool install would prompt for confirmation: %s", pacmanCmd)
	}
	for _, tool := range []string{"arch-install-scripts", "gptfdisk", "dosfstools", "e2fsprogs"} {
		if !strings.Contains(pacmanCmd, tool) {
			t.Errorf("the script does not restore %q: %s", tool, pacmanCmd)
		}
	}
}

func TestScriptUsesARMKernel(t *testing.T) {
	s := mustScript(t, testProfile())
	if !strings.Contains(s, "linux-aarch64") {
		t.Error("the script must install linux-aarch64; plain 'linux' does not exist in the ALARM repos")
	}
	// A bare "linux" package name in the pacstrap line would fail the install.
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "pacstrap") {
			for _, f := range strings.Fields(line) {
				if f == "linux" {
					t.Errorf("pacstrap line requests the nonexistent 'linux' package: %s", line)
				}
			}
		}
	}
}

func TestScriptPartitioning(t *testing.T) {
	p := testProfile()
	p.Disk = "/dev/vdb"
	s := mustScript(t, p)

	if !strings.Contains(s, "DISK='/dev/vdb'") {
		t.Error("the target disk was not honoured")
	}
	for _, want := range []string{
		"sgdisk --zap-all",
		"-t1:ef00", // EFI system partition
		"-t2:8300", // Linux filesystem
		"mkfs.fat -F32",
		"mkfs.ext4",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the partitioning step is missing %q", want)
		}
	}
	// The ESP goes under /boot/efi so the kernel stays on ext4 where GRUB can
	// find it without FAT limitations.
	if !strings.Contains(s, "/mnt/boot/efi") {
		t.Error("the ESP is not mounted at /boot/efi")
	}
}

func TestScriptBootloader(t *testing.T) {
	s := mustScript(t, testProfile())
	if !strings.Contains(s, "--target=arm64-efi") {
		t.Error("GRUB must be installed for arm64-efi")
	}
	// --removable writes BOOTAA64.EFI, which boots without an NVRAM entry.
	if !strings.Contains(s, "--removable") {
		t.Error("GRUB should be installed in removable mode so firmware finds it")
	}
	if !strings.Contains(s, "grub-mkconfig -o /boot/grub/grub.cfg") {
		t.Error("GRUB config is never generated")
	}
	// The installed system must keep a serial console for march to watch.
	if !strings.Contains(s, "console=ttyAMA0") {
		t.Error("the installed system loses its serial console")
	}
	// "quiet" would suppress the systemd progress march watches for, making a
	// booted desktop indistinguishable from a hung one.
	if strings.Contains(s, `GRUB_CMDLINE_LINUX_DEFAULT="quiet`) {
		t.Error("the installed system boots quietly, hiding the readiness signal")
	}
}

func TestScriptAccountSetup(t *testing.T) {
	p := testProfile()
	s := mustScript(t, p)

	if !strings.Contains(s, "useradd -m -G wheel") {
		t.Error("the user is not created with wheel membership")
	}
	if !strings.Contains(s, "'melvin'") {
		t.Error("the username was not interpolated")
	}
	if !strings.Contains(s, "'hunter2'") {
		t.Error("the password was not interpolated")
	}
	if !strings.Contains(s, "chpasswd") {
		t.Error("no password is set")
	}
	if !strings.Contains(s, "sudoers.d") {
		t.Error("sudo is not configured for the new account")
	}
	if !strings.Contains(s, "systemctl enable NetworkManager") {
		t.Error("networking is not enabled")
	}
}

// Shell metacharacters in a password must be quoted, not executed.
func TestScriptQuotesHostileValues(t *testing.T) {
	p := testProfile()
	p.Password = `p'; rm -rf /; echo '`
	p.Username = "melvin"

	s := mustScript(t, p)

	if strings.Contains(s, "rm -rf /;") && !strings.Contains(s, `'\''`) {
		t.Error("a password containing quotes was not escaped")
	}
	// The proof is that bash still parses it.
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("a hostile password broke the script: %v\n%s", err, out)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"plain":    "'plain'",
		"":         "''",
		"a b":      "'a b'",
		"it's":     `'it'\''s'`,
		"$(x)":     "'$(x)'",
		"a\"b":     "'a\"b'",
		"back\\sl": "'back\\sl'",
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDesktopPackages(t *testing.T) {
	tests := []struct {
		desktop Desktop
		dm      string
		session string
		pkg     string
	}{
		{DesktopXFCE, "lightdm", "xfce", "xfce4"},
		{DesktopGNOME, "gdm", "gnome", "gnome"},
		{DesktopPlasma, "sddm", "plasma", "plasma-meta"},
	}
	for _, tc := range tests {
		t.Run(string(tc.desktop), func(t *testing.T) {
			p := testProfile()
			p.Desktop = tc.desktop
			s := mustScript(t, p)

			if !strings.Contains(s, tc.pkg) {
				t.Errorf("the script does not install %q", tc.pkg)
			}
			if !strings.Contains(s, "systemctl enable "+tc.dm) {
				t.Errorf("the display manager %q is not enabled", tc.dm)
			}
			if !strings.Contains(s, "systemctl set-default graphical.target") {
				t.Error("the system does not default to a graphical target")
			}
			// Xorg and Mesa are what make the desktop render at all.
			for _, want := range []string{"xorg-server", "mesa"} {
				if !strings.Contains(s, want) {
					t.Errorf("the graphics stack is missing %q", want)
				}
			}
		})
	}
}

// Autologin is what makes the machine land on a desktop with no interaction,
// and each display manager configures it differently.
func TestScriptAutologin(t *testing.T) {
	tests := []struct {
		desktop Desktop
		want    []string
	}{
		{DesktopXFCE, []string{"/etc/lightdm/lightdm.conf.d/", "autologin-user=melvin", "groupadd -r autologin"}},
		{DesktopGNOME, []string{"/etc/gdm/custom.conf", "AutomaticLogin=melvin"}},
		{DesktopPlasma, []string{"/etc/sddm.conf.d/autologin.conf", "User=melvin"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.desktop), func(t *testing.T) {
			p := testProfile()
			p.Desktop = tc.desktop
			s := mustScript(t, p)
			for _, want := range tc.want {
				if !strings.Contains(s, want) {
					t.Errorf("autologin config is missing %q", want)
				}
			}
		})
	}

	// Arch's lightdm.conf carries session-wrapper=/etc/lightdm/Xsession.
	// Replacing the file loses it, LightDM falls back to a wrapper that does
	// not exist, the session dies instantly, and the greeter appears instead
	// of the desktop — a silent failure that looks like a working install.
	t.Run("lightdm config is a drop-in, not a replacement", func(t *testing.T) {
		p := testProfile()
		p.Desktop = DesktopXFCE
		s := mustScript(t, p)

		for _, line := range strings.Split(s, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, ">") || strings.Contains(trimmed, "cat >") {
				if strings.Contains(trimmed, "/etc/lightdm/lightdm.conf") &&
					!strings.Contains(trimmed, "lightdm.conf.d/") {
					t.Errorf("the script overwrites Arch's lightdm.conf: %s", trimmed)
				}
			}
		}
		if !strings.Contains(s, "/etc/lightdm/lightdm.conf.d/") {
			t.Error("autologin should be configured through a drop-in")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		p := testProfile()
		p.Autologin = false
		s := mustScript(t, p)
		if strings.Contains(s, "autologin-user") {
			t.Error("autologin was configured despite being disabled")
		}
	})
}

func TestScriptRejectsInvalidProfile(t *testing.T) {
	p := testProfile()
	p.Username = "ROOT"
	if _, err := Script(p); err == nil {
		t.Error("Script should refuse an invalid profile")
	}
}

func TestScriptIsDeterministic(t *testing.T) {
	p := testProfile()
	first := mustScript(t, p)
	for i := 0; i < 3; i++ {
		if got := mustScript(t, p); got != first {
			t.Fatal("Script is not deterministic")
		}
	}
}

func TestPhaseLabels(t *testing.T) {
	for _, p := range Phases {
		if p.Label() == "" || p.Label() == string(p) {
			t.Errorf("phase %q has no human-readable label", p)
		}
	}
	if phaseIndex(PhaseTools) != 1 {
		t.Errorf("phaseIndex(tools) = %d, want 1", phaseIndex(PhaseTools))
	}
	if phaseIndex(PhaseBootloader) != len(Phases) {
		t.Errorf("phaseIndex(bootloader) = %d, want %d", phaseIndex(PhaseBootloader), len(Phases))
	}
	if phaseIndex("nonsense") != 0 {
		t.Error("an unknown phase should have index 0")
	}
}

func TestDesktopDescriptions(t *testing.T) {
	for _, d := range Desktops {
		if d.Description() == "" {
			t.Errorf("desktop %q has no description", d)
		}
	}
}

// extractHeredoc returns the body of a quoted heredoc, which is what actually
// runs inside the chroot.
func extractHeredoc(t *testing.T, script, tag string) string {
	t.Helper()
	start := strings.Index(script, "<<'"+tag+"'\n")
	if start < 0 {
		t.Fatalf("no %s heredoc in the script", tag)
	}
	body := script[start+len("<<'"+tag+"'\n"):]
	end := strings.Index(body, "\n"+tag+"\n")
	if end < 0 {
		t.Fatalf("unterminated %s heredoc", tag)
	}
	return body[:end]
}

// The chroot bodies are quoted heredocs, so `bash -n` on the outer script does
// not look inside them. They are the largest part of the install and a syntax
// error there would only surface partway through a real run.
func TestChrootScriptsAreValidBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	for _, d := range Desktops {
		t.Run(string(d), func(t *testing.T) {
			p := testProfile()
			p.Desktop = d
			script := mustScript(t, p)

			for _, tag := range []string{"MARCH_CHROOT", "MARCH_BOOT"} {
				body := extractHeredoc(t, script, tag)
				cmd := exec.Command(bash, "-n")
				cmd.Stdin = strings.NewReader(body)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("bash rejected the %s body: %v\n%s\n--- body ---\n%s",
						tag, err, out, body)
				}
			}
		})
	}
}

// Resizing the QEMU window only reaches the guest if something applies the new
// mode. GNOME and KDE do it themselves; a plain X session does not.
func TestScriptInstallsAutoResizeForXFCEOnly(t *testing.T) {
	p := testProfile()
	p.Desktop = DesktopXFCE // the helper is an X11/xrandr mechanism
	p.FollowHostResize = true
	xfce := mustScript(t, p)
	for _, want := range []string{
		"/usr/local/bin/march-autoresize",
		"/etc/xdg/autostart/march-autoresize.desktop",
		"xrandr --output",
	} {
		if !strings.Contains(xfce, want) {
			t.Errorf("the XFCE install is missing %q", want)
		}
	}
	// xorg-xrandr is what the helper depends on.
	if !strings.Contains(xfce, "xorg-xrandr") {
		t.Error("the resize helper needs xorg-xrandr installed")
	}

	for _, d := range []Desktop{DesktopGNOME, DesktopPlasma} {
		p := testProfile()
		p.Desktop = d
		p.FollowHostResize = true
		if s := mustScript(t, p); strings.Contains(s, "march-autoresize") {
			t.Errorf("%s handles display changes itself; a second agent would fight it", d)
		}
	}

	// When the window is sized from the guest instead, the helper would keep
	// overriding whatever resolution the user picks inside the guest.
	fixed := testProfile()
	fixed.Desktop = DesktopXFCE
	fixed.FollowHostResize = false
	if s := mustScript(t, fixed); strings.Contains(s, "march-autoresize") {
		t.Error("the resize helper was installed for a guest that owns its own resolution")
	}
}

// The helper is a shell script embedded in a heredoc, so it too escapes the
// outer syntax check.
func TestAutoResizeHelperIsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is not available")
	}
	p := testProfile()
	p.Desktop = DesktopXFCE
	p.FollowHostResize = true
	body := extractHeredoc(t, mustScript(t, p), "RESIZE")

	cmd := exec.Command(sh, "-n")
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh rejected the resize helper: %v\n%s\n--- helper ---\n%s", err, out, body)
	}
}

// Arch Linux ARM rotates build-server signing keys faster than the installer
// image is rebuilt. Without a keyring refresh first, pacman stops on a PGP
// import prompt that an unattended run cannot answer.
func TestScriptRefreshesKeyringBeforeInstalling(t *testing.T) {
	s := mustScript(t, testProfile())

	keyring := strings.Index(s, "archlinuxarm-keyring")
	tools := strings.Index(s, "arch-install-scripts")
	if keyring < 0 {
		t.Fatal("the script never refreshes the package signing keyring")
	}
	if tools >= 0 && keyring > tools {
		t.Error("the keyring is refreshed after the tools are installed, which is too late")
	}
	// The live-environment refresh is best-effort: it runs before anything is
	// installed and a mirror without the package must not end the install.
	// pacstrapping the keyring into the target is a different matter and must
	// stay fatal, so only the refresh line is checked here.
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "pacman -Sy") {
			continue
		}
		if strings.Contains(line, "archlinuxarm-keyring") && !strings.Contains(line, "|| true") {
			t.Errorf("keyring refresh is not tolerant of failure: %s", line)
		}
	}
}

// A guest framebuffer matches the host's physical pixels, so on a high-density
// screen an unscaled desktop is sharp but too small to read.
// The X11 desktops scale through toolkit settings and font DPI; Wayland scales
// in the compositor and is covered separately.
func TestScriptSetsDesktopScaling(t *testing.T) {
	p := testProfile()
	p.Desktop = DesktopXFCE
	p.ScalePercent = 200
	s := mustScript(t, p)

	for _, want := range []string{
		"GDK_SCALE=2",       // GTK
		"QT_SCALE_FACTOR=2", // Qt
		"XCURSOR_SIZE=48",   // pointer, which does not scale on its own
		`WindowScalingFactor" type="int" value="2"`, // XFCE's own XSETTINGS
		"xft-dpi=192",      // the greeter, which runs before any session
		"xrandr --dpi 192", // X clients that predate XSETTINGS
	} {
		if !strings.Contains(s, want) {
			t.Errorf("scaling config is missing %q", want)
		}
	}
}

func TestScriptScalingCanBeDisabled(t *testing.T) {
	p := testProfile()
	p.Desktop = DesktopXFCE
	p.ScalePercent = 100
	s := mustScript(t, p)

	for _, unwanted := range []string{"GDK_SCALE", "WindowScalingFactor", "xrandr --dpi"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("scaling was configured at 100%%: found %q", unwanted)
		}
	}
}

func TestProfileValidatesScale(t *testing.T) {
	for _, bad := range []int{50, 99, 500} {
		p := testProfile()
		p.ScalePercent = bad
		if err := p.Validate(); err == nil {
			t.Errorf("scale %d%% was accepted", bad)
		}
	}
	for _, ok := range []int{0, 100, 200, 300} {
		p := testProfile()
		p.ScalePercent = ok
		if err := p.Validate(); err != nil {
			t.Errorf("scale %d%% was rejected: %v", ok, err)
		}
	}
	// A profile that never mentions scale still gets a readable desktop.
	if got := DefaultProfile("x").ScalePercent; got != 200 {
		t.Errorf("default scale = %d%%, want 200%%", got)
	}
}

// pacstrap seeds the target keyring from the live environment, which leaves it
// without the master signatures that make Arch Linux ARM's build keys trusted.
// Without this the finished system cannot install a single package: every
// "pacman -S" fails with "unknown trust".
func TestScriptMakesInstalledSystemAbleToInstallPackages(t *testing.T) {
	s := mustScript(t, testProfile())

	if !strings.Contains(s, "archlinuxarm-keyring") {
		t.Error("the installed system never gets the Arch Linux ARM keyring")
	}
	// It has to be pacstrapped into the target, not merely present in the live
	// environment where the base install happened to work.
	var inPacstrap bool
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "pacstrap") && strings.Contains(line, "archlinuxarm-keyring") {
			inPacstrap = true
		}
	}
	if !inPacstrap {
		t.Error("the keyring is not installed into the target system")
	}

	chroot := extractHeredoc(t, s, "MARCH_CHROOT")
	for _, want := range []string{"pacman-key --init", "pacman-key --populate archlinuxarm"} {
		if !strings.Contains(chroot, want) {
			t.Errorf("the target keyring is never initialised: missing %q", want)
		}
	}
}

// ── Hyprland ────────────────────────────────────────────────────────────────

func hyprlandProfile() Profile {
	p := testProfile()
	p.Desktop = DesktopHyprland
	return p
}

func TestHyprlandIsTheDefaultDesktop(t *testing.T) {
	if got := DefaultProfile("x").Desktop; got != DesktopHyprland {
		t.Errorf("default desktop = %q, want Hyprland", got)
	}
	if Desktops[0] != DesktopHyprland {
		t.Errorf("Desktops[0] = %q, want Hyprland to lead", Desktops[0])
	}
	// The others must remain installable.
	for _, d := range []Desktop{DesktopXFCE, DesktopGNOME, DesktopPlasma} {
		p := testProfile()
		p.Desktop = d
		if _, err := Script(p); err != nil {
			t.Errorf("desktop %q no longer builds: %v", d, err)
		}
	}
}

// Every embedded asset must reach the guest, or a config file silently goes
// missing and the desktop comes up unconfigured.
func TestHyprlandAssetsAreAllWritten(t *testing.T) {
	if err := hyprlandAssetsValid(); err != nil {
		t.Fatalf("embedded assets are unreadable: %v", err)
	}
	s := mustScript(t, hyprlandProfile())

	for _, name := range HyprlandAssetNames() {
		dest, ok := hyprlandFileMap[name]
		if !ok {
			t.Errorf("asset %q has no destination", name)
			continue
		}
		if _, err := HyprlandAsset(name); err != nil {
			t.Errorf("asset %q is mapped but not embedded: %v", name, err)
		}
		if !strings.Contains(s, dest) {
			t.Errorf("asset %q is never written to %s", name, dest)
		}
	}
	// The generated monitor line carries the resolution and scale.
	if !strings.Contains(s, "monitor = ,preferred,auto,") {
		t.Error("no monitor configuration was written")
	}
}

// The configs are copied through /etc/skel, which only works if they exist
// before the account is created.
func TestHyprlandConfigIsWrittenBeforeUseradd(t *testing.T) {
	s := mustScript(t, hyprlandProfile())
	skel := strings.Index(s, "/etc/skel/.config/hypr/hyprland.conf")
	user := strings.Index(s, "useradd -m")
	if skel < 0 || user < 0 {
		t.Fatal("expected both the skel config and useradd in the script")
	}
	if skel > user {
		t.Error("the desktop config is written after useradd, so the account never receives it")
	}
}

// Vendoring bindings from Omarchy is exactly where dead shortcuts creep in:
// none of Omarchy's own binaries exist for aarch64, so any leftover reference
// is a key that silently does nothing.
func TestHyprlandBindingsOnlyCallInstalledPrograms(t *testing.T) {
	bindings, err := HyprlandAsset("bindings.conf")
	if err != nil {
		t.Fatal(err)
	}

	// Everything the install puts on the guest's PATH.
	provided := map[string]bool{}
	for _, p := range append(append([]string{}, basePackages...), hyprlandPackages...) {
		provided[p] = true
	}
	// Binaries whose names differ from their package, plus march's own helpers
	// and shell builtins used in the bindings.
	for _, extra := range []string{
		"hyprctl", "makoctl", "swayosd-client", "wl-copy", "nmtui", "pkill",
		"march-keybindings", "march-powermenu", "nvim", "wiremix", "fuzzel",
		"waybar", "hyprlock", "hyprpicker", "grim", "slurp", "playerctl", "jq",
	} {
		provided[extra] = true
	}

	// hyprlang variables are resolved in apps.conf, not here.
	vars := map[string]bool{"$terminal": true, "$browser": true, "$fileManager": true}

	for _, line := range strings.Split(bindings, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, ", exec, ") {
			continue
		}
		cmd := strings.TrimSpace(line[strings.Index(line, ", exec, ")+len(", exec, "):])
		first := strings.Fields(cmd)
		if len(first) == 0 {
			continue
		}
		bin := first[0]
		if vars[bin] || provided[bin] {
			continue
		}
		t.Errorf("binding runs %q, which nothing installs:\n  %s", bin, line)
	}
}

// The keybindings are Omarchy's; drifting from them defeats the point.
func TestHyprlandMirrorsOmarchyShortcuts(t *testing.T) {
	bindings, err := HyprlandAsset("bindings.conf")
	if err != nil {
		t.Fatal(err)
	}
	// A representative sample spanning apps, tiling, workspaces and utilities.
	for _, want := range []string{
		"SUPER, RETURN", // terminal
		"SUPER, SPACE",  // launcher
		"SUPER, W, Close window",
		"SUPER, J, Toggle window split",
		"SUPER, LEFT, Move focus left",
		"SUPER SHIFT, LEFT, Swap window to the left",
		"SUPER, code:10, Switch to workspace 1",
		"SUPER SHIFT, code:10, Move window to workspace 1",
		"SUPER, TAB, Next workspace",
		"ALT, TAB, Cycle to next window",
		"SUPER, code:20, Expand window left",
		"SUPER, S, Toggle scratchpad",
		"SUPER, G, Toggle window grouping",
	} {
		if !strings.Contains(bindings, want) {
			t.Errorf("omarchy shortcut %q is missing", want)
		}
	}
}

// Wayland scales in the compositor; adding GDK_SCALE on top double-scales
// every GTK window.
func TestHyprlandScalesInTheCompositor(t *testing.T) {
	p := hyprlandProfile()
	p.ScalePercent = 200
	s := mustScript(t, p)

	if !strings.Contains(s, "monitor = ,preferred,auto,2") {
		t.Error("the compositor scale was not set")
	}
	if strings.Contains(s, "GDK_SCALE") {
		t.Error("GDK_SCALE is set for a Wayland desktop, which double-scales GTK windows")
	}
	// XWayland clients still need telling.
	if !strings.Contains(s, "QT_SCALE_FACTOR=2") {
		t.Error("Qt/XWayland scaling was not set")
	}
	// The X11 helper must not be installed for a Wayland session.
	if strings.Contains(s, "xrandr --dpi") {
		t.Error("X11 DPI handling leaked into the Hyprland install")
	}
}

// SDDM starts an X session unless told otherwise, which turns autologin into a
// greeter and leaves the user staring at a login box.
func TestHyprlandAutologinUsesWayland(t *testing.T) {
	s := mustScript(t, hyprlandProfile())
	for _, want := range []string{
		"/etc/sddm.conf.d/autologin.conf",
		"Session=hyprland",
		"DisplayServer=wayland",
		"systemctl enable sddm",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Hyprland autologin is missing %q", want)
		}
	}
}

// Blur, shadows and animations are drawn by llvmpipe on the CPU here.
func TestHyprlandDisablesEffectsForSoftwareRendering(t *testing.T) {
	look, err := HyprlandAsset("looknfeel.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"blur {\n        enabled = false",
		"shadow {\n        enabled = false",
		"animations {\n    enabled = false",
		"no_hardware_cursors = true",
	} {
		if !strings.Contains(look, want) {
			t.Errorf("software-rendering adaptation missing: %q", want)
		}
	}
}

// The helper scripts stand in for Omarchy's menu binaries and are embedded, so
// nothing else would catch a syntax error in them before a real install.
func TestHyprlandHelperScriptsAreValidShell(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	for _, name := range []string{"bin/march-keybindings", "bin/march-powermenu"} {
		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash rejected %s: %v\n%s", name, err, out)
		}
		if !strings.HasPrefix(body, "#!") {
			t.Errorf("%s has no shebang", name)
		}
	}
}

// The generated script embeds each config in a heredoc; a marker colliding with
// file content would truncate that file silently.
func TestHyprlandHeredocMarkersAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, name := range HyprlandAssetNames() {
		marker := heredocMarker(name)
		if prev, dup := seen[marker]; dup {
			t.Errorf("assets %q and %q share heredoc marker %q", prev, name, marker)
		}
		seen[marker] = name

		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == marker {
				t.Errorf("asset %q contains its own heredoc marker %q", name, marker)
			}
		}
	}
}

// Hyprland 0.53 replaced the old "windowrule = float, class:^(x)$" form, and
// 0.56 rejects it outright — the desktop then starts with an error banner. The
// renamed fields are easy to miss, so they are pinned here.
func TestHyprlandWindowRulesUseCurrentSyntax(t *testing.T) {
	apps, err := HyprlandAsset("apps.conf")
	if err != nil {
		t.Fatal(err)
	}

	for _, line := range strings.Split(apps, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "windowrule") {
			continue
		}
		if strings.HasPrefix(line, "windowrulev2") {
			t.Errorf("windowrulev2 is deprecated: %s", line)
		}
		// Every rule must state what it matches on.
		if !strings.Contains(line, "match:") {
			t.Errorf("rule has no match: condition, which 0.56 rejects: %s", line)
		}
		// Fields renamed in 0.56; the old names are silently invalid.
		for old, current := range map[string]string{
			"suppressevent": "suppress_event",
			"nofocus":       "no_focus",
		} {
			if strings.Contains(line, old+" ") || strings.Contains(line, ", "+old) {
				t.Errorf("%q was renamed to %q: %s", old, current, line)
			}
		}
	}
}

// Options that no longer exist make Hyprland report a config error on every
// start, which is what the user sees as a broken desktop.
func TestHyprlandConfigUsesNoRemovedOptions(t *testing.T) {
	// Removed or renamed in the 0.5x series.
	removed := []string{"misc:vfr", "vfr =", "windowrulev2"}

	for _, name := range HyprlandAssetNames() {
		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, opt := range removed {
			if strings.Contains(body, opt) {
				t.Errorf("asset %q uses %q, which Hyprland no longer accepts", name, opt)
			}
		}
	}
}

// Effects are disabled because software rendering makes them painful. With the
// guest drawing on the host GPU they cost little, so Omarchy's look is restored.
func TestHyprlandEffectsFollowRendering(t *testing.T) {
	soft := hyprlandProfile()
	soft.GPUAccelerated = false
	s := mustScript(t, soft)
	if strings.Contains(s, "blur {\n        enabled = true") {
		t.Error("blur was enabled on a guest that renders in software")
	}

	accel := hyprlandProfile()
	accel.GPUAccelerated = true
	s = mustScript(t, accel)
	for _, want := range []string{"blur {", "enabled = true", "animations {"} {
		if !strings.Contains(s, want) {
			t.Errorf("hardware-rendered guest is missing %q", want)
		}
	}
	// Whichever way, the file must exist so hyprland.conf's source line resolves.
	if !strings.Contains(s, "effects.conf") {
		t.Error("effects.conf is never written, so hyprland.conf would fail to source it")
	}
}

// The virtio Vulkan driver only helps when the host forwards Vulkan. Installed
// without that, it finds no device and makes Vulkan programs fail in a more
// confusing way than if it were simply absent.
func TestVulkanPackagesFollowTheHost(t *testing.T) {
	p := DefaultProfile("arch")
	p.Desktop = DesktopHyprland
	p.Username, p.Password = "arch", "marchtest"

	p.VulkanAccelerated = false
	off, err := Script(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range vulkanPackages {
		if strings.Contains(off, pkg) {
			t.Errorf("%s is installed on a guest whose host cannot forward Vulkan", pkg)
		}
	}

	p.VulkanAccelerated = true
	on, err := Script(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range vulkanPackages {
		if !strings.Contains(on, pkg) {
			t.Errorf("%s is missing from a Venus-capable guest", pkg)
		}
	}
}
