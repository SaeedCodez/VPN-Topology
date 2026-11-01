package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

// TestMain lets the test binary act as vpnmap-agent for the installer
// dry-run tests (they need a real binary to verify and run).
func TestMain(m *testing.M) {
	if os.Getenv("VPNMAP_AGENT_TESTMAIN") == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func runCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckNftScriptAcceptsRendered(t *testing.T) {
	counters := []agentcfg.NftCounter{
		{Name: "relay_in", Hook: "input", Proto: "tcp", Dport: 2053},
		{Name: "wg_out", Hook: "output", Oif: "wg0"},
		{Name: "multi", Hook: "forward", Proto: "tcp", Dports: []int{443, 8443}, Saddr: "10.0.0.0/24"},
	}
	script := agentcfg.RenderNft(counters)
	if err := checkNftScript(script); err != nil {
		t.Fatalf("rendered script rejected: %v\n%s", err, script)
	}
}

func TestCheckNftScriptRejects(t *testing.T) {
	bad := map[string]string{
		"other table":    "table inet filter {\n}\n",
		"flush ruleset":  "flush ruleset\n",
		"delete other":   "delete table ip nat\n",
		"drop rule":      "table inet vpnmap {\n\tchain input {\n\t\ttype filter hook input priority -310; policy accept;\n\t\ttcp dport 22 drop\n\t}\n}\n",
		"jump":           "table inet vpnmap {\n\tchain input {\n\t\tip saddr 1.2.3.4 jump x\n\t}\n}\n",
		"nat":            "table inet vpnmap {\n\tchain p {\n\t\tmasquerade\n\t}\n}\n",
		"unbalanced":     "table inet vpnmap {\n",
		"extra closing":  "}\n",
		"add rule other": "add rule inet filter input drop\n",
	}
	for name, s := range bad {
		if err := checkNftScript(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNftApply(t *testing.T) {
	cfg := &agentcfg.Config{Nft: &agentcfg.NftConfig{Counters: []agentcfg.NftCounter{{Name: "relay_in", Hook: "input", Proto: "tcp", Dport: 2053}}}}
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{"nft -f -": {}}}
	var out bytes.Buffer
	if err := nftApply(context.Background(), cfg, f, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || string(f.Calls[0].Stdin) != agentcfg.RenderNft(cfg.Nft.Counters) {
		t.Errorf("calls %+v", f.Calls)
	}

	// No nft section: delete our table if present, and only our table.
	f = &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"nft list table inet vpnmap":   {Stdout: "table inet vpnmap {}"},
		"nft delete table inet vpnmap": {},
	}}
	if err := nftApply(context.Background(), &agentcfg.Config{}, f, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 || sysexec.Key(f.Calls[1]) != "nft delete table inet vpnmap" {
		t.Errorf("calls %+v", f.Calls)
	}
	// Absent table (or nft missing): nothing to do, success.
	f = &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"nft list table inet vpnmap": {ExitCode: 1, Stderr: "Error: No such file or directory"},
	}}
	if err := nftApply(context.Background(), &agentcfg.Config{}, f, &out); err != nil || len(f.Calls) != 1 {
		t.Errorf("err=%v calls=%+v", err, f.Calls)
	}
}

func TestNftApplyDryRunCommand(t *testing.T) {
	p := writeConfig(t, "hub: https://map.example.com\nnode: relay-1\ntoken_file: /x\nnft:\n  counters:\n    - {name: relay_in, hook: input, proto: tcp, dport: 2053}\n")
	code, out, errs := runCmd("nft-apply", "--config", p, "--dry-run")
	if code != 0 || !strings.Contains(out, "[dry-run] nft -f -") || !strings.Contains(out, "counter name \"relay_in\"") {
		t.Errorf("code=%d out=%s err=%s", code, out, errs)
	}
	code, out, _ = runCmd("render-nft", "--config", p)
	if code != 0 || !strings.HasPrefix(out, "#!/usr/sbin/nft -f") {
		t.Errorf("render-nft: %d %s", code, out)
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	os.WriteFile(tok, []byte("secret-token-value\n"), 0o600)
	logp := filepath.Join(dir, "x.log")
	os.WriteFile(logp, nil, 0o644)
	p := writeConfig(t, "hub: https://map.example.com\nnode: relay-1\ntoken_file: "+tok+"\n"+
		"logs:\n  - {id: xray, path: "+logp+"}\n  - {id: env, path: /opt/remnawave/.env}\n"+
		"checks:\n  - {id: xbridger-doctor}\n")

	code, out, errs := runCmd("check-config", "--config", p, "--helper-socket", filepath.Join(dir, "none.sock"))
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errs)
	}
	for _, want := range []string{"node        relay-1 (kind agent)", "[ok]   token file", "[ok]   log xray", "refusing to read /opt/remnawave/.env", "[warn] check xbridger-doctor: ", "helper socket", "warning(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-token-value") {
		t.Error("token printed")
	}

	code, out, _ = runCmd("check-config", "--config", p, "--get", "log-paths")
	if code != 0 || out != logp+"\n/opt/remnawave/.env\n" {
		t.Errorf("log-paths: %d %q", code, out)
	}
	if code, out, _ = runCmd("check-config", "--config", p, "--get", "token-file"); code != 0 || out != tok+"\n" {
		t.Errorf("token-file: %d %q", code, out)
	}
	if code, out, _ = runCmd("check-config", "--config", p, "--get", "has-probe"); code != 0 || out != "no\n" {
		t.Errorf("has-probe: %d %q", code, out)
	}
	if code, _, errs = runCmd("check-config", "--config", p, "--expect-node", "relay-2"); code != 1 || !strings.Contains(errs, `not "relay-2"`) {
		t.Errorf("expect-node: %d %s", code, errs)
	}
	// Look-alike loopback hosts are refused (agentcfg parses the URL; the agent re-checks).
	bad := writeConfig(t, "hub: http://127.0.0.1.evil.example\nnode: relay-1\ntoken_file: /x\n")
	if code, _, errs = runCmd("check-config", "--config", bad); code != 1 || !strings.Contains(errs, "hub") {
		t.Errorf("bad hub: %d %s", code, errs)
	}
	if code, _, _ = runCmd("check-config", "--config", filepath.Join(dir, "missing.yaml")); code != 1 {
		t.Errorf("missing config: %d", code)
	}
}

func TestRunOncePrint(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := writeConfig(t, "hub: https://map.example.com\nnode: relay-1\ntoken_file: /nonexistent\n"+
		"ss:\n  - {id: in-2053, port: 2053, side: local}\n"+
		"tcp:\n  - {target: \""+ln.Addr().String()+"\"}\n"+
		"containers: [backend-app-1]\n")
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	code, out, errs := runCmd("run", "--once", "--print", "--config", p, "--helper-socket", filepath.Join(t.TempDir(), "none.sock"))
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errs)
	}
	var push protocol.Push
	if err := json.Unmarshal([]byte(out), &push); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if push.Agent.Node != "relay-1" || push.Host == nil || len(push.TCP) != 1 || !push.TCP[0].OK || len(push.SS) != 1 {
		t.Errorf("push %+v", push)
	}
	if len(push.Errors) != 1 || push.Errors[0].Collector != "docker" || push.Errors[0].Error != "helper not available" {
		t.Errorf("errors %+v", push.Errors)
	}
	if code, _, _ := runCmd("run", "--print", "--config", p); code != 2 {
		t.Errorf("--print without --once: %d", code)
	}
}

func TestCheckPeer(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, b := os.NewFile(uintptr(fds[0]), "a"), os.NewFile(uintptr(fds[1]), "b")
	defer a.Close()
	defer b.Close()
	err = checkPeer(a)
	if os.Geteuid() == 0 && err != nil {
		t.Errorf("root peer refused: %v", err)
	}
	if os.Geteuid() != 0 && err == nil {
		t.Errorf("non-root, non-%s peer accepted", agentUser)
	}
	// Not a socket (started by hand): allowed.
	f, _ := os.CreateTemp(t.TempDir(), "x")
	defer f.Close()
	if err := checkPeer(f); err != nil {
		t.Errorf("regular file: %v", err)
	}
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := runCmd("version"); code != 0 || !strings.HasPrefix(out, "vpnmap-agent ") {
		t.Errorf("%d %q", code, out)
	}
	if code, _, _ := runCmd("bogus"); code != 2 {
		t.Errorf("unknown command: %d", code)
	}
	if code, _, _ := runCmd(); code != 2 {
		t.Errorf("no command: %d", code)
	}
}
