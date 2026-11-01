// Package sysexec runs the few external programs the agent and helper use
// (systemctl, nft, wg, ip, docker, local checks) with a fixed environment, a
// timeout that kills the whole process group, and bounded output. Callers go
// through the Runner interface so tests never execute real system tools.
package sysexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// SafePATH is the only PATH child processes see. It contains root-owned
// system directories only, so a writable directory can never shadow nft or wg.
const SafePATH = "/usr/sbin:/usr/bin:/sbin:/bin"

// SafeEnv is the complete environment of child processes.
var SafeEnv = []string{"PATH=" + SafePATH, "LANG=C", "LC_ALL=C"}

// searchDirs is where programs are looked up (SafePATH plus /usr/local, where
// sing-box and vpnmap-agent itself are installed; both are root-owned).
var searchDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

const (
	defaultTimeout   = 10 * time.Second
	defaultMaxOutput = 1 << 20
	maxStderr        = 4 << 10
)

// Cmd describes one program execution.
type Cmd struct {
	// Name is an absolute path or a bare program name looked up in searchDirs
	// (never in the caller's $PATH).
	Name  string
	Args  []string
	Stdin []byte
	// Env replaces SafeEnv when non-nil.
	Env []string
	// Dir is the working directory ("/" when empty).
	Dir string
	// Timeout bounds the run (default 10s). The process group is killed.
	Timeout time.Duration
	// MaxOutput caps stdout (default 1 MiB). Extra output is discarded and
	// Result.Truncated is set.
	MaxOutput int
}

// Result is what a finished command produced.
type Result struct {
	Stdout    []byte
	Stderr    []byte // first 4 KiB
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// ExitError is returned when the program ran but exited non-zero.
type ExitError struct {
	Name   string
	Code   int
	Stderr string // first line, for operator-facing messages
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: exit status %d: %s", e.Name, e.Code, e.Stderr)
	}
	return fmt.Sprintf("%s: exit status %d", e.Name, e.Code)
}

// ErrTimeout is returned (wrapped) when a command exceeded its timeout.
var ErrTimeout = errors.New("timed out")

// Runner executes commands. Exec is the real implementation.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// Exec runs real processes.
type Exec struct{}

// LookPath finds a program in the fixed system directories.
func LookPath(name string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			return "", fmt.Errorf("%s: relative program paths are not allowed", name)
		}
		return name, nil
	}
	for _, d := range searchDirs {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not installed (looked in %s)", name, strings.Join(searchDirs, ":"))
}

// Run implements Runner.
func (Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	var res Result
	path, err := LookPath(c.Name)
	if err != nil {
		return res, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	max := c.MaxOutput
	if max <= 0 {
		max = defaultMaxOutput
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, c.Args...)
	cmd.Env = SafeEnv
	if c.Env != nil {
		cmd.Env = c.Env
	}
	cmd.Dir = c.Dir
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	} // nil Stdin means /dev/null
	stdout := &limitedBuffer{max: max}
	stderr := &limitedBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Own process group, so a timeout also kills grandchildren (a check
	// script's docker exec, `ip netns exec` children, ...).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Don't wait forever for pipes held open by an escaped grandchild.
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	err = cmd.Run()
	res.Duration = time.Since(start)
	res.Stdout, res.Stderr, res.Truncated = stdout.Bytes(), stderr.Bytes(), stdout.truncated
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	name := filepath.Base(path)
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s: %w after %s", name, ErrTimeout, timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return res, &ExitError{Name: name, Code: res.ExitCode, Stderr: FirstLine(res.Stderr)}
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", name, err)
	}
	return res, nil
}

// FirstLine returns the first non-empty line of b, trimmed to 200 bytes.
func FirstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > 200 {
			l = l[:200] + "..."
		}
		return strings.ToValidUTF8(l, "?")
	}
	return ""
}

// limitedBuffer keeps the first max bytes and silently drops the rest, while
// still accepting writes so the child never blocks on a full pipe.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	room := l.max - l.buf.Len()
	if room <= 0 {
		l.truncated = l.truncated || len(p) > 0
		return len(p), nil
	}
	if len(p) > room {
		l.buf.Write(p[:room])
		l.truncated = true
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) Bytes() []byte { return l.buf.Bytes() }
