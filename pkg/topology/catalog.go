package topology

import "sort"

// Param types used by the collector catalog.
const (
	PString   = "string"
	PInt      = "int"
	PDuration = "duration"
	PBool     = "bool"
	PNode     = "node"     // a node id with agent: true (where the data is collected)
	PFrom     = "from"     // "hub", a declared source (e.g. "probe") or a node id with agent: true
	PTarget   = "target"   // host:port
	PURL      = "url"      // http(s) URL
	PRegex    = "regex"    // RE2 regular expression
	PMatch    = "match"    // nft match object {hook, proto, saddr, daddr, sport, dport, iif, oif}
	PEnum     = "enum"     // one of Enum
	PRoute    = "route"    // a route id
	PCountry  = "country"  // ISO 3166 alpha-2 code
	PCheckID  = "check-id" // a local check id ([a-z0-9][a-z0-9_.-]*)
	PIface    = "iface"    // network interface or netns name
	PInts     = "ints"     // list of integers
	PAbsPath  = "path"     // absolute file path
)

// Usage says where a collector may be used in a component file.
const (
	UseCheck   = "check"   // node checks and link health
	UseTraffic = "traffic" // link traffic (up/down bytes)
	UseConns   = "conns"   // link connection count
	UseUsers   = "users"   // route users
	UseProbe   = "probe"   // route end-to-end probe
	UseMetric  = "metric"  // node card metric
)

// Param describes one collector parameter.
type Param struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Doc      string   `json:"doc"`
}

// CollectorInfo is one entry of the closed collector catalog.
type CollectorInfo struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	RunsOn   string   `json:"runs_on"` // agent | hub | probe | from (decided by the `from` param)
	Produces []string `json:"produces"`
	Usage    []string `json:"usage"`
	Doc      string   `json:"doc"`
	Params   []Param  `json:"params"`
	Example  string   `json:"example"`
	Notes    string   `json:"notes,omitempty"`
}

// Param returns the named parameter definition.
func (c *CollectorInfo) Param(name string) *Param {
	for i := range c.Params {
		if c.Params[i].Name == name {
			return &c.Params[i]
		}
	}
	return nil
}

// UsableAs reports whether the collector can be used in the given place.
func (c *CollectorInfo) UsableAs(u string) bool {
	for _, x := range c.Usage {
		if x == u {
			return true
		}
	}
	return false
}

var hostParam = Param{Name: "host", Type: PNode, Required: true, Doc: "Node id whose agent collects this (must have agent: true)."}

// Catalog is the closed list of collectors. Adding a collector means adding
// it here, implementing it in the hub resolver (and the agent if it runs
// there), and regenerating the schema.
var Catalog = []CollectorInfo{
	{
		Name: "tcp-connect", Title: "TCP connect", RunsOn: "from",
		Produces: []string{"health", "latency_ms"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Opens a TCP connection to target and reports ok/refused/timeout and the connect time. Runs on the agent of `from` (configured locally in its agent.yaml), on the probe, or on the hub.",
		Params: []Param{
			{Name: "from", Type: PFrom, Default: "probe", Doc: "Where the connect is made from: a node id with agent: true, \"probe\" or \"hub\"."},
			{Name: "target", Type: PTarget, Required: true, Doc: "host:port to connect to."},
			{Name: "timeout", Type: PDuration, Default: "3s", Doc: "Connect timeout."},
			{Name: "interval", Type: PDuration, Default: "15s", Doc: "How often to try."},
		},
		Example: "{ collector: tcp-connect, from: panel, target: 31.56.89.125:9000, interval: 15s }",
	},
	{
		Name: "http-probe", Title: "HTTP probe", RunsOn: "from",
		Produces: []string{"health", "latency_ms", "status_code"}, Usage: []string{UseCheck, UseMetric, UseProbe},
		Doc: "GET url and check the status code and latency. Slower than max_latency counts as failed; slower than slow counts as degraded.",
		Params: []Param{
			{Name: "from", Type: PFrom, Default: "hub", Doc: "Where the request is made from."},
			{Name: "url", Type: PURL, Required: true, Doc: "URL to GET (read-only endpoints only)."},
			{Name: "path_file", Type: PString, Doc: "Agents and the probe only: absolute path of a local file whose contents are appended to url, for a credential in the path. Its contents never reach the files or the hub; without the file the check shows no data."},
			{Name: "expect", Type: PString, Default: "2xx,3xx", Doc: "Accepted status codes: \"200\", \"2xx\", \"2xx,401\"."},
			{Name: "max_latency", Type: PDuration, Default: "10s", Doc: "Slower than this = failed."},
			{Name: "slow", Type: PDuration, Doc: "Slower than this = degraded."},
			{Name: "interval", Type: PDuration, Default: "30s", Doc: "How often to request."},
		},
		Example: "{ collector: http-probe, from: hub, url: https://pl.api.mohiraweb.ir/api/system/health, max_latency: 5s, expect: \"2xx,401\" }",
	},
	{
		Name: "tls-expiry", Title: "TLS certificate expiry", RunsOn: "hub",
		Produces: []string{"health", "days_left"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Connects with TLS and reports the days until the leaf certificate expires. Below warn_days = degraded, expired = down.",
		Params: []Param{
			{Name: "host", Type: PString, Required: true, Doc: "Hostname (also used as SNI)."},
			{Name: "port", Type: PInt, Default: "443", Doc: "TLS port."},
			{Name: "connect", Type: PString, Doc: "Connect to this address instead of resolving host (to check an origin behind a CDN); host is still the SNI."},
			{Name: "warn_days", Type: PInt, Default: "14", Doc: "Degraded below this many days."},
			{Name: "interval", Type: PDuration, Default: "1h", Doc: "How often to check."},
		},
		Example: "{ collector: tls-expiry, host: wp.api.mohiraweb.ir, warn_days: 14 }",
	},
	{
		Name: "nft-counter", Title: "nftables counter", RunsOn: "agent",
		Produces: []string{"up_bps", "down_bps"}, Usage: []string{UseTraffic, UseMetric},
		Doc: "Byte counters in the agent's own counter-only table `inet vpnmap` (no accept/drop rules). `up` matches bytes flowing from the link's `from` node to its `to` node; `down` the reverse. `vpnmap agent nft <node>` renders the rules.",
		Params: []Param{
			hostParam,
			{Name: "up", Type: PMatch, Required: true, Doc: "Match for bytes from→to: {hook: input|output|forward, proto, saddr, daddr, sport, dport, iif, oif}."},
			{Name: "down", Type: PMatch, Doc: "Match for bytes to→from."},
		},
		Example: "{ collector: nft-counter, host: relay-1, up: { hook: output, proto: tcp, daddr: 81.12.50.23, dport: 2053 }, down: { hook: input, proto: tcp, saddr: 81.12.50.23, sport: 2053 } }",
	},
	{
		Name: "ss-conns", Title: "Established TCP connections", RunsOn: "agent",
		Produces: []string{"conns"}, Usage: []string{UseConns, UseMetric},
		Doc: "Counts ESTABLISHED TCP sockets on the host whose local port (side: local) or remote port (side: remote) is `port` (read from /proc/net/tcp, no privileges).",
		Params: []Param{
			hostParam,
			{Name: "port", Type: PInt, Doc: "TCP port (or use ports)."},
			{Name: "ports", Type: PInts, Doc: "Several TCP ports; the counts are added up."},
			{Name: "side", Type: PEnum, Default: "local", Enum: []string{"local", "remote"}, Doc: "local = we listen on port; remote = we dial port."},
		},
		Example: "{ collector: ss-conns, host: relay-1, port: 2053 }",
		Notes:   "Set port or ports.",
	},
	{
		Name: "wg-transfer", Title: "WireGuard transfer", RunsOn: "agent",
		Produces: []string{"up_bps", "down_bps"}, Usage: []string{UseTraffic, UseMetric},
		Doc: "rx/tx bytes from `wg show <interface> dump` (inside netns if set). Direction is inferred: if host is the link's `from` node, tx = up; if it is the `to` node, rx = up. Override with `up: rx|tx`.",
		Params: []Param{
			hostParam,
			{Name: "interface", Type: PIface, Required: true, Doc: "WireGuard interface name."},
			{Name: "netns", Type: PIface, Doc: "Network namespace, if the interface lives in one."},
			{Name: "up", Type: PEnum, Enum: []string{"rx", "tx"}, Doc: "Which counter is the from→to direction (normally inferred)."},
		},
		Example: "{ collector: wg-transfer, host: iran-mullvad, netns: mv-de, interface: wg-de }",
	},
	{
		Name: "nic-counter", Title: "Network interface counters", RunsOn: "agent",
		Produces: []string{"up_bps", "down_bps"}, Usage: []string{UseTraffic, UseMetric},
		Doc: "rx/tx bytes of the host's network interfaces from /proc/net/dev, which every agent sends anyway (no nft, no privileges). Direction is inferred like wg-transfer: if host is the link's `from` node, tx = up. Use it when nftables cannot be installed or a link is simply \"all traffic of this server\".",
		Params: []Param{
			hostParam,
			{Name: "interface", Type: PString, Default: "*", Doc: "Interface name, or * for all interfaces except loopback/docker/veth."},
			{Name: "up", Type: PEnum, Enum: []string{"rx", "tx"}, Doc: "Which counter is the from→to direction (normally inferred)."},
		},
		Example: "{ collector: nic-counter, host: france-exit }",
	},
	{
		Name: "wg-handshake", Title: "WireGuard handshake age", RunsOn: "agent",
		Produces: []string{"health", "age_s"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Age of the latest handshake of the interface's peer (the newest one if there are several). Older than max_age = failed.",
		Params: []Param{
			hostParam,
			{Name: "interface", Type: PIface, Required: true, Doc: "WireGuard interface name."},
			{Name: "netns", Type: PIface, Doc: "Network namespace, if any."},
			{Name: "max_age", Type: PDuration, Default: "3m", Doc: "Handshake older than this = failed."},
		},
		Example: "{ collector: wg-handshake, host: iran-mullvad, interface: wg-game, max_age: 3m }",
	},
	{
		Name: "xray-stats", Title: "Xray inbound/outbound traffic", RunsOn: "agent",
		Produces: []string{"up_bps", "down_bps"}, Usage: []string{UseTraffic, UseMetric},
		Doc: "Per-inbound or per-outbound Xray byte counters as recorded by the Remnawave panel (`remnawave_node_outbound_upload_bytes` / `..._download_bytes{node_uuid,tag}` on the panel's local 127.0.0.1:3001/metrics). The agent on the panel host scrapes that endpoint and forwards only these metrics. Data arrives in 30 s steps.",
		Params: []Param{
			{Name: "host", Type: PNode, Required: true, Doc: "Node whose agent scrapes the panel metrics (the panel server)."},
			{Name: "node", Type: PString, Required: true, Doc: "Remnawave node name, e.g. Local-Node."},
			{Name: "outbound", Type: PString, Doc: "Outbound tag, e.g. proxy_MAIN or DIRECT."},
			{Name: "inbound", Type: PString, Doc: "Inbound tag, e.g. VLESS_XBRIDGER_MAIN (set outbound or inbound)."},
		},
		Example: "{ collector: xray-stats, host: panel, node: Local-Node, outbound: proxy_MAIN, fallback: { collector: nft-counter, ... } }",
		Notes:   "Verified from remnanode 3.4.2 / backend 3.4.5 source: the node enables statsOutboundUplink/Downlink and the panel resets the node counters every 30 s and accumulates them in its own Prometheus counters.",
	},
	{
		Name: "remnawave-node", Title: "Remnawave node status", RunsOn: "hub",
		Produces: []string{"health", "xray_uptime_s", "users_online"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Reads GET /api/nodes from the Remnawave panel: isConnected, isNodeOnline, isXrayRunning, lastStatusMessage, xray uptime. Uses the dedicated monitoring API token from the hub's env.",
		Params: []Param{
			{Name: "node", Type: PString, Required: true, Doc: "Remnawave node name, e.g. Local-Node."},
		},
		Example: "{ collector: remnawave-node, node: Local-Node }",
	},
	{
		Name: "remnawave-online", Title: "Remnawave online users", RunsOn: "hub",
		Produces: []string{"users"}, Usage: []string{UseUsers, UseMetric},
		Doc: "Online users from the Remnawave API: per node (usersOnline) or, without `node`, the panel-wide onlineNow.",
		Params: []Param{
			{Name: "node", Type: PString, Doc: "Remnawave node name; omit for all users."},
		},
		Example: "{ collector: remnawave-online, node: Local-Node }",
	},
	{
		Name: "xbridger-connection", Title: "XBridger connection", RunsOn: "agent",
		Produces: []string{"health", "latency_ms"}, Usage: []string{UseCheck, UseMetric},
		Doc: "State of one XBridger connection from `xbridger doctor --json`, run by the read-only local check `check` on the XBridger node.",
		Params: []Param{
			hostParam,
			{Name: "connection", Type: PString, Required: true, Doc: "XBridger connection id, e.g. mt6snj5mlbnn."},
			{Name: "check", Type: PCheckID, Default: "xbridger-doctor", Doc: "Local check id that runs `xbridger doctor --json`."},
		},
		Example: "{ collector: xbridger-connection, host: xbridger-node-3, connection: mt6snj5mlbnn }",
	},
	{
		Name: "3xui-inbound", Title: "3x-ui inbound", RunsOn: "hub",
		Produces: []string{"health", "up_bps", "down_bps"}, Usage: []string{UseCheck, UseTraffic, UseMetric},
		Doc: "Reads the inbound list of a 3x-ui panel (POST login + GET panel/api/inbounds/list) with read-only use of the API; reports enable flag and up/down byte counters. Panel URL and credentials come from hub env vars.",
		Params: []Param{
			{Name: "panel", Type: PString, Required: true, Doc: "Name of the 3x-ui panel; the hub reads XUI_<NAME>_URL/USER/PASS env vars."},
			{Name: "inbound", Type: PString, Required: true, Doc: "Inbound remark or tag."},
		},
		Example: "{ collector: 3xui-inbound, panel: france, inbound: Cloudflare-WS }",
	},
	{
		Name: "systemd-unit", Title: "systemd unit", RunsOn: "agent",
		Produces: []string{"health"}, Usage: []string{UseCheck},
		Doc:     "ActiveState/SubState of a unit (systemctl show, no privileges). active = ok, activating/reloading = degraded, failed/inactive = failed.",
		Params:  []Param{hostParam, {Name: "unit", Type: PString, Required: true, Doc: "Unit name, e.g. realm.service."}},
		Example: "{ collector: systemd-unit, host: relay-1, unit: realm.service }",
	},
	{
		Name: "docker-container", Title: "Docker container", RunsOn: "agent",
		Produces: []string{"health", "restarts"}, Usage: []string{UseCheck},
		Doc: "State, health and restart count of a container, read by the agent's root helper (only name, state, health, start time, restart count, image).",
		Params: []Param{
			hostParam,
			{Name: "container", Type: PString, Required: true, Doc: "Container name."},
			{Name: "require_healthy", Type: PBool, Default: "true", Doc: "Treat unhealthy as failed (otherwise degraded)."},
		},
		Example: "{ collector: docker-container, host: panel, container: remnawave }",
		Notes:   "A container can report healthy while its app hangs (2026-10-20/22 rw-api). Pair it with an http-probe.",
	},
	{
		Name: "boot-time", Title: "Boot time", RunsOn: "agent",
		Produces: []string{"health", "uptime_s"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Every node with agent: true gets this implicitly: a reboot inside `window` gives a reboot badge and a degraded check; each reboot is an event.",
		Params: []Param{
			hostParam,
			{Name: "window", Type: PDuration, Default: "30m", Doc: "How long after a boot the node counts as recently rebooted."},
		},
		Example: "{ collector: boot-time, host: xbridger-node-3, window: 30m }",
	},
	{
		Name: "log-match", Title: "Log line match", RunsOn: "agent",
		Produces: []string{"health", "events"}, Usage: []string{UseCheck},
		Doc: "Watches new lines of a log file (redacted by the agent). mode event: each matching line becomes an event (e.g. watchdog restarts). mode freshness: the last matching line must be younger than max_age (e.g. backups).",
		Params: []Param{
			hostParam,
			{Name: "path", Type: PAbsPath, Required: true, Doc: "Absolute log file path."},
			{Name: "pattern", Type: PRegex, Required: true, Doc: "RE2 regular expression."},
			{Name: "mode", Type: PEnum, Default: "event", Enum: []string{"event", "freshness"}, Doc: "event or freshness."},
			{Name: "max_age", Type: PDuration, Doc: "freshness mode: last match must be younger than this."},
			{Name: "event", Type: PString, Doc: "event mode: event title, e.g. \"watchdog restarted remnawave\"."},
		},
		Example: "{ collector: log-match, host: panel, path: /var/log/remnawave-watchdog.log, pattern: \"restart\", mode: event, event: watchdog restarted remnawave }",
	},
	{
		Name: "e2e-probe", Title: "End-to-end probe", RunsOn: "probe",
		Produces: []string{"health", "latency_ms", "exit_country"}, Usage: []string{UseProbe, UseCheck},
		Doc: "A real VPN client (sing-box, dedicated monitoring user) on the probe server fetches url through the route every interval and checks the exit country. `failures` consecutive failures = down; fewer = degraded.",
		Params: []Param{
			{Name: "expect_country", Type: PCountry, Doc: "Expected exit country (ISO code), e.g. FR."},
			{Name: "url", Type: PURL, Default: "http://cp.cloudflare.com/generate_204", Doc: "URL fetched through the route (expects 204)."},
			{Name: "interval", Type: PDuration, Default: "30s", Doc: "Probe interval."},
			{Name: "failures", Type: PInt, Default: "3", Doc: "Consecutive failures before the route is down."},
		},
		Example: "{ collector: e2e-probe, expect_country: FR, interval: 30s, failures: 3 }",
	},
	{
		Name: "local-check", Title: "Local check", RunsOn: "agent",
		Produces: []string{"health", "value"}, Usage: []string{UseCheck, UseMetric},
		Doc: "Result of a read-only check script that already exists in /etc/vpnmap-agent/checks.d/<check>.check on that server (root-owned, JSON output). The hub cannot add or change checks; `vpnmap plan` prints the file and install command when one is missing.",
		Params: []Param{
			hostParam,
			{Name: "check", Type: PCheckID, Required: true, Doc: "Check id (file name without .check)."},
			{Name: "interval", Type: PDuration, Default: "30s", Doc: "How often the agent runs it."},
		},
		Example: "{ collector: local-check, host: relay-1, check: relay-forwards }",
	},
}

// Collector returns the catalog entry for name, or nil.
func Collector(name string) *CollectorInfo {
	for i := range Catalog {
		if Catalog[i].Name == name {
			return &Catalog[i]
		}
	}
	return nil
}

// CollectorNames returns the sorted collector names.
func CollectorNames() []string {
	var out []string
	for _, c := range Catalog {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// Built-in node metrics available for every node with agent: true.
var BuiltinMetrics = []struct{ ID, Label, Unit, Doc string }{
	{"cpu", "CPU", "%", "CPU usage, all cores"},
	{"mem", "Memory", "%", "Memory used (total - available)"},
	{"disk", "Disk", "%", "Root filesystem used"},
	{"load", "Load", "", "1-minute load average"},
	{"uptime", "Uptime", "s", "Seconds since boot"},
	{"net", "Network", "bps", "Total NIC throughput (rx + tx)"},
}

// IsBuiltinMetric reports whether id is a built-in agent metric.
func IsBuiltinMetric(id string) bool {
	for _, b := range BuiltinMetrics {
		if b.ID == id {
			return true
		}
	}
	return false
}
