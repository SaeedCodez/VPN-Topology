package topology

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vpnmap/pkg/agentcfg"
)

// repoBundle loads the repository's own topology (../../topology).
func repoBundle(t *testing.T) *Bundle {
	t.Helper()
	root, err := FindRoot(".")
	if err != nil {
		t.Skip("repository topology not found")
	}
	b, ds := Load(Options{Root: root})
	if ds.HasErrors() {
		t.Fatalf("repo topology has errors: %v", ds)
	}
	return b
}

func genCfg(t *testing.T, b *Bundle, node string) *agentcfg.Config {
	t.Helper()
	cfg, err := AgentConfig(b, node, "")
	if err != nil {
		t.Fatalf("AgentConfig(%s): %v", node, err)
	}
	return cfg
}

func findCounter(cfg *agentcfg.Config, name string) *agentcfg.NftCounter {
	if cfg.Nft == nil {
		return nil
	}
	for i := range cfg.Nft.Counters {
		if cfg.Nft.Counters[i].Name == name {
			return &cfg.Nft.Counters[i]
		}
	}
	return nil
}

func TestAgentConfigRelay1(t *testing.T) {
	b := repoBundle(t)
	cfg := genCfg(t, b, "relay-1")
	if cfg.Hub != "https://map.imdev.ir" || cfg.Node != "relay-1" || cfg.TokenFile != AgentTokenFile {
		t.Errorf("header: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Units, []string{"realm.service"}) {
		t.Errorf("units = %v", cfg.Units)
	}
	up := findCounter(cfg, "l_e1_traffic_up")
	if up == nil || up.Hook != "input" || up.Proto != "tcp" || up.Dport != 2053 {
		t.Errorf("l_e1_traffic_up = %+v", up)
	}
	e2 := findCounter(cfg, "l_e2_traffic_up")
	if e2 == nil || e2.Hook != "output" || e2.Daddr != "81.12.50.23" || e2.Dport != 2053 {
		t.Errorf("l_e2_traffic_up = %+v", e2)
	}
	if findCounter(cfg, "l_e3_traffic_fallback_up") != nil {
		t.Error("relay-1 must not get the panel's counters")
	}
	wantSS := []agentcfg.SSConfig{
		{ID: "link-e1-conns-2053", Port: 2053, Side: "local"},
		{ID: "link-e2-conns-2053", Port: 2053, Side: "remote"},
		{ID: "node-relay-1-conns-2053", Port: 2053, Side: "local"},
	}
	if !reflect.DeepEqual(cfg.SS, wantSS) {
		t.Errorf("ss = %+v", cfg.SS)
	}
	if len(cfg.TCP) != 1 || cfg.TCP[0].Target != "81.12.50.23:2053" {
		t.Errorf("tcp = %+v", cfg.TCP)
	}
	if !reflect.DeepEqual(LocalCheckIDs(cfg), []string{"relay-forwards"}) {
		t.Errorf("checks = %+v", cfg.Checks)
	}
	if cfg.Probe != nil || len(cfg.Prom) != 0 {
		t.Error("relay-1 has no probe or prom")
	}
}

func TestAgentConfigPanel(t *testing.T) {
	b := repoBundle(t)
	cfg := genCfg(t, b, "panel")
	if len(cfg.Prom) != 1 || cfg.Prom[0].ID != "remnawave" || cfg.Prom[0].URL != RemnawaveLocalURL || cfg.Prom[0].AuthFile != AgentPromAuthFile || len(cfg.Prom[0].Metrics) != 5 {
		t.Errorf("prom = %+v", cfg.Prom)
	}
	for _, c := range []string{"remnanode", "remnawave", "remnawave-db", "remnawave-nginx", "remnawave-redis"} {
		found := false
		for _, x := range cfg.Containers {
			found = found || x == c
		}
		if !found {
			t.Errorf("container %s missing from %v", c, cfg.Containers)
		}
	}
	fb := findCounter(cfg, NftName("link:e3:traffic-fallback", "down"))
	if fb == nil || fb.Saddr != "31.56.89.125" || fb.Sport != 9000 || fb.Hook != "input" {
		t.Errorf("e3 fallback down = %+v", fb)
	}
	if len(cfg.HTTP) != 1 || cfg.HTTP[0].ID != "node-panel-api" || cfg.HTTP[0].URL != "http://127.0.0.1:3000/api/auth/status" || cfg.HTTP[0].Timeout.D().Seconds() != 5 {
		t.Errorf("http = %+v", cfg.HTTP)
	}
	if len(cfg.Logs) != 1 || cfg.Logs[0].ID != "node-panel-watchdog" || cfg.Logs[0].Path != "/var/log/remnawave-watchdog.log" || cfg.Logs[0].Match != "" {
		t.Errorf("logs = %+v", cfg.Logs)
	}
	if !reflect.DeepEqual(LocalCheckIDs(cfg), []string{"remnawave-backup-age"}) || cfg.Checks[0].Interval.D().Minutes() != 1 {
		t.Errorf("checks = %+v", cfg.Checks)
	}
	if len(cfg.TCP) != 1 || cfg.TCP[0].Target != "31.56.89.125:9000" {
		t.Errorf("tcp = %+v", cfg.TCP)
	}
	var ss []string
	for _, s := range cfg.SS {
		ss = append(ss, s.ID+"/"+s.Side)
	}
	if !reflect.DeepEqual(ss, []string{"link-e3-conns-9000/remote", "node-local-node-conns-2053/local"}) {
		t.Errorf("ss = %v", ss)
	}
}

func TestAgentConfigIranMullvad(t *testing.T) {
	b := repoBundle(t)
	cfg := genCfg(t, b, "iran-mullvad")
	want := []agentcfg.WGConfig{{Iface: "wg-game"}, {Netns: "mv-de", Iface: "wg-de"}, {Netns: "mv-tr", Iface: "wg-tr"}, {Netns: "mv-us", Iface: "wg-us"}}
	if !reflect.DeepEqual(cfg.WG, want) {
		t.Errorf("wg = %+v (wg-transfer and wg-handshake must be deduplicated)", cfg.WG)
	}
	up := findCounter(cfg, "l_e7_traffic_up")
	if up == nil || !reflect.DeepEqual(up.Dports, []int{443, 8443, 2053}) || up.Dport != 0 {
		t.Errorf("a port list must become dports: %+v", up)
	}
	down := findCounter(cfg, "l_e7_traffic_down")
	if down == nil || !reflect.DeepEqual(down.Sports, []int{443, 8443, 2053}) {
		t.Errorf("down = %+v", down)
	}
	ids := map[string]bool{}
	for _, s := range cfg.SS {
		ids[s.ID] = true
	}
	for _, id := range []string{"link-e7-conns-443", "link-e7-conns-8443", "link-e7-conns-2053", "node-iran-mullvad-conns-443"} {
		if !ids[id] {
			t.Errorf("ss id %s missing", id)
		}
	}
	if len(cfg.Units) != 3 || cfg.Units[0] != "mullvad-ns@de.service" {
		t.Errorf("units = %v", cfg.Units)
	}
}

func TestAgentConfigProbe(t *testing.T) {
	b := repoBundle(t)
	cfg := genCfg(t, b, "probe")
	if cfg.Probe == nil || cfg.Probe.SingBox != AgentSingBox {
		t.Fatalf("probe section missing: %+v", cfg.Probe)
	}
	var routes []string
	for _, r := range cfg.Probe.Routes {
		routes = append(routes, r.Route+":"+r.ExpectCountry)
		if r.OutboundFile != "/etc/vpnmap-agent/probe/"+r.Route+".json" {
			t.Errorf("outbound file %s", r.OutboundFile)
		}
	}
	if !reflect.DeepEqual(routes, []string{"main:FR", "mullvad-de:DE", "mullvad-tr:TR", "mullvad-us:US"}) {
		t.Errorf("routes = %v", routes)
	}
	targets := map[string]bool{}
	for _, x := range cfg.TCP {
		targets[x.Target] = true
	}
	for _, want := range []string{"relay-1.aitopology.ir:2053", "92.42.207.243:8443", "wp.api.mohiraweb.ir:443", "81.12.50.23:443"} {
		if !targets[want] {
			t.Errorf("probe tcp target %s missing", want)
		}
	}
	// The subscription route's probe is an http-probe run from the probe.
	if len(cfg.HTTP) != 1 || cfg.HTTP[0].ID != "route-subscription-probe" {
		t.Errorf("http = %+v", cfg.HTTP)
	}
	if len(cfg.Units)+len(cfg.Containers)+len(cfg.SS) != 0 || cfg.Nft != nil {
		t.Error("the probe has no host collectors")
	}
}

// Every generated config passes Validate, survives agentcfg.Load (strict
// fields) with the same sha, and is byte-for-byte deterministic.
func TestAgentConfigsRoundTrip(t *testing.T) {
	b := repoBundle(t)
	hosts := b.AgentHosts()
	if len(hosts) < 5 {
		t.Fatalf("agent hosts = %v", hosts)
	}
	dir := t.TempDir()
	for _, n := range hosts {
		cfg := genCfg(t, b, n)
		raw, sha, err := AgentConfigYAML(cfg)
		if err != nil {
			t.Fatal(err)
		}
		again, sha2, _ := AgentConfigYAML(genCfg(t, b, n))
		if sha != sha2 || string(raw) != string(again) {
			t.Errorf("%s: output is not deterministic", n)
		}
		p := filepath.Join(dir, n+".yaml")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		_, loadedSHA, err := agentcfg.Load(p)
		if err != nil {
			t.Errorf("%s: agentcfg.Load: %v", n, err)
		}
		if loadedSHA != sha {
			t.Errorf("%s: sha %s != loaded %s", n, sha, loadedSHA)
		}
		if cfg.Nft != nil {
			if out := agentcfg.RenderNft(cfg.Nft.Counters); !strings.Contains(out, "counter name") {
				t.Errorf("%s: nft render empty", n)
			}
		}
	}
}

func TestAgentConfigErrors(t *testing.T) {
	b := repoBundle(t)
	for node, want := range map[string]string{
		"users":   "has no agent",
		"relay-9": "unknown node",
		"hub":     "runs its own checks",
	} {
		if _, err := AgentConfig(b, node, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("AgentConfig(%s) err = %v, want %q", node, err, want)
		}
	}
	cfg, err := AgentConfig(b, "relay-1", "http://localhost:4100/")
	if err != nil || cfg.Hub != "http://localhost:4100" {
		t.Errorf("hub override: %v %v", cfg, err)
	}
	if _, err := AgentConfig(b, "relay-1", "http://map.example.com"); err == nil {
		t.Error("plain http hub must be rejected by Validate")
	}
	b2 := *b
	b2.Map.Settings.Hub = ""
	if _, err := AgentConfig(&b2, "relay-1", ""); err == nil || !strings.Contains(err.Error(), "settings.hub") {
		t.Errorf("missing hub: %v", err)
	}
}
