package install

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/melvinsh/march/internal/console"
)

// Guest-side constants discovered by inspecting a live Archboot aarch64 image.
const (
	// guestHostAddr is the host as seen from QEMU's user-mode network stack.
	guestHostAddr = "10.0.2.2"

	// kernelPath and initrdPath are where the Archboot ISO keeps its boot
	// files. They are referenced from GRUB's own default menu entry.
	kernelPath = "/boot/Image-aarch64.gz"
	initrdPath = "/boot/init-aarch64.img"

	// baseCmdline is Archboot's own command line minus nr_cpus=1, which caps
	// the live environment at a single core and roughly halves install speed.
	baseCmdline = "console=ttyAMA0,115200 console=tty0 loglevel=4 audit=0"
)

// Progress reports how far an installation has got.
type Progress struct {
	Phase   Phase
	Index   int // 1-based position within Phases
	Total   int
	Message string
}

// Event callbacks. Both may be nil.
type Hooks struct {
	// OnProgress fires when the guest enters a new phase.
	OnProgress func(Progress)
	// OnOutput receives raw console output, for a live log view.
	OnOutput func(string)
}

// Runner performs one unattended installation.
type Runner struct {
	Profile Profile
	Hooks   Hooks

	// SerialSocket is the VM's console socket. The VM must already be running
	// and sitting at its bootloader.
	SerialSocket string
}

// scriptServer serves the install script to the guest over the user-mode
// network. It binds to loopback, so nothing outside this machine can read it —
// which matters because the script carries the account password.
type scriptServer struct {
	srv     *http.Server
	port    int
	fetched chan struct{}
	once    sync.Once
}

func serveScript(script string) (*scriptServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the install script server: %w", err)
	}
	s := &scriptServer{
		port:    ln.Addr().(*net.TCPAddr).Port,
		fetched: make(chan struct{}),
	}
	s.srv = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/install.sh" {
				http.NotFound(w, r)
				return
			}
			s.once.Do(func() { close(s.fetched) })
			w.Header().Set("Content-Type", "text/x-shellscript")
			_, _ = w.Write([]byte(script))
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *scriptServer) url() string {
	return fmt.Sprintf("http://%s:%d/install.sh", guestHostAddr, s.port)
}

func (s *scriptServer) close() { _ = s.srv.Close() }

// Timeouts for the stages of an install. The package phases dominate and vary
// hugely with connection speed, hence the generous ceiling.
const (
	grubTimeout     = 3 * time.Minute
	fetchTimeout    = 5 * time.Minute
	phaseTimeout    = 60 * time.Minute
	overallDeadline = 90 * time.Minute
)

// Run installs Arch onto the running VM and returns when the guest reports it
// has finished. The VM must have been started with the installer ISO attached
// and be at, or approaching, its GRUB menu.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Profile.Validate(); err != nil {
		return err
	}
	script, err := Script(r.Profile)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, overallDeadline)
	defer cancel()

	srv, err := serveScript(script)
	if err != nil {
		return err
	}
	defer srv.close()

	c, err := console.Dial(ctx, r.SerialSocket)
	if err != nil {
		return err
	}
	defer c.Close()
	if r.Hooks.OnOutput != nil {
		c.OnOutput(r.Hooks.OnOutput)
	}

	r.report(Progress{Message: "Waiting for the bootloader"})
	if err := r.bootWithAutorun(ctx, c, srv.url()); err != nil {
		return err
	}

	r.report(Progress{Message: "Booting the installer"})
	select {
	case <-srv.fetched:
	case <-time.After(fetchTimeout):
		return fmt.Errorf("the guest never fetched the install script; last output:\n%s", c.Tail(2000))
	case <-ctx.Done():
		return ctx.Err()
	}

	return r.trackPhases(ctx, c)
}

// bootWithAutorun drives GRUB's command line to boot the live environment with
// a cmdline of our choosing. Archboot's stock entry cannot be used as-is: it
// pins the installer to one CPU and carries no autorun hook.
func (r *Runner) bootWithAutorun(ctx context.Context, c *console.Console, scriptURL string) error {
	grubCtx, cancel := context.WithTimeout(ctx, grubTimeout)
	defer cancel()

	if _, err := c.Expect(grubCtx, "GNU GRUB"); err != nil {
		return fmt.Errorf("the GRUB menu never appeared: %w\nlast output:\n%s", err, c.Tail(2000))
	}
	// The menu counts down before auto-booting the stock entry; give it a
	// moment to finish drawing, then take over.
	time.Sleep(1500 * time.Millisecond)

	if err := c.Send("c"); err != nil {
		return err
	}
	if _, err := c.Expect(grubCtx, "grub>"); err != nil {
		return fmt.Errorf("could not reach the GRUB command line: %w\nlast output:\n%s", err, c.Tail(2000))
	}

	cmdline := fmt.Sprintf("linux %s %s autorun=%s", kernelPath, baseCmdline, scriptURL)
	for _, line := range []string{
		cmdline,
		"initrd " + initrdPath,
		"boot",
	} {
		if err := c.SendLine(line); err != nil {
			return err
		}
		// GRUB's line editor echoes as it goes; a short pause keeps commands
		// from running together.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}
	}
	return nil
}

// trackPhases follows the guest through the installation, reporting each phase
// and returning once it completes or fails.
func (r *Runner) trackPhases(ctx context.Context, c *console.Console) error {
	patterns := []string{MarkerComplete, MarkerFailed}
	for _, p := range Phases {
		patterns = append(patterns, MarkerPhase+string(p)+"===")
	}

	for {
		phaseCtx, cancel := context.WithTimeout(ctx, phaseTimeout)
		match, err := c.Expect(phaseCtx, patterns...)
		cancel()

		if err != nil {
			if errors.Is(err, console.ErrConsoleClosed) {
				return fmt.Errorf("the guest stopped responding during installation; last output:\n%s", c.Tail(3000))
			}
			return fmt.Errorf("installation stalled: %w\nlast output:\n%s", err, c.Tail(3000))
		}

		switch match {
		case MarkerComplete:
			r.report(Progress{
				Phase: "", Index: len(Phases), Total: len(Phases),
				Message: "Installation complete",
			})
			return nil

		case MarkerFailed:
			// Give the guest a beat to print the failing line number.
			time.Sleep(500 * time.Millisecond)
			return fmt.Errorf("the guest reported an installation failure; last output:\n%s", c.Tail(3000))

		default:
			name := strings.TrimSuffix(strings.TrimPrefix(match, MarkerPhase), "===")
			phase := Phase(name)
			r.report(Progress{
				Phase:   phase,
				Index:   phaseIndex(phase),
				Total:   len(Phases),
				Message: phase.Label(),
			})
		}
	}
}

func phaseIndex(p Phase) int {
	for i, candidate := range Phases {
		if candidate == p {
			return i + 1
		}
	}
	return 0
}

func (r *Runner) report(p Progress) {
	if r.Hooks.OnProgress != nil {
		r.Hooks.OnProgress(p)
	}
}
