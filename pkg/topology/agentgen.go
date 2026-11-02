package topology

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"vpnmap/pkg/agentcfg"
)

// Fixed paths on the servers. They match apps/agent/deploy/install.sh.
const (
	AgentTokenFile     = "/etc/vpnmap-agent/token"
	AgentChecksDir     = "/etc/vpnmap-agent/checks.d"
	AgentProbeDir      = "/etc/vpnmap-agent/probe"
	AgentPromAuthFile  = "/etc/vpnmap-agent/remnawave-metrics.auth"
	AgentSingBox       = "/usr/local/bin/sing-box"
	AgentPushInterval  = 5 * time.Second
	RemnawaveLocalURL  = "http://127.0.0.1:3001/metrics"
	DefaultXbridgerChk = "xbridger-doctor"
)

// RemnawaveMetrics are the only panel metrics the panel agent forwards.
var RemnawaveMetrics = []string{
	"remnawave_node_basic_info",
	"remnawave_node_outbound_upload_bytes",
	"remnawave_node_outbound_download_bytes",
	"remnawave_node_inbound_upload_bytes",
	"remnawave_node_inbound_download_bytes",
}

// AllChecks returns every check spec of the bundle (node checks and metrics,
// link traffic + fallback, conns and health, route probe and users).
func (b *Bundle) AllChecks() []*Check {
	var out []*Check
	add := func(c *Check) {
		if c != nil {
			out = append(out, c)
		}
	}
	for i := range b.Nodes {
		n := &b.Nodes[i]
		for j := range n.Checks {
			add(&n.Checks[j])
		}
		for j := range n.Metrics {
			add(&n.Metrics[j])
		}
	}
	for i := range b.Links {
		l := &b.Links[i]
		if l.Traffic != nil {
			add(l.Traffic)
			add(l.Traffic.Fallback)
		}
		add(l.Conns)
		for j := range l.Health {
			add(&l.Health[j])
		}
	}
	for i := range b.Routes {
		add(b.Routes[i].Probe)
		add(b.Routes[i].Users)
	}
	return out
}

// AgentHosts returns the ids that run vpnmap-agent: every node with
// agent: true, plus the probe source when anything runs on it.
func (b *Bundle) AgentHosts() []string {
	var out []string
	for _, n := range b.Nodes {
		if n.Agent {
			out = append(out, n.ID)
		}
	}
	if b.usesProbe() {
		out = append(out, "probe")
	}
	sort.Strings(out)
	return out
}

func (b *Bundle) usesProbe() bool {
	if !b.isSource("probe") {
		return false
	}
	for _, c := range b.AllChecks() {
		if c.Collector == "e2e-probe" || ((c.Collector == "tcp-connect" || c.Collector == "http-probe") && c.Str("from") == "probe") {
			return true
		}
	}
	return false
}

func (b *Bundle) isSource(id string) bool {
	for _, s := range b.Map.Sources {
		if s.ID == id {
			return true
		}
	}
	return false
}

// AgentConfig derives /etc/vpnmap-agent/agent.yaml for one node (or for the
// probe source) from every check, metric, traffic, conns, health and probe
// spec in the bundle. hubURL overrides settings.hub. The output is sorted and
// deduplicated so the same topology always gives the same file (and sha).
func AgentConfig(b *Bundle, node, hubURL string) (*agentcfg.Config, error) {
	if b == nil {
		return nil, fmt.Errorf("no topology")
	}
	hub := strings.TrimRight(firstNonEmptyStr(hubURL, b.Map.Settings.Hub), "/")
	if hub == "" {
		return nil, fmt.Errorf("no hub URL: set settings.hub in topology/map.yaml or pass --hub")
	}
	probeMode := false
	switch n := b.NodeByID(node); {
	case node == "hub":
		return nil, fmt.Errorf(`"hub" runs its own checks; agent configs are for nodes with agent: true and for the probe`)
	case n != nil && !n.Agent:
		return nil, fmt.Errorf("node %q has no agent: set `agent: true` in %s:%d first (then run `vpnmap plan`)", node, n.Defined.File, n.Defined.Line)
	case n == nil && b.isSource(node):
		probeMode = true
	case n == nil:
		return nil, fmt.Errorf("unknown node %q: %s", node, didYouMean(node, b.AgentHosts(), "agent node"))
	}

	cfg := &agentcfg.Config{
		Hub:       hub,
		Node:      node,
		TokenFile: AgentTokenFile,
		Interval:  agentcfg.Duration(AgentPushInterval),
	}
	g := newGen()
	for _, c := range b.AllChecks() {
		if err := g.add(c, node); err != nil {
			return nil, err
		}
	}
	if probeMode && node == "probe" {
		for _, r := range b.Routes {
			if r.Probe == nil || r.Probe.Collector != "e2e-probe" {
				continue
			}
			g.probe = append(g.probe, agentcfg.ProbeRoute{
				Route:         r.ID,
				OutboundFile:  AgentProbeDir + "/" + r.ID + ".json",
				ExpectCountry: r.Probe.Str("expect_country"),
			})
			iv := r.Probe.Dur("interval", 30*time.Second)
			if g.probeInterval == 0 || iv < g.probeInterval {
				g.probeInterval = iv
			}
		}
	}
	g.fill(cfg)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agent config for %s: %w", node, err)
	}
	return cfg, nil
}

// AgentConfigYAML renders the config and its config_sha (as the agent computes it).
func AgentConfigYAML(cfg *agentcfg.Config) ([]byte, string, error) {
	raw, err := cfg.Marshal()
	if err != nil {
		return nil, "", err
	}
	return raw, agentcfg.SHA(raw), nil
}

// gen accumulates config entries with deduplication.
type gen struct {
	units, containers map[string]bool
	nft               map[string]agentcfg.NftCounter
	wg                map[string]agentcfg.WGConfig
	ss                map[string]agentcfg.SSConfig
	tcp               map[string]agentcfg.TCPTarget
	http              map[string]agentcfg.HTTPTarget
	logs              map[string]agentcfg.LogConfig
	checks            map[string]agentcfg.LocalCheck
	prom              bool
	probe             []agentcfg.ProbeRoute
	probeInterval     time.Duration
}

func newGen() *gen {
	return &gen{
		units: map[string]bool{}, containers: map[string]bool{},
		nft: map[string]agentcfg.NftCounter{}, wg: map[string]agentcfg.WGConfig{},
		ss: map[string]agentcfg.SSConfig{}, tcp: map[string]agentcfg.TCPTarget{},
		http: map[string]agentcfg.HTTPTarget{}, logs: map[string]agentcfg.LogConfig{},
		checks: map[string]agentcfg.LocalCheck{},
	}
}

func (g *gen) add(c *Check, node string) error {
	host := c.Str("host")
	from := c.Str("from")
	switch c.Collector {
	case "systemd-unit":
		if host == node {
			g.units[SystemdUnitName(c)] = true
		}
	case "docker-container":
		if host == node {
			g.containers[c.Str("container")] = true
		}
	case "nft-counter":
		if host != node {
			return nil
		}
		for _, dir := range []string{"up", "down"} {
			m := c.Map(dir)
			if m == nil {
				continue
			}
			nc, err := nftCounter(NftName(c.Key, dir), m)
			if err != nil {
				return fmt.Errorf("%s (%s): %v", c.Key, dir, err)
			}
			g.nft[nc.Name] = nc
		}
	case "wg-transfer", "wg-handshake":
		if host == node {
			w := agentcfg.WGConfig{Netns: c.Str("netns"), Iface: c.Str("interface")}
			g.wg[w.Netns+"/"+w.Iface] = w
		}
	case "ss-conns":
		if host != node {
			return nil
		}
		side := c.Str("side")
		if side == "" {
			side = "local"
		}
		ports := intList(c.Params["ports"])
		if len(ports) == 0 {
			ports = intList(c.Params["port"])
		}
		for _, p := range ports {
			s := agentcfg.SSConfig{ID: SSID(c.Key, p), Port: p, Side: side}
			g.ss[s.ID] = s
		}
	case "tcp-connect":
		if from != node {
			return nil
		}
		t := agentcfg.TCPTarget{
			Target:   c.Str("target"),
			Interval: agentcfg.Duration(c.Dur("interval", 15*time.Second)),
			Timeout:  agentcfg.Duration(c.Dur("timeout", 3*time.Second)),
		}
		if old, ok := g.tcp[t.Target]; ok {
			// The hub finds results by target: one entry, the most demanding settings.
			t.Interval = min(t.Interval, old.Interval)
			t.Timeout = max(t.Timeout, old.Timeout)
		}
		g.tcp[t.Target] = t
	case "http-probe":
		if from != node {
			return nil
		}
		h := agentcfg.HTTPTarget{
			ID:       HTTPID(c.Key),
			URL:      c.Str("url"),
			Interval: agentcfg.Duration(c.Dur("interval", 30*time.Second)),
			Timeout:  agentcfg.Duration(c.Dur("max_latency", 10*time.Second)),
			PathFile: c.Str("path_file"),
		}
		g.http[h.ID] = h
	case "log-match":
		if host == node {
			l := agentcfg.LogConfig{ID: LogID(c.Key), Path: c.Str("path")}
			g.logs[l.ID] = l
		}
	case "local-check":
		if host == node {
			g.addCheck(c.Str("check"), c.Dur("interval", 30*time.Second))
		}
	case "xbridger-connection":
		if host == node {
			g.addCheck(firstNonEmptyStr(c.Str("check"), DefaultXbridgerChk), 30*time.Second)
		}
	case "xray-stats":
		if host == node {
			g.prom = true
		}
	}
	return nil
}

func (g *gen) addCheck(id string, interval time.Duration) {
	lc := agentcfg.LocalCheck{ID: id, Interval: agentcfg.Duration(interval)}
	if old, ok := g.checks[id]; ok {
		lc.Interval = min(lc.Interval, old.Interval)
	}
	g.checks[id] = lc
}

func (g *gen) fill(cfg *agentcfg.Config) {
	cfg.Units = sortedKeys(g.units)
	cfg.Containers = sortedKeys(g.containers)
	if len(g.nft) > 0 {
		cfg.Nft = &agentcfg.NftConfig{}
		for _, k := range sortedKeys(g.nft) {
			cfg.Nft.Counters = append(cfg.Nft.Counters, g.nft[k])
		}
	}
	for _, k := range sortedKeys(g.wg) {
		cfg.WG = append(cfg.WG, g.wg[k])
	}
	for _, k := range sortedKeys(g.ss) {
		cfg.SS = append(cfg.SS, g.ss[k])
	}
	for _, k := range sortedKeys(g.tcp) {
		cfg.TCP = append(cfg.TCP, g.tcp[k])
	}
	for _, k := range sortedKeys(g.http) {
		cfg.HTTP = append(cfg.HTTP, g.http[k])
	}
	for _, k := range sortedKeys(g.logs) {
		cfg.Logs = append(cfg.Logs, g.logs[k])
	}
	if g.prom {
		cfg.Prom = []agentcfg.PromTarget{{
			ID:       PromRemnawave,
			URL:      RemnawaveLocalURL,
			AuthFile: AgentPromAuthFile,
			Metrics:  append([]string(nil), RemnawaveMetrics...),
		}}
	}
	for _, k := range sortedKeys(g.checks) {
		cfg.Checks = append(cfg.Checks, g.checks[k])
	}
	if len(g.probe) > 0 {
		sort.Slice(g.probe, func(i, j int) bool { return g.probe[i].Route < g.probe[j].Route })
		cfg.Probe = &agentcfg.ProbeConfig{SingBox: AgentSingBox, Interval: agentcfg.Duration(g.probeInterval), Routes: g.probe}
	}
}

// nftCounter converts an nft match ({hook, proto, saddr, daddr, sport,
// dport, iif, oif}) into a counter. A YAML list of ports becomes sports/dports.
func nftCounter(name string, m map[string]any) (agentcfg.NftCounter, error) {
	str := func(k string) string {
		if v, ok := m[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	n := agentcfg.NftCounter{
		Name: name, Hook: str("hook"), Proto: str("proto"),
		Saddr: str("saddr"), Daddr: str("daddr"), Iif: str("iif"), Oif: str("oif"),
	}
	for _, k := range []string{"sport", "dport"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		ports := intList(v)
		if len(ports) == 0 {
			return n, fmt.Errorf("%s %v is not a port number", k, v)
		}
		_, isList := v.([]any)
		switch {
		case k == "sport" && isList:
			n.Sports = ports
		case k == "sport":
			n.Sport = ports[0]
		case isList:
			n.Dports = ports
		default:
			n.Dport = ports[0]
		}
	}
	return n, nil
}

// intList reads a port or a list of ports from YAML (int) or JSON (float64) values.
func intList(v any) []int {
	switch t := v.(type) {
	case nil:
		return nil
	case int:
		return []int{t}
	case int64:
		return []int{int(t)}
	case float64:
		return []int{int(t)}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return []int{n}
		}
	case []int:
		return append([]int(nil), t...)
	case []any:
		var out []int
		for _, e := range t {
			out = append(out, intList(e)...)
		}
		return out
	}
	return nil
}

func firstNonEmptyStr(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// SystemdUnitName returns the unit of a systemd-unit check. The YAML key
// `unit:` is also the reserved metric-unit key of every check, so the loader
// stores it in Check.Unit instead of Params["unit"]; accept both.
func SystemdUnitName(c *Check) string {
	return firstNonEmptyStr(c.Str("unit"), c.Unit)
}

// LocalCheckIDs returns the local check ids an agent config needs in checks.d.
func LocalCheckIDs(cfg *agentcfg.Config) []string {
	var out []string
	for _, c := range cfg.Checks {
		out = append(out, c.ID)
	}
	return out
}
