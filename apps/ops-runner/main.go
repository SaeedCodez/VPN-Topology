// Command vpnmap-ops is the ops action runner on one server
// (docs/ops-actions.md). sshd starts it as the forced command of the hub's
// key:
//
//	restrict,command="/usr/local/bin/vpnmap-ops serve" ssh-ed25519 … vpnmap-hub
//
// `serve` runs as the unprivileged user vpnmap-ops. It reads the request
// from SSH_ORIGINAL_COMMAND, validates it against the root-owned
// /etc/vpnmap-ops/ops.yaml and hands it on stdin to
// `sudo -n /usr/local/bin/vpnmap-ops exec`, the only command sudoers allows.
// `exec` (root) checks its own files, validates the request again, applies
// the cooldown, runs a fixed argv (never a shell), writes the audit log and
// prints one JSON Response.
//
// It never takes a command, path or URL from the request; anything outside
// the closed types in pkg/ops and this server's config is refused.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"vpnmap/pkg/ops"
	"vpnmap/pkg/sysexec"
)

var version = "0.1.0-dev"

// Fixed locations. `exec` takes no flags (sudoers allows no arguments), so
// these cannot be redirected by the caller.
const (
	BinaryPath  = "/usr/local/bin/vpnmap-ops"
	ConfigPath  = "/etc/vpnmap-ops/ops.yaml"
	SudoersPath = "/etc/sudoers.d/vpnmap-ops"
	StateDir    = "/var/lib/vpnmap-ops"
	AuditPath   = "/var/log/vpnmap-ops.log"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: vpnmap-ops serve | exec | check [config] | version")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "check":
		path := ConfigPath
		if len(args) > 1 {
			path = args[1]
		}
		cfg, err := loadConfig(path)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		fmt.Fprintf(stdout, "ok node=%s config=%s\n", cfg.Node, cfg.Hash())
		return 0
	case "serve":
		if len(args) != 1 {
			return refuse(stdout, "serve takes no arguments")
		}
		return serve(stdout)
	case "exec":
		if len(args) != 1 {
			return refuse(stdout, "exec takes no arguments")
		}
		return execRoot(stdin, stdout)
	}
	fmt.Fprintln(stderr, "unknown command", args[0])
	return 2
}

func refuse(w io.Writer, msg string) int {
	writeJSON(w, Response{OK: false, Error: msg, Version: version})
	return 1
}

func writeJSON(w io.Writer, r Response) {
	raw, _ := json.Marshal(r)
	_, _ = w.Write(append(raw, '\n'))
}

// loadConfig reads and validates a runner config strictly.
func loadConfig(path string) (*ops.RunnerConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg ops.RunnerConfig
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return &cfg, nil
}

var reIP = regexp.MustCompile(`^[0-9a-fA-F:.]{2,45}$`)

// serve is the forced command. It validates early (so obvious garbage never
// reaches sudo) and passes the canonical request to `exec`.
func serve(stdout io.Writer) int {
	line := os.Getenv("SSH_ORIGINAL_COMMAND")
	cfg, err := loadConfig(ConfigPath)
	if err != nil {
		return refuse(stdout, "runner config: "+err.Error())
	}
	req, err := ops.ParseRequest(line)
	if err == nil {
		_, err = cfg.Allow(req)
	}
	if err != nil {
		writeJSON(stdout, Response{OK: false, Node: cfg.Node, ConfigHash: cfg.Hash(), Version: version, Error: err.Error()})
		return 1
	}
	peer := ""
	if f := strings.Fields(os.Getenv("SSH_CONNECTION")); len(f) > 0 && reIP.MatchString(f[0]) {
		peer = f[0]
	}
	input := req.String() + "\n" + peer + "\n"
	if os.Geteuid() == 0 {
		return execRoot(strings.NewReader(input), stdout)
	}
	cmd := exec.Command("/usr/bin/sudo", "-n", BinaryPath, "exec")
	cmd.Env = sysexec.SafeEnv
	cmd.Dir = "/"
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = stdout
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok && errBuf.Len() == 0 {
			return 1 // exec already printed its Response
		}
		writeJSON(stdout, Response{OK: false, Node: cfg.Node, ConfigHash: cfg.Hash(), Version: version,
			Error: "sudo: " + sysexec.FirstLine([]byte(errBuf.String()+" "+err.Error()))})
		return 1
	}
	return 0
}

// execRoot is the privileged half: one request on stdin, one Response out.
func execRoot(stdin io.Reader, stdout io.Writer) int {
	if os.Geteuid() != 0 {
		return refuse(stdout, "exec must run as root (through sudo)")
	}
	for _, p := range []string{BinaryPath, ConfigPath} {
		if err := trustedFile(p, 0); err != nil {
			return refuse(stdout, "refusing to run: "+err.Error())
		}
	}
	if _, err := os.Lstat(SudoersPath); err == nil {
		if err := trustedFile(SudoersPath, 0); err != nil {
			return refuse(stdout, "refusing to run: "+err.Error())
		}
	}
	if fi, err := os.Lstat(ops.ScriptDir); err == nil {
		if err := trustedDir(ops.ScriptDir, fi, 0); err != nil {
			return refuse(stdout, "refusing to run: "+err.Error())
		}
	}
	cfg, err := loadConfig(ConfigPath)
	if err != nil {
		return refuse(stdout, "runner config: "+err.Error())
	}
	br := bufio.NewReaderSize(io.LimitReader(stdin, ops.MaxLine+64), ops.MaxLine+64)
	line, _ := br.ReadString('\n')
	peer, _ := br.ReadString('\n')
	peer = strings.TrimSpace(peer)
	if !reIP.MatchString(peer) {
		peer = ""
	}
	r := &Runner{
		Cfg: cfg, Exec: sysexec.Exec{}, ScriptDir: ops.ScriptDir, StateDir: StateDir, AuditPath: AuditPath,
		Now: time.Now, Dial: (&net.Dialer{}).DialContext, Peer: peer,
		CheckTrust: func(p string) error { return trustedFile(p, 0) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), ops.MaxScriptTimeout+30*time.Second)
	defer cancel()
	resp := r.Handle(ctx, strings.TrimSuffix(line, "\n"))
	writeJSON(stdout, resp)
	if !resp.OK {
		return 1
	}
	return 0
}
