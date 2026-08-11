package console

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGuest is a unix socket that plays the part of a VM's serial console.
type fakeGuest struct {
	path string

	mu       sync.Mutex
	received []byte
	conn     net.Conn
	ready    chan struct{}
}

func newFakeGuest(t *testing.T) *fakeGuest {
	t.Helper()
	// sun_path is ~104 bytes and t.TempDir() embeds the test name, which can
	// overflow it on its own, so the socket gets a deliberately short home.
	dir, err := os.MkdirTemp("/tmp", "mc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on %s: %v", path, err)
	}
	g := &fakeGuest{path: path, ready: make(chan struct{})}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		g.mu.Lock()
		g.conn = conn
		g.mu.Unlock()
		close(g.ready)

		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				g.mu.Lock()
				g.received = append(g.received, buf[:n]...)
				g.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() { ln.Close() })
	return g
}

// say writes output as the guest would.
func (g *fakeGuest) say(t *testing.T, s string) {
	t.Helper()
	select {
	case <-g.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing connected to the fake guest")
	}
	g.mu.Lock()
	conn := g.conn
	g.mu.Unlock()
	if _, err := conn.Write([]byte(s)); err != nil {
		t.Fatalf("writing guest output: %v", err)
	}
}

func (g *fakeGuest) hangUp(t *testing.T) {
	t.Helper()
	<-g.ready
	g.mu.Lock()
	conn := g.conn
	g.mu.Unlock()
	conn.Close()
}

func (g *fakeGuest) typed() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return string(g.received)
}

func dialGuest(t *testing.T, g *fakeGuest) *Console {
	t.Helper()
	c, err := Dial(context.Background(), g.path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello", "hello"},
		{"colour", "\x1b[0;32mgreen\x1b[m", "green"},
		{"cursor moves", "\x1b[2J\x1b[01;01Hmenu", "menu"},
		{"osc title", "\x1b]0;a title\x07text", "text"},
		{"charset", "\x1b(Btext", "text"},
		{"bel and nul", "a\x07b\x00c", "abc"},
		// GRUB paints with cursor positioning between every character.
		{"grub paint", "\x1b[05;03Hg\x1b[05;04Hr\x1b[05;05Hu\x1b[05;06Hb", "grub"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripANSI(tc.in); got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Carriage returns carry meaning — pacman redraws its progress bars with them
// — so they must survive stripping for a consumer to render output sensibly.
func TestStripANSIPreservesCarriageReturns(t *testing.T) {
	got := StripANSI("downloading\rdone")
	if got != "downloading\rdone" {
		t.Errorf("StripANSI = %q, want carriage returns preserved", got)
	}
}

// Matching works by substring across the whole buffer, so a pattern is found
// regardless of the carriage returns around it.
func TestExpectMatchesAcrossCarriageReturns(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "  0%\r 50%\r100%\r\n===MARCH:COMPLETE===\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Expect(ctx, "===MARCH:COMPLETE==="); err != nil {
		t.Errorf("progress redraws hid the marker: %v", err)
	}
}

func TestExpectMatches(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	go func() {
		time.Sleep(50 * time.Millisecond)
		g.say(t, "booting...\nGNU GRUB version 2\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := c.Expect(ctx, "GNU GRUB")
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	if got != "GNU GRUB" {
		t.Errorf("matched %q", got)
	}
}

// Output that arrives before Expect is called must still be matched, or a fast
// guest could race past a prompt.
func TestExpectSeesEarlierOutput(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "the guest said something\ngrub> ")
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Expect(ctx, "grub>"); err != nil {
		t.Fatalf("Expect did not see output that arrived before the call: %v", err)
	}
}

// A match consumes the text, so waiting for the same prompt twice waits for a
// genuinely new occurrence rather than rematching history.
func TestExpectConsumesMatch(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "prompt> ")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Expect(ctx, "prompt>"); err != nil {
		t.Fatal(err)
	}

	// The same pattern must not match again without new output.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer shortCancel()
	if _, err := c.Expect(shortCtx, "prompt>"); err == nil {
		t.Error("Expect rematched already-consumed output")
	}

	g.say(t, "prompt> ")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := c.Expect(ctx2, "prompt>"); err != nil {
		t.Errorf("Expect did not see the second occurrence: %v", err)
	}
}

// With several alternatives, the one appearing earliest in the stream wins —
// otherwise a failure marker could be reported as a success.
func TestExpectPrefersEarliestMatch(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "===MARCH:FAILED===\nlater ===MARCH:COMPLETE===\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := c.Expect(ctx, "===MARCH:COMPLETE===", "===MARCH:FAILED===")
	if err != nil {
		t.Fatal(err)
	}
	if got != "===MARCH:FAILED===" {
		t.Errorf("matched %q, want the marker that appeared first", got)
	}
}

func TestExpectMatchesThroughANSI(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	// A phase marker painted with colour codes must still be found.
	g.say(t, "\x1b[1;32m===MARCH:PHASE:\x1b[0mbase===\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Expect(ctx, "===MARCH:PHASE:base==="); err != nil {
		t.Errorf("Expect could not match through escape sequences: %v", err)
	}
}

func TestExpectTimeout(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := c.Expect(ctx, "never appears"); err == nil {
		t.Error("expected a timeout")
	}
}

func TestExpectNoPatterns(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)
	if _, err := c.Expect(context.Background()); err == nil {
		t.Error("Expect with no patterns should be an error")
	}
}

// A guest that goes away mid-install must surface promptly rather than
// blocking until the caller's timeout.
func TestExpectDetectsHangUp(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "working\n")
	time.Sleep(100 * time.Millisecond)
	g.hangUp(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := c.Expect(ctx, "never")
	if err == nil {
		t.Fatal("expected an error after the guest hung up")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %s to notice the console closed", time.Since(start))
	}
}

func TestSendAndSendLine(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	if err := c.Send("c"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendLine("boot"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	got := g.typed()
	// A terminal sends a carriage return for enter, not a newline.
	if got != "cboot\r" {
		t.Errorf("guest received %q, want %q", got, "cboot\r")
	}
}

func TestSendSlow(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.SendSlow(ctx, "abc", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := g.typed(); got != "abc" {
		t.Errorf("guest received %q, want abc", got)
	}
}

func TestSendAfterClose(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Send("x"); err == nil {
		t.Error("Send after Close should fail")
	}
	// Close is idempotent.
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestOnOutputStreams(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	var mu sync.Mutex
	var seen strings.Builder
	c.OnOutput(func(s string) {
		mu.Lock()
		defer mu.Unlock()
		seen.WriteString(s)
	})

	g.say(t, "\x1b[32minstalling\x1b[m\n")
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	got := seen.String()
	mu.Unlock()

	if !strings.Contains(got, "installing") {
		t.Errorf("OnOutput received %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("OnOutput received unstripped escape sequences: %q", got)
	}
}

func TestTailReturnsRecentOutput(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, strings.Repeat("x", 200)+"THE-END")
	time.Sleep(200 * time.Millisecond)

	tail := c.Tail(20)
	if !strings.Contains(tail, "THE-END") {
		t.Errorf("Tail = %q, want the most recent output", tail)
	}
	if len(tail) > 25 {
		t.Errorf("Tail returned %d bytes, want it bounded", len(tail))
	}

	// Tail must survive Expect having consumed the buffer.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = c.Expect(ctx, "THE-END")
	if !strings.Contains(c.Tail(50), "THE-END") {
		t.Error("Tail lost output that Expect consumed")
	}
}

func TestDialMissingSocket(t *testing.T) {
	if _, err := Dial(context.Background(), filepath.Join(t.TempDir(), "nope.sock")); err == nil {
		t.Error("expected an error dialling a socket that does not exist")
	}
}

func TestSnapshot(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "hello world")
	time.Sleep(200 * time.Millisecond)
	if !strings.Contains(c.Snapshot(), "hello world") {
		t.Errorf("Snapshot = %q", c.Snapshot())
	}
}

// ExpectCapture is how callers read back the output of a command they sent, so
// it must return exactly what arrived between the two markers.
func TestExpectCapture(t *testing.T) {
	g := newFakeGuest(t)
	c := dialGuest(t, g)

	g.say(t, "noise before\nM-BEGIN\nactive\nM-END\ntrailing\n")
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.Expect(ctx, "M-BEGIN"); err != nil {
		t.Fatal(err)
	}
	matched, out, err := c.ExpectCapture(ctx, "M-END")
	if err != nil {
		t.Fatal(err)
	}
	if matched != "M-END" {
		t.Errorf("matched %q", matched)
	}
	if strings.TrimSpace(out) != "active" {
		t.Errorf("captured %q, want just the command output", out)
	}
	// Content before the first marker must not leak into the capture.
	if strings.Contains(out, "noise") {
		t.Errorf("capture included earlier output: %q", out)
	}
}
