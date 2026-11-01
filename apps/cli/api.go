package main

import (
	"encoding/json"

	"vpnmap/pkg/topology"
)

// Types mirrored from apps/hub/src/contract.ts (only the fields the CLI
// uses; unknown fields are ignored). JSON null decodes to "" or nil.

type definedIn struct {
	Component string `json:"component"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

func (d definedIn) String() string {
	if d.File == "" {
		return "–"
	}
	if d.Line > 0 {
		return d.File + ":" + itoa(d.Line)
	}
	return d.File
}

type runbookCmd struct {
	Title string `json:"title"`
	Cmd   string `json:"cmd"`
	Notes string `json:"notes,omitempty"`
	From  string `json:"from,omitempty"`
}

type checkState struct {
	Key        string   `json:"key"`
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Collector  string   `json:"collector"`
	Status     string   `json:"status"`
	Effective  string   `json:"effective"`
	Value      *float64 `json:"value"`
	Unit       string   `json:"unit"`
	Detail     string   `json:"detail"`
	ObservedAt string   `json:"observedAt"`
	Source     string   `json:"source"`
}

type metricValue struct {
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
	Label string   `json:"label"`
}

type agentState struct {
	LastSeen  string   `json:"lastSeen"`
	Version   string   `json:"version"`
	ConfigSha string   `json:"configSha"`
	AgeSec    *float64 `json:"ageSec"`
}

type nodeState struct {
	ID               string                 `json:"id"`
	Status           string                 `json:"status"`
	Observed         bool                   `json:"observed"`
	Impact           string                 `json:"impact"`
	Checks           []checkState           `json:"checks"`
	Metrics          map[string]metricValue `json:"metrics"`
	BootTime         string                 `json:"bootTime"`
	UptimeSec        *float64               `json:"uptimeSec"`
	RebootedRecently bool                   `json:"rebootedRecently"`
	Agent            *agentState            `json:"agent"`
	Since            string                 `json:"since"`
}

type linkState struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	Observed      bool         `json:"observed"`
	Impact        string       `json:"impact"`
	UpBps         *float64     `json:"upBps"`
	DownBps       *float64     `json:"downBps"`
	Conns         *float64     `json:"conns"`
	TrafficSource string       `json:"trafficSource"`
	Checks        []checkState `json:"checks"`
	Since         string       `json:"since"`
}

type routeProbe struct {
	Status      string   `json:"status"`
	LatencyMs   *float64 `json:"latencyMs"`
	ExitCountry string   `json:"exitCountry"`
	Failures    int      `json:"failures"`
	Detail      string   `json:"detail"`
}

type elementRef struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Status string `json:"status,omitempty"`
	Impact string `json:"impact,omitempty"`
}

type routeState struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	Broken      bool         `json:"broken"`
	Probe       *routeProbe  `json:"probe"`
	Users       *float64     `json:"users"`
	UsersSource string       `json:"usersSource"`
	Root        *elementRef  `json:"root"`
	Hops        []elementRef `json:"hops"`
	Since       string       `json:"since"`
	// Added by GET /api/status.
	Title  string   `json:"title"`
	Impact string   `json:"impact"`
	Squads []string `json:"squads"`
}

type rootCause struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	ElementID     string   `json:"elementId"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Impact        string   `json:"impact"`
	UsersAffected float64  `json:"usersAffected"`
	UsersSource   string   `json:"usersSource"`
	Routes        []string `json:"routes"`
	Affected      struct {
		Nodes []string `json:"nodes"`
		Links []string `json:"links"`
	} `json:"affected"`
	Since     string       `json:"since"`
	Evidence  []string     `json:"evidence"`
	Runbook   []runbookCmd `json:"runbook"`
	Docs      []string     `json:"docs"`
	DefinedIn *definedIn   `json:"definedIn"`
}

type activeAlert struct {
	ID            string    `json:"id"`
	Instance      string    `json:"instance"`
	Severity      string    `json:"severity"`
	Title         string    `json:"title"`
	Message       string    `json:"message"`
	MessageFa     string    `json:"messageFa"`
	Since         string    `json:"since"`
	SilencedUntil string    `json:"silencedUntil"`
	DefinedIn     definedIn `json:"definedIn"`
}

type overall struct {
	Status      string   `json:"status"`
	UsersOnline *float64 `json:"usersOnline"`
	Throughput  struct {
		UpBps   float64 `json:"upBps"`
		DownBps float64 `json:"downBps"`
	} `json:"throughput"`
	ActiveAlerts int            `json:"activeAlerts"`
	RootCauses   []rootCause    `json:"rootCauses"`
	Counts       map[string]int `json:"counts"`
}

type topoRef struct {
	Version   int    `json:"version"`
	Hash      string `json:"hash"`
	GitCommit string `json:"gitCommit"`
}

type driftItem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Host   string `json:"host"`
}

type snapshot struct {
	At       string                `json:"at"`
	Live     bool                  `json:"live"`
	Mode     string                `json:"mode"`
	Topology topoRef               `json:"topology"`
	Overall  overall               `json:"overall"`
	Nodes    map[string]nodeState  `json:"nodes"`
	Links    map[string]linkState  `json:"links"`
	Routes   map[string]routeState `json:"routes"`
	Alerts   []activeAlert         `json:"alerts"`
	Drift    []driftItem           `json:"drift"`
}

// statusResp is GET /api/status.
type statusResp struct {
	At       string        `json:"at"`
	Live     bool          `json:"live"`
	Mode     string        `json:"mode"`
	Topology topoRef       `json:"topology"`
	Overall  overall       `json:"overall"`
	Routes   []routeState  `json:"routes"`
	Alerts   []activeAlert `json:"alerts"`
	Drift    []driftItem   `json:"drift"`
}

type hubEvent struct {
	ID         int64          `json:"id"`
	TS         string         `json:"ts"`
	Kind       string         `json:"kind"`
	Severity   string         `json:"severity"`
	EntityKind string         `json:"entityKind"`
	EntityID   string         `json:"entityId"`
	Title      string         `json:"title"`
	Detail     string         `json:"detail"`
	Data       map[string]any `json:"data"`
}

type series struct {
	Key    string        `json:"key"`
	Label  string        `json:"label"`
	Unit   string        `json:"unit"`
	Points [][2]*float64 `json:"points"`
}

type describeResp struct {
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	At         string          `json:"at"`
	Spec       json.RawMessage `json:"spec"`
	State      json.RawMessage `json:"state"`
	DefinedIn  definedIn       `json:"definedIn"`
	Upstream   []elementRef    `json:"upstream"`
	Downstream []elementRef    `json:"downstream"`
	Routes     []string        `json:"routes"`
	Events     []hubEvent      `json:"events"`
	Runbook    []runbookCmd    `json:"runbook"`
	Docs       []string        `json:"docs"`
	Why        []string        `json:"why"`
}

type pathHop struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Impact  string `json:"impact"`
	Detail  string `json:"detail"`
	Metrics string `json:"metrics"`
}

type pathView struct {
	At      string    `json:"at"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Route   string    `json:"route"`
	Hops    []pathHop `json:"hops"`
	Verdict string    `json:"verdict"`
	Status  string    `json:"status"`
}

type degradedItem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type diagnosis struct {
	At           string         `json:"at"`
	Status       string         `json:"status"`
	Headline     string         `json:"headline"`
	RootCauses   []rootCause    `json:"rootCauses"`
	Degraded     []degradedItem `json:"degraded"`
	Alerts       []activeAlert  `json:"alerts"`
	RecentEvents []hubEvent     `json:"recentEvents"`
}

type agentInfo struct {
	Node         string              `json:"node"`
	Kind         string              `json:"kind"`
	Version      string              `json:"version"`
	Hostname     string              `json:"hostname"`
	ConfigSha    string              `json:"configSha"`
	LastSeen     string              `json:"lastSeen"`
	AgeSec       float64             `json:"ageSec"`
	Capabilities map[string][]string `json:"capabilities"`
}

type topologyVersion struct {
	Version   int    `json:"version"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"createdAt"`
	Message   string `json:"message"`
	GitCommit string `json:"gitCommit"`
	AppliedBy string `json:"appliedBy"`
	Active    bool   `json:"active"`
	Counts    struct {
		Nodes  int `json:"nodes"`
		Links  int `json:"links"`
		Routes int `json:"routes"`
		Alerts int `json:"alerts"`
	} `json:"counts"`
}

type demoScenario struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Date        string `json:"date"`
	Description string `json:"description"`
	DurationSec int    `json:"durationSec"`
	LastStart   string `json:"lastStart"`
	Running     bool   `json:"running"`
}

// GET /api/nodes, /api/links, /api/routes.
type nodeItem struct {
	Spec  topology.Node `json:"spec"`
	State nodeState     `json:"state"`
}

type linkItem struct {
	Spec  topology.Link `json:"spec"`
	Name  string        `json:"name"`
	State linkState     `json:"state"`
}

type routeItem struct {
	Spec  topology.Route `json:"spec"`
	State routeState     `json:"state"`
}

type alertsResp struct {
	Active []activeAlert    `json:"active"`
	Rules  []topology.Alert `json:"rules"`
}

type componentItem struct {
	Name   string   `json:"name"`
	Title  string   `json:"title"`
	Docs   string   `json:"docs"`
	File   string   `json:"file"`
	Nodes  []string `json:"nodes"`
	Links  []string `json:"links"`
	Routes []string `json:"routes"`
	Alerts []string `json:"alerts"`
}

// GET /api/topology.
type topologyResp struct {
	Version int             `json:"version"`
	Bundle  json.RawMessage `json:"bundle"`
}
