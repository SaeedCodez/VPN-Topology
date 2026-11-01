// Package protocol defines the JSON payload that agents and the probe push to
// the hub (POST /api/ingest/v1/push). It is the only contract between a server
// and the hub: data flows one way, from the server to the hub. The hub never
// sends commands back; the response body only acknowledges the push.
//
// Rules for every field:
//   - Counters are cumulative raw values (bytes, packets). The hub computes rates.
//   - Timestamps are RFC 3339 UTC strings or Unix seconds where noted.
//   - Nothing secret is ever included: no keys, no tokens, no .env values.
//     Log lines are redacted by the agent before they are sent.
package protocol

// Version is the payload schema version. Bump it on breaking changes.
const Version = 1

// Push is one report from an agent or probe.
type Push struct {
	V     int       `json:"v"`
	Agent AgentInfo `json:"agent"`
	// TS is when the report was assembled (RFC 3339, UTC).
	TS string `json:"ts"`
	// Seq increases by one per push since agent start; resets on restart.
	Seq uint64 `json:"seq"`

	Host       *Host            `json:"host,omitempty"`
	Units      []Unit           `json:"units,omitempty"`
	Containers []Container      `json:"containers,omitempty"`
	Nft        []NftCounter     `json:"nft,omitempty"`
	WG         []WGInterface    `json:"wg,omitempty"`
	SS         []SSCount        `json:"ss,omitempty"`
	TCP        []TCPResult      `json:"tcp,omitempty"`
	HTTP       []HTTPResult     `json:"http,omitempty"`
	Logs       []LogLines       `json:"logs,omitempty"`
	Checks     []CheckResult    `json:"checks,omitempty"`
	Prom       []PromSample     `json:"prom,omitempty"`
	Probes     []ProbeResult    `json:"probes,omitempty"`
	Errors     []CollectorError `json:"errors,omitempty"`
}

// AgentInfo identifies the sender.
type AgentInfo struct {
	// Node is the topology node id this agent runs on (e.g. "relay-1").
	// For the probe it is the probe source id ("probe").
	Node     string `json:"node"`
	Kind     string `json:"kind"` // "agent" | "probe"
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	// ConfigSHA is the sha256 (hex, first 12 chars) of the agent's config file,
	// so `vpnmap plan` can tell when a server runs an outdated config.
	ConfigSHA string `json:"config_sha"`
	// Capabilities lists what this agent is configured to collect, so the hub
	// and `vpnmap plan` can report missing collectors without asking the agent.
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities is a summary of the agent's local configuration.
type Capabilities struct {
	Units       []string `json:"units,omitempty"`
	Containers  []string `json:"containers,omitempty"`
	NftCounters []string `json:"nft_counters,omitempty"`
	WG          []string `json:"wg,omitempty"` // "netns/iface" or "iface"
	SSPorts     []string `json:"ss,omitempty"` // ids
	TCPTargets  []string `json:"tcp,omitempty"`
	HTTPTargets []string `json:"http,omitempty"`
	Logs        []string `json:"logs,omitempty"`   // ids
	Checks      []string `json:"checks,omitempty"` // local check ids found in checks.d
	Prom        []string `json:"prom,omitempty"`   // prom target ids
	Routes      []string `json:"routes,omitempty"` // probe only
	// ListeningPorts is the set of listening TCP/UDP sockets ("tcp/2053",
	// "udp/51821"), used by `vpnmap discover` to find undeclared services.
	ListeningPorts []string `json:"listening,omitempty"`
}

// Host holds basic host metrics.
type Host struct {
	BootTime int64   `json:"boot_time"` // Unix seconds
	UptimeS  int64   `json:"uptime_s"`
	CPUPct   float64 `json:"cpu_pct"` // 0..100 over the last interval, all cores
	NumCPU   int     `json:"num_cpu"`
	Load1    float64 `json:"load1"`
	Load5    float64 `json:"load5"`
	Load15   float64 `json:"load15"`
	MemTotal uint64  `json:"mem_total_b"`
	MemUsed  uint64  `json:"mem_used_b"` // total - available
	Disks    []Disk  `json:"disks,omitempty"`
	NICs     []NIC   `json:"nics,omitempty"`
}

type Disk struct {
	Mount  string  `json:"mount"`
	Total  uint64  `json:"total_b"`
	Used   uint64  `json:"used_b"`
	UsedPc float64 `json:"used_pct"`
}

// NIC counters are cumulative (from /proc/net/dev).
type NIC struct {
	Name string `json:"name"`
	RxB  uint64 `json:"rx_b"`
	TxB  uint64 `json:"tx_b"`
	RxP  uint64 `json:"rx_p"`
	TxP  uint64 `json:"tx_p"`
}

// Unit is the state of a systemd unit (systemctl show).
type Unit struct {
	Name        string `json:"name"`
	ActiveState string `json:"active"` // active, inactive, failed, activating...
	SubState    string `json:"sub"`    // running, exited, dead...
	Since       string `json:"since,omitempty"`
	NRestarts   int    `json:"n_restarts"`
}

// Container is the state of a docker container (read through the helper).
type Container struct {
	Name         string `json:"name"`
	State        string `json:"state"`  // running, exited, restarting...
	Health       string `json:"health"` // healthy, unhealthy, starting, "" (no healthcheck)
	StartedAt    string `json:"started_at,omitempty"`
	RestartCount int    `json:"restart_count"`
	Image        string `json:"image,omitempty"`
}

// NftCounter is a named counter object in table `inet vpnmap`.
type NftCounter struct {
	Name    string `json:"name"`
	Bytes   uint64 `json:"bytes"`
	Packets uint64 `json:"packets"`
}

// WGInterface is the result of `wg show <iface> dump` (inside a netns if set).
// Public keys are shortened to the first 8 characters; private keys are never read.
type WGInterface struct {
	Netns  string   `json:"netns,omitempty"`
	Iface  string   `json:"iface"`
	Listen int      `json:"listen_port,omitempty"`
	Peers  []WGPeer `json:"peers"`
}

type WGPeer struct {
	Key           string `json:"key"` // first 8 chars of the public key
	Endpoint      string `json:"endpoint,omitempty"`
	LastHandshake int64  `json:"last_handshake"` // Unix seconds, 0 = never
	RxB           uint64 `json:"rx_b"`
	TxB           uint64 `json:"tx_b"`
}

// SSCount is the number of TCP sockets in a state, matched by port.
type SSCount struct {
	ID          string `json:"id"`
	Port        int    `json:"port"`
	Side        string `json:"side"` // "local" (we listen on Port) | "remote" (we dial Port)
	Established int    `json:"established"`
}

// TCPResult is one TCP connect attempt done by the agent or probe.
type TCPResult struct {
	Target string  `json:"target"` // host:port
	OK     bool    `json:"ok"`
	RTTMs  float64 `json:"rtt_ms"`
	Error  string  `json:"error,omitempty"` // "refused", "timeout", ...
	At     string  `json:"at"`
}

// HTTPResult is one HTTP(S) request done by the agent or probe.
type HTTPResult struct {
	ID        string  `json:"id"`
	URL       string  `json:"url"`
	OK        bool    `json:"ok"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
	CertDays  float64 `json:"cert_days,omitempty"` // days until the leaf certificate expires
	At        string  `json:"at"`
}

// LogLines are new lines of a watched log file since the previous push.
// The agent redacts anything that looks like a secret before sending.
type LogLines struct {
	ID    string   `json:"id"`
	Lines []string `json:"lines"`
	// Truncated is true when more lines were written than the agent keeps per push.
	Truncated bool `json:"truncated,omitempty"`
}

// CheckResult is the output of a local check from /etc/vpnmap-agent/checks.d.
// A check prints JSON: {"status":"ok|degraded|down","value":<number>,"detail":"...","data":{...}}
type CheckResult struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"` // ok | degraded | down | unknown
	Value      *float64       `json:"value,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	ExitCode   int            `json:"exit_code"`
	DurationMs float64        `json:"duration_ms"`
	At         string         `json:"at"`
}

// PromSample is one sample of an allow-listed metric from a local Prometheus
// endpoint (counters stay cumulative; the hub computes rates).
type PromSample struct {
	Target string            `json:"target"` // PromTarget id
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// ProbeResult is one end-to-end probe of a route through a real VPN client.
type ProbeResult struct {
	Route       string  `json:"route"`
	OK          bool    `json:"ok"`
	LatencyMs   float64 `json:"latency_ms"`
	ExitCountry string  `json:"exit_country,omitempty"`
	ExitIP      string  `json:"exit_ip,omitempty"` // shown masked in the UI
	Error       string  `json:"error,omitempty"`
	At          string  `json:"at"`
}

// CollectorError reports a collector that failed on the agent side
// (for example nft missing). It lets the board show grey "no data" with a reason.
type CollectorError struct {
	Collector string `json:"collector"`
	Error     string `json:"error"`
}

// PushResponse is the hub's reply. It carries no instructions.
type PushResponse struct {
	OK       bool   `json:"ok"`
	Received string `json:"received"`
}
