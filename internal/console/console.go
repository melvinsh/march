// Package console drives a VM's serial console programmatically. It is the
// mechanism behind unattended installation: march watches the guest's output
// for known prompts and types the responses, exactly as a person would.
package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Console is an expect-style client for a QEMU serial socket.
//
// A background reader accumulates output into a buffer that Expect scans, so
// no output is lost between calls — a pattern that arrives while the caller is
// busy is still matched by the next Expect.
type Console struct {
	conn net.Conn

	mu       sync.Mutex
	buf      bytes.Buffer // ANSI-stripped output seen so far
	raw      bytes.Buffer // everything, for diagnostics
	readErr  error
	closed   bool
	onOutput func(string)
}

// Dial connects to a VM's serial console socket.
func Dial(ctx context.Context, socket string) (*Console, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("attaching to the serial console: %w", err)
	}
	c := &Console{conn: conn}
	go c.read()
	return c, nil
}

// OnOutput registers a callback invoked with each chunk of guest output, so a
// UI can stream the install log as it happens.
func (c *Console) OnOutput(fn func(string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOutput = fn
}

func (c *Console) read() {
	buf := make([]byte, 8192)
	for {
		// A long deadline detects a wedged guest without cutting short the
		// slow stretches of an install.
		_ = c.conn.SetReadDeadline(time.Now().Add(30 * time.Minute))
		n, err := c.conn.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			clean := StripANSI(string(chunk))

			c.mu.Lock()
			c.raw.Write(chunk)
			c.buf.WriteString(clean)
			fn := c.onOutput
			c.mu.Unlock()

			if fn != nil && clean != "" {
				fn(clean)
			}
		}
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
	}
}

// ansiRe matches CSI/OSC escape sequences. Bootloaders and installers paint
// with cursor positioning, which would otherwise wreck pattern matching.
var ansiRe = regexp.MustCompile(
	`\x1b\[[0-9;?]*[a-zA-Z]` + // CSI
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC
		`|\x1b[()][A-Za-z0-9]` + // charset selection
		`|\x1b[=><PM]` + // misc single-char escapes
		`|[\x00\x07\x0e\x0f]`) // NUL, BEL, shift-in/out

// StripANSI removes escape sequences and control noise, leaving text that can
// be matched against.
//
// Carriage returns are deliberately preserved. They carry meaning — a lone CR
// is a redraw of the current line, which is how pacman animates its progress
// bars — and a consumer that wants readable output needs to distinguish that
// from a real line break. Matching is unaffected either way, since patterns
// are found by substring search across the whole buffer.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ErrConsoleClosed is returned once the guest's console has gone away.
var ErrConsoleClosed = errors.New("serial console closed")

// Expect blocks until one of the patterns appears in the guest's output, and
// returns the one that matched. Matching is case-sensitive and substring-based
// against the whole accumulated output.
//
// The matched text and everything before it is consumed, so a later Expect for
// the same pattern waits for a fresh occurrence rather than matching history.
func (c *Console) Expect(ctx context.Context, patterns ...string) (string, error) {
	matched, _, err := c.ExpectCapture(ctx, patterns...)
	return matched, err
}

// ExpectCapture behaves like Expect and additionally returns everything the
// guest printed before the match. That is how a caller reads back the output
// of a command it just sent, without racing against output still arriving.
func (c *Console) ExpectCapture(ctx context.Context, patterns ...string) (matched, before string, err error) {
	if len(patterns) == 0 {
		return "", "", errors.New("Expect needs at least one pattern")
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for {
		c.mu.Lock()
		text := c.buf.String()
		best, bestAt := "", -1
		for _, p := range patterns {
			if i := strings.Index(text, p); i >= 0 && (bestAt < 0 || i < bestAt) {
				best, bestAt = p, i
			}
		}
		if bestAt >= 0 {
			// Consume through the match so it is not seen twice.
			preceding := text[:bestAt]
			rest := text[bestAt+len(best):]
			c.buf.Reset()
			c.buf.WriteString(rest)
			c.mu.Unlock()
			return best, preceding, nil
		}
		readErr := c.readErr
		closed := c.closed
		c.mu.Unlock()

		if readErr != nil || closed {
			return "", "", fmt.Errorf("waiting for %q: %w", patterns, ErrConsoleClosed)
		}

		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("waiting for one of %q: %w", patterns, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Send writes raw bytes to the guest.
func (c *Console) Send(s string) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrConsoleClosed
	}
	if _, err := c.conn.Write([]byte(s)); err != nil {
		return fmt.Errorf("writing to the serial console: %w", err)
	}
	return nil
}

// SendLine writes a line followed by a carriage return, which is what a
// terminal sends when the user presses enter.
func (c *Console) SendLine(s string) error { return c.Send(s + "\r") }

// SendSlow types a line character by character. Some early-boot consoles and
// bootloader editors drop input that arrives faster than they can process it.
func (c *Console) SendSlow(ctx context.Context, s string, perChar time.Duration) error {
	for _, r := range s {
		if err := c.Send(string(r)); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(perChar):
		}
	}
	return nil
}

// Snapshot returns the output accumulated but not yet consumed by Expect.
func (c *Console) Snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// Tail returns the last n bytes of everything the guest has printed, for
// embedding in an error message.
func (c *Console) Tail(n int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := StripANSI(c.raw.String())
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return strings.TrimSpace(s)
}

// Close disconnects from the console.
func (c *Console) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}
