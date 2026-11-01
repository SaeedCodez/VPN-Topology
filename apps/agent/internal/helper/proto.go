// Package helper is the privileged half of the agent. The agent itself runs
// as an unprivileged user with no capabilities; the few reads that need root
// (nft counters, wg peers, docker state, local checks) are answered by
// `vpnmap-agent helper-serve`, started by systemd per connection on the unix
// socket /run/vpnmap-agent/helper.sock (Accept=yes, mode 0660 root:vpnmap-agent).
//
// The protocol is one JSON request line and one JSON response line:
//
//	→ {"op":"wg-dump","args":["mullvad","wg0"]}
//	← {"ok":true,"data":{...}}   or   {"ok":false,"error":"..."}
//
// The helper only knows a fixed list of read-only operations, and every
// argument must match an entry of the root-owned agent.yaml. The agent can
// therefore never make the helper do more than the owner configured.
package helper

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Operations. Anything else is rejected.
const (
	OpNftCounters = "nft-counters"
	OpWGDump      = "wg-dump"
	OpDockerPS    = "docker-ps"
	OpCheck       = "check"
)

// Limits.
const (
	MaxRequest          = 4 << 10 // bytes, including the newline
	MaxResponse         = 8 << 20 // wg dumps with thousands of peers stay below this
	OverallTimeout      = 20 * time.Second
	DefaultCheckTimeout = 10 * time.Second
	MaxCheckTimeout     = 30 * time.Second
	MaxCheckOutput      = 64 << 10
)

// DefaultSocket is where systemd listens for helper connections.
const DefaultSocket = "/run/vpnmap-agent/helper.sock"

// Request is what the agent sends.
type Request struct {
	Op   string   `json:"op"`
	Args []string `json:"args,omitempty"`
}

// Response is what the helper answers.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// ErrTooLarge is returned for requests over MaxRequest bytes.
var ErrTooLarge = errors.New("request too large")

// ReadRequest reads one newline-terminated request of at most MaxRequest
// bytes. A request without newline is accepted at EOF (the client may have
// shut down its write side).
func ReadRequest(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, MaxRequest+1))
	var buf bytes.Buffer
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			if buf.Len() > MaxRequest {
				return nil, ErrTooLarge
			}
			if buf.Len() == 0 {
				return nil, fmt.Errorf("empty request")
			}
			return buf.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf.Bytes(), nil
		}
		buf.WriteByte(b)
		if buf.Len() >= MaxRequest {
			return nil, ErrTooLarge
		}
	}
}

// DecodeRequest parses a request strictly: one JSON object, known fields only.
func DecodeRequest(line []byte) (Request, error) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fmt.Errorf("malformed request")
	}
	if dec.More() {
		return req, fmt.Errorf("malformed request: trailing data")
	}
	if rest, _ := io.ReadAll(dec.Buffered()); len(bytes.TrimSpace(rest)) > 0 {
		return req, fmt.Errorf("malformed request: trailing data")
	}
	return req, nil
}

func fail(format string, a ...any) Response {
	return Response{OK: false, Error: fmt.Sprintf(format, a...)}
}

// WriteResponse writes one response line.
func WriteResponse(w io.Writer, resp Response) error {
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(fail("internal error: cannot encode response"))
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
