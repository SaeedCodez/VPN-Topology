// Package topology loads, validates and resolves the topology-as-code files in
// topology/ (map.yaml, components/*.yaml, layout.yaml). It is the single
// implementation of the format: the CLI validates with it, and the hub receives
// the resolved Bundle that it produces. The JSON schema in
// schema/topology.schema.json is generated from schema.go.
package topology

// SchemaVersion is the value of `schema:` in map.yaml.
const SchemaVersion = "vpnmap/v1"

// MapFile is topology/map.yaml: global settings, groups and observation sources.
type MapFile struct {
	Schema   string   `yaml:"schema" json:"schema"`
	Name     string   `yaml:"name" json:"name"`
	Docs     string   `yaml:"docs,omitempty" json:"docs,omitempty"`
	Settings Settings `yaml:"settings" json:"settings"`
	Groups   []Group  `yaml:"groups" json:"groups"`
	Sources  []Source `yaml:"sources,omitempty" json:"sources,omitempty"`
}

// Settings are global evaluation settings.
type Settings struct {
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	// StaleAfter: data older than this is treated as missing (grey).
	StaleAfter Duration `yaml:"stale_after,omitempty" json:"stale_after,omitempty"`
	// RebootWindow: a node that booted within this window gets a reboot badge.
	RebootWindow Duration `yaml:"reboot_window,omitempty" json:"reboot_window,omitempty"`
	// Hub is the public URL of the hub (used in generated agent configs).
	Hub string `yaml:"hub,omitempty" json:"hub,omitempty"`
	// CriticalSquads are the paying squads. A broken route that serves one of
	// them makes the whole board an outage (red), however few users are online;
	// broken routes of other squads (tests, personal) only make it degraded.
	CriticalSquads []string `yaml:"critical_squads,omitempty" json:"critical_squads,omitempty"`
}

// Group is a frame on the canvas (a datacenter, a provider, home).
type Group struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
	Kind  string `yaml:"kind" json:"kind"` // datacenter | provider | external | personal
	// Frame draws a group frame on the canvas. Default true except kind external.
	Frame *bool  `yaml:"frame,omitempty" json:"frame,omitempty"`
	Notes string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Source is a place that observes from outside the servers: "hub" always
// exists; "probe" is the end-to-end probe server inside Iran.
type Source struct {
	ID       string `yaml:"id" json:"id"`
	Title    string `yaml:"title" json:"title"`
	Location string `yaml:"location,omitempty" json:"location,omitempty"`
	Notes    string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Component is one file in topology/components/. Adding a feature to the VPN
// means adding or editing one component.
type Component struct {
	Component   string       `yaml:"component" json:"name"`
	Title       string       `yaml:"title" json:"title"`
	Docs        string       `yaml:"docs,omitempty" json:"docs,omitempty"`
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Nodes       []Node       `yaml:"nodes,omitempty" json:"-"`
	Links       []Link       `yaml:"links,omitempty" json:"-"`
	Routes      []Route      `yaml:"routes,omitempty" json:"-"`
	Alerts      []Alert      `yaml:"alerts,omitempty" json:"-"`
	Runbook     []RunbookCmd `yaml:"runbook,omitempty" json:"runbook,omitempty"`
	File        string       `yaml:"-" json:"file"`
}

// DefinedIn tells where an entity is declared ("defined in topology/components/x.yaml").
type DefinedIn struct {
	Component string `json:"component"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	// fields holds the line of each key, for precise error positions.
	fields map[string]int
}

// At returns the line of a field of the entity (or the entity's own line).
func (d DefinedIn) At(field string) int {
	if l, ok := d.fields[field]; ok {
		return l
	}
	return d.Line
}

// Node is a box on the board: a server, an external service, the users.
type Node struct {
	ID      string       `yaml:"id" json:"id"`
	Name    string       `yaml:"name" json:"name"`
	Kind    string       `yaml:"kind" json:"kind"`
	Group   string       `yaml:"group" json:"group"`
	Address StringList   `yaml:"address,omitempty" json:"address,omitempty"`
	Role    string       `yaml:"role,omitempty" json:"role,omitempty"`
	Agent   bool         `yaml:"agent,omitempty" json:"agent"`
	Docs    string       `yaml:"docs,omitempty" json:"docs,omitempty"`
	Facts   []Fact       `yaml:"facts,omitempty" json:"facts,omitempty"`
	Checks  []Check      `yaml:"checks,omitempty" json:"checks,omitempty"`
	Metrics []Check      `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	Card    []string     `yaml:"card,omitempty" json:"card,omitempty"`
	Runbook []RunbookCmd `yaml:"runbook,omitempty" json:"runbook,omitempty"`
	Tags    []string     `yaml:"tags,omitempty" json:"tags,omitempty"`
	// Listens declares ports the server is expected to listen on beyond those
	// implied by checks and links ("tcp/80", "tcp/8000-8100"), so drift only
	// shows what is really unexpected.
	Listens []string  `yaml:"listens,omitempty" json:"listens,omitempty"`
	Defined DefinedIn `yaml:"-" json:"defined_in"`
}

// Fact is a short key/value shown in the side panel and `vpnmap describe`.
type Fact struct {
	Label string `yaml:"label" json:"label"`
	Value string `yaml:"value" json:"value"`
}

// Link is an edge: a data-plane connection (solid) or a control-plane one (dashed).
type Link struct {
	ID       string       `yaml:"id" json:"id"`
	Label    string       `yaml:"label,omitempty" json:"label,omitempty"`
	Title    string       `yaml:"title,omitempty" json:"title,omitempty"`
	From     string       `yaml:"from" json:"from"`
	To       string       `yaml:"to" json:"to"`
	Plane    string       `yaml:"plane" json:"plane"` // data | control
	Protocol string       `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Traffic  *Check       `yaml:"traffic,omitempty" json:"traffic,omitempty"`
	Conns    *Check       `yaml:"conns,omitempty" json:"conns,omitempty"`
	Health   CheckList    `yaml:"health,omitempty" json:"health,omitempty"`
	Docs     string       `yaml:"docs,omitempty" json:"docs,omitempty"`
	Notes    string       `yaml:"notes,omitempty" json:"notes,omitempty"`
	Runbook  []RunbookCmd `yaml:"runbook,omitempty" json:"runbook,omitempty"`
	Defined  DefinedIn    `yaml:"-" json:"defined_in"`
}

// Route is a path users take, from the users node to an exit, as an ordered
// list of links. Root-cause analysis walks routes hop by hop.
type Route struct {
	ID    string   `yaml:"id" json:"id"`
	Title string   `yaml:"title" json:"title"`
	Hops  []string `yaml:"hops" json:"hops"`
	// Nodes is derived: the node sequence along the hops.
	Nodes []string `yaml:"-" json:"nodes"`
	// Users gives the live number of users on this route (e.g. remnawave-online).
	Users *Check `yaml:"users,omitempty" json:"users,omitempty"`
	// UsersEstimate is used when there is no live number.
	UsersEstimate int      `yaml:"users_estimate,omitempty" json:"users_estimate,omitempty"`
	Squads        []string `yaml:"squads,omitempty" json:"squads,omitempty"`
	// Probe is the end-to-end check of the whole route.
	Probe *Check `yaml:"probe,omitempty" json:"probe,omitempty"`
	// Impact when broken: outage (users lose connectivity) or degraded.
	Impact  string    `yaml:"impact,omitempty" json:"impact"`
	Docs    string    `yaml:"docs,omitempty" json:"docs,omitempty"`
	Defined DefinedIn `yaml:"-" json:"defined_in"`
}

// Alert is a rule evaluated by the hub.
type Alert struct {
	ID        string    `yaml:"id" json:"id"`
	When      string    `yaml:"when" json:"when"`
	Severity  string    `yaml:"severity" json:"severity"` // critical | warning | info
	Title     string    `yaml:"title,omitempty" json:"title,omitempty"`
	MessageFa string    `yaml:"message_fa" json:"message_fa"`
	MessageEn string    `yaml:"message_en,omitempty" json:"message_en,omitempty"`
	Expr      *Expr     `yaml:"-" json:"expr"`
	Defined   DefinedIn `yaml:"-" json:"defined_in"`
}

// RunbookCmd is a read-only diagnostic command. It is shown with a copy
// button and never executed by the hub, the CLI or the UI.
type RunbookCmd struct {
	Title string `yaml:"title" json:"title"`
	Cmd   string `yaml:"cmd" json:"cmd"`
	Notes string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Check is a collector reference with its parameters. In YAML it is written
// inline: { collector: tcp-connect, from: panel, target: 1.2.3.4:9000 }.
type Check struct {
	// ID is unique within its owner (node, link or route). Generated if empty.
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Collector string `json:"collector"`
	// OnFail is the status the owner gets when this check fails: down | degraded.
	OnFail string `json:"on_fail,omitempty"`
	// Label and Unit are used when the check is shown as a metric.
	Label    string         `json:"label,omitempty"`
	Unit     string         `json:"unit,omitempty"`
	Params   map[string]any `json:"params"`
	Fallback *Check         `json:"fallback,omitempty"`
	// Key is the global id: "<owner kind>:<owner id>:<check id>".
	Key  string `json:"key"`
	Line int    `json:"line"`
}

// Pos is a node position on the canvas (layout.yaml, written by the UI).
type Pos struct {
	X float64 `yaml:"x" json:"x"`
	Y float64 `yaml:"y" json:"y"`
}

// Bundle is the resolved, validated topology. `vpnmap apply` sends it to the
// hub; the hub never reads YAML itself.
type Bundle struct {
	Schema     string          `json:"schema"`
	Map        MapFile         `json:"map"`
	Components []Component     `json:"components"`
	Nodes      []Node          `json:"nodes"`
	Links      []Link          `json:"links"`
	Routes     []Route         `json:"routes"`
	Alerts     []Alert         `json:"alerts"`
	Layout     map[string]Pos  `json:"layout"`
	Catalog    []CollectorInfo `json:"catalog"`
	Source     BundleSource    `json:"source"`
	// Hash is a content hash of the topology (excluding source and layout),
	// so plan/apply can tell whether anything changed.
	Hash string `json:"hash"`
}

// BundleSource records where a bundle came from.
type BundleSource struct {
	GitCommit   string `json:"git_commit,omitempty"`
	GitBranch   string `json:"git_branch,omitempty"`
	Dirty       bool   `json:"dirty"`
	GeneratedAt string `json:"generated_at"`
	Generator   string `json:"generator"`
}
