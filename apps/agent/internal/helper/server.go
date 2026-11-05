package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"time"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/sysexec"
)

// Server answers helper requests. It runs as root, once per connection.
type Server struct {
	// Cfg is loaded from the root-owned agent.yaml by helper-serve itself;
	// nothing the agent sends can change it.
	Cfg    *agentcfg.Config
	Runner sysexec.Runner
	// TrustedUID must own the checks dir and check files (0 in production;
	// tests use their own uid).
	TrustedUID uint32
	Log        *slog.Logger
	// Now is used for timestamps (tests may fix it).
	Now func() time.Time
}

// Re-declared here (and not taken from agentcfg) so the helper's input
// validation does not silently change if the config package is relaxed.
var (
	reCheckID = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	reIface   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
)

// Validate checks a request against the fixed op list and the config.
// It is the security boundary of the helper: everything that passes here
// is something the owner explicitly listed in agent.yaml.
func (s *Server) Validate(req Request) error {
	if len(req.Args) > 4 {
		return fmt.Errorf("too many arguments")
	}
	for _, a := range req.Args {
		if len(a) > 128 {
			return fmt.Errorf("argument too long")
		}
	}
	switch req.Op {
	case OpNftCounters:
		if len(req.Args) != 0 {
			return fmt.Errorf("%s takes no arguments", req.Op)
		}
		if s.Cfg.Nft == nil {
			return fmt.Errorf("nft counters are not configured in agent.yaml")
		}
	case OpDockerPS:
		if len(req.Args) != 0 {
			return fmt.Errorf("%s takes no arguments", req.Op)
		}
		if len(s.Cfg.Containers) == 0 {
			return fmt.Errorf("no containers are configured in agent.yaml")
		}
	case OpWGDump:
		if len(req.Args) != 2 {
			return fmt.Errorf("%s needs [netns, iface]", req.Op)
		}
		netns, iface := req.Args[0], req.Args[1]
		if !reIface.MatchString(iface) || (netns != "" && !reIface.MatchString(netns)) {
			return fmt.Errorf("invalid interface or netns name")
		}
		if !s.wgConfigured(netns, iface) {
			return fmt.Errorf("wg %q/%q is not configured in agent.yaml", netns, iface)
		}
	case OpCheck:
		if len(req.Args) != 1 {
			return fmt.Errorf("%s needs [id]", req.Op)
		}
		id := req.Args[0]
		if !reCheckID.MatchString(id) {
			return fmt.Errorf("invalid check id")
		}
		if _, ok := s.check(id); !ok {
			return fmt.Errorf("check %q is not configured in agent.yaml", id)
		}
	default:
		return fmt.Errorf("unknown op")
	}
	return nil
}

func (s *Server) wgConfigured(netns, iface string) bool {
	for _, w := range s.Cfg.WG {
		if w.Netns == netns && w.Iface == iface {
			return true
		}
	}
	return false
}

func (s *Server) check(id string) (agentcfg.LocalCheck, bool) {
	for _, c := range s.Cfg.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return agentcfg.LocalCheck{}, false
}

// Deadline returns how long a request may take. Checks may run up to 30s,
// longer than the 20s default, so they get their own timeout plus a margin.
func (s *Server) Deadline(req Request) time.Duration {
	if req.Op == OpCheck && len(req.Args) == 1 {
		if c, ok := s.check(req.Args[0]); ok {
			if d := CheckTimeout(c) + 5*time.Second; d > OverallTimeout {
				return d
			}
		}
	}
	return OverallTimeout
}

// CheckTimeout is a check's configured timeout, defaulted and capped.
func CheckTimeout(c agentcfg.LocalCheck) time.Duration {
	d := c.Timeout.D()
	if d <= 0 {
		d = DefaultCheckTimeout
	}
	if d > MaxCheckTimeout {
		d = MaxCheckTimeout
	}
	return d
}

// Handle decodes, validates and executes one request line.
func (s *Server) Handle(ctx context.Context, line []byte) Response {
	req, err := DecodeRequest(line)
	if err != nil {
		return fail("%v", err)
	}
	if err := s.Validate(req); err != nil {
		s.logf("rejected request", "op", truncate(req.Op, 32), "reason", err.Error())
		return fail("%v", err)
	}
	var data any
	switch req.Op {
	case OpNftCounters:
		data, err = s.nftCounters(ctx)
	case OpWGDump:
		data, err = s.wgDump(ctx, req.Args[0], req.Args[1])
	case OpDockerPS:
		data, err = s.dockerPS(ctx)
	case OpCheck:
		c, _ := s.check(req.Args[0])
		data, err = s.runCheck(ctx, c)
	}
	if err != nil {
		return fail("%s: %v", req.Op, err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fail("%s: cannot encode result", req.Op)
	}
	return Response{OK: true, Data: raw}
}

// ServeConn reads one request from r, answers on w and returns. The whole
// exchange is bounded by OverallTimeout (or a check's own deadline).
func (s *Server) ServeConn(ctx context.Context, r io.Reader, w io.Writer) error {
	type read struct {
		line []byte
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		l, err := ReadRequest(r)
		ch <- read{l, err}
	}()
	readCtx, cancel := context.WithTimeout(ctx, OverallTimeout)
	defer cancel()
	var resp Response
	select {
	case <-readCtx.Done():
		resp = fail("timeout waiting for request")
	case rd := <-ch:
		if rd.err != nil {
			resp = fail("%v", rd.err)
			break
		}
		req, _ := DecodeRequest(rd.line) // only to size the deadline
		opCtx, cancel := context.WithTimeout(ctx, s.Deadline(req))
		defer cancel()
		resp = s.Handle(opCtx, rd.line)
	}
	return WriteResponse(w, resp)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, args...)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
