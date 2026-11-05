package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/redact"
	"vpnmap/pkg/sysexec"
)

// checkEnv is the complete environment of a local check.
var checkEnv = []string{"PATH=" + sysexec.SafePATH, "LANG=C"}

const maxDetail = 1024

// runCheck executes checks.d/<id>.check after verifying that nobody but root
// could have written it, and turns its stdout into a CheckResult. A broken
// check yields status "unknown", never an error: the op itself worked.
func (s *Server) runCheck(ctx context.Context, c agentcfg.LocalCheck) (protocol.CheckResult, error) {
	path, err := CheckPath(s.Cfg.ChecksDir, c.ID, s.TrustedUID)
	if err != nil {
		return protocol.CheckResult{}, err
	}
	timeout := CheckTimeout(c)
	start := s.now()
	res, runErr := s.Runner.Run(ctx, sysexec.Cmd{
		Name: path, Env: checkEnv, Timeout: timeout, MaxOutput: MaxCheckOutput,
	})
	r := ParseCheckOutput(c.ID, res, runErr)
	r.DurationMs = float64(s.now().Sub(start).Microseconds()) / 1000
	r.At = start.UTC().Format(time.RFC3339)
	return r, nil
}

// CheckPath returns checksDir/<id>.check after verifying the chain of trust:
// the file is a regular file (not a symlink) owned by trustedUID and not
// writable by group or others, and so are checksDir and all its parents
// (a sticky world-writable parent such as /tmp is tolerated).
func CheckPath(checksDir, id string, trustedUID uint32) (string, error) {
	if !reCheckID.MatchString(id) {
		return "", fmt.Errorf("invalid check id")
	}
	if !filepath.IsAbs(checksDir) {
		return "", fmt.Errorf("checks_dir must be absolute")
	}
	dir := filepath.Clean(checksDir)
	if err := trustedDirChain(dir, trustedUID); err != nil {
		return "", err
	}
	p := filepath.Join(dir, id+".check")
	if filepath.Dir(p) != dir {
		return "", fmt.Errorf("invalid check path")
	}
	fi, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("check %s is not installed in %s", id, dir)
		}
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", p)
	}
	if err := trustedOwner(p, fi, trustedUID, false); err != nil {
		return "", err
	}
	if fi.Mode().Perm()&0o100 == 0 {
		return "", fmt.Errorf("%s is not executable", p)
	}
	return p, nil
}

// TrustedFile verifies a file the helper relies on (agent.yaml): a regular
// file owned by trustedUID, not group/world-writable, in a trusted dir chain.
func TrustedFile(path string, trustedUID uint32) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := trustedOwner(path, fi, trustedUID, false); err != nil {
		return err
	}
	return trustedDirChain(filepath.Dir(filepath.Clean(path)), trustedUID)
}

func trustedDirChain(dir string, trustedUID uint32) error {
	for d := dir; ; d = filepath.Dir(d) {
		fi, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
		if err := trustedOwner(d, fi, trustedUID, d != dir); err != nil {
			return err
		}
		if d == "/" || d == "." {
			return nil
		}
	}
}

// trustedOwner rejects files not owned by root/trustedUID or writable by
// group/others. Parents may be sticky world-writable dirs (only the owner
// of an entry can rename it there).
func trustedOwner(p string, fi os.FileInfo, trustedUID uint32, parent bool) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: cannot read owner", p)
	}
	if st.Uid != trustedUID && !(parent && st.Uid == 0) {
		return fmt.Errorf("%s must be owned by root", p)
	}
	if fi.Mode().Perm()&0o022 != 0 && !(parent && fi.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("%s must not be group- or world-writable", p)
	}
	return nil
}

// checkOutput is the JSON contract of a local check's stdout.
type checkOutput struct {
	Status string         `json:"status"`
	Value  *float64       `json:"value"`
	Detail string         `json:"detail"`
	Data   map[string]any `json:"data"`
}

// ParseCheckOutput maps a check run to a CheckResult. Everything textual
// that came from the check is redacted.
func ParseCheckOutput(id string, res sysexec.Result, runErr error) protocol.CheckResult {
	r := protocol.CheckResult{ID: id, Status: "unknown", ExitCode: res.ExitCode}
	var ee *sysexec.ExitError
	switch {
	case errors.Is(runErr, sysexec.ErrTimeout):
		r.Detail = "check timed out"
		r.ExitCode = -1
		return r
	case runErr != nil && !errors.As(runErr, &ee):
		r.Detail = clip(redact.String("cannot run check: " + runErr.Error()))
		r.ExitCode = -1
		return r
	case res.Truncated:
		r.Detail = fmt.Sprintf("check output is larger than %d bytes", MaxCheckOutput)
		return r
	}
	var out checkOutput
	dec := json.NewDecoder(bytes.NewReader(res.Stdout))
	if err := dec.Decode(&out); err != nil {
		r.Detail = "check output is not valid JSON"
		if l := sysexec.FirstLine(res.Stderr); l != "" {
			r.Detail += " (stderr: " + l + ")"
		}
		r.Detail = clip(redact.String(r.Detail))
		return r
	}
	switch out.Status {
	case "ok", "degraded", "down":
		r.Status = out.Status
	default:
		r.Detail = clip(redact.String(fmt.Sprintf("invalid status %q", out.Status)))
		return r
	}
	r.Value = out.Value
	r.Detail = clip(redact.String(out.Detail))
	r.Data = redact.Map(out.Data)
	return r
}

func clip(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail] + "..."
	}
	return s
}
