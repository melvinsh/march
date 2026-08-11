package install

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	script := mustScript(t, testProfile())
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash rejected the generated script: %v\n%s\n--- script ---\n%s",
			err, out, script)
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
		// Chrome comes from Google rather than from a mirror. Without these two
		// a unit test would download 126 MB and unpack it onto the host.
		"curl":   "exit 0",
		"bsdtar": "exit 0",
		// Not a stub but a shim: the guest has GNU sed, where -i takes no
		// argument, and macOS has BSD sed, where it takes one. The script is
		// written for the guest, so the difference is absorbed here rather
		// than by writing the rewrite in a dialect neither prefers.
		"sed": `if [ "$(uname)" = Darwin ]; then
  args=()
  for a in "$@"; do
    if [ "$a" = -i ]; then args+=(-i ''); else args+=("$a"); fi
  done
  exec /usr/bin/sed "${args[@]}"
fi
exec /usr/bin/sed "$@"`,
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
	// The script checks that Chrome really unpacked before carrying on, which
	// is exactly the guard that should stay; with bsdtar stubbed out, the dry
	// run has to put the file there itself.
	if err := os.MkdirAll(filepath.Join(root, "opt", "google", "chrome"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "opt", "google", "chrome", "chrome"),
		[]byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The same for the desktop entry the script repoints at march's launcher:
	// it comes out of the .deb bsdtar is standing in for, and the script now
	// refuses to continue if the rewrite did not take.
	apps := filepath.Join(root, "usr", "share", "applications")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "[Desktop Entry]\nExec=/usr/bin/" + chromeBinary + " %U\n" +
		"[Desktop Action new-window]\nExec=/usr/bin/" + chromeBinary + "\n"
	if err := os.WriteFile(filepath.Join(apps, chromeDesktopFile), []byte(entry), 0o644); err != nil {
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
	// An installed VM has a single boot target and nobody waiting at its GRUB
	// menu, so the timeout should be zeroed rather than left on the default.
	if !strings.Contains(s, "GRUB_TIMEOUT=0") {
		t.Error("GRUB should boot straight through instead of waiting at a menu")
	}
	// "quiet" would suppress the systemd progress march watches for, making a
	// booted desktop indistinguishable from a hung one.
	if strings.Contains(s, `GRUB_CMDLINE_LINUX_DEFAULT="quiet`) {
		t.Error("the installed system boots quietly, hiding the readiness signal")
	}
}

func TestScriptBootBranding(t *testing.T) {
	s := mustScript(t, testProfile())

	// The wallpaper is shipped base64 inside a heredoc, because it is binary
	// and everything else in the script is text. It must decode to a real PNG
	// at the destination the window-side readers agree on.
	if !strings.Contains(s, `base64 -d > '/usr/share/backgrounds/march.png'`) {
		t.Fatal("the wallpaper is never written to /usr/share/backgrounds")
	}
	blob := regexp.MustCompile(`(?s)MARCHBGP'\n([A-Za-z0-9+/=]+)\nMARCHBGP`).FindStringSubmatch(s)
	if blob == nil {
		t.Fatal("no base64 wallpaper blob found in the script")
	}
	decoded, err := base64.StdEncoding.DecodeString(blob[1])
	if err != nil {
		t.Fatalf("the wallpaper blob is not base64: %v", err)
	}
	if !bytes.Equal(decoded[:8], []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("the wallpaper blob does not decode to a PNG")
	}

	// The SDDM greeter theme is installed and selected, so the display-manager
	// phase paints the background rather than sitting black.
	for _, want := range []string{
		"'/usr/share/sddm/themes/march'",
		"Main.qml",
		"metadata.desktop",
		"Current=march",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the SDDM branding is missing %q", want)
		}
	}

	// Hyprland draws the same wallpaper on its first frame; the autorestarted
	// swaybg must name the shared background, not a hex colour.
	if !strings.Contains(s, `swaybg -i /usr/share/backgrounds/march.png`) {
		t.Error("Hyprland is not pointed at the shared wallpaper")
	}
	if strings.Contains(s, `swaybg -c "#1a1b26"`) {
		t.Error("Hyprland still paints a flat colour, not the wallpaper")
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

// Autologin is what makes the machine land on a desktop with no interaction.
func TestScriptAutologin(t *testing.T) {
	s := mustScript(t, testProfile())
	for _, want := range []string{
		"/etc/sddm.conf.d/autologin.conf",
		"User=melvin",
		"Session=hyprland",
		// Without this SDDM starts X, and the autologin silently becomes a
		// greeter nobody asked for.
		"DisplayServer=wayland",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("autologin config is missing %q", want)
		}
	}

	t.Run("disabled", func(t *testing.T) {
		p := testProfile()
		p.Autologin = false
		if s := mustScript(t, p); strings.Contains(s, "[Autologin]") {
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
	script := mustScript(t, testProfile())
	for _, tag := range []string{"MARCH_CHROOT", "MARCH_BOOT"} {
		body := extractHeredoc(t, script, tag)
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash rejected the %s body: %v\n%s\n--- body ---\n%s",
				tag, err, out, body)
		}
	}
}

// A mirror that has merely slowed down makes pacman abort the whole transaction
// after ten silent seconds ("Operation too slow"). The install must wait a slow
// mirror out instead, so the live environment's download timeout is disabled
// before any pacman or pacstrap runs.
func TestScriptDisablesDownloadTimeout(t *testing.T) {
	s := mustScript(t, testProfile())

	if !strings.Contains(s, "DownloadTimeout = 0") {
		t.Fatal("the live environment never disables pacman's download timeout")
	}
	// pacstrap runs pacman against /etc/pacman.conf, so the setting must land
	// there before the first pacstrap and apply to every later download too.
	pacmanConf := strings.Index(s, "/etc/pacman.conf")
	firstPacstrap := strings.Index(s, "pacstrap ")
	if pacmanConf < 0 {
		t.Fatal("the timeout is not written to /etc/pacman.conf")
	}
	if firstPacstrap < 0 || pacmanConf > firstPacstrap {
		t.Error("the timeout is configured after the first pacstrap, so the base install is still unprotected")
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
// screen an unscaled desktop is sharp but too small to read. Hyprland scales in
// the compositor; only what XWayland cannot learn from it is set here.
func TestScriptSetsDesktopScaling(t *testing.T) {
	p := testProfile()
	p.ScalePercent = 200
	s := mustScript(t, p)

	for _, want := range []string{
		"QT_SCALE_FACTOR=2", // Qt under XWayland
		"XCURSOR_SIZE=48",   // pointer, which does not scale on its own
	} {
		if !strings.Contains(s, want) {
			t.Errorf("scaling config is missing %q", want)
		}
	}
}

func TestScriptScalingCanBeDisabled(t *testing.T) {
	p := testProfile()
	p.ScalePercent = 100
	s := mustScript(t, p)

	// envs.lua sets a cursor size of its own, so the check is for the block
	// scalingSnippet writes rather than for the names in it.
	if strings.Contains(s, "ENVSCALE") {
		t.Error("scaling was configured at 100%")
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

// hyprlandProfile is testProfile under its old name, kept because the checks
// below read as statements about the desktop rather than about a profile.
func hyprlandProfile() Profile { return testProfile() }

// march installs one desktop, and nothing may reintroduce a second: an X11
// stack or a second display manager in the script means a path that no test
// exercises and no user asked for.
func TestOnlyOneDesktopIsInstalled(t *testing.T) {
	s := mustScript(t, testProfile())

	// Package names are matched with their separators, so "gnome" cannot match
	// the gnome-keyring the desktop legitimately installs.
	for _, gone := range []string{
		"xorg-server", "xorg-xinit", "xfce4", "lightdm", "gnome", "plasma-meta", "gdm",
	} {
		for _, form := range []string{" " + gone + " ", " " + gone + "\n"} {
			if strings.Contains(s, form) {
				t.Errorf("the install still carries %q from the days of a desktop picker", gone)
			}
		}
	}
	for _, gone := range []string{"GDK_SCALE", "march-autoresize", "lightdm.conf", "custom.conf"} {
		if strings.Contains(s, gone) {
			t.Errorf("the install still carries %q from the days of a desktop picker", gone)
		}
	}
	if !strings.Contains(s, "systemctl enable sddm") {
		t.Error("SDDM is not enabled, so nothing starts the session")
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
	// The generated monitor call carries the resolution and scale.
	if !strings.Contains(s, `hl.monitor({`) || !strings.Contains(s, `mode = "preferred"`) {
		t.Error("no monitor configuration was written")
	}
}

// The configs are copied through /etc/skel, which only works if they exist
// before the account is created.
func TestHyprlandConfigIsWrittenBeforeUseradd(t *testing.T) {
	s := mustScript(t, hyprlandProfile())
	skel := strings.Index(s, "/etc/skel/.config/hypr/hyprland.lua")
	user := strings.Index(s, "useradd -m")
	if skel < 0 || user < 0 {
		t.Fatal("expected both the skel config and useradd in the script")
	}
	if skel > user {
		t.Error("the desktop config is written after useradd, so the account never receives it")
	}
}

// installedPrograms is every command name a Hyprland guest can be expected to
// have: the packages the install lists, march's own helpers taken from the file
// map, and the binaries whose names differ from the package shipping them.
//
// It is the yardstick for three different "does this actually run anything"
// checks — the bindings, the menu and the bar — so a package added for one of
// them is available to all three.
func installedPrograms() map[string]bool {
	provided := map[string]bool{}
	for _, list := range [][]string{
		basePackages, waylandCommonPackages, hyprlandPackages, standardPackages,
	} {
		for _, p := range list {
			provided[p] = true
		}
	}

	// Read out of the file map rather than listed here, so a helper that is
	// added and then never installed cannot pass this check.
	for _, dest := range hyprlandFileMap {
		if name, ok := strings.CutPrefix(dest, "/usr/local/bin/"); ok {
			provided[name] = true
		}
	}

	for _, extra := range []string{
		// Named differently from the package that ships them.
		"hyprctl",                          // hyprland
		"makoctl",                          // mako
		"nvim",                             // neovim
		"nmtui",                            // networkmanager
		"notify-send",                      // libnotify
		"pactl",                            // libpulse, via pipewire-pulse
		"checkupdates",                     // pacman-contrib
		"paccache",                         // pacman-contrib
		"swayosd-client", "swayosd-server", // swayosd
		"wl-copy", "wl-paste", // wl-clipboard

		// Not a package at all: unpacked from Google's own .deb, see chrome.go.
		chromeBinary,
		// Generated by the install rather than shipped, like the march-*
		// helpers: it is where Chrome's flags live.
		chromeLauncher,

		// Shipped by the base system rather than by anything march lists.
		"bash", "cat", "ip", "pgrep", "pkill", "setsid", "sudo", "systemctl",
		"pacman", "dbus-update-activation-environment",
	} {
		provided[extra] = true
	}
	return provided
}

// Vendoring bindings from Omarchy is exactly where dead shortcuts creep in:
// none of Omarchy's own binaries exist for aarch64, so any leftover reference
// is a key that silently does nothing.
func TestHyprlandBindingsOnlyCallInstalledPrograms(t *testing.T) {
	provided := installedPrograms()

	for _, name := range HyprlandAssetNames() {
		if !strings.HasSuffix(name, ".lua") {
			continue
		}
		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range luaExecCommands(body) {
			fields := strings.Fields(cmd)
			if len(fields) == 0 {
				continue
			}
			bin := fields[0]
			switch {
			// The app commands themselves are the apps.lua table, checked below.
			case strings.HasPrefix(bin, "apps."):
				continue
			// An absolute path stands on its own.
			case strings.HasPrefix(bin, "/"):
				continue
			case provided[bin]:
				continue
			}
			t.Errorf("%s runs %q, which nothing installs:\n  %s", name, bin, cmd)
		}
	}

	// The three commands the bindings reach through the apps table.
	apps, err := HyprlandAsset("apps.lua")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"terminal", "browser", "file_manager"} {
		cmd := luaTableString(apps, field)
		if cmd == "" {
			t.Errorf("apps.lua defines no %q", field)
			continue
		}
		if bin := strings.Fields(cmd)[0]; !provided[bin] {
			t.Errorf("apps.%s is %q, which nothing installs", field, bin)
		}
	}
}

// luaExecCommands pulls the command out of every hl.exec_cmd / hl.dsp.exec_cmd
// call. The argument is either a "quoted" string, a [[long]] string, or an
// expression built from the apps table, and all three are returned verbatim.
func luaExecCommands(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "--") {
			continue
		}
		rest := line
		for {
			i := strings.Index(rest, "exec_cmd(")
			if i < 0 {
				break
			}
			rest = rest[i+len("exec_cmd("):]
			arg := rest
			switch {
			case strings.HasPrefix(arg, `"`), strings.HasPrefix(arg, `'`):
				quote := arg[:1]
				if end := strings.Index(arg[1:], quote); end >= 0 {
					arg = arg[1 : 1+end]
				}
			case strings.HasPrefix(arg, "[["):
				if end := strings.Index(arg, "]]"); end >= 0 {
					arg = arg[2:end]
				}
			default:
				if end := strings.Index(arg, ")"); end >= 0 {
					arg = arg[:end]
				}
			}
			out = append(out, arg)
		}
	}
	return out
}

// luaTableString returns the value of a `field = "value"` entry.
func luaTableString(body, field string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, field+" = \"") {
			continue
		}
		v := line[len(field+" = \""):]
		if end := strings.Index(v, `"`); end >= 0 {
			return v[:end]
		}
	}
	return ""
}

// ── The menu and the bar ────────────────────────────────────────────────────

// menuRows returns march-menu's table, one row of five fields: id, icon, label,
// action and condition. Rows are the tab-separated lines of the heredoc; every
// other line of the script has no tabs in it.
func menuRows(t *testing.T) [][]string {
	t.Helper()
	body, err := HyprlandAsset("bin/march-menu")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			continue
		}
		rows = append(rows, fields)
	}
	if len(rows) == 0 {
		t.Fatal("march-menu has no rows at all")
	}
	return rows
}

// The whole point of the menu is that its entries do something. Omarchy's own
// binaries have no aarch64 build, so a row copied over unchanged is a line that
// looks right and runs nothing.
func TestMenuOnlyRunsInstalledPrograms(t *testing.T) {
	provided := installedPrograms()

	check := func(id, kind, command string) {
		if command == "-" {
			return
		}
		fields := strings.Fields(command)
		if len(fields) == 0 {
			t.Errorf("%s has an empty %s", id, kind)
			return
		}
		// A condition may be negated; the program is what follows.
		bin := fields[0]
		if bin == "!" && len(fields) > 1 {
			bin = fields[1]
		}
		if !provided[bin] {
			t.Errorf("menu row %s %s runs %q, which nothing installs:\n  %s",
				id, kind, bin, command)
		}
	}

	for _, row := range menuRows(t) {
		check(row[0], "action", row[3])
		check(row[0], "condition", row[4])
	}
}

// A branch with no rows under it opens an empty list, and a row whose parent is
// missing can never be reached.
func TestMenuTreeIsWellFormed(t *testing.T) {
	rows := menuRows(t)

	ids := map[string]string{} // id -> action
	for _, row := range rows {
		if prev, dup := ids[row[0]]; dup {
			t.Errorf("menu id %q appears twice (%q and %q)", row[0], prev, row[3])
		}
		ids[row[0]] = row[3]
	}

	hasChildren := map[string]bool{}
	for id := range ids {
		// The parent is everything up to the last dot: routes nest as deep as
		// the table cares to go, and march-menu walks them a level at a time.
		dot := strings.LastIndex(id, ".")
		if dot < 0 {
			continue
		}
		parent := id[:dot]
		if _, ok := ids[parent]; !ok {
			t.Errorf("menu row %q hangs off %q, which is not a row", id, parent)
		}
		hasChildren[parent] = true
	}

	for id, action := range ids {
		if action == "-" && !hasChildren[id] {
			t.Errorf("menu branch %q has no rows under it, so it opens empty", id)
		}
	}
}

// A key bound to a route the table does not have opens nothing at all, which is
// indistinguishable from a broken keyboard.
func TestBindingsOpenMenuRoutesThatExist(t *testing.T) {
	rows := menuRows(t)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row[0]] = true
	}

	bindings, err := HyprlandAsset("bindings.lua")
	if err != nil {
		t.Fatal(err)
	}
	routes := 0
	for _, cmd := range luaExecCommands(bindings) {
		fields := strings.Fields(cmd)
		if len(fields) == 0 || fields[0] != "march-menu" {
			continue
		}
		if len(fields) == 1 {
			continue // the root
		}
		routes++
		if !ids[fields[1]] {
			t.Errorf("a key opens the menu at %q, which is not a row in it", fields[1])
		}
	}
	if routes == 0 {
		t.Error("no key opens a menu branch directly; Omarchy binds several")
	}
}

// waybar refuses to start on a config it cannot parse, which takes the whole
// bar with it. The file is JSONC only in that it carries comments.
func TestBarConfigIsValidJSON(t *testing.T) {
	body, err := HyprlandAsset("waybar/config.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	var stripped strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		stripped.WriteString(line)
		stripped.WriteString("\n")
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(stripped.String()), &config); err != nil {
		t.Fatalf("waybar config is not valid JSON: %v", err)
	}

	// Every module named in a section must be configured, or waybar draws a gap.
	for _, section := range []string{"modules-left", "modules-center", "modules-right"} {
		names, ok := config[section].([]any)
		if !ok {
			t.Errorf("the bar has no %s", section)
			continue
		}
		for _, name := range names {
			module, _ := name.(string)
			if _, ok := config[module]; !ok && !strings.HasPrefix(module, "hyprland/") {
				t.Errorf("%s lists %q, which the config never defines", section, module)
			}
		}
	}
}

// The complaint this change answers: the bar's buttons opened a terminal or
// nothing. Every one of them must run something the guest actually has.
func TestBarButtonsRunInstalledPrograms(t *testing.T) {
	body, err := HyprlandAsset("waybar/config.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	provided := installedPrograms()

	commands := regexp.MustCompile(`"(on-click|on-click-right|exec)"\s*:\s*"([^"]+)"`)
	found := 0
	for _, m := range commands.FindAllStringSubmatch(body, -1) {
		command := m[2]
		// "activate" is waybar's own workspace action, not a program.
		if command == "activate" {
			continue
		}
		found++
		if bin := strings.Fields(command)[0]; !provided[bin] {
			t.Errorf("the bar runs %q, which nothing installs:\n  %s", bin, command)
		}
	}
	if found == 0 {
		t.Error("no module in the bar runs anything")
	}
}

// An idle daemon with no rules is a process that does nothing; one with rules
// locks a VM that the host already locks. Neither is wanted, so nothing should
// start it.
func TestNoIdleDaemonIsInstalledOrStarted(t *testing.T) {
	s := mustScript(t, hyprlandProfile())
	if strings.Contains(s, "hypridle") {
		t.Error("hypridle is still installed or autostarted")
	}
}

// The keybindings are Omarchy's; drifting from them defeats the point.
func TestHyprlandMirrorsOmarchyShortcuts(t *testing.T) {
	bindings, err := HyprlandAsset("bindings.lua")
	if err != nil {
		t.Fatal(err)
	}
	// A representative sample spanning apps, the clipboard, tiling, workspaces
	// and utilities. Every one of these is where Omarchy's own Lua
	// configuration puts it — the modifiers matter as much as the key, since
	// what makes them worth vendoring is that muscle memory carries over.
	for _, want := range []string{
		// Applications, all on SUPER + SHIFT since Omarchy moved them there.
		`"SUPER + Return", hl.dsp.exec_cmd("march-launch terminal")`,
		`"SUPER + SHIFT + Return", hl.dsp.exec_cmd("march-launch browser")`,
		`"SUPER + SHIFT + B", hl.dsp.exec_cmd("march-launch browser")`,
		`"SUPER + SHIFT + F", hl.dsp.exec_cmd("march-launch files")`,
		`"SUPER + SHIFT + N", hl.dsp.exec_cmd("march-launch editor")`,

		// Universal copy and paste, which is what SUPER + C and SUPER + V are
		// for on an Omarchy desktop.
		`"SUPER + C", send_shortcut_once("CTRL", "Insert")`,
		`"SUPER + V", send_shortcut_once("SHIFT", "Insert")`,
		`"SUPER + X", send_shortcut_once("CTRL", "X")`,

		// Tiling. SUPER + T floats and SUPER + F fills the screen; both were
		// elsewhere before Omarchy's tiling-v2 and are the two most commonly
		// missed if this drifts.
		`"SUPER + W", hl.dsp.window.close()`,
		`"SUPER + T", hl.dsp.window.float({ action = "toggle" })`,
		`"SUPER + F", hl.dsp.window.fullscreen({ mode = "fullscreen" })`,
		`"SUPER + ALT + F", hl.dsp.window.fullscreen({ mode = "maximized" })`,
		`"SUPER + J", hl.dsp.layout("togglesplit")`,
		`"SUPER + left", hl.dsp.focus({ direction = "left" })`,
		`"SUPER + SHIFT + left", hl.dsp.window.swap({ direction = "left" })`,
		`"SUPER + Tab", hl.dsp.focus({ workspace = "e+1" })`,
		`"ALT + Tab", hl.dsp.window.cycle_next()`,
		`"SUPER + code:20"`,
		`hl.dsp.workspace.toggle_special("scratchpad")`,
		`hl.dsp.group.toggle()`,

		// The launcher and the menu, one level apart as Omarchy has them.
		`"SUPER + space", hl.dsp.exec_cmd("fuzzel")`,
		`"SUPER + ALT + space", hl.dsp.exec_cmd("march-menu")`,
	} {
		if !strings.Contains(bindings, want) {
			t.Errorf("omarchy shortcut %q is missing", want)
		}
	}

	// The workspace bindings are a loop rather than twenty lines, so assert the
	// keycodes and dispatchers it is built from.
	for _, want := range []string{
		`for i = 1, 10 do`,
		`"code:" .. (9 + i)`,
		`hl.dsp.focus({ workspace = i })`,
		`hl.dsp.window.move({ workspace = i })`,
	} {
		if !strings.Contains(bindings, want) {
			t.Errorf("workspace bindings are missing %q", want)
		}
	}
}

// Wayland scales in the compositor; adding GDK_SCALE on top double-scales
// every GTK window.
func TestHyprlandScalesInTheCompositor(t *testing.T) {
	p := hyprlandProfile()
	p.ScalePercent = 200
	s := mustScript(t, p)

	if !strings.Contains(s, "scale = 2,") {
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
	look, err := HyprlandAsset("looknfeel.lua")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"blur = {\n            enabled = false,",
		"shadow = {\n            enabled = false,",
		"animations = {\n        enabled = false,",
		// An int (0 hw / 1 never / 2 auto), not a bool: Hyprland's Lua config is
		// typed, and a bool here is a config error rather than a silent 1.
		"no_hardware_cursors = 1,",
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
	for _, name := range HyprlandAssetNames() {
		if !strings.HasPrefix(name, "bin/") {
			continue
		}
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

// A window rule with no match table is rejected outright — the desktop then
// starts with an error banner. Every rule march ships must carry one.
func TestHyprlandWindowRulesUseCurrentSyntax(t *testing.T) {
	apps, err := HyprlandAsset("apps.lua")
	if err != nil {
		t.Fatal(err)
	}

	rules := strings.Split(apps, "hl.window_rule({")
	if len(rules) < 2 {
		t.Fatal("apps.lua declares no window rules at all")
	}
	for _, rule := range rules[1:] {
		body := rule
		if end := strings.Index(body, "})"); end >= 0 {
			body = body[:end]
		}
		if !strings.Contains(body, "match = {") {
			t.Errorf("rule has no match table, which Hyprland rejects:\nhl.window_rule({%s})", body)
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

// hyprlang is deprecated since Hyprland 0.55 and will be dropped a release or
// two later. A line left in the old syntax is not a syntax error in Lua — it
// parses as something else entirely, or not at all — so it is worth naming the
// leftovers directly.
func TestHyprlandConfigHasNoLeftoverHyprlang(t *testing.T) {
	// The hyprlang keywords, as they appear at the head of a line. The value has
	// to be something other than a table constructor, or Lua's own `binds = {`
	// and `animations = {` sections read as hyprlang.
	leftover := regexp.MustCompile(`^\s*(bind[a-z]*|windowrule[a-z0-9]*|layerrule|exec-once|exec|env|source|monitor|animation|bezier|submap|\$[A-Za-z_]\w*)\s*=\s*[^{\s]`)

	for _, name := range HyprlandAssetNames() {
		if !strings.HasSuffix(name, ".lua") {
			continue
		}
		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if leftover.MatchString(line) {
				t.Errorf("%s:%d is still hyprlang, not Lua:\n  %s", name, i+1, strings.TrimSpace(line))
			}
			// hyprctl lost `keyword` when the config became Lua, and getoption
			// now takes section.option rather than section:option.
			if strings.Contains(line, "hyprctl keyword") {
				t.Errorf("%s:%d calls `hyprctl keyword`, which no longer exists", name, i+1)
			}
		}
	}
}

// The generated files are Lua too, and nothing else parses them before a real
// install would.
func TestHyprlandGeneratedConfigIsLua(t *testing.T) {
	for _, accelerated := range []bool{false, true} {
		p := hyprlandProfile()
		p.GPUAccelerated = accelerated

		for name, body := range map[string]string{
			"monitor.lua": hyprlandMonitorConfig(p),
			"effects.lua": hyprlandEffectsConfig(p),
		} {
			for i, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == "" {
					continue
				}
				// Every non-blank line is either a Lua comment or Lua code; a
				// stray "#" comment would abort the whole file.
				if strings.HasPrefix(trimmed, "#") {
					t.Errorf("%s:%d (accelerated=%v) uses a shell comment, not a Lua one:\n  %s",
						name, i+1, accelerated, trimmed)
				}
			}
		}
	}
}

// Hyprland reads hyprland.lua in preference to hyprland.conf, and every file it
// pulls in has to be require()d rather than sourced.
func TestHyprlandTopLevelRequiresEveryConfigFile(t *testing.T) {
	top, err := HyprlandAsset("hyprland.lua")
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"apps", "envs", "looknfeel", "bindings", "effects", "monitor"} {
		if !strings.Contains(top, `require("`+module+`")`) {
			t.Errorf("hyprland.lua never requires %q, so that file is dead", module)
		}
	}

	// Autostart moved from exec-once onto the start event.
	if !strings.Contains(top, `hl.on("hyprland.start"`) {
		t.Error("nothing is autostarted: the hyprland.start handler is missing")
	}
}

// A syntax error means Hyprland refuses the config outright and falls back to
// its own default desktop. luac only ships with Lua, so this skips when no
// parser is installed — the E2E is the gate that always runs.
func TestHyprlandAssetsAreValidLua(t *testing.T) {
	var check func(path string) *exec.Cmd
	switch {
	case lookPathOK("luac"):
		check = func(path string) *exec.Cmd { return exec.Command("luac", "-p", path) }
	case lookPathOK("lua"):
		check = func(path string) *exec.Cmd { return exec.Command("lua", "-e", "assert(loadfile('"+path+"'))") }
	case lookPathOK("luajit"):
		check = func(path string) *exec.Cmd { return exec.Command("luajit", "-e", "assert(loadfile('"+path+"'))") }
	default:
		t.Skip("no Lua parser (luac, lua or luajit) on PATH; install one to check the configs here")
	}

	dir := t.TempDir()
	files := map[string]string{}
	for _, name := range HyprlandAssetNames() {
		if !strings.HasSuffix(name, ".lua") {
			continue
		}
		body, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = body
	}
	// The generated ones are Lua the guest has to load too.
	p := hyprlandProfile()
	p.GPUAccelerated = true
	files["monitor.lua"] = hyprlandMonitorConfig(p)
	files["effects.lua"] = hyprlandEffectsConfig(p)
	p.GPUAccelerated = false
	files["effects-software.lua"] = hyprlandEffectsConfig(p)

	if len(files) == 0 {
		t.Fatal("no Lua assets were found to check")
	}
	for name, body := range files {
		path := filepath.Join(dir, strings.ReplaceAll(name, "/", "_"))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := check(path).CombinedOutput(); err != nil {
			t.Errorf("%s is not valid Lua: %v\n%s", name, err, out)
		}
	}
}

func lookPathOK(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// Effects are disabled because software rendering makes them painful. With the
// guest drawing on the host GPU they cost little, so Omarchy's look is restored.
func TestHyprlandEffectsFollowRendering(t *testing.T) {
	soft := hyprlandProfile()
	soft.GPUAccelerated = false
	s := mustScript(t, soft)
	if strings.Contains(s, "blur = {\n            enabled = true") {
		t.Error("blur was enabled on a guest that renders in software")
	}

	accel := hyprlandProfile()
	accel.GPUAccelerated = true
	s = mustScript(t, accel)
	for _, want := range []string{"blur = {", "enabled = true,", "animations = {", "hl.curve(", "hl.animation("} {
		if !strings.Contains(s, want) {
			t.Errorf("hardware-rendered guest is missing %q", want)
		}
	}
	// Whichever way, the file must exist or hyprland.lua's require fails.
	if !strings.Contains(s, "effects.lua") {
		t.Error("effects.lua is never written, so hyprland.lua would fail to require it")
	}
}

// The virtio Vulkan driver only helps when the host forwards Vulkan. Installed
// without that, it finds no device and makes Vulkan programs fail in a more
// confusing way than if it were simply absent.
func TestVulkanPackagesFollowTheHost(t *testing.T) {
	p := DefaultProfile("arch")
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

// ── The browser ─────────────────────────────────────────────────────────────

// Chrome is the one thing march installs that does not come from a mirror, so
// every step of getting it in has to be in the script: fetched, unpacked,
// checked, and made the default.
func TestChromeIsInstalledAndDefault(t *testing.T) {
	s := mustScript(t, testProfile())

	for _, want := range []string{
		chromeDebURL,
		"bsdtar -xpf /tmp/chrome/data.tar.*",
		// Unpacking silently produces nothing when the archive layout changes,
		// and the next thing a user would notice is a browser that is not there.
		"/mnt/opt/google/chrome/chrome",
		// Google's package adds an apt repository through a daily cron job.
		"--exclude './etc/cron.daily*'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the Chrome install is missing %q", want)
		}
	}

	// bsdtar and curl are not in the live environment by default, and the whole
	// fetch depends on both.
	if !strings.Contains(s, "libarchive curl") {
		t.Error("the tools phase does not install what the Chrome fetch needs")
	}
	for _, tool := range []string{"bsdtar", "curl"} {
		if !strings.Contains(s, " "+tool+" ") && !strings.Contains(s, tool+";") {
			t.Errorf("%s is never checked for before it is used", tool)
		}
	}

	// Default browser, three ways: the desktop entry for anything asking
	// xdg-mime, $BROWSER for anything that does not, and apps.lua for the key.
	if !strings.Contains(s, "x-scheme-handler/https="+chromeDesktopFile) {
		t.Error("Chrome is installed but is not the default for https links")
	}
	envs, err := HyprlandAsset("envs.lua")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envs, `hl.env("BROWSER", "`+chromeLauncher+`")`) {
		t.Error("$BROWSER does not point at Chrome")
	}
	apps, err := HyprlandAsset("apps.lua")
	if err != nil {
		t.Fatal(err)
	}
	if browser := luaTableString(apps, "browser"); browser != chromeLauncher {
		t.Errorf("apps.browser is %q, want %q", browser, chromeLauncher)
	}
	// The desktop entry is what anything launching through xdg-open reads, so
	// it has to reach the launcher too — and keep the %U that carries the link
	// it was asked to open.
	if !strings.Contains(s, "sed -i 's|^Exec=/usr/bin/"+chromeBinary+"|Exec="+chromeLauncherPath+"|'") {
		t.Error("Chrome's desktop entry is not repointed at march's launcher")
	}
}

// TestChromeFlagsLiveInOneplace is the guard on the arrangement rather than on
// any one flag: Chrome has no flags file, so before the launcher existed the
// same string was repeated in five places and nothing checked that they agreed.
// Anything that names the binary directly is a browser that starts without
// march's flags, and the way that shows up is a four-minute welcome tour or a
// window that never appears.
func TestChromeFlagsLiveInOnePlace(t *testing.T) {
	for _, name := range HyprlandAssetNames() {
		asset, err := HyprlandAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(asset, "\n") {
			trimmed := strings.TrimSpace(line)
			// Prose may name the binary; only what runs it matters.
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "--") {
				continue
			}
			if !strings.Contains(line, chromeBinary) {
				continue
			}
			t.Errorf("%s names Chrome directly rather than going through %s:\n  %s",
				name, chromeLauncher, strings.TrimSpace(line))
		}
	}
}

// TestChromeLauncherCarriesTheFlags covers the launcher itself: the flags every
// guest gets, and the graphics flags only a guest that renders on the host GPU
// should get. On a software-rendered guest ANGLE would be a translation layer
// on top of llvmpipe, which is slower than the CPU rasterizer Chrome ships.
func TestChromeLauncherCarriesTheFlags(t *testing.T) {
	accel := testProfile()
	accel.GPUAccelerated = true
	software := testProfile()
	software.GPUAccelerated = false

	for _, tc := range []struct {
		name string
		p    Profile
	}{{"accelerated", accel}, {"software", software}} {
		s := mustScript(t, tc.p)

		if !strings.Contains(s, "cat > "+chromeLauncherPath+" <<") {
			t.Fatalf("%s: nothing writes the launcher", tc.name)
		}
		if !strings.Contains(s, "chmod 0755 "+chromeLauncherPath) {
			t.Errorf("%s: the launcher is never made executable", tc.name)
		}
		if !strings.Contains(s, `exec /usr/bin/`+chromeBinary) {
			t.Errorf("%s: the launcher does not start Chrome", tc.name)
		}
		// Without "$@" the launcher swallows the URL it was given, so every
		// clicked link opens the home page instead.
		if !strings.Contains(s, `"$@"`) {
			t.Errorf("%s: the launcher passes nothing through to Chrome", tc.name)
		}
		for _, f := range chromeBaseFlags {
			if !strings.Contains(s, f) {
				t.Errorf("%s: the launcher is missing %q", tc.name, f)
			}
		}
		// A sandbox is not something to trade for a frame rate.
		for _, unsafe := range []string{"--no-sandbox", "--disable-gpu-sandbox"} {
			if strings.Contains(s, unsafe) {
				t.Errorf("%s: the launcher disables Chrome's sandbox with %s", tc.name, unsafe)
			}
		}
	}

	for _, f := range chromeGPUFlags(accel) {
		if !strings.Contains(mustScript(t, accel), f) {
			t.Errorf("an accelerated guest does not get %q", f)
		}
		if strings.Contains(mustScript(t, software), f) {
			t.Errorf("a software-rendered guest is given the graphics flag %q", f)
		}
	}
	if got := chromeGPUFlags(software); len(got) > 0 {
		t.Errorf("a software-rendered guest gets graphics flags: %v", got)
	}

	// The launcher is generated shell inside a quoted heredoc, so nothing on
	// the way to the guest parses it: a stray backslash would first be noticed
	// as a browser that does not start.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	script := mustScript(t, accel)
	const open = "cat > " + chromeLauncherPath + " <<'MARCHCHROME'\n"
	start := strings.Index(script, open)
	if start < 0 {
		t.Fatal("the launcher heredoc is not in the script")
	}
	body := script[start+len(open):]
	end := strings.Index(body, "\nMARCHCHROME\n")
	if end < 0 {
		t.Fatal("the launcher heredoc is never terminated")
	}
	path := filepath.Join(t.TempDir(), chromeLauncher)
	if err := os.WriteFile(path, []byte(body[:end]), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
		t.Errorf("the launcher is not valid shell: %v\n%s\n%s", err, out, body[:end])
	}
}

// Chrome is not a pacman package, so nothing resolves its shared libraries for
// it. Missing one is a browser that exits with a linker error.
func TestChromeDependenciesAreInstalled(t *testing.T) {
	s := mustScript(t, testProfile())
	for _, pkg := range chromePackages {
		if !strings.Contains(s, " "+pkg+" ") && !strings.Contains(s, " "+pkg+"\n") {
			t.Errorf("Chrome needs %q and nothing installs it", pkg)
		}
	}
	// Chromium was the browser before; two of them is one more than was asked
	// for, and the second is 190 MB of download nobody opens.
	if strings.Contains(s, " chromium ") {
		t.Error("chromium is still installed alongside Chrome")
	}
}

// The standard toolset is what makes a march guest feel like a finished
// machine rather than a bare Arch install, so every package in it has to
// actually reach pacstrap.
func TestStandardPackagesAreInstalled(t *testing.T) {
	s := mustScript(t, testProfile())

	pacstrap := ""
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "pacstrap /mnt ") {
			pacstrap = line
		}
	}
	if pacstrap == "" {
		t.Fatal("no desktop pacstrap line in the script")
	}
	installed := map[string]bool{}
	for _, f := range strings.Fields(pacstrap) {
		installed[f] = true
	}
	for _, pkg := range standardPackages {
		if !installed[pkg] {
			t.Errorf("%q is in standardPackages but never installed", pkg)
		}
	}
}

// They are the distro's tools, not the compositor's, and they go in with the
// desktop rather than the base so a failure here cannot leave a machine
// without a bootable system.
func TestStandardPackagesAreInstalledWithTheDesktop(t *testing.T) {
	s := mustScript(t, testProfile())
	for _, pkg := range []string{"git", "ripgrep", "unzip", "mpv", "docker", "wireplumber"} {
		if !strings.Contains(s, " "+pkg+" ") && !strings.HasSuffix(s, " "+pkg) {
			t.Errorf("the install does not carry %q", pkg)
		}
	}
}

// linux-firmware is 277 MB of vendor blobs for hardware that does not exist
// behind QEMU's virt machine, and it was the single largest thing march
// downloaded.
func TestFirmwareBlobsAreNotInstalled(t *testing.T) {
	s := mustScript(t, testProfile())
	if strings.Contains(s, "linux-firmware") {
		t.Error("linux-firmware is installed; it is a gigabyte of blobs a virtio guest cannot use")
	}
	// The kernel itself must still be there — dropping the wrong line would
	// leave an unbootable machine.
	if !strings.Contains(s, baseKernel) {
		t.Errorf("the kernel %q is missing from the base install", baseKernel)
	}
}

// Enabling a service whose hardware does not exist turns `systemctl --failed`
// from a signal into noise.
func TestNoServicesForAbsentHardware(t *testing.T) {
	s := mustScript(t, testProfile())
	for _, unit := range []string{"bluetooth.service", "power-profiles-daemon"} {
		if strings.Contains(s, "systemctl enable "+unit) {
			t.Errorf("%s is enabled, but nothing behind virt answers to it", unit)
		}
	}
}

// march forwards a host port to the guest's sshd and prints the ssh command on
// its detail screen. Omarchy's firewall defaults would silently break that,
// since Omarchy has no such forward.
func TestFirewallKeepsSSHReachable(t *testing.T) {
	s := mustScript(t, testProfile())
	if !strings.Contains(s, "ufw default deny incoming") {
		t.Fatal("the firewall is never configured")
	}
	deny := strings.Index(s, "ufw default deny incoming")
	allow := strings.Index(s, "ufw allow 22/tcp")
	if allow < 0 {
		t.Fatal("ufw denies incoming traffic without allowing ssh, locking march out of its own VM")
	}
	if allow < deny {
		t.Error("ssh is allowed before the default policy that would override it")
	}
	if !strings.Contains(s, "systemctl enable ufw.service") {
		t.Error("the firewall rules are written but the service never starts")
	}
}

// Group membership only takes effect at account creation or later, never
// before, and docker without it means sudo for every command.
func TestDockerGroupIsGrantedAfterTheAccountExists(t *testing.T) {
	s := mustScript(t, testProfile())
	user := strings.Index(s, "useradd -m")
	docker := strings.Index(s, "usermod -aG docker")
	if user < 0 || docker < 0 {
		t.Fatal("expected both useradd and the docker group grant")
	}
	if docker < user {
		t.Error("the docker group is granted before the account exists")
	}
}

// Nothing gets installed that the hardware cannot support. A package whose
// device does not exist is a command that fails, a daemon that idles, or a
// unit in `systemctl --failed` — and it is paid for in download time on every
// single install.
func TestNothingIsInstalledForHardwareThatIsAbsent(t *testing.T) {
	{
		s := mustScript(t, testProfile())

		for _, pkg := range append(append([]string{}, omittedForVirtualHardware...), omittedBrokenOnARM...) {
			// Word boundaries: "bluez" must not match "bluez-utils", and a
			// package name must not match a substring of another.
			for _, form := range []string{" " + pkg + " ", " " + pkg + "\n"} {
				if strings.Contains(s, form) {
					t.Errorf("the install carries %q, which has no hardware behind it in a VM", pkg)
					break
				}
			}
		}
	}
}

// The omission list only means something if it is honest about what it covers.
func TestOmittedPackagesAreNotAlsoInstalled(t *testing.T) {
	installed := map[string]bool{}
	for _, pkg := range append(append(append([]string{},
		basePackages...), standardPackages...), hyprlandPackages...) {
		installed[pkg] = true
	}
	for _, pkg := range omittedForVirtualHardware {
		if installed[pkg] {
			t.Errorf("%q is listed as omitted but is also installed", pkg)
		}
	}
}

// march-launch's fallbacks run whenever it is called from outside the session
// apps.lua sets up: over ssh, from a TTY, or from the end-to-end suite. A
// browser default without Chrome's flags picks X11, finds no display and exits
// before it maps a window, so the default has to be the launcher that carries
// them rather than the binary that does not.
func TestLaunchFallbacksCanActuallyOpenAWindow(t *testing.T) {
	b, err := HyprlandAsset("bin/march-launch")
	if err != nil {
		t.Fatalf("march-launch is not embedded: %v", err)
	}
	launch := string(b)

	var fallback string
	for _, line := range strings.Split(launch, "\n") {
		if strings.HasPrefix(line, "BROWSER=") {
			fallback = line
			break
		}
	}
	if fallback == "" {
		t.Fatal("march-launch names no browser fallback")
	}
	if !strings.Contains(fallback, chromeLauncher) {
		t.Errorf("the browser fallback does not go through %s:\n%s", chromeLauncher, fallback)
	}
}
