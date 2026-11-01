// Package agentcfg defines /etc/vpnmap-agent/agent.yaml, the local
// configuration of one agent (or the probe). The file is generated from the
// topology by `vpnmap agent config <node>` and installed by the owner. The hub
// cannot change it: the agent only ever reads its own local file.
package agentcfg

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the agent's local configuration.
type Config struct {
	// Hub is the base URL of the hub, e.g. https://map.imdev.ir
	Hub string `yaml:"hub" json:"hub"`
	// Node is this server's topology node id, e.g. "relay-1".
	Node string `yaml:"node" json:"node"`
	// TokenFile holds the per-agent push token (mode 0600, owned by the agent user).
	TokenFile string `yaml:"token_file" json:"token_file"`
	// Interval between pushes. Default 5s.
	Interval Duration `yaml:"interval" json:"interval"`

	Units      []string     `yaml:"units,omitempty" json:"units,omitempty"`
	Containers []string     `yaml:"containers,omitempty" json:"containers,omitempty"`
	Nft        *NftConfig   `yaml:"nft,omitempty" json:"nft,omitempty"`
	WG         []WGConfig   `yaml:"wg,omitempty" json:"wg,omitempty"`
	SS         []SSConfig   `yaml:"ss,omitempty" json:"ss,omitempty"`
	TCP        []TCPTarget  `yaml:"tcp,omitempty" json:"tcp,omitempty"`
	HTTP       []HTTPTarget `yaml:"http,omitempty" json:"http,omitempty"`
	Logs       []LogConfig  `yaml:"logs,omitempty" json:"logs,omitempty"`
	// Prom scrapes local Prometheus endpoints (e.g. the Remnawave panel's
	// 127.0.0.1:3001/metrics) and forwards only allow-listed metric names.
	Prom []PromTarget `yaml:"prom,omitempty" json:"prom,omitempty"`
	// ChecksDir holds local read-only checks (<id>.check, root-owned).
	ChecksDir string `yaml:"checks_dir,omitempty" json:"checks_dir,omitempty"`
	// Checks lists the local check ids this agent runs, with their intervals.
	Checks []LocalCheck `yaml:"checks,omitempty" json:"checks,omitempty"`
	// Probe holds the end-to-end routes (probe mode only).
	Probe *ProbeConfig `yaml:"probe,omitempty" json:"probe,omitempty"`
}

// NftConfig lists the counters of table `inet vpnmap`. The rules themselves
// are installed by the installer from /etc/vpnmap-agent/vpnmap.nft.
type NftConfig struct {
	Counters []NftCounter `yaml:"counters" json:"counters"`
}

// NftCounter is one counter-only rule. All fields are validated so they can
// be rendered into nft syntax safely; there is no free-form expression.
type NftCounter struct {
	Name  string `yaml:"name" json:"name"`                       // [a-z0-9_]{1,63}
	Hook  string `yaml:"hook" json:"hook"`                       // input | output | forward
	Proto string `yaml:"proto,omitempty" json:"proto,omitempty"` // tcp | udp
	Saddr string `yaml:"saddr,omitempty" json:"saddr,omitempty"` // IPv4 or CIDR
	Daddr string `yaml:"daddr,omitempty" json:"daddr,omitempty"`
	Sport int    `yaml:"sport,omitempty" json:"sport,omitempty"`
	Dport int    `yaml:"dport,omitempty" json:"dport,omitempty"`
	// Sports/Dports match any of several ports (rendered as an nft set).
	Sports []int  `yaml:"sports,omitempty" json:"sports,omitempty"`
	Dports []int  `yaml:"dports,omitempty" json:"dports,omitempty"`
	Iif    string `yaml:"iif,omitempty" json:"iif,omitempty"` // interface name
	Oif    string `yaml:"oif,omitempty" json:"oif,omitempty"`
}

// WGConfig selects a WireGuard interface, optionally inside a netns.
type WGConfig struct {
	Netns string `yaml:"netns,omitempty" json:"netns,omitempty"`
	Iface string `yaml:"iface" json:"iface"`
}

// SSConfig counts established TCP sockets for a port.
type SSConfig struct {
	ID   string `yaml:"id" json:"id"`
	Port int    `yaml:"port" json:"port"`
	Side string `yaml:"side" json:"side"` // local | remote
}

// TCPTarget is a TCP connect check done from this server.
type TCPTarget struct {
	Target   string   `yaml:"target" json:"target"` // host:port
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// HTTPTarget is an HTTP(S) GET done from this server (read-only).
type HTTPTarget struct {
	ID       string   `yaml:"id" json:"id"`
	URL      string   `yaml:"url" json:"url"`
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// LogConfig tails a log file and sends new lines (redacted).
type LogConfig struct {
	ID   string `yaml:"id" json:"id"`
	Path string `yaml:"path" json:"path"`
	// Match, if set, only forwards lines matching this RE2 expression.
	Match string `yaml:"match,omitempty" json:"match,omitempty"`
}

// PromTarget is a local Prometheus text endpoint. Only metrics whose name
// is in Metrics are forwarded. Basic-auth credentials, if needed, are read
// from AuthFile ("user:password", mode 0640 root:vpnmap-agent), a copy the
// owner makes; the agent never reads application .env files.
type PromTarget struct {
	ID       string   `yaml:"id" json:"id"`
	URL      string   `yaml:"url" json:"url"`
	AuthFile string   `yaml:"auth_file,omitempty" json:"auth_file,omitempty"`
	Metrics  []string `yaml:"metrics" json:"metrics"`
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
}

// LocalCheck runs /etc/vpnmap-agent/checks.d/<id>.check through the helper.
type LocalCheck struct {
	ID       string   `yaml:"id" json:"id"`
	Interval Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// ProbeConfig is used by the probe (vpnmap-agent --mode probe).
type ProbeConfig struct {
	// SingBox is the path of the sing-box binary.
	SingBox string `yaml:"sing_box" json:"sing_box"`
	// URL fetched through each route. Default http://cp.cloudflare.com/generate_204
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
	// TraceURL returns the exit IP and country. Default https://www.cloudflare.com/cdn-cgi/trace
	TraceURL string       `yaml:"trace_url,omitempty" json:"trace_url,omitempty"`
	Interval Duration     `yaml:"interval,omitempty" json:"interval,omitempty"`
	Routes   []ProbeRoute `yaml:"routes" json:"routes"`
}

// ProbeRoute is one route probed through its own sing-box outbound.
// The outbound JSON (with the monitoring user's credentials) lives in a local
// file only; it is never in the repo and never sent to the hub.
type ProbeRoute struct {
	Route         string `yaml:"route" json:"route"`
	OutboundFile  string `yaml:"outbound_file" json:"outbound_file"`
	ExpectCountry string `yaml:"expect_country,omitempty" json:"expect_country,omitempty"`
}

// Duration is a time.Duration that marshals as "5s".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return nil, nil
	}
	return time.Duration(d).String(), nil
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q (use e.g. 5s, 2m)", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

var (
	reName  = regexp.MustCompile(`^[a-z0-9_]{1,63}$`)
	reID    = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	reIface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	reAddr  = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}(/[0-9]{1,2})?$`)
	reUnit  = regexp.MustCompile(`^[A-Za-z0-9@_.:-]{1,128}$`)
	reCtr   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// ValidID reports whether s is a valid local check / log / ss id.
func ValidID(s string) bool { return reID.MatchString(s) }

// Validate checks every field so the config can be rendered and used safely.
func (c *Config) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if !strings.HasPrefix(c.Hub, "https://") && !strings.HasPrefix(c.Hub, "http://127.0.0.1") && !strings.HasPrefix(c.Hub, "http://localhost") {
		add("hub must be an https:// URL")
	}
	if !reID.MatchString(c.Node) {
		add("node %q is not a valid id", c.Node)
	}
	if c.TokenFile == "" {
		add("token_file is required")
	}
	for _, u := range c.Units {
		if !reUnit.MatchString(u) {
			add("unit %q is not a valid systemd unit name", u)
		}
	}
	for _, ct := range c.Containers {
		if !reCtr.MatchString(ct) {
			add("container %q is not a valid container name", ct)
		}
	}
	if c.Nft != nil {
		seen := map[string]bool{}
		for _, n := range c.Nft.Counters {
			if err := n.Validate(); err != nil {
				add("nft counter %q: %v", n.Name, err)
			}
			if seen[n.Name] {
				add("nft counter %q is defined twice", n.Name)
			}
			seen[n.Name] = true
		}
	}
	for _, w := range c.WG {
		if !reIface.MatchString(w.Iface) || (w.Netns != "" && !reIface.MatchString(w.Netns)) {
			add("wg %s/%s: invalid interface or netns name", w.Netns, w.Iface)
		}
	}
	for _, s := range c.SS {
		if !reID.MatchString(s.ID) || s.Port <= 0 || s.Port > 65535 || (s.Side != "local" && s.Side != "remote") {
			add("ss %q: needs id, port 1-65535 and side local|remote", s.ID)
		}
	}
	for _, t := range c.TCP {
		if !validHostPort(t.Target) {
			add("tcp target %q must be host:port", t.Target)
		}
	}
	for _, h := range c.HTTP {
		if !reID.MatchString(h.ID) || !(strings.HasPrefix(h.URL, "https://") || strings.HasPrefix(h.URL, "http://")) {
			add("http %q: needs an id and an http(s) URL", h.ID)
		}
	}
	for _, l := range c.Logs {
		if !reID.MatchString(l.ID) || !strings.HasPrefix(l.Path, "/") {
			add("log %q: needs an id and an absolute path", l.ID)
		}
		if l.Match != "" {
			if _, err := regexp.Compile(l.Match); err != nil {
				add("log %q: invalid match regexp: %v", l.ID, err)
			}
		}
	}
	for _, p := range c.Prom {
		if !reID.MatchString(p.ID) || !(strings.HasPrefix(p.URL, "http://127.0.0.1") || strings.HasPrefix(p.URL, "http://localhost")) {
			add("prom %q: needs an id and a local http://127.0.0.1 URL", p.ID)
		}
		if len(p.Metrics) == 0 {
			add("prom %q: list the metric names to forward", p.ID)
		}
	}
	for _, ch := range c.Checks {
		if !reID.MatchString(ch.ID) {
			add("local check id %q is invalid", ch.ID)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid agent config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func validHostPort(s string) bool {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return false
	}
	host, port := s[:i], s[i+1:]
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return regexp.MustCompile(`^[A-Za-z0-9.-]+$`).MatchString(host)
}

// Validate checks one counter rule.
func (n NftCounter) Validate() error {
	if !reName.MatchString(n.Name) {
		return fmt.Errorf("name must match [a-z0-9_]{1,63}")
	}
	switch n.Hook {
	case "input", "output", "forward":
	default:
		return fmt.Errorf("hook must be input, output or forward")
	}
	if n.Proto != "" && n.Proto != "tcp" && n.Proto != "udp" {
		return fmt.Errorf("proto must be tcp or udp")
	}
	if (n.Sport != 0 || n.Dport != 0 || len(n.Sports) > 0 || len(n.Dports) > 0) && n.Proto == "" {
		return fmt.Errorf("sport/dport need proto")
	}
	for _, a := range []string{n.Saddr, n.Daddr} {
		if a != "" && !reAddr.MatchString(a) {
			return fmt.Errorf("address %q must be IPv4 or IPv4/CIDR", a)
		}
	}
	if (n.Sport != 0 && len(n.Sports) > 0) || (n.Dport != 0 && len(n.Dports) > 0) {
		return fmt.Errorf("use either sport or sports (dport or dports), not both")
	}
	ports := append([]int{n.Sport, n.Dport}, append(append([]int{}, n.Sports...), n.Dports...)...)
	for _, p := range ports {
		if p < 0 || p > 65535 {
			return fmt.Errorf("port out of range")
		}
	}
	for _, i := range []string{n.Iif, n.Oif} {
		if i != "" && !reIface.MatchString(i) {
			return fmt.Errorf("interface %q is invalid", i)
		}
	}
	return nil
}

// Rule renders the counter as an nft rule body (without "add rule ...").
func (n NftCounter) Rule() string {
	var p []string
	if n.Iif != "" {
		p = append(p, fmt.Sprintf("iifname %q", n.Iif))
	}
	if n.Oif != "" {
		p = append(p, fmt.Sprintf("oifname %q", n.Oif))
	}
	if n.Saddr != "" {
		p = append(p, "ip saddr "+n.Saddr)
	}
	if n.Daddr != "" {
		p = append(p, "ip daddr "+n.Daddr)
	}
	if n.Proto != "" && n.Sport == 0 && n.Dport == 0 && len(n.Sports) == 0 && len(n.Dports) == 0 {
		p = append(p, "meta l4proto "+n.Proto)
	}
	if n.Sport != 0 {
		p = append(p, fmt.Sprintf("%s sport %d", n.Proto, n.Sport))
	}
	if len(n.Sports) > 0 {
		p = append(p, fmt.Sprintf("%s sport %s", n.Proto, portSet(n.Sports)))
	}
	if n.Dport != 0 {
		p = append(p, fmt.Sprintf("%s dport %d", n.Proto, n.Dport))
	}
	if len(n.Dports) > 0 {
		p = append(p, fmt.Sprintf("%s dport %s", n.Proto, portSet(n.Dports)))
	}
	p = append(p, fmt.Sprintf("counter name %q", n.Name))
	return strings.Join(p, " ")
}

func portSet(ports []int) string {
	if len(ports) == 1 {
		return fmt.Sprint(ports[0])
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprint(p)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// RenderNft renders the complete counter-only ruleset for table inet vpnmap.
// The chains have policy accept and the rules have no verdicts, so the table
// never changes what is accepted or dropped by other tables.
func RenderNft(counters []NftCounter) string {
	var b strings.Builder
	b.WriteString("#!/usr/sbin/nft -f\n")
	b.WriteString("# Generated by `vpnmap agent nft`. Counter-only: no accept/drop/jump rules.\n")
	b.WriteString("# Remove with: nft delete table inet vpnmap\n")
	b.WriteString("table inet vpnmap\n")
	b.WriteString("delete table inet vpnmap\n")
	b.WriteString("table inet vpnmap {\n")
	sorted := append([]NftCounter(nil), counters...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, c := range sorted {
		fmt.Fprintf(&b, "\tcounter %s { }\n", c.Name)
	}
	for _, hook := range []string{"input", "output", "forward"} {
		fmt.Fprintf(&b, "\tchain %s {\n\t\ttype filter hook %s priority -310; policy accept;\n", hook, hook)
		for _, c := range sorted {
			if c.Hook == hook {
				fmt.Fprintf(&b, "\t\t%s\n", c.Rule())
			}
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// Load reads and validates a config file.
func Load(path string) (*Config, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if c.Interval == 0 {
		c.Interval = Duration(5 * time.Second)
	}
	if c.ChecksDir == "" {
		c.ChecksDir = "/etc/vpnmap-agent/checks.d"
	}
	if err := c.Validate(); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	return &c, SHA(raw), nil
}

// SHA returns the short sha256 used as config_sha.
func SHA(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])[:12]
}

// Marshal renders the config as YAML with a header.
func (c *Config) Marshal() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	hdr := fmt.Sprintf("# /etc/vpnmap-agent/agent.yaml for node %q\n# Generated by `vpnmap agent config %s`. Do not edit by hand; edit the topology and regenerate.\n", c.Node, c.Node)
	return append([]byte(hdr), body...), nil
}
