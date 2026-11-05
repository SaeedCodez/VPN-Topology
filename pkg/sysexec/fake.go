package sysexec

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Fake is a Runner for tests: it answers from a table keyed by the command
// line ("nft -j list counters table inet vpnmap") and records every call.
type Fake struct {
	mu        sync.Mutex
	Responses map[string]FakeResponse
	Calls     []Cmd
}

// FakeResponse is the canned outcome of one command line.
type FakeResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error // returned as-is (e.g. a timeout)
}

// Key renders the lookup key of a command.
func Key(c Cmd) string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// Run implements Runner.
func (f *Fake) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
	r, ok := f.Responses[Key(c)]
	if !ok {
		return Result{}, fmt.Errorf("fake: unexpected command %q", Key(c))
	}
	res := Result{Stdout: []byte(r.Stdout), Stderr: []byte(r.Stderr), ExitCode: r.ExitCode}
	if r.Err != nil {
		return res, r.Err
	}
	if r.ExitCode != 0 {
		return res, &ExitError{Name: c.Name, Code: r.ExitCode, Stderr: FirstLine(res.Stderr)}
	}
	return res, nil
}
