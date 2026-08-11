package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"
)

// QMP is a minimal client for QEMU's Machine Protocol. It implements just the
// handful of commands march needs: querying run state and asking the machine
// to stop.
type QMP struct {
	conn net.Conn
	dec  *json.Decoder
	enc  *json.Encoder
}

// qmpResponse is the envelope QEMU returns for every command.
type qmpResponse struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
	// Event is set on asynchronous notifications, which are interleaved with
	// command replies and must be skipped while waiting for one.
	Event string `json:"event"`
}

// DialQMP connects to a VM's QMP socket and completes the capability
// negotiation QEMU requires before it will accept any other command.
func DialQMP(ctx context.Context, socket string) (*QMP, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to QMP socket: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	q := &QMP{
		conn: conn,
		dec:  json.NewDecoder(bufio.NewReader(conn)),
		enc:  json.NewEncoder(conn),
	}

	// QEMU opens with a greeting banner before anything else.
	var greeting map[string]json.RawMessage
	if err := q.dec.Decode(&greeting); err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading QMP greeting: %w", err)
	}
	if _, ok := greeting["QMP"]; !ok {
		conn.Close()
		return nil, errors.New("unexpected QMP greeting")
	}
	if _, err := q.Execute("qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("negotiating QMP capabilities: %w", err)
	}
	return q, nil
}

// Close releases the connection.
func (q *QMP) Close() error {
	if q == nil || q.conn == nil {
		return nil
	}
	return q.conn.Close()
}

// Execute issues a command and returns its raw result.
func (q *QMP) Execute(command string, arguments any) (json.RawMessage, error) {
	req := map[string]any{"execute": command}
	if arguments != nil {
		req["arguments"] = arguments
	}
	if err := q.enc.Encode(req); err != nil {
		return nil, fmt.Errorf("sending %s: %w", command, err)
	}

	// Events can arrive at any time; keep reading until the command's reply.
	for {
		var resp qmpResponse
		if err := q.dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("reading reply to %s: %w", command, err)
		}
		if resp.Event != "" {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("%s failed: %s", command, resp.Error.Desc)
		}
		return resp.Return, nil
	}
}

// RunState mirrors QEMU's notion of what the machine is doing.
type RunState struct {
	Running    bool   `json:"running"`
	Status     string `json:"status"`
	SingleStep bool   `json:"singlestep"`
}

// QueryStatus reports the guest's current run state.
func (q *QMP) QueryStatus() (RunState, error) {
	var rs RunState
	raw, err := q.Execute("query-status", nil)
	if err != nil {
		return rs, err
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		return rs, fmt.Errorf("parsing run state: %w", err)
	}
	return rs, nil
}

// Powerdown presses the virtual power button. The guest OS sees an ACPI event
// and shuts down cleanly; it may also ignore it, which is why callers pair this
// with a timeout and a fallback to Quit.
func (q *QMP) Powerdown() error {
	_, err := q.Execute("system_powerdown", nil)
	return err
}

// Reset performs a hard reset, equivalent to the reset button.
func (q *QMP) Reset() error {
	_, err := q.Execute("system_reset", nil)
	return err
}

// Quit terminates QEMU immediately without involving the guest. This is the
// equivalent of pulling the plug and can leave the guest filesystem dirty.
func (q *QMP) Quit() error {
	_, err := q.Execute("quit", nil)
	// QEMU frequently closes the socket before the reply is flushed, so a
	// connection teardown here means the command took effect.
	if err != nil && isClosed(err) {
		return nil
	}
	return err
}

// isClosed reports whether an error just means the peer went away. QEMU tears
// the socket down as it exits, so a quit that "fails" this way succeeded.
func isClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"EOF", "broken pipe", "connection reset", "use of closed network connection"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
