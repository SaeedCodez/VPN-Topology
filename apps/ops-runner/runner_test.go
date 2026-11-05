package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpnmap/pkg/ops"
	"vpnmap/pkg/sysexec"
	"vpnmap/pkg/topology"
)

type sharedCases struct {
	Config ops.RunnerConfig `json:"config"`
	Cases  []struct {
		Name string `json:"name"`
		Line string `json:"line"`
		OK   bool   `json:"ok"`
	} `json:"cases"`
}

func loadShared(t *testing.T) sharedCases {
	t.Helper()
	raw, err := os.ReadFile("../../pkg/ops/testdata/requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var sc sharedCases
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatal(err)
	}
	return sc
}

type rig struct {
	r    *Runner
	fake *sysexec.Fake
	now  time.Time
	dir  string
}

func newRig(t *testing.T, cfg *ops.RunnerConfig) *rig {
	t.Helper()
	dir := t.TempDir()
	g := &rig{fake: &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{}}, now: time.Unix(1_800_000_000, 0), dir: dir}
	g.r = &Runner{
		Cfg: cfg, Exec: g.fake, ScriptDir: filepath.Join(dir, "actions.d"), StateDir: filepath.Join(dir, "state"),
		AuditPath: filepath.Join(dir, "audit.log"), Now: func() time.Time { return g.now },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "31.56.89.125:9000" {
				a, b := net.Pipe()
				_ = b.Close()
				return a, nil
			}
			return nil, errors.New("connection refused")
		},
		CheckTrust: func(string) error { return nil },
	}
	return g
}

func (g *rig) on(cmd, out string) { g.fake.Responses[cmd] = sysexec.FakeResponse{Stdout: out} }

// Every refused shared case must be refused without running anything.
func TestRunnerRefusesWhatTheCatalogRefuses(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	for _, c := range sc.Cases {
		if c.OK {
			continue
		}
		before := len(g.fake.Calls)
		resp := g.r.Handle(context.Background(), c.Line)
		if resp.OK {
			t.Errorf("%s: %q was accepted", c.Name, c.Line)
		}
		if len(g.fake.Calls) != before {
			t.Errorf("%s: ran %v", c.Name, g.fake.Calls[before:])
		}
	}
}

func TestRunnerFixedArgv(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	cases := map[string]string{
		"container.stats container=db":                          "docker stats --no-stream --format {{.Name}}\tcpu={{.CPUPerc}}\tmem={{.MemUsage}}\tpids={{.PIDs}} db",
		"container.logs container=app since=15m tail=100":       "docker logs --since 15m --tail 100 app",
		"container.restart container=app":                       "docker restart --time 20 app",
		"unit.status unit=mullvad-ns@de.service":                "systemctl show mullvad-ns@de.service --no-pager --property=Id,ActiveState,SubState,Result,ActiveEnterTimestamp,NRestarts,MainPID",
		"unit.restart unit=x-ui.service":                        "systemctl restart x-ui.service",
		"disk.usage":                                            "df -P -h /",
		"script.change script=select cc=de relay=de-dus-wg-103": filepath.Join(g.r.ScriptDir, "select") + " --cc de --relay de-dus-wg-103",
		"script.read script=doctor":                             filepath.Join(g.r.ScriptDir, "doctor"),
	}
	for line, want := range cases {
		g.on(want, "fine")
		resp := g.r.Handle(context.Background(), line)
		if !resp.OK {
			t.Errorf("%s: %s", line, resp.Error)
			continue
		}
		last := g.fake.Calls[len(g.fake.Calls)-1]
		if got := sysexec.Key(last); got != want {
			t.Errorf("%s: ran %q, want %q", line, got, want)
		}
	}
}

func TestRunnerCooldownAndLock(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	g.on("docker restart --time 20 app", "app")
	if r := g.r.Handle(context.Background(), "container.restart container=app"); !r.OK {
		t.Fatal(r.Error)
	}
	g.now = g.now.Add(5 * time.Minute)
	r := g.r.Handle(context.Background(), "container.restart container=app req=00ff00ff")
	if r.OK || !strings.Contains(r.Error, "cooldown") || r.CooldownLeftS != 300 {
		t.Fatalf("second restart within 10m must be refused: %+v", r)
	}
	g.now = g.now.Add(6 * time.Minute)
	if r := g.r.Handle(context.Background(), "container.restart container=app"); !r.OK {
		t.Fatalf("after the cooldown: %s", r.Error)
	}
	// Reads have no cooldown.
	g.on("df -P -h /", "/dev/sda1 50%")
	for i := 0; i < 3; i++ {
		if r := g.r.Handle(context.Background(), "disk.usage"); !r.OK {
			t.Fatal(r.Error)
		}
	}
	// A held lock (another change running) refuses a second change.
	release, _, err := g.r.takeCooldown("other", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if r := g.r.Handle(context.Background(), "unit.restart unit=x-ui.service"); r.OK || !strings.Contains(r.Error, "another change") {
		t.Fatalf("expected busy: %+v", r)
	}
}

func TestRunnerTCPAndConns(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	r := g.r.Handle(context.Background(), "tcp.check target=31.56.89.125:9000")
	if !r.OK || !strings.Contains(r.Output, "OPEN") {
		t.Fatalf("%+v", r)
	}
	g.on("ss -Htn state established sport = :2053", "a\nb\n")
	g.on("ss -Htn state established dport = :2053", "")
	r = g.r.Handle(context.Background(), "conns.count port=2053")
	if !r.OK || !strings.Contains(r.Output, "sport=2 dport=0") {
		t.Fatalf("%+v", r)
	}
}

func TestRunnerWGHidesKeys(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	key := "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcdefg="
	g.on("ip netns exec mv-de wg show wg-de latest-handshakes", key+"\t"+"1799999940\n")
	g.on("ip netns exec mv-de wg show wg-de transfer", key+"\t100\t200\n")
	r := g.r.Handle(context.Background(), "wg.handshakes iface=wg-de")
	if !r.OK || strings.Contains(r.Output, key) || !strings.Contains(r.Output, "peer1 handshake=1m0s ago rx=100 tx=200") {
		t.Fatalf("%+v", r)
	}
}

func TestRunnerLogTail(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "w.log")
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("line " + strings.Repeat("x", i%7) + "\n")
	}
	b.WriteString("last\n")
	_ = os.WriteFile(log, []byte(b.String()), 0o644)
	link := filepath.Join(dir, "link.log")
	_ = os.Symlink("/etc/hosts", link)
	cfg := &ops.RunnerConfig{Node: "n", Logs: []ops.Log{{ID: "w", Path: log}, {ID: "l", Path: link}}}
	g := newRig(t, cfg)
	r := g.r.Handle(context.Background(), "log.tail log=w lines=20")
	lines := strings.Split(r.Output, "\n")
	if !r.OK || len(lines) != 20 || lines[19] != "last" {
		t.Fatalf("%+v", r)
	}
	if r := g.r.Handle(context.Background(), "log.tail log=l lines=20"); r.OK {
		t.Fatal("a symlinked log must not be followed")
	}
}

func TestRunnerRedactsAndAudits(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	g.on("docker logs --since 5m --tail 50 app", "connecting with password=hunter2secret ok\n")
	r := g.r.Handle(context.Background(), "container.logs container=app since=5m tail=50 req=0123abcd")
	if !r.OK || strings.Contains(r.Output, "hunter2secret") {
		t.Fatalf("output not redacted: %q", r.Output)
	}
	g.r.Handle(context.Background(), "container.stats container=$(id)\x1b[31m")
	raw, err := os.ReadFile(g.r.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	recs := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(recs) != 2 {
		t.Fatalf("want 2 audit records, got %d", len(recs))
	}
	var rec auditRecord
	_ = json.Unmarshal([]byte(recs[0]), &rec)
	if rec.Req != "0123abcd" || !rec.OK || rec.ConfigHash != sc.Config.Hash() {
		t.Fatalf("%+v", rec)
	}
	if strings.Contains(recs[1], "\x1b") || strings.Contains(recs[1], "hunter2") {
		t.Fatalf("audit line not sanitised: %s", recs[1])
	}
}

func TestRunnerScriptMustBeTrusted(t *testing.T) {
	sc := loadShared(t)
	g := newRig(t, &sc.Config)
	g.r.CheckTrust = func(string) error { return errors.New("owned by uid 1000") }
	if r := g.r.Handle(context.Background(), "script.read script=doctor"); r.OK || len(g.fake.Calls) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestExecTakesNoArguments(t *testing.T) {
	for _, args := range [][]string{{"exec", "--config", "/tmp/x"}, {"serve", "extra"}} {
		var out bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &out); code == 0 || !strings.Contains(out.String(), "takes no arguments") {
			t.Errorf("%v: code %d %s", args, code, out.String())
		}
	}
}

func TestTrustedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	uid := uint32(os.Getuid())
	fi, _ := os.Lstat(p)
	if err := owned(p, fi, uid); err != nil {
		t.Fatalf("own file: %v", err)
	}
	_ = os.Chmod(p, 0o666)
	fi, _ = os.Lstat(p)
	if err := owned(p, fi, uid); err == nil {
		t.Fatal("a world-writable file must be refused")
	}
	l := filepath.Join(dir, "l")
	_ = os.Symlink(p, l)
	if err := trustedFile(l, uid); err == nil {
		t.Fatal("a symlink must be refused")
	}
}

// Every script declared in the repo topology has an example to install.
func TestRepoScriptsHaveExamples(t *testing.T) {
	b, ds := topology.Load(topology.Options{Root: "../.."})
	if ds.HasErrors() {
		t.Fatalf("topology: %v", ds)
	}
	for _, p := range b.Ops {
		if p.Config == nil {
			continue
		}
		for _, s := range p.Config.Scripts {
			if _, err := os.Stat(filepath.Join("deploy", "actions.d", s.ID)); err != nil {
				t.Errorf("%s declares script %s but deploy/actions.d/%s is missing", p.Node, s.ID, s.ID)
			}
		}
	}
}
