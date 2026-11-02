package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"vpnmap/apps/agent/internal/helper"
	"vpnmap/apps/agent/internal/sysexec"
)

func readDeploy(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("deploy", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAgentUnitHardening(t *testing.T) {
	u := readDeploy(t, "vpnmap-agent.service")
	for _, want := range []string{
		"User=vpnmap-agent\n", "Group=vpnmap-agent\n",
		"ExecStartPre=-+/usr/local/bin/vpnmap-agent nft-apply --config /etc/vpnmap-agent/agent.yaml\n",
		"ExecStart=/usr/local/bin/vpnmap-agent run --config /etc/vpnmap-agent/agent.yaml\n",
		"Restart=always\n", "RestartSec=5\n", "NoNewPrivileges=yes\n",
		"CapabilityBoundingSet=\n", "AmbientCapabilities=\n",
		"ProtectSystem=strict\n", "ProtectHome=yes\n", "PrivateTmp=yes\n", "PrivateDevices=yes\n",
		"ProtectKernelTunables=yes\n", "ProtectKernelModules=yes\n", "ProtectControlGroups=yes\n",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX\n", "RestrictNamespaces=yes\n",
		"LockPersonality=yes\n", "MemoryDenyWriteExecute=yes\n", "SystemCallArchitectures=native\n",
		"StateDirectory=vpnmap-agent\n", "MemoryMax=64M\n", "CPUQuota=10%\n", "Nice=10\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("vpnmap-agent.service lacks %q", strings.TrimSpace(want))
		}
	}
	var inaccessible string
	for _, l := range strings.Split(u, "\n") {
		if strings.HasPrefix(l, "InaccessiblePaths=") {
			inaccessible = l
		}
	}
	for _, p := range []string{"-/opt/remnawave/.env", "-/opt/remnanode/.env", "-/etc/wireguard", "-/etc/mullvad-ns", "-/etc/x-ui", "-/root"} {
		if !strings.Contains(inaccessible+" ", " "+p+" ") && !strings.Contains(inaccessible, "="+p+" ") {
			t.Errorf("InaccessiblePaths lacks %s", p)
		}
	}
	for _, forbidden := range []string{"AmbientCapabilities=CAP", "ListenStream", "User=root"} {
		if strings.Contains(u, forbidden) {
			t.Errorf("vpnmap-agent.service contains %q", forbidden)
		}
	}

	s := readDeploy(t, "vpnmap-agent-helper.socket")
	for _, want := range []string{"ListenStream=/run/vpnmap-agent/helper.sock\n", "SocketUser=root\n", "SocketGroup=vpnmap-agent\n", "SocketMode=0660\n", "Accept=yes\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("helper socket lacks %q", strings.TrimSpace(want))
		}
	}
	h := readDeploy(t, "vpnmap-agent-helper@.service")
	for _, want := range []string{"ExecStart=/usr/local/bin/vpnmap-agent helper-serve --config /etc/vpnmap-agent/agent.yaml\n", "StandardInput=socket\n", "StandardOutput=socket\n", "NoNewPrivileges=yes\n", "ProtectSystem=strict\n"} {
		if !strings.Contains(h, want) {
			t.Errorf("helper service lacks %q", strings.TrimSpace(want))
		}
	}
	p := readDeploy(t, "vpnmap-probe.conf")
	if !strings.Contains(p, "RuntimeDirectory=vpnmap-probe\n") || !strings.Contains(p, "MemoryDenyWriteExecute=no\n") {
		t.Error("probe drop-in incomplete")
	}
}

func TestUnitCommandServesEmbeddedFiles(t *testing.T) {
	code, out, _ := runCmd("unit", "--list")
	want := "vpnmap-agent-helper.socket\nvpnmap-agent-helper@.service\nvpnmap-agent.service\nvpnmap-probe.conf\n"
	if code != 0 || out != want {
		t.Errorf("--list: %d %q", code, out)
	}
	for _, n := range strings.Fields(want) {
		code, out, _ := runCmd("unit", n)
		if code != 0 || out != readDeploy(t, n) {
			t.Errorf("unit %s differs from deploy/%s", n, n)
		}
	}
	if code, _, _ := runCmd("unit", "../../etc/passwd"); code != 1 {
		t.Errorf("traversal: %d", code)
	}
}

func TestScriptsSyntax(t *testing.T) {
	files, _ := filepath.Glob("deploy/*.sh")
	checks, _ := filepath.Glob("deploy/checks.d/*.check")
	for _, f := range append(files, checks...) {
		if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
		fi, _ := os.Stat(f)
		if fi.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable", f)
		}
	}
}

// fakeTools writes small shell scripts into a new directory.
func fakeTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// minimalPath links only the given system tools into a directory, so a
// script can be run without python3/jq to exercise its fallbacks.
func minimalPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not installed", tool)
		}
		os.Symlink(p, filepath.Join(dir, tool))
	}
	return dir
}

var coreTools = []string{"bash", "sh", "timeout", "tr", "cat", "grep", "date", "stat", "tail", "cut"}

func runCheckScript(t *testing.T, name, path string, env ...string) (map[string]any, string) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("deploy/checks.d", name+".check"))
	cmd.Env = append([]string{"PATH=" + path, "LANG=C"}, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	// The helper must accept it as a valid check result.
	res := helper.ParseCheckOutput(name, sysexec.Result{Stdout: out}, nil)
	if res.Status == "unknown" {
		t.Fatalf("%s: helper rejected output %q: %s", name, out, res.Detail)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	return m, res.Status
}

const doctorJSON = `{"status":"degraded","summary":"1 of 2 connections degraded","connections":[{"id":"mt6snj5mlbnn","status":"ok"},{"id":"zz9","status":"degraded"}]}`

func TestXbridgerDoctorCheck(t *testing.T) {
	out := filepath.Join(t.TempDir(), "doctor.json")
	os.WriteFile(out, []byte(doctorJSON), 0o644)
	fake := fakeTools(t, map[string]string{"docker": `[ "$1 $2 $3 $4 $5" = "exec backend-app-1 xbridger doctor --json" ] || exit 99
cat "$FAKE_OUT" 2>/dev/null; exit ${FAKE_RC:-0}`})
	sys := fake + ":" + sysexec.SafePATH

	if py, err := exec.LookPath("python3"); err == nil {
		m, status := runCheckScript(t, "xbridger-doctor", sys+":"+filepath.Dir(py), "FAKE_OUT="+out, "FAKE_RC=1")
		data, _ := m["data"].(map[string]any)
		conns, _ := data["connections"].([]any)
		if status != "degraded" || m["value"] != float64(2) || len(conns) != 2 || m["detail"] != "1 of 2 connections degraded" {
			t.Errorf("python path: %v", m)
		}
		if c0, _ := conns[0].(map[string]any); c0["id"] != "mt6snj5mlbnn" {
			t.Errorf("connection id: %v", conns)
		}
	}
	if _, err := exec.LookPath("jq"); err == nil {
		path := fake + ":" + minimalPath(t, append(coreTools, "jq")...)
		m, status := runCheckScript(t, "xbridger-doctor", path, "FAKE_OUT="+out, "FAKE_RC=0")
		if status != "ok" || m["value"] != float64(2) {
			t.Errorf("jq path: %v", m)
		}
	}
	plain := fake + ":" + minimalPath(t, coreTools...)
	for rc, want := range map[string]string{"0": "ok", "1": "degraded", "2": "down", "125": "down"} {
		m, status := runCheckScript(t, "xbridger-doctor", plain, "FAKE_OUT="+out, "FAKE_RC="+rc)
		if status != want {
			t.Errorf("plain rc=%s: %v", rc, m)
		}
	}
}

// The real shape of `xbridger doctor --json` (see doctor.ts in the xbridger repo).
const doctorRealJSON = `{"health":"healthy","exitCode":0,"failed":0,"warned":0,"checks":[
{"name":"db","status":"pass","detail":"reachable (80ms)"},
{"name":"conn mt6snj5mlbnn #0 process","status":"pass","detail":"pid 394"},
{"name":"conn mt6snj5mlbnn #0 ports","status":"pass","detail":"listening on ss-in 127.0.0.1:9000"},
{"name":"conn mt6snj5mlbnn #0 end-to-end","status":"pass","detail":"506ms via http-in 127.0.0.1:8000"},
{"name":"conn mt6snj5mlbnn #0 db status","status":"pass","detail":"Connected, matches reality"}]}`

// After a reboot on 2026-10-13: nothing runs while the DB still says Connected.
const doctorDownJSON = `{"health":"down","exitCode":2,"failed":3,"warned":0,"checks":[
{"name":"conn mt6snj5mlbnn #0 process","status":"fail","detail":"xray is not running for this connection"},
{"name":"conn mt6snj5mlbnn #0 ports","status":"fail","detail":"not listening on ss-in 127.0.0.1:9000"},
{"name":"conn mt6snj5mlbnn #0 end-to-end","status":"skip","detail":"skipped: the local process is down"},
{"name":"conn mt6snj5mlbnn #0 db status","status":"fail","detail":"DB says Connected but xray is not serving"}]}`

const doctorStaleJSON = `{"health":"degraded","exitCode":1,"failed":0,"warned":1,"checks":[
{"name":"conn mt6snj5mlbnn #0 process","status":"pass","detail":"pid 394"},
{"name":"conn mt6snj5mlbnn #0 ports","status":"pass","detail":"listening on ss-in 127.0.0.1:9000"},
{"name":"conn mt6snj5mlbnn #0 end-to-end","status":"pass","detail":"480ms via http-in 127.0.0.1:8000"},
{"name":"conn mt6snj5mlbnn #0 db status","status":"warn","detail":"DB says Error but xray is up and traffic flows"}]}`

// These are the fields the hub's xbridger-connection collector reads.
func TestXbridgerDoctorRealOutput(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	fake := fakeTools(t, map[string]string{
		"docker":  `cat "$FAKE_OUT"; exit ${FAKE_RC:-0}`,
		"timeout": `shift; exec "$@"`,
	})
	path := fake + ":" + sysexec.SafePATH + ":" + filepath.Dir(py)
	for _, tc := range []struct {
		name, doc, rc, wantStatus, wantDetail string
		want                                  map[string]any
	}{
		{"healthy", doctorRealJSON, "0", "ok", "healthy", map[string]any{"id": "mt6snj5mlbnn", "status": "Connected", "process": true, "ports_open": true, "latency": float64(506)}},
		{"down", doctorDownJSON, "2", "down", "down: conn mt6snj5mlbnn #0 process, conn mt6snj5mlbnn #0 ports, conn mt6snj5mlbnn #0 db status",
			map[string]any{"id": "mt6snj5mlbnn", "status": "Connected", "process": false, "ports_open": false, "reason": "xray is not running for this connection"}},
		{"stale db row", doctorStaleJSON, "1", "degraded", "degraded: conn mt6snj5mlbnn #0 db status", map[string]any{"id": "mt6snj5mlbnn", "status": "Connected", "process": true, "ports_open": true, "latency": float64(480)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "doctor.json")
			os.WriteFile(out, []byte(tc.doc), 0o644)
			m, status := runCheckScript(t, "xbridger-doctor", path, "FAKE_OUT="+out, "FAKE_RC="+tc.rc)
			data, _ := m["data"].(map[string]any)
			conns, _ := data["connections"].([]any)
			if status != tc.wantStatus || m["detail"] != tc.wantDetail || m["value"] != float64(1) || len(conns) != 1 {
				t.Fatalf("%v", m)
			}
			if !reflect.DeepEqual(conns[0], tc.want) {
				t.Errorf("connection: got %v want %v", conns[0], tc.want)
			}
		})
	}
}

func TestRelayForwardsCheck(t *testing.T) {
	out := filepath.Join(t.TempDir(), "relay.txt")
	os.WriteFile(out, []byte("realm: running\n0.0.0.0:443 -> 10.0.0.2:443\n0.0.0.0:8443 -> 10.0.0.3:8443\n"), 0o644)
	fake := fakeTools(t, map[string]string{
		"relay-ctl": `[ "$1" = status ] || exit 99; cat "$FAKE_OUT"`,
		"systemctl": `[ "$1" = is-active ] || exit 99; echo "$FAKE_STATE"; [ "$FAKE_STATE" = active ]`,
	})
	path := fake + ":" + minimalPath(t, coreTools...)
	m, status := runCheckScript(t, "relay-forwards", path, "FAKE_OUT="+out, "FAKE_STATE=active")
	if status != "ok" || m["value"] != float64(2) {
		t.Errorf("%v", m)
	}
	m, status = runCheckScript(t, "relay-forwards", path, "FAKE_OUT="+out, "FAKE_STATE=failed")
	if status != "down" || m["detail"] != "realm is failed" {
		t.Errorf("%v", m)
	}
}

// The real table of `relay-ctl status`, with its colour codes.
const relayCtlStatus = "=== Service ===\n\u25cf realm.service - Realm TCP Relay\n     Active: active (running)\n\n" +
	"=== Forwards ===\nNAME                   LISTEN  STATUS   REMOTE                         COMMENT\n" +
	"-------------------------------------------------\n" +
	"main                   443     \x1b[0;32mUP      \x1b[0m 31.56.89.58:443                Initial forward\n" +
	"DE                     8443    DOWN     92.42.207.135:8443             \n" +
	"XBRIDGER-1             2053    OFF      81.12.50.23:2053               Xbridger-1 Server\n\n" +
	"=== Active connections per forward ===\n  main                   port 443    : 0 connections\n"

func TestRelayForwardsRealTable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "relay.txt")
	os.WriteFile(out, []byte(relayCtlStatus), 0o644)
	fake := fakeTools(t, map[string]string{
		"relay-ctl": `cat "$FAKE_OUT"`,
		"systemctl": `echo active`,
	})
	path := fake + ":" + minimalPath(t, coreTools...)
	if m, status := runCheckScript(t, "relay-forwards", path, "FAKE_OUT="+out); status != "ok" || m["value"] != float64(3) {
		t.Errorf("%v", m)
	}
}

func TestRemnawaveBackupAgeCheck(t *testing.T) {
	log := filepath.Join(t.TempDir(), "backup.log")
	path := minimalPath(t, coreTools...)
	set := func(lastLine string, age time.Duration) {
		os.WriteFile(log, []byte("started\n"+lastLine+"\n"), 0o644)
		at := time.Now().Add(-age)
		os.Chtimes(log, at, at)
	}
	cases := []struct {
		line   string
		age    time.Duration
		status string
		value  float64
	}{
		{"backup ok: remnawave-2026.tar.gz", 10 * time.Minute, "ok", 10},
		{"backup ok", 40 * time.Minute, "degraded", 40},
		{"backup ok", 50 * time.Minute, "down", 50},
		{"ERROR upload failed", time.Minute, "degraded", 1},
	}
	for _, c := range cases {
		set(c.line, c.age)
		m, status := runCheckScript(t, "remnawave-backup-age", path, "VPNMAP_BACKUP_LOG="+log)
		if status != c.status || m["value"] != c.value {
			t.Errorf("%+v: %v", c, m)
		}
	}
	m, status := runCheckScript(t, "remnawave-backup-age", path, "VPNMAP_BACKUP_LOG="+log+".missing")
	if status != "down" {
		t.Errorf("missing log: %v", m)
	}
}

// stubDangerous puts logging stubs for every system-changing command first
// in PATH. A dry run must never call them; read-only queries are allowed.
func stubDangerous(t *testing.T, names ...string) (dir, log string) {
	dir = t.TempDir()
	log = filepath.Join(t.TempDir(), "calls.log")
	for _, n := range names {
		body := `case "$1" in cat|is-active|list) echo "readonly $(basename "$0") $*" >>"$STUB_LOG"; exit 1;; esac
echo "MUTATING $(basename "$0") $*" >>"$STUB_LOG"; exit 97`
		os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	return dir, log
}

var dangerous = []string{"useradd", "userdel", "groupadd", "groupdel", "systemctl", "setfacl", "nft", "curl", "runuser", "install", "chown", "chgrp"}

func installFixture(t *testing.T) (bin, sha, cfg, logp string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "vpnmap-agent")
	src, _ := os.Open(self)
	dst, _ := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY, 0o755)
	h := sha256.New()
	io.Copy(io.MultiWriter(dst, h), src)
	src.Close()
	dst.Close()
	sha = hex.EncodeToString(h.Sum(nil))
	logp = filepath.Join(dir, "xray-error.log")
	os.WriteFile(logp, []byte("x\n"), 0o640)
	cfg = writeConfig(t, "hub: https://map.example.com\nnode: relay-1\ntoken_file: /etc/vpnmap-agent/token\n"+
		"logs:\n  - {id: xray, path: "+logp+"}\n")
	return
}

func runScript(t *testing.T, stubs, stubLog string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", args...)
	cmd.Env = []string{
		"PATH=" + stubs + ":" + sysexec.SafePATH, "STUB_LOG=" + stubLog, "LANG=C",
		"VPNMAP_AGENT_TESTMAIN=1", "VPNMAP_TOKEN=tok-must-never-be-printed",
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func assertNoMutation(t *testing.T, stubLog string) {
	t.Helper()
	b, _ := os.ReadFile(stubLog)
	if strings.Contains(string(b), "MUTATING") {
		t.Fatalf("dry run called system-changing commands:\n%s", b)
	}
}

func TestInstallDryRun(t *testing.T) {
	_, etcErr := os.Stat("/etc/vpnmap-agent")
	bin, sha, cfg, logp := installFixture(t)
	stubs, stubLog := stubDangerous(t, dangerous...)
	out, err := runScript(t, stubs, stubLog, "deploy/install.sh",
		"--hub", "https://map.example.com", "--node", "relay-1", "--sha256", sha,
		"--config-file", cfg, "--binary", bin, "--arch", "amd64", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertNoMutation(t, stubLog)
	for _, want := range []string{
		"sha256 verified: " + sha,
		"+ install -o root -g root -m 0755 ",
		"+ install -d -o root -g vpnmap-agent -m 0750 /etc/vpnmap-agent",
		"+ install -d -o root -g root -m 0755 /etc/vpnmap-agent/checks.d",
		"+ install -o root -g root -m 0644 " + cfg + " /etc/vpnmap-agent/agent.yaml",
		"+ write /etc/vpnmap-agent/token (root:vpnmap-agent, mode 0640)",
		"+ setfacl -m u:vpnmap-agent:r " + logp,
		"+ write /etc/systemd/system/vpnmap-agent.service",
		"+ write /etc/systemd/system/vpnmap-agent-helper.socket",
		"+ write /etc/systemd/system/vpnmap-agent-helper@.service",
		"+ systemctl daemon-reload",
		"+ systemctl enable --now vpnmap-agent-helper.socket",
		"+ systemctl enable --now vpnmap-agent.service",
		"dry run: nothing was changed",
		"uninstall: curl -fsSL \"https://map.example.com/agent/uninstall.sh\" | bash -s --",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, "tok-must-never-be-printed") {
		t.Error("token printed")
	}
	for _, other := range []string{"ufw", "iptables", "systemctl restart", "systemctl reload", "nft "} {
		if strings.Contains(out, other) {
			t.Errorf("installer mentions %q:\n%s", other, out)
		}
	}
	if _, err := os.Stat("/etc/vpnmap-agent"); (err == nil) != (etcErr == nil) {
		t.Error("dry run changed /etc/vpnmap-agent")
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestInstallRefusesBadInput(t *testing.T) {
	bin, sha, cfg, _ := installFixture(t)
	stubs, stubLog := stubDangerous(t, dangerous...)
	bad := strings.Repeat("0", 64)
	cases := map[string][]string{
		"sha256 mismatch":                 {"--hub", "https://map.example.com", "--node", "relay-1", "--sha256", bad, "--config-file", cfg, "--binary", bin, "--dry-run"},
		"not a valid agent config":        {"--hub", "https://map.example.com", "--node", "relay-2", "--sha256", sha, "--config-file", cfg, "--binary", bin, "--dry-run"},
		"--hub must be https":             {"--hub", "http://map.example.com", "--node", "relay-1", "--sha256", sha, "--dry-run"},
		"--sha256 must be":                {"--hub", "https://map.example.com", "--node", "relay-1", "--sha256", "abc", "--dry-run"},
		"--node must be":                  {"--hub", "https://map.example.com", "--node", "../x", "--sha256", sha, "--dry-run"},
		"unknown option":                  {"--bogus"},
		"--config-file /nonexistent.yaml": {"--hub", "https://map.example.com", "--node", "relay-1", "--sha256", sha, "--config-file", "/nonexistent.yaml", "--dry-run"},
	}
	for want, args := range cases {
		out, err := runScript(t, stubs, stubLog, append([]string{"deploy/install.sh"}, args...)...)
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("%s: err=%v\n%s", want, err, out)
		}
	}
	assertNoMutation(t, stubLog)
}

func TestUninstallDryRun(t *testing.T) {
	stubs, stubLog := stubDangerous(t, append(dangerous, "rm")...)
	out, err := runScript(t, stubs, stubLog, "deploy/uninstall.sh", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertNoMutation(t, stubLog)
	for _, want := range []string{"+ rm -f /etc/systemd/system/vpnmap-agent.service", "+ rm -f /usr/local/bin/vpnmap-agent", "+ rm -rf /etc/vpnmap-agent", "dry run: nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	out, err = runScript(t, stubs, stubLog, "deploy/uninstall.sh", "--dry-run", "--keep-config")
	if err != nil || strings.Contains(out, "rm -rf /etc/vpnmap-agent") || !strings.Contains(out, "keeping /etc/vpnmap-agent") {
		t.Errorf("--keep-config: %v\n%s", err, out)
	}
	assertNoMutation(t, stubLog)
}
