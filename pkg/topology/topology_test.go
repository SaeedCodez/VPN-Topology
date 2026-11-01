package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMap = `schema: vpnmap/v1
name: Test
groups:
  - { id: dc, title: DC, kind: datacenter }
  - { id: ext, title: External, kind: external }
sources:
  - { id: probe, title: Probe }
`

const goodComponent = `component: base
title: Base
nodes:
  - { id: users, name: Users, kind: users, group: ext }
  - { id: relay, name: Relay, kind: relay, group: dc, agent: true, card: [cpu] }
  - { id: internet, name: Internet, kind: internet, group: ext }
links:
  - id: e1
    from: users
    to: relay
    plane: data
    health: { collector: tcp-connect, from: probe, target: "relay.example:2053" }
  - { id: e2, from: relay, to: internet, plane: data }
routes:
  - { id: main, title: Main, hops: [e1, e2], probe: { collector: e2e-probe, expect_country: FR } }
alerts:
  - { id: a1, when: "link.e1.health == down for 2m", severity: warning, message_fa: "قطع" }
`

func writeRepo(t *testing.T, comps map[string]string) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "topology", "components"), 0o755))
	must(os.WriteFile(filepath.Join(root, "topology", "map.yaml"), []byte(testMap), 0o644))
	for name, body := range comps {
		must(os.WriteFile(filepath.Join(root, "topology", "components", name+".yaml"), []byte(body), 0o644))
	}
	return root
}

func TestLoadGood(t *testing.T) {
	root := writeRepo(t, map[string]string{"base": goodComponent})
	b, ds := Load(Options{Root: root})
	if ds.HasErrors() {
		t.Fatalf("unexpected errors: %v", ds)
	}
	if len(b.Nodes) != 3 || len(b.Links) != 2 || len(b.Routes) != 1 {
		t.Fatalf("counts: %d nodes %d links %d routes", len(b.Nodes), len(b.Links), len(b.Routes))
	}
	r := b.RouteByID("main")
	if strings.Join(r.Nodes, ",") != "users,relay,internet" {
		t.Fatalf("route nodes = %v", r.Nodes)
	}
	if r.Probe.Key != "route:main:probe" || r.Probe.Params["route"] != "main" {
		t.Fatalf("probe = %+v", r.Probe)
	}
	relay := b.NodeByID("relay")
	if !hasCollector(relay.Checks, "boot-time") {
		t.Fatal("agent node should get an implicit boot-time check")
	}
	if b.LinkByID("e1").Health[0].Params["timeout"] != "3s" {
		t.Fatal("defaults should be filled in")
	}
	if b.Alerts[0].Expr == nil || b.Alerts[0].Expr.For.Seconds != 120 {
		t.Fatalf("alert expr = %+v", b.Alerts[0].Expr)
	}
	if relay.Defined.File != "topology/components/base.yaml" || relay.Defined.Line != 5 {
		t.Fatalf("defined_in = %+v", relay.Defined)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name, body, want string
		line             int
	}{
		{"unknown node", strings.Replace(goodComponent, "to: relay\n", "to: rely\n", 1), `unknown node "rely"`, 10},
		{"did you mean", strings.Replace(goodComponent, "to: relay\n", "to: rely\n", 1), `did you mean "relay"`, 10},
		{"unknown field", strings.Replace(goodComponent, "plane: data\n    health", "plane: data\n    helth", 1), `unknown field "helth"`, 12},
		{"bad collector", strings.Replace(goodComponent, "collector: tcp-connect", "collector: tcp-conect", 1), `unknown collector "tcp-conect"`, 12},
		{"bad param", strings.Replace(goodComponent, `target: "relay.example:2053"`, `targt: "relay.example:2053"`, 1), `no parameter "targt"`, 12},
		{"missing param", strings.Replace(goodComponent, `, target: "relay.example:2053"`, ``, 1), `needs parameter "target"`, 12},
		{"no agent", strings.Replace(goodComponent, "from: probe", "from: users", 1), `not a source or a node with an agent`, 12},
		{"route chain", strings.Replace(goodComponent, "hops: [e1, e2]", "hops: [e2, e1]", 1), `hops must chain`, 15},
		{"alert ref", strings.Replace(goodComponent, "link.e1.health", "link.e9.health", 1), `unknown link "e9"`, 17},
		{"alert syntax", strings.Replace(goodComponent, "== down for 2m", "= down", 1), `unknown operator`, 17},
		{"dangerous runbook", goodComponent + "runbook:\n  - { title: fix, cmd: \"ssh root@x 'systemctl restart realm'\" }\n", "runs `systemctl ... restart`", 0},
		{"secret", strings.Replace(goodComponent, "title: Base", "title: Base\ndescription: \"password: hunter2hunter2\"", 1), "credential", 3},
		{"wrong file name", strings.Replace(goodComponent, "component: base", "component: other", 1), "does not match its file name", 1},
		{"bad group", strings.Replace(goodComponent, "group: dc, agent", "group: dcc, agent", 1), `unknown group "dcc"`, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeRepo(t, map[string]string{"base": c.body})
			_, ds := Load(Options{Root: root})
			for _, d := range ds {
				if strings.Contains(d.Message+" "+d.Fix, c.want) {
					if c.line != 0 && d.Line != c.line {
						t.Fatalf("found %q at line %d, want line %d", c.want, d.Line, c.line)
					}
					if d.File != "topology/components/base.yaml" {
						t.Fatalf("file = %s", d.File)
					}
					return
				}
			}
			t.Fatalf("expected %q in:\n%v", c.want, ds)
		})
	}
}

func TestDuplicateAcrossFiles(t *testing.T) {
	other := "component: other\ntitle: Other\nnodes:\n  - { id: relay, name: Relay again, kind: relay, group: dc }\n"
	root := writeRepo(t, map[string]string{"base": goodComponent, "other": other})
	_, ds := Load(Options{Root: root})
	found := false
	for _, d := range ds {
		if strings.Contains(d.Message, `node "relay" is already defined in topology/components/base.yaml:5`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate error, got %v", ds)
	}
}

func TestExpr(t *testing.T) {
	good := []string{
		"link.wg-game.health == down for 2m",
		"route.main.probe_failures >= 3",
		"link.e3.health == down and node.x.check.api.status == ok",
		"node.*.rebooted == true",
		"link.*.traffic_drop > 80% for 10m",
		"collector.tls-expiry.status != ok",
		"not (node.a.cpu > 90 or node.a.mem > 90) for 3 samples",
	}
	for _, s := range good {
		if _, err := ParseExpr(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := map[string]string{
		"link.e1.health > down":      "use == or !=",
		"link.e1.health == 5s":       "is a status",
		"link.e1.speed == ok":        "no field",
		"host.e1.health == ok":       "unknown entity",
		"link.e1.health == down for": "expected a duration",
	}
	for s, want := range bad {
		_, err := ParseExpr(s)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", s, err, want)
		}
	}
}

func TestRunbookProblem(t *testing.T) {
	ok := []string{
		"ssh root@1.2.3.4 'relay-ctl status; tail -5 /var/log/realm/realm.log'",
		"ssh root@1.2.3.4 'docker exec backend-app-1 xbridger doctor'",
		`timeout 5 bash -c "echo > /dev/tcp/31.56.89.125/9000" && echo OPEN`,
		"ssh root@x 'ip netns exec mv-de wg show all latest-handshakes'",
		"ssh root@x 'docker logs --since 1h remnawave-nginx 2>&1 | grep -c favicons'",
	}
	for _, c := range ok {
		if p := RunbookProblem(c); p != "" {
			t.Errorf("%s: unexpected %s", c, p)
		}
	}
	bad := []string{
		"docker restart remnawave",
		"ssh root@x 'relay-ctl disable main'",
		"mullvad-ns select us",
		"docker compose up -d",
		"echo x > /etc/realm/forwards.yaml",
		"xbridger conn start mt6snj5mlbnn",
		"systemctl stop x-ui",
	}
	for _, c := range bad {
		if RunbookProblem(c) == "" {
			t.Errorf("%s: should be rejected", c)
		}
	}
}

func TestRepoTopologyIsValid(t *testing.T) {
	root, err := FindRoot(".")
	if err != nil {
		t.Skip("repo topology not found")
	}
	_, ds := Load(Options{Root: root})
	if ds.HasErrors() {
		t.Fatalf("repo topology has errors:\n%v", ds)
	}
}

func TestSchemaFilesAreCurrent(t *testing.T) {
	root, err := FindRoot(".")
	if err != nil {
		t.Skip()
	}
	files, err := SchemaFiles()
	if err != nil {
		t.Fatal(err)
	}
	for p, content := range files {
		old, _ := os.ReadFile(filepath.Join(root, p))
		if string(old) != string(content) {
			t.Errorf("%s is out of date: run `vpnmap schema`", p)
		}
	}
}
