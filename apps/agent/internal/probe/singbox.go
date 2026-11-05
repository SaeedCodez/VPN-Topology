package probe

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"vpnmap/pkg/redact"
	"vpnmap/pkg/sysexec"
)

// Process is one supervised `sing-box run` child.
type Process struct {
	Bin        string
	ConfigPath string
	WorkDir    string
	Port       int
	Env        []string // default sysexec.SafeEnv

	cmd    *exec.Cmd
	exited chan struct{}
	out    *lastLine
}

// Start launches sing-box and waits until its inbound accepts connections.
func (p *Process) Start(ctx context.Context) error {
	bin, err := sysexec.LookPath(p.Bin)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "run", "-c", p.ConfigPath, "-D", p.WorkDir)
	cmd.Env = p.Env
	if cmd.Env == nil {
		cmd.Env = sysexec.SafeEnv
	}
	cmd.Dir = p.WorkDir
	p.out = &lastLine{}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	cmd.SysProcAttr = childAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}
	p.cmd = cmd
	p.exited = make(chan struct{})
	go func() {
		cmd.Wait()
		close(p.exited)
	}()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-p.exited:
			return fmt.Errorf("sing-box exited at start: %s", p.LastOutput())
		case <-ctx.Done():
			p.Stop()
			return ctx.Err()
		default:
		}
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			p.Stop()
			return fmt.Errorf("sing-box did not open %s within 10s", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Running reports whether the child is still alive.
func (p *Process) Running() bool {
	if p.exited == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// Stop terminates the child's process group: SIGTERM, then SIGKILL after 3s.
func (p *Process) Stop() {
	if p.cmd == nil || p.cmd.Process == nil || !p.Running() {
		return
	}
	pgid := p.cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
		syscall.Kill(-pgid, syscall.SIGKILL)
		<-p.exited
	}
}

// LastOutput is sing-box's last output line, redacted (it may echo parts of
// the outbound on configuration errors).
func (p *Process) LastOutput() string {
	if p.out == nil {
		return ""
	}
	return redact.String(p.out.String())
}

// freePort asks the kernel for an unused loopback port. There is a small
// race until sing-box binds it; a failed start is simply retried.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// lastLine keeps only the most recent non-empty output line (max 300 bytes).
type lastLine struct {
	mu   sync.Mutex
	line []byte
	cur  []byte
}

func (l *lastLine) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range b {
		if c == '\n' {
			if len(l.cur) > 0 {
				l.line = append(l.line[:0], l.cur...)
			}
			l.cur = l.cur[:0]
			continue
		}
		if len(l.cur) < 300 {
			l.cur = append(l.cur, c)
		}
	}
	return len(b), nil
}

func (l *lastLine) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.cur) > 0 {
		return string(l.cur)
	}
	return string(l.line)
}

// removeQuietly deletes a generated config (it contains credentials).
func removeQuietly(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
