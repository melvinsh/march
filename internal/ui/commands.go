package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/melvinsh/march/internal/config"
	"github.com/melvinsh/march/internal/host"
	"github.com/melvinsh/march/internal/image"
	"github.com/melvinsh/march/internal/install"
	"github.com/melvinsh/march/internal/qemu"
	"github.com/melvinsh/march/internal/vm"
)

// Messages exchanged between commands and the root model.
type (
	capsMsg struct {
		caps *host.Caps
		err  error
	}

	vmsMsg struct {
		vms    []config.VM
		status map[string]vm.Status
		err    error
	}

	releaseMsg struct {
		release image.Release
		err     error
	}

	installProgressMsg install.Progress

	installOutputMsg string

	installDoneMsg struct {
		name string
		err  error
	}

	downloadProgressMsg image.Progress

	downloadDoneMsg struct {
		path string
		err  error
	}

	vmCreatedMsg struct {
		name string
		err  error
	}

	opDoneMsg struct {
		verb string
		name string
		err  error
	}

	// statusTickMsg drives periodic refresh of the VM list while the app is
	// on the list screen.
	statusTickMsg time.Time

	// flashExpiredMsg clears a transient status message.
	flashExpiredMsg struct{ id int }
)

const statusPollInterval = 3 * time.Second

func probeHostCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		caps, err := host.Probe(ctx)
		return capsMsg{caps: caps, err: err}
	}
}

func loadVMsCmd(mgr *vm.Manager) tea.Cmd {
	return func() tea.Msg {
		vms, err := mgr.Store.List()
		if err != nil {
			return vmsMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		status, err := mgr.StatusAll(ctx)
		return vmsMsg{vms: vms, status: status, err: err}
	}
}

func fetchReleaseCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r, err := image.Resolve(ctx, nil)
		return releaseMsg{release: r, err: err}
	}
}

// installCmd runs the unattended installation, streaming progress and console
// output back through the program's message loop.
func installCmd(p *tea.Program, mgr *vm.Manager, name string, profile install.Profile, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		hooks := install.Hooks{
			OnProgress: func(pr install.Progress) {
				if p != nil {
					p.Send(installProgressMsg(pr))
				}
			},
			OnOutput: func(s string) {
				if p != nil {
					p.Send(installOutputMsg(s))
				}
			},
		}
		err := mgr.Install(ctx, name, profile, hooks)
		return installDoneMsg{name: name, err: err}
	}
}

// downloadCmd streams an ISO to the cache. Progress is delivered through the
// program's message loop rather than a callback, so rendering stays on the
// Bubble Tea goroutine.
func downloadCmd(p *tea.Program, dl *image.Downloader, r image.Release, ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		path, err := dl.Fetch(ctx, r, func(pr image.Progress) {
			if p != nil {
				p.Send(downloadProgressMsg(pr))
			}
		})
		return downloadDoneMsg{path: path, err: err}
	}
}

func createVMCmd(mgr *vm.Manager, spec config.VM, isoPath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		v, err := mgr.Create(ctx, vm.CreateOptions{Spec: spec, ISOPath: isoPath})
		return vmCreatedMsg{name: v.Name, err: err}
	}
}

func startVMCmd(mgr *vm.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := mgr.Start(ctx, name, qemu.BuildOptions{})
		return opDoneMsg{verb: "started", name: name, err: err}
	}
}

func stopVMCmd(mgr *vm.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		// The guest gets a generous window to flush and unmount before march
		// resorts to terminating it.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		err := mgr.Stop(ctx, name, 90*time.Second)
		return opDoneMsg{verb: "stopped", name: name, err: err}
	}
}

func restartVMCmd(mgr *vm.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		err := mgr.Restart(ctx, name, qemu.BuildOptions{})
		return opDoneMsg{verb: "restarted", name: name, err: err}
	}
}

func deleteVMCmd(mgr *vm.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := mgr.Delete(ctx, name)
		return opDoneMsg{verb: "deleted", name: name, err: err}
	}
}

func statusTickCmd() tea.Cmd {
	return tea.Tick(statusPollInterval, func(t time.Time) tea.Msg { return statusTickMsg(t) })
}

func flashExpireCmd(id int) tea.Cmd {
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashExpiredMsg{id: id} })
}
