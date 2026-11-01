package helper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"syscall"
	"time"
)

// ErrUnavailable means the helper socket is missing or nobody listens on it
// (the socket unit is not installed or not started). Collectors report it as
// a CollectorError; the agent keeps running.
var ErrUnavailable = errors.New("helper not available")

// Client talks to the helper from the unprivileged agent.
type Client struct {
	Socket string
	// Timeout applies when ctx has no deadline (default OverallTimeout+5s).
	Timeout time.Duration
}

// Call sends one request and decodes the response data into out.
func (c *Client) Call(ctx context.Context, op string, args []string, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		t := c.Timeout
		if t <= 0 {
			t = OverallTimeout + 5*time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
			return ErrUnavailable
		case errors.Is(err, fs.ErrPermission):
			return fmt.Errorf("%w: permission denied on %s (is the agent in group vpnmap-agent?)", ErrUnavailable, c.Socket)
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()

	req, err := json.Marshal(Request{Op: op, Args: args})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("helper: send: %w", err)
	}
	line, err := bufio.NewReader(io.LimitReader(conn, MaxResponse+1)).ReadBytes('\n')
	if err != nil && !(err == io.EOF && len(line) > 0) {
		if len(line) > MaxResponse {
			return fmt.Errorf("helper: response too large")
		}
		return fmt.Errorf("helper: receive: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("helper: malformed response")
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unknown error"
		}
		return fmt.Errorf("helper: %s", resp.Error)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("helper: unexpected %s data: %w", op, err)
	}
	return nil
}
