// Package ops is the closed catalog of ops action types and the wire format
// between the hub and the vpnmap-ops runner on each server.
//
// Two layers (docs/ops-actions.md):
//   - the action *types* below are closed and live in code, like the
//     collector catalog: adding one is a reviewed code change;
//   - which containers, units, logs, scripts, targets... a type may touch on
//     a server is data: the node's `ops:` block in the topology, resolved by
//     pkg/topology into a RunnerConfig that is installed root-owned on that
//     server and enforced there.
//
// No type takes a free-form command, path or URL. Every argument is a token
// (see ValidToken) that must also be one of the values the server's config
// allows.
package ops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kinds.
const (
	Read   = "read"
	Change = "change"
)

// Where an action type runs.
const (
	OnServer    = "server"    // over SSH through vpnmap-ops on the node
	OnRemnawave = "remnawave" // the hub calls a fixed Remnawave API endpoint
)

// Source names say where the allowed values of a parameter come from in the
// node's RunnerConfig (or, for Remnawave, the hub policy).
const (
	FromContainers  = "containers" // any declared container
	FromRestartable = "restartable-containers"
	FromUnits       = "units"
	FromRestartUnit = "restartable-units"
	FromLogs        = "logs"
	FromTargets     = "tcp-targets"
	FromPorts       = "ports"
	FromWG          = "wg"
	FromScriptsRead = "read-scripts"
	FromScriptsChg  = "change-scripts"
	FromRemnawave   = "remnawave"
	FromFixed       = "fixed" // Enum lists the values
)

// Param is one parameter of an action type, or of a declared script.
type Param struct {
	Name string `json:"name" yaml:"name"`
	// Source tells where the allowed values are listed (built-in types).
	Source string `json:"source,omitempty" yaml:"-"`
	// Exactly one of Enum, Pattern or Min/Max (scripts; Enum also for fixed).
	Enum    []string `json:"enum,omitempty" yaml:"enum,omitempty"`
	Pattern string   `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	Min     *int     `json:"min,omitempty" yaml:"min,omitempty"`
	Max     *int     `json:"max,omitempty" yaml:"max,omitempty"`
	Doc     string   `json:"doc,omitempty" yaml:"doc,omitempty"`
}

// Type is one closed action type.
type Type struct {
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`  // read | change
	Where  string  `json:"where"` // server | remnawave
	Title  string  `json:"title"`
	Doc    string  `json:"doc"`
	Params []Param `json:"params"`
	// Disruptive: interrupts the traffic through the node (restarts).
	Disruptive bool `json:"disruptive"`
	// Cooldown is the default per action+args (changes only).
	Cooldown string `json:"cooldown,omitempty"`
	// Rollback names the type that undoes this one with the same args.
	Rollback string `json:"rollback,omitempty"`
}

func fixed(name string, values ...string) Param {
	return Param{Name: name, Source: FromFixed, Enum: values}
}

// Types is the closed catalog. Order is the display order.
var Types = []Type{
	{Name: "ping", Kind: Read, Where: OnServer, Title: "Runner check", Doc: "Answers with the node id and the hash of the installed runner config."},
	{Name: "container.list", Kind: Read, Where: OnServer, Title: "Containers", Doc: "docker ps (name and status) of the declared containers."},
	{Name: "container.stats", Kind: Read, Where: OnServer, Title: "Container CPU/memory", Doc: "docker stats --no-stream for one container.",
		Params: []Param{{Name: "container", Source: FromContainers}}},
	{Name: "container.logs", Kind: Read, Where: OnServer, Title: "Container logs", Doc: "docker logs --since/--tail for one container.",
		Params: []Param{{Name: "container", Source: FromContainers}, fixed("since", "5m", "15m", "1h"), fixed("tail", "50", "100", "200")}},
	{Name: "container.restart", Kind: Change, Where: OnServer, Title: "Restart container", Doc: "docker restart of a container declared with restart: true.",
		Params: []Param{{Name: "container", Source: FromRestartable}}, Disruptive: true, Cooldown: "10m"},
	{Name: "unit.status", Kind: Read, Where: OnServer, Title: "Unit status", Doc: "systemctl show: active state, since, restarts.",
		Params: []Param{{Name: "unit", Source: FromUnits}}},
	{Name: "unit.restart", Kind: Change, Where: OnServer, Title: "Restart unit", Doc: "systemctl restart of a unit declared with restart: true.",
		Params: []Param{{Name: "unit", Source: FromRestartUnit}}, Disruptive: true, Cooldown: "10m"},
	{Name: "log.tail", Kind: Read, Where: OnServer, Title: "Log tail", Doc: "Last lines of a declared log file (by id; the path stays on the server).",
		Params: []Param{{Name: "log", Source: FromLogs}, fixed("lines", "20", "50", "100")}},
	{Name: "disk.usage", Kind: Read, Where: OnServer, Title: "Disk usage", Doc: "df of /."},
	{Name: "tcp.check", Kind: Read, Where: OnServer, Title: "TCP connect", Doc: "TCP connect from this server, 5 s timeout.",
		Params: []Param{{Name: "target", Source: FromTargets}}},
	{Name: "conns.count", Kind: Read, Where: OnServer, Title: "Connections", Doc: "Established TCP connections with this local or remote port.",
		Params: []Param{{Name: "port", Source: FromPorts}}},
	{Name: "wg.handshakes", Kind: Read, Where: OnServer, Title: "WireGuard handshakes", Doc: "Latest handshake age and bytes per peer (never keys).",
		Params: []Param{{Name: "iface", Source: FromWG}}},
	{Name: "script.read", Kind: Read, Where: OnServer, Title: "Read script", Doc: "A declared read script in /etc/vpnmap-ops/actions.d (plus its own params).",
		Params: []Param{{Name: "script", Source: FromScriptsRead}}},
	{Name: "script.change", Kind: Change, Where: OnServer, Title: "Change script", Doc: "A declared change script in /etc/vpnmap-ops/actions.d (plus its own params).",
		Params: []Param{{Name: "script", Source: FromScriptsChg}}, Disruptive: true, Cooldown: "10m"},
	{Name: "rw.nodes", Kind: Read, Where: OnRemnawave, Title: "Remnawave nodes", Doc: "GET /api/nodes: name, connected, disabled, Xray uptime, users online."},
	{Name: "rw.system", Kind: Read, Where: OnRemnawave, Title: "Remnawave system", Doc: "GET /api/system/stats: counts only."},
	{Name: "rw.node-restart", Kind: Change, Where: OnRemnawave, Title: "Restart Remnawave node", Doc: "POST /api/nodes/{uuid}/actions/restart (uuid looked up by name).",
		Params: []Param{{Name: "node", Source: FromRemnawave}}, Disruptive: true, Cooldown: "10m"},
	{Name: "rw.node-disable", Kind: Change, Where: OnRemnawave, Title: "Disable Remnawave node", Doc: "POST /api/nodes/{uuid}/actions/disable.",
		Params: []Param{{Name: "node", Source: FromRemnawave}}, Disruptive: true, Cooldown: "10m", Rollback: "rw.node-enable"},
	{Name: "rw.node-enable", Kind: Change, Where: OnRemnawave, Title: "Enable Remnawave node", Doc: "POST /api/nodes/{uuid}/actions/enable.",
		Params: []Param{{Name: "node", Source: FromRemnawave}}, Cooldown: "10m", Rollback: "rw.node-disable"},
}

// TypeByName returns the type, or nil.
func TypeByName(name string) *Type {
	for i := range Types {
		if Types[i].Name == name {
			return &Types[i]
		}
	}
	return nil
}

// RemnawaveActions maps the `ops.remnawave` keywords to types.
var RemnawaveActions = map[string]string{
	"restart": "rw.node-restart",
	"disable": "rw.node-disable",
	"enable":  "rw.node-enable",
}

// --- tokens and the wire format -------------------------------------------

// MaxToken is the longest argument value.
const MaxToken = 64

// MaxLine is the longest request line (SSH_ORIGINAL_COMMAND).
const MaxLine = 1024

// A token is ASCII letters, digits and . _ @ : - ; it never starts with
// '-' (so it can never become an option) and never contains "..".
var reToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]{0,63}$`)

var reKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// reReqID is the request id the hub adds for the audit log.
var reReqID = regexp.MustCompile(`^[a-f0-9]{8,32}$`)

// ValidToken reports whether s may be an argument value.
func ValidToken(s string) bool {
	return reToken.MatchString(s) && !strings.Contains(s, "..")
}

// ValidKey reports whether s may be a parameter name.
func ValidKey(s string) bool { return reKey.MatchString(s) }

// ReservedKeys may not be used as script parameter names.
var ReservedKeys = map[string]bool{"script": true, "req": true, "type": true}

// Request is one parsed request: `<type> k=v k=v req=<id>`.
type Request struct {
	Type string
	Args map[string]string
	ID   string // optional request id for the audit log
}

// ParseRequest parses a request line strictly: printable ASCII only, single
// spaces, no empty or duplicate keys, every value a token.
func ParseRequest(line string) (*Request, error) {
	if line == "" {
		return nil, fmt.Errorf("empty request")
	}
	if len(line) > MaxLine {
		return nil, fmt.Errorf("request longer than %d bytes", MaxLine)
	}
	for i := 0; i < len(line); i++ {
		if c := line[i]; c < 0x21 && c != ' ' || c > 0x7e {
			return nil, fmt.Errorf("request contains a control or non-ASCII byte at %d", i)
		}
	}
	parts := strings.Split(line, " ")
	r := &Request{Type: parts[0], Args: map[string]string{}}
	if TypeByName(r.Type) == nil {
		return nil, fmt.Errorf("unknown action type %q", r.Type)
	}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok || !ValidKey(k) {
			return nil, fmt.Errorf("bad argument %q (want key=value)", p)
		}
		if k == "req" {
			if !reReqID.MatchString(v) || r.ID != "" {
				return nil, fmt.Errorf("bad request id")
			}
			r.ID = v
			continue
		}
		if !ValidToken(v) {
			return nil, fmt.Errorf("argument %s: value is not allowed (letters, digits and . _ @ : - only, at most %d, not starting with -)", k, MaxToken)
		}
		if _, dup := r.Args[k]; dup {
			return nil, fmt.Errorf("argument %s given twice", k)
		}
		r.Args[k] = v
	}
	return r, nil
}

// String is the canonical form (sorted args, id last).
func (r *Request) String() string {
	var b strings.Builder
	b.WriteString(r.Type)
	for _, k := range sortedKeys(r.Args) {
		b.WriteString(" " + k + "=" + r.Args[k])
	}
	if r.ID != "" {
		b.WriteString(" req=" + r.ID)
	}
	return b.String()
}

// Key identifies the action and arguments for cooldowns (no request id).
func (r *Request) Key() string {
	c := *r
	c.ID = ""
	return c.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
