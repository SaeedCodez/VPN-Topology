package helper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

// checksDir creates a trusted checks dir with the given scripts (0755).
func checksDir(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checks.d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, body := range scripts {
		p := filepath.Join(dir, id+".check")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func checkServer(dir string, checks ...agentcfg.LocalCheck) *Server {
	return &Server{
		Cfg:        &agentcfg.Config{ChecksDir: dir, Checks: checks},
		Runner:     sysexec.Exec{},
		TrustedUID: uint32(os.Geteuid()),
	}
}

func runCheckReq(t *testing.T, s *Server, id string) (protocol.CheckResult, Response) {
	t.Helper()
	resp := s.Handle(context.Background(), []byte(`{"op":"check","args":["`+id+`"]}`))
	var r protocol.CheckResult
	if resp.OK {
		if err := json.Unmarshal(resp.Data, &r); err != nil {
			t.Fatal(err)
		}
	}
	return r, resp
}

func TestCheckOK(t *testing.T) {
	dir := checksDir(t, map[string]string{
		"xbridger-doctor": `echo '{"status":"ok","value":3,"detail":"3 connections, token=abc123","data":{"connections":[{"id":"mt6snj5mlbnn","status":"ok","password":"hunter2"}]}}'`,
	})
	r, resp := runCheckReq(t, checkServer(dir, agentcfg.LocalCheck{ID: "xbridger-doctor"}), "xbridger-doctor")
	if !resp.OK {
		t.Fatalf("%+v", resp)
	}
	if r.ID != "xbridger-doctor" || r.Status != "ok" || r.Value == nil || *r.Value != 3 || r.ExitCode != 0 || r.At == "" {
		t.Errorf("%+v", r)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "abc123") || strings.Contains(string(raw), "hunter2") {
		t.Errorf("not redacted: %s", raw)
	}
	if !strings.Contains(string(raw), "mt6snj5mlbnn") {
		t.Errorf("connection id lost: %s", raw)
	}
}

func TestCheckStatuses(t *testing.T) {
	dir := checksDir(t, map[string]string{
		"degraded": `echo '{"status":"degraded","detail":"slow"}'; exit 1`,
		"down":     `echo '{"status":"down"}'; exit 2`,
		"garbage":  `echo 'not json'; echo 'boom password=x' >&2`,
		"badstat":  `echo '{"status":"great"}'`,
		"empty":    `true`,
		"huge":     `head -c 70000 /dev/zero | tr '\0' a`,
		"slow":     `sleep 5; echo '{"status":"ok"}'`,
		"env":      `printf '{"status":"ok","detail":"%s"}' "$(env | sort | tr '\n' ' ')"`,
		"stdin":    `read -r x; echo "{\"status\":\"ok\",\"detail\":\"[$x]\"}"`,
	})
	checks := []agentcfg.LocalCheck{{ID: "degraded"}, {ID: "down"}, {ID: "garbage"}, {ID: "badstat"}, {ID: "empty"}, {ID: "huge"},
		{ID: "slow", Timeout: agentcfg.Duration(300 * time.Millisecond)}, {ID: "env"}, {ID: "stdin"}}
	s := checkServer(dir, checks...)
	t.Setenv("LEAKY_SECRET", "x")

	cases := map[string]struct {
		status string
		exit   int
		detail string
	}{
		"degraded": {"degraded", 1, "slow"},
		"down":     {"down", 2, ""},
		"garbage":  {"unknown", 0, "not valid JSON (stderr: boom password=[REDACTED]"},
		"badstat":  {"unknown", 0, `invalid status "great"`},
		"empty":    {"unknown", 0, "not valid JSON"},
		"huge":     {"unknown", 0, "larger than"},
		"slow":     {"unknown", -1, "timed out"},
		"env":      {"ok", 0, "LANG=C PATH=/usr/sbin:/usr/bin:/sbin:/bin "},
		"stdin":    {"ok", 0, "[]"},
	}
	for id, want := range cases {
		r, resp := runCheckReq(t, s, id)
		if !resp.OK {
			t.Errorf("%s: %+v", id, resp)
			continue
		}
		if r.Status != want.status || r.ExitCode != want.exit || !strings.Contains(r.Detail, want.detail) {
			t.Errorf("%s: status=%q exit=%d detail=%q; want %q %d ~%q", id, r.Status, r.ExitCode, r.Detail, want.status, want.exit, want.detail)
		}
	}
}

func TestCheckPathTrust(t *testing.T) {
	uid := uint32(os.Geteuid())
	dir := checksDir(t, map[string]string{"good": "true", "gw": "true", "noexec": "true"})
	if _, err := CheckPath(dir, "good", uid); err != nil {
		t.Errorf("good: %v", err)
	}
	os.Chmod(filepath.Join(dir, "gw.check"), 0o775)
	os.Chmod(filepath.Join(dir, "noexec.check"), 0o644)
	os.Symlink("/bin/true", filepath.Join(dir, "link.check"))
	os.Mkdir(filepath.Join(dir, "subdir.check"), 0o755)
	for id, why := range map[string]string{
		"gw":      "group- or world-writable",
		"noexec":  "not executable",
		"link":    "not a regular file",
		"subdir":  "not a regular file",
		"missing": "not installed",
		"../x":    "invalid check id",
	} {
		if _, err := CheckPath(dir, id, uid); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err = %v, want %q", id, err, why)
		}
	}
	if _, err := CheckPath("relative/checks.d", "good", uid); err == nil {
		t.Error("relative checks dir accepted")
	}
	// A writable checks dir lets anyone swap scripts: refuse everything.
	os.Chmod(dir, 0o777)
	if _, err := CheckPath(dir, "good", uid); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Errorf("writable dir: %v", err)
	}
	os.Chmod(dir, 0o755)
	// Wrong owner.
	if _, err := CheckPath(dir, "good", uid+1); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Errorf("wrong owner: %v", err)
	}
}

func TestCheckOwnedByOtherUserIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	dir := checksDir(t, map[string]string{"evil": `echo '{"status":"ok"}'`})
	if err := os.Chown(filepath.Join(dir, "evil.check"), 65534, 65534); err != nil {
		t.Fatal(err)
	}
	r, resp := runCheckReq(t, checkServer(dir, agentcfg.LocalCheck{ID: "evil"}), "evil")
	if resp.OK || !strings.Contains(resp.Error, "owned by root") {
		t.Errorf("%+v %+v", r, resp)
	}
}

func TestTrustedFile(t *testing.T) {
	uid := uint32(os.Geteuid())
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.yaml")
	os.WriteFile(p, []byte("node: x\n"), 0o644)
	if err := TrustedFile(p, uid); err != nil {
		t.Errorf("trusted: %v", err)
	}
	os.Chmod(p, 0o666)
	if err := TrustedFile(p, uid); err == nil {
		t.Error("world-writable config accepted")
	}
}
