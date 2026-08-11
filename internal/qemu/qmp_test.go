package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeQMP is an in-process stand-in for QEMU's control socket. It speaks just
// enough of the protocol to drive the client through its real code paths.
type fakeQMP struct {
	t    *testing.T
	ln   net.Listener
	path string

	// Everything the serving goroutine touches is guarded, because tests
	// change the fake's behaviour while it is running.
	mu       sync.Mutex
	received []string
	behave   behaviour
}

// behaviour configures how the fake responds. Tests pass it up front so the
// serving goroutine never observes a half-configured fake.
type behaviour struct {
	skipGreeting bool
	failCommand  string
	emitEvent    bool
	closeOnQuit  bool
	running      bool
	status       string
}

func defaultBehaviour() behaviour {
	return behaviour{closeOnQuit: true, running: true, status: "running"}
}

func newFakeQMP(t *testing.T, b behaviour) *fakeQMP {
	t.Helper()
	// Keep the socket path short; sun_path is only ~104 bytes.
	path := filepath.Join(t.TempDir(), "q.sock")

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on the fake QMP socket: %v", err)
	}
	f := &fakeQMP{t: t, ln: ln, path: path, behave: b}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

// setState updates the run state a live fake reports.
func (f *fakeQMP) setState(running bool, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.behave.running, f.behave.status = running, status
}

func (f *fakeQMP) snapshot() behaviour {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.behave
}

func (f *fakeQMP) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeQMP) handle(conn net.Conn) {
	defer conn.Close()
	enc := json.NewEncoder(conn)

	if !f.snapshot().skipGreeting {
		enc.Encode(map[string]any{
			"QMP": map[string]any{
				"version":      map[string]any{"qemu": map[string]int{"major": 11, "minor": 0, "micro": 3}},
				"capabilities": []string{},
			},
		})
	}

	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		var req struct {
			Execute string `json:"execute"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}

		f.mu.Lock()
		f.received = append(f.received, req.Execute)
		b := f.behave
		f.mu.Unlock()

		// Asynchronous events are interleaved with replies in real QEMU; the
		// client must skip past them to find its answer.
		if b.emitEvent && req.Execute != "qmp_capabilities" {
			enc.Encode(map[string]any{
				"event":     "RTC_CHANGE",
				"timestamp": map[string]int{"seconds": 1, "microseconds": 0},
			})
		}

		if req.Execute == b.failCommand {
			enc.Encode(map[string]any{
				"error": map[string]string{"class": "GenericError", "desc": "command not supported"},
			})
			continue
		}

		switch req.Execute {
		case "query-status":
			enc.Encode(map[string]any{
				"return": map[string]any{
					"running": b.running, "status": b.status, "singlestep": false,
				},
			})
		case "quit":
			if b.closeOnQuit {
				// Real QEMU tears down the socket as it exits, often before the
				// reply is flushed.
				return
			}
			enc.Encode(map[string]any{"return": map[string]any{}})
		default:
			enc.Encode(map[string]any{"return": map[string]any{}})
		}
	}
}

func (f *fakeQMP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.received...)
}

func TestDialQMPNegotiates(t *testing.T) {
	f := newFakeQMP(t, defaultBehaviour())

	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatalf("DialQMP: %v", err)
	}
	defer q.Close()

	got := f.commands()
	if len(got) == 0 || got[0] != "qmp_capabilities" {
		t.Errorf("first command was %v, want qmp_capabilities", got)
	}
}

func TestDialQMPMissingSocket(t *testing.T) {
	_, err := DialQMP(context.Background(), filepath.Join(t.TempDir(), "nope.sock"))
	if err == nil {
		t.Error("expected an error dialling a nonexistent socket")
	}
}

func TestDialQMPBadGreeting(t *testing.T) {
	f := newFakeQMP(t, behaviour{skipGreeting: true})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := DialQMP(ctx, f.path); err == nil {
		t.Error("expected an error when the greeting never arrives")
	}
}

func TestQueryStatus(t *testing.T) {
	f := newFakeQMP(t, defaultBehaviour())
	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	rs, err := q.QueryStatus()
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if !rs.Running || rs.Status != "running" {
		t.Errorf("QueryStatus = %+v, want a running machine", rs)
	}

	f.setState(false, "paused")
	rs, err = q.QueryStatus()
	if err != nil {
		t.Fatal(err)
	}
	if rs.Running || rs.Status != "paused" {
		t.Errorf("QueryStatus = %+v, want paused", rs)
	}
}

// Events arrive unsolicited and must not be mistaken for command replies.
func TestExecuteSkipsEvents(t *testing.T) {
	b := defaultBehaviour()
	b.emitEvent = true
	f := newFakeQMP(t, b)

	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	rs, err := q.QueryStatus()
	if err != nil {
		t.Fatalf("QueryStatus with interleaved events: %v", err)
	}
	if !rs.Running {
		t.Errorf("QueryStatus = %+v after an event, want the real reply", rs)
	}
}

func TestExecuteSurfacesErrors(t *testing.T) {
	b := defaultBehaviour()
	b.failCommand = "system_powerdown"
	f := newFakeQMP(t, b)

	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	err = q.Powerdown()
	if err == nil {
		t.Fatal("expected the QMP error to surface")
	}
	if !strings.Contains(err.Error(), "command not supported") {
		t.Errorf("error %q should carry QEMU's description", err)
	}
}

func TestPowerdownAndReset(t *testing.T) {
	f := newFakeQMP(t, defaultBehaviour())
	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if err := q.Powerdown(); err != nil {
		t.Errorf("Powerdown: %v", err)
	}
	if err := q.Reset(); err != nil {
		t.Errorf("Reset: %v", err)
	}

	got := strings.Join(f.commands(), ",")
	for _, want := range []string{"system_powerdown", "system_reset"} {
		if !strings.Contains(got, want) {
			t.Errorf("commands %q missing %q", got, want)
		}
	}
}

// QEMU usually closes the socket before acknowledging quit. That is success,
// not a failure, and must not be reported as an error.
func TestQuitToleratesSocketTeardown(t *testing.T) {
	b := defaultBehaviour()
	b.closeOnQuit = true
	f := newFakeQMP(t, b)

	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if err := q.Quit(); err != nil {
		t.Errorf("Quit returned %v; a socket teardown means it worked", err)
	}
}

func TestQuitWithReply(t *testing.T) {
	b := defaultBehaviour()
	b.closeOnQuit = false
	f := newFakeQMP(t, b)

	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	if err := q.Quit(); err != nil {
		t.Errorf("Quit: %v", err)
	}
}

func TestCloseIsSafe(t *testing.T) {
	var nilQMP *QMP
	if err := nilQMP.Close(); err != nil {
		t.Errorf("Close on a nil client should be a no-op, got %v", err)
	}

	f := newFakeQMP(t, defaultBehaviour())
	q, err := DialQMP(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestIsClosed(t *testing.T) {
	if isClosed(nil) {
		t.Error("nil is not a closed-connection error")
	}
	for _, msg := range []string{"EOF", "write: broken pipe", "read: connection reset by peer"} {
		if !isClosed(&stringError{msg}) {
			t.Errorf("isClosed(%q) = false, want true", msg)
		}
	}
	if isClosed(&stringError{"permission denied"}) {
		t.Error("an unrelated error was classified as a closed connection")
	}
}

type stringError struct{ s string }

func (e *stringError) Error() string { return e.s }
