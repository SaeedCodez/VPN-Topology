package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const opsMap = `schema: vpnmap/v1
name: Test
settings: { critical_squads: [Paid] }
groups:
  - { id: dc, title: DC, kind: datacenter }
  - { id: ext, title: External, kind: external }
`

const opsBase = `component: base
title: Base
nodes:
  - { id: users, name: Users, kind: users, group: ext }
  - id: relay
    name: Relay
    kind: relay
    group: dc
    agent: true
    address: [10.0.0.1]
    checks:
      - { id: realm, collector: systemd-unit, host: relay, unit: realm.service }
      - { id: wd, collector: log-match, host: relay, path: /var/log/wd.log, pattern: restart }
      - { id: rw, collector: remnawave-node, node: Relay-Node }
    metrics:
      - { id: conns, label: Conns, collector: ss-conns, host: relay, port: 2053 }
    ops:
      runner: true
      remnawave: [restart, disable]
      containers:
        - { name: app, restart: true, cooldown: 5m }
      scripts:
        - { id: current, kind: read, params: [{ name: cc, enum: [de, us] }] }
        - id: select
          kind: change
          params:
            - { name: cc, enum: [de, us] }
            - { name: relay, pattern: "^[a-z]{2}-[a-z]{3}-wg-[0-9]{3}$" }
          read_before: { script: current, args: { cc: "{cc}" } }
          rollback: { script: select, args: { cc: "{cc}", relay: "{before.relay}" } }
          verify: ["link.e-{cc}"]
  - { id: test, name: Test, kind: server, group: dc }
  - { id: internet, name: Internet, kind: internet, group: ext }
links:
  - { id: e1, from: users, to: relay, plane: data, health: { collector: tcp-connect, from: relay, target: "10.0.0.2:9000" } }
  - { id: e-de, from: relay, to: internet, plane: data }
  - { id: e-us, from: relay, to: internet, plane: data }
  - { id: t1, from: users, to: test, plane: data }
routes:
  - { id: main, title: Main, hops: [e1, e-de], squads: [Paid] }
  - { id: lab, title: Lab, hops: [t1], squads: [Lab] }
`

func loadOps(t *testing.T, base string) (*Bundle, Diagnostics) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "topology", "components"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "topology", "map.yaml"), []byte(opsMap), 0o644)
	_ = os.WriteFile(filepath.Join(root, "topology", "components", "base.yaml"), []byte(base), 0o644)
	return Load(Options{Root: root})
}

func TestOpsResolve(t *testing.T) {
	b, ds := loadOps(t, opsBase)
	if ds.HasErrors() {
		t.Fatalf("unexpected errors: %v", ds)
	}
	p := b.OpsPolicy("relay")
	if p == nil || !p.Runner || p.Config == nil {
		t.Fatalf("no policy: %+v", p)
	}
	if !p.CutsUsers || strings.Join(p.CutRoutes, ",") != "main" {
		t.Errorf("relay is on the Paid route: cuts=%v %v", p.CutsUsers, p.CutRoutes)
	}
	want := "node.relay link.e-de link.e-us link.e1 route.main"
	if got := strings.Join(p.Verify, " "); got != want {
		t.Errorf("verify %q, want %q", got, want)
	}
	c := p.Config
	// Derived from checks: the unit, the log-match file, the tcp target, the port.
	if len(c.Units) != 1 || c.Units[0].Name != "realm.service" || c.Units[0].Restart {
		t.Errorf("units %+v", c.Units)
	}
	if len(c.Logs) != 1 || c.Logs[0].ID != "wd" {
		t.Errorf("logs %+v", c.Logs)
	}
	if len(c.TCPTargets) != 1 || c.TCPTargets[0] != "10.0.0.2:9000" || len(c.Ports) != 1 || c.Ports[0] != 2053 {
		t.Errorf("targets %v ports %v", c.TCPTargets, c.Ports)
	}
	if p.Remnawave == nil || p.Remnawave.Name != "Relay-Node" || strings.Join(p.Remnawave.Actions, ",") != "rw.node-disable,rw.node-restart" {
		t.Errorf("remnawave %+v", p.Remnawave)
	}
	if p.ConfigHash == "" || p.ConfigHash != c.Hash() {
		t.Error("config hash missing")
	}
	cfg, err := OpsConfig(b, "relay")
	if err != nil || cfg.Node != "relay" {
		t.Fatalf("OpsConfig: %v", err)
	}
	if _, err := OpsConfig(b, "test"); err == nil {
		t.Error("a node without ops has no runner config")
	}
}

func TestOpsValidation(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"restart and never": {"- { name: app, restart: true, cooldown: 5m }", "- { name: app, restart: true, never_restart: true }", "both restart and never_restart"},
		"no runner":         {"runner: true", "runner: false", "need runner: true"},
		"verify template":   {`verify: ["link.e-{cc}"]`, `verify: ["link.x-{cc}"]`, "link.x-de does not exist"},
		"verify pattern":    {`verify: ["link.e-{cc}"]`, `verify: ["link.e-{relay}"]`, "must be an enum parameter"},
		"rollback unknown":  {`rollback: { script: select,`, `rollback: { script: nope,`, `"nope" is not a change script`},
		"before missing":    {`read_before: { script: current, args: { cc: "{cc}" } }`, ``, "without read_before"},
		"rollback literal":  {`relay: "{before.relay}"`, `relay: "x;y"`, "not accepted"},
		"remnawave check":   {"- { id: rw, collector: remnawave-node, node: Relay-Node }", "", "remnawave-node check"},
		"bad log path":      {"containers:\n        - { name: app, restart: true, cooldown: 5m }", "logs: [{ id: s, path: /etc/mullvad-ns/de.log }]", "never readable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(opsBase, c.from) {
				t.Fatalf("fixture has no %q", c.from)
			}
			_, ds := loadOps(t, strings.Replace(opsBase, c.from, c.to, 1))
			var msgs []string
			for _, d := range ds {
				if d.Severity == "error" {
					msgs = append(msgs, d.Message)
				}
			}
			if !strings.Contains(strings.Join(msgs, "\n"), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, msgs)
			}
		})
	}
}

// The real map must keep resolving, with today's decisions intact.
func TestRepoOpsPolicy(t *testing.T) {
	root, err := FindRoot(".")
	if err != nil {
		t.Skip(err)
	}
	b, ds := Load(Options{Root: root})
	if ds.HasErrors() {
		t.Fatalf("repo topology has errors: %v", ds)
	}
	for _, p := range b.Ops {
		if p.Config == nil {
			continue
		}
		for _, n := range p.NeverRestart {
			kind, name, _ := strings.Cut(n, ":")
			for _, c := range p.Config.Containers {
				if kind == "container" && c.Name == name && c.Restart {
					t.Errorf("%s: %s is never_restart but restartable", p.Node, n)
				}
			}
			for _, u := range p.Config.Units {
				if kind == "unit" && u.Name == name && u.Restart {
					t.Errorf("%s: %s is never_restart but restartable", p.Node, n)
				}
			}
		}
	}
}
