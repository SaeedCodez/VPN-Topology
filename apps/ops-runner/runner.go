package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vpnmap/pkg/ops"
	"vpnmap/pkg/redact"
	"vpnmap/pkg/sysexec"
)

// Response is the single JSON object the runner writes to stdout.
type Response struct {
	OK         bool   `json:"ok"`
	Node       string `json:"node,omitempty"`
	ConfigHash string `json:"config_hash,omitempty"`
	Version    string `json:"version"`
	Type       string `json:"type,omitempty"`
	Req        string `json:"req,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	// CooldownLeftS is set when a change was refused because of its cooldown.
	CooldownLeftS int `json:"cooldown_left_s,omitempty"`
}

// Runner executes allowed requests. Every dependency is injectable so the
// tests never touch the real system.
type Runner struct {
	Cfg        *ops.RunnerConfig
	Exec       sysexec.Runner
	ScriptDir  string
	StateDir   string
	AuditPath  string
	Now        func() time.Time
	Dial       func(ctx context.Context, network, addr string) (net.Conn, error)
	TrustedUID uint32
	// CheckTrust verifies a script before it runs (root-owned, not writable
	// by others). Tests replace it.
	CheckTrust func(path string) error
	// Peer is the SSH client address (from serve), for the audit log only.
	Peer string
}

const tailWindow = 256 << 10

// Handle validates and runs one request line. It never panics on input and
// always returns a Response (errors included) plus an audit record.
func (r *Runner) Handle(ctx context.Context, line string) Response {
	start := r.Now()
	resp := Response{Node: r.Cfg.Node, ConfigHash: r.Cfg.Hash(), Version: version}
	req, err := ops.ParseRequest(line)
	if err == nil {
		resp.Type, resp.Req = req.Type, req.ID
		var a *ops.Allowed
		a, err = r.Cfg.Allow(req)
		if err == nil {
			err = r.run(ctx, a, &resp)
		}
	}
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
	} else {
		resp.OK = true
	}
	resp.Output = redact.String(strings.ToValidUTF8(resp.Output, "?"))
	if len(resp.Output) > ops.MaxOutput {
		resp.Output, resp.Truncated = resp.Output[:ops.MaxOutput], true
	}
	resp.DurationMs = r.Now().Sub(start).Milliseconds()
	r.audit(line, &resp)
	return resp
}

func (r *Runner) run(ctx context.Context, a *ops.Allowed, resp *Response) error {
	if a.Type.Kind == ops.Change {
		release, left, err := r.takeCooldown(a.Request.Key(), a.Cooldown)
		if err != nil {
			if left > 0 {
				resp.CooldownLeftS = int((left + time.Second - 1) / time.Second)
			}
			return err
		}
		defer release()
	}
	cmd := func(timeout time.Duration, name string, args ...string) error {
		res, err := r.Exec.Run(ctx, sysexec.Cmd{Name: name, Args: args, Timeout: timeout, MaxOutput: ops.MaxOutput})
		resp.ExitCode = res.ExitCode
		resp.Truncated = resp.Truncated || res.Truncated
		resp.Output += string(res.Stdout)
		if len(res.Stderr) > 0 {
			resp.Output += "\n[stderr]\n" + string(res.Stderr)
		}
		return err
	}
	switch a.Type.Name {
	case "ping":
		resp.Output = fmt.Sprintf("node=%s config=%s version=%s", r.Cfg.Node, r.Cfg.Hash(), version)
		return nil
	case "container.list":
		res, err := r.Exec.Run(ctx, sysexec.Cmd{Name: "docker", Args: []string{"ps", "-a", "--format", "{{.Names}}\t{{.Status}}"}, Timeout: 15 * time.Second})
		resp.ExitCode = res.ExitCode
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, c := range r.Cfg.Containers {
			want[c.Name] = true
		}
		var out []string
		for _, l := range strings.Split(string(res.Stdout), "\n") {
			name, _, _ := strings.Cut(l, "\t")
			if want[name] {
				out = append(out, l)
				delete(want, name)
			}
		}
		for _, n := range sortedSet(want) {
			out = append(out, n+"\t(not found)")
		}
		resp.Output = strings.Join(out, "\n")
		return nil
	case "container.stats":
		return cmd(20*time.Second, "docker", "stats", "--no-stream", "--format", "{{.Name}}\tcpu={{.CPUPerc}}\tmem={{.MemUsage}}\tpids={{.PIDs}}", a.Container.Name)
	case "container.logs":
		return cmd(20*time.Second, "docker", "logs", "--since", a.Request.Args["since"], "--tail", a.Request.Args["tail"], a.Container.Name)
	case "container.restart":
		return cmd(90*time.Second, "docker", "restart", "--time", "20", a.Container.Name)
	case "unit.status":
		return cmd(10*time.Second, "systemctl", "show", a.Unit.Name, "--no-pager",
			"--property=Id,ActiveState,SubState,Result,ActiveEnterTimestamp,NRestarts,MainPID")
	case "unit.restart":
		return cmd(90*time.Second, "systemctl", "restart", a.Unit.Name)
	case "log.tail":
		n, _ := strconv.Atoi(a.Request.Args["lines"])
		out, err := tailFile(a.Log.Path, n)
		resp.Output = out
		return err
	case "disk.usage":
		return cmd(10*time.Second, "df", "-P", "-h", "/")
	case "tcp.check":
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		t0 := r.Now()
		c, err := r.Dial(dctx, "tcp", a.Target)
		if err != nil {
			resp.Output = fmt.Sprintf("%s CLOSED: %v", a.Target, err)
			return nil // the check ran; the result is in the output
		}
		_ = c.Close()
		resp.Output = fmt.Sprintf("%s OPEN in %dms", a.Target, r.Now().Sub(t0).Milliseconds())
		return nil
	case "conns.count":
		p := ":" + strconv.Itoa(a.Port)
		var parts []string
		for _, side := range []string{"sport", "dport"} {
			res, err := r.Exec.Run(ctx, sysexec.Cmd{Name: "ss", Args: []string{"-Htn", "state", "established", side, "=", p}, Timeout: 10 * time.Second})
			if err != nil {
				return err
			}
			parts = append(parts, fmt.Sprintf("%s=%d", side, countLines(res.Stdout)))
		}
		resp.Output = fmt.Sprintf("established on port %d: %s", a.Port, strings.Join(parts, " "))
		return nil
	case "wg.handshakes":
		out, err := r.wgSummary(ctx, a.WG)
		resp.Output = out
		return err
	case "script.read", "script.change":
		path := filepath.Join(r.ScriptDir, a.Script.ID)
		if err := r.CheckTrust(path); err != nil {
			return fmt.Errorf("script %s is not installed safely: %v", a.Script.ID, err)
		}
		var args []string
		for _, kv := range a.ScriptArgs {
			args = append(args, "--"+kv[0], kv[1])
		}
		timeout := ops.DefaultScriptTimeout
		if a.Script.Timeout != "" {
			timeout, _ = time.ParseDuration(a.Script.Timeout)
		}
		return cmd(timeout, path, args...)
	}
	return fmt.Errorf("%s is not implemented by this runner (version %s)", a.Type.Name, version)
}

// wgSummary prints handshake age and bytes per peer, never the keys.
func (r *Runner) wgSummary(ctx context.Context, w *ops.WG) (string, error) {
	run := func(what string) ([]byte, error) {
		name, args := "wg", []string{"show", w.Iface, what}
		if w.Netns != "" {
			name, args = "ip", append([]string{"netns", "exec", w.Netns, "wg"}, args...)
		}
		res, err := r.Exec.Run(ctx, sysexec.Cmd{Name: name, Args: args, Timeout: 10 * time.Second})
		return res.Stdout, err
	}
	hs, err := run("latest-handshakes")
	if err != nil {
		return "", err
	}
	tr, err := run("transfer")
	if err != nil {
		return "", err
	}
	type peer struct {
		hs     int64
		rx, tx string
	}
	peers := map[string]*peer{}
	var order []string
	get := func(k string) *peer {
		if p, ok := peers[k]; ok {
			return p
		}
		peers[k] = &peer{}
		order = append(order, k)
		return peers[k]
	}
	for _, l := range strings.Split(string(hs), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			get(f[0]).hs = n
		}
	}
	for _, l := range strings.Split(string(tr), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			p := get(f[0])
			p.rx, p.tx = f[1], f[2]
		}
	}
	now := r.Now().Unix()
	var out []string
	for i, k := range order {
		p := peers[k]
		age := "never"
		if p.hs > 0 {
			age = (time.Duration(now-p.hs) * time.Second).String() + " ago"
		}
		out = append(out, fmt.Sprintf("%s peer%d handshake=%s rx=%s tx=%s", w.Iface, i+1, age, p.rx, p.tx))
	}
	if len(out) == 0 {
		return w.Iface + ": no peers", nil
	}
	return strings.Join(out, "\n"), nil
}

// tailFile returns the last n lines of a regular file without following a
// symlink, reading at most the last 256 KiB.
func tailFile(path string, n int) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	off := fi.Size() - tailWindow
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(f, tailWindow))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is partial
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

func countLines(b []byte) int {
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- cooldowns ---------------------------------------------------------

// errBusy: another change is running on this server.
var errBusy = errors.New("another change action is running on this server")

// takeCooldown holds an exclusive lock on the state file for the whole
// change (one change at a time per server) and refuses the action while its
// cooldown runs. The time is recorded before the action runs, so a crash or
// a timeout still counts.
func (r *Runner) takeCooldown(key string, cd time.Duration) (release func(), left time.Duration, err error) {
	if err := os.MkdirAll(r.StateDir, 0o700); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(filepath.Join(r.StateDir, "cooldowns.json"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, 0, errBusy
	}
	release = func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
	raw, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	state := map[string]int64{}
	_ = json.Unmarshal(raw, &state)
	now := r.Now()
	if last, ok := state[key]; ok && cd > 0 {
		if wait := time.Unix(last, 0).Add(cd).Sub(now); wait > 0 {
			release()
			return nil, wait, fmt.Errorf("cooldown: %s ran %s ago, wait %s", key, now.Sub(time.Unix(last, 0)).Round(time.Second), wait.Round(time.Second))
		}
	}
	// Forget entries older than a day.
	for k, v := range state {
		if now.Sub(time.Unix(v, 0)) > 24*time.Hour {
			delete(state, k)
		}
	}
	state[key] = now.Unix()
	out, _ := json.Marshal(state)
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(out, 0)
		_ = f.Sync()
	}
	return release, 0, nil
}

// --- audit --------------------------------------------------------------

type auditRecord struct {
	Time       string `json:"time"`
	Req        string `json:"req,omitempty"`
	Line       string `json:"line"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	OutBytes   int    `json:"out_bytes"`
	Peer       string `json:"peer,omitempty"`
	ConfigHash string `json:"config_hash"`
}

// audit appends one JSON line per call. A failing audit log never blocks a
// read, but the error is reported on stderr (the journal under sshd).
func (r *Runner) audit(line string, resp *Response) {
	if r.AuditPath == "" {
		return
	}
	rec := auditRecord{
		Time: r.Now().UTC().Format(time.RFC3339), Req: resp.Req, Line: printable(line), OK: resp.OK,
		ExitCode: resp.ExitCode, Error: resp.Error, DurationMs: resp.DurationMs, OutBytes: len(resp.Output),
		Peer: r.Peer, ConfigHash: resp.ConfigHash,
	}
	raw, _ := json.Marshal(rec)
	f, err := os.OpenFile(r.AuditPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vpnmap-ops: audit log: %v\n", err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

// printable keeps the audit line readable even for hostile input.
func printable(s string) string {
	if len(s) > ops.MaxLine {
		s = s[:ops.MaxLine] + "…"
	}
	return strings.Map(func(c rune) rune {
		if c < 0x20 || c > 0x7e {
			return '?'
		}
		return c
	}, s)
}
