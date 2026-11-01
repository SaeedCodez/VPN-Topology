package topology

import (
	"encoding/json"
	"sort"
)

// S describes the shape of a YAML value. The same description drives the
// structural validation of the files (with line numbers) and the generated
// JSON schema for editors and agents, so they cannot drift apart.
type S struct {
	Type     string // object | array | string | integer | number | boolean | any
	Desc     string
	Fields   []F
	Items    *S
	Enum     []string
	Pattern  string
	Alt      []*S   // anyOf: e.g. string or list of strings
	Check    string // non-empty: a collector spec; value is the usage (UseCheck, UseTraffic...)
	Examples []any
}

// F is one field of an object.
type F struct {
	Name     string
	S        *S
	Required bool
}

const idPattern = `^[a-z0-9][a-z0-9-]{0,62}$`

var (
	sID       = &S{Type: "string", Pattern: idPattern, Desc: "Lowercase id: letters, digits and dashes."}
	sText     = &S{Type: "string"}
	sURL      = &S{Type: "string", Pattern: `^https://`, Desc: "https:// URL (Notion page or other docs)."}
	sDuration = &S{Type: "string", Pattern: `^[0-9]+(\.[0-9]+)?(ms|s|m|h|d)$`, Desc: "Duration such as 15s, 3m, 2h, 14d."}
	sStrList  = &S{Alt: []*S{{Type: "string"}, {Type: "array", Items: &S{Type: "string"}}}}
)

func sCheck(usage, desc string) *S { return &S{Type: "object", Check: usage, Desc: desc} }

var sRunbook = &S{Type: "array", Desc: "Read-only diagnostic commands. Shown with a copy button; never executed.", Items: &S{Type: "object", Fields: []F{
	{Name: "title", S: sText, Required: true},
	{Name: "cmd", S: &S{Type: "string", Desc: "Shell command (read-only: no restart/stop/rm/apply...)."}, Required: true},
	{Name: "notes", S: sText},
}}}

// MapSchema is topology/map.yaml.
var MapSchema = &S{Type: "object", Desc: "Global settings, groups and observation sources of the VPN map.", Fields: []F{
	{Name: "schema", S: &S{Type: "string", Enum: []string{SchemaVersion}}, Required: true},
	{Name: "name", S: sText, Required: true},
	{Name: "docs", S: sURL},
	{Name: "settings", S: &S{Type: "object", Fields: []F{
		{Name: "timezone", S: &S{Type: "string", Desc: "IANA zone for human output, e.g. Asia/Tehran."}},
		{Name: "stale_after", S: sDuration},
		{Name: "reboot_window", S: sDuration},
		{Name: "hub", S: &S{Type: "string", Pattern: `^https?://`, Desc: "Public hub URL, used in generated agent configs."}},
		{Name: "critical_squads", S: &S{Type: "array", Items: &S{Type: "string"}, Desc: "Paying squads: a broken route serving one of them turns the board red."}},
	}}},
	{Name: "groups", Required: true, S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "title", S: sText, Required: true},
		{Name: "kind", S: &S{Type: "string", Enum: []string{"datacenter", "provider", "external", "personal"}}, Required: true},
		{Name: "frame", S: &S{Type: "boolean"}},
		{Name: "notes", S: sText},
	}}}},
	{Name: "sources", S: &S{Type: "array", Desc: "Observers outside the servers (\"hub\" is implicit).", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "title", S: sText, Required: true},
		{Name: "location", S: sText},
		{Name: "notes", S: sText},
	}}}},
}}

// NodeKinds are the icons/roles a node can have.
var NodeKinds = []string{"users", "relay", "panel", "vpn-node", "bridge", "exit", "cdn", "edge", "vpn-provider", "internet", "router", "server", "storage", "service"}

// ComponentSchema is one topology/components/*.yaml file.
var ComponentSchema = &S{Type: "object", Desc: "One VPN feature: its nodes, links, routes, checks, alerts and docs.", Fields: []F{
	{Name: "component", S: sID, Required: true},
	{Name: "title", S: sText, Required: true},
	{Name: "docs", S: sURL},
	{Name: "description", S: sText},
	{Name: "nodes", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "name", S: sText, Required: true},
		{Name: "kind", S: &S{Type: "string", Enum: NodeKinds}, Required: true},
		{Name: "group", S: sID, Required: true},
		{Name: "address", S: sStrList},
		{Name: "role", S: sText},
		{Name: "agent", S: &S{Type: "boolean", Desc: "true when vpnmap-agent runs on this server."}},
		{Name: "docs", S: sURL},
		{Name: "facts", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{{Name: "label", S: sText, Required: true}, {Name: "value", S: sText, Required: true}}}}},
		{Name: "checks", S: &S{Type: "array", Items: sCheck(UseCheck, "Node health check.")}},
		{Name: "metrics", S: &S{Type: "array", Items: sCheck(UseMetric, "Extra numbers for the node card (needs id and label).")}},
		{Name: "card", S: &S{Type: "array", Desc: "Up to 3 metric/check ids shown on the card (built-ins: cpu, mem, disk, load, uptime, net).", Items: &S{Type: "string"}}},
		{Name: "runbook", S: sRunbook},
		{Name: "tags", S: &S{Type: "array", Items: &S{Type: "string"}}},
	}}}},
	{Name: "links", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "label", S: &S{Type: "string", Desc: "Short code shown on the edge, e.g. E3."}},
		{Name: "title", S: sText},
		{Name: "from", S: sID, Required: true},
		{Name: "to", S: sID, Required: true},
		{Name: "plane", S: &S{Type: "string", Enum: []string{"data", "control"}}, Required: true},
		{Name: "protocol", S: sText},
		{Name: "traffic", S: sCheck(UseTraffic, "Throughput source.")},
		{Name: "conns", S: sCheck(UseConns, "Connection count source.")},
		{Name: "health", S: &S{Alt: []*S{sCheck(UseCheck, "Health check."), {Type: "array", Items: sCheck(UseCheck, "Health check.")}}}},
		{Name: "docs", S: sURL},
		{Name: "notes", S: sText},
		{Name: "runbook", S: sRunbook},
	}}}},
	{Name: "routes", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "title", S: sText, Required: true},
		{Name: "hops", S: &S{Type: "array", Desc: "Link ids in order, from the users to the exit.", Items: sID}, Required: true},
		{Name: "users", S: sCheck(UseUsers, "Live user count on this route.")},
		{Name: "users_estimate", S: &S{Type: "integer"}},
		{Name: "squads", S: &S{Type: "array", Items: &S{Type: "string"}}},
		{Name: "probe", S: sCheck(UseProbe, "End-to-end probe of the route.")},
		{Name: "impact", S: &S{Type: "string", Enum: []string{"outage", "degraded"}}},
		{Name: "docs", S: sURL},
	}}}},
	{Name: "alerts", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "when", S: &S{Type: "string", Desc: "Condition, e.g. \"link.wg-game.health == down for 2m\"."}, Required: true},
		{Name: "severity", S: &S{Type: "string", Enum: []string{"critical", "warning", "info"}}, Required: true},
		{Name: "title", S: sText},
		{Name: "message_fa", S: &S{Type: "string", Desc: "Telegram message in Persian."}, Required: true},
		{Name: "message_en", S: sText},
	}}}},
	{Name: "runbook", S: sRunbook},
}}

// LayoutSchema is topology/layout.yaml (written by the UI, never by agents).
var LayoutSchema = &S{Type: "object", Fields: []F{
	{Name: "positions", S: &S{Type: "object", Desc: "node id → {x, y}"}},
}}

// checkBase are the reserved keys of every collector spec.
func checkBase(usage string) []F {
	f := []F{
		{Name: "id", S: sID},
		{Name: "title", S: sText},
		{Name: "collector", S: &S{Type: "string"}, Required: true},
		{Name: "on_fail", S: &S{Type: "string", Enum: []string{"down", "degraded"}}},
		{Name: "label", S: sText},
		{Name: "unit", S: sText},
	}
	if usage == UseTraffic {
		f = append(f, F{Name: "fallback", S: sCheck(UseTraffic, "Used when the primary source has no data.")})
	}
	return f
}

var sPorts = &S{Alt: []*S{{Type: "integer"}, {Type: "array", Items: &S{Type: "integer"}}}, Desc: "A port or a list of ports."}

var sMatch = &S{Type: "object", Desc: "Counter-only nft match.", Fields: []F{
	{Name: "hook", S: &S{Type: "string", Enum: []string{"input", "output", "forward"}}, Required: true},
	{Name: "proto", S: &S{Type: "string", Enum: []string{"tcp", "udp"}}},
	{Name: "saddr", S: &S{Type: "string", Pattern: `^[0-9.]+(/[0-9]+)?$`}},
	{Name: "daddr", S: &S{Type: "string", Pattern: `^[0-9.]+(/[0-9]+)?$`}},
	{Name: "sport", S: sPorts},
	{Name: "dport", S: sPorts},
	{Name: "iif", S: &S{Type: "string"}},
	{Name: "oif", S: &S{Type: "string"}},
}}

// ParamSchema returns the schema of a collector parameter type.
func ParamSchema(p Param) *S {
	var s *S
	switch p.Type {
	case PInt:
		s = &S{Type: "integer"}
	case PBool:
		s = &S{Type: "boolean"}
	case PDuration:
		s = &S{Type: "string", Pattern: sDuration.Pattern}
	case PMatch:
		s = sMatch
	case PEnum:
		s = &S{Type: "string", Enum: p.Enum}
	case PInts:
		s = &S{Type: "array", Items: &S{Type: "integer"}}
	case PURL:
		s = &S{Type: "string", Pattern: `^https?://`}
	case PTarget:
		s = &S{Type: "string", Pattern: `^[A-Za-z0-9.-]+:[0-9]{1,5}$`}
	case PCountry:
		s = &S{Type: "string", Pattern: `^[A-Z]{2}$`}
	case PCheckID:
		s = &S{Type: "string", Pattern: `^[a-z0-9][a-z0-9_.-]{0,62}$`}
	case PAbsPath:
		s = &S{Type: "string", Pattern: `^/`}
	default:
		s = &S{Type: "string"}
	}
	cp := *s
	cp.Desc = p.Doc
	if p.Default != "" {
		cp.Desc += " Default: " + p.Default + "."
	}
	return &cp
}

// JSONSchema renders a draft-07 JSON schema for one file kind.
func JSONSchema(root *S, id, title string) ([]byte, error) {
	doc := toJSON(root)
	doc["$schema"] = "http://json-schema.org/draft-07/schema#"
	doc["$id"] = id
	doc["title"] = title
	if root == ComponentSchema {
		doc["definitions"] = checkDefinitions()
	}
	return json.MarshalIndent(doc, "", "  ")
}

func toJSON(s *S) map[string]any {
	if s.Check != "" {
		out := map[string]any{"$ref": "#/definitions/check-" + s.Check}
		if s.Desc != "" {
			out["description"] = s.Desc
		}
		return out
	}
	out := map[string]any{}
	if s.Desc != "" {
		out["description"] = s.Desc
	}
	if len(s.Alt) > 0 {
		var alts []any
		for _, a := range s.Alt {
			alts = append(alts, toJSON(a))
		}
		out["anyOf"] = alts
		return out
	}
	if s.Type != "" && s.Type != "any" {
		out["type"] = s.Type
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if s.Items != nil {
		out["items"] = toJSON(s.Items)
	}
	if s.Type == "object" && len(s.Fields) > 0 {
		props := map[string]any{}
		var req []string
		for _, f := range s.Fields {
			props[f.Name] = toJSON(f.S)
			if f.Required {
				req = append(req, f.Name)
			}
		}
		out["properties"] = props
		out["additionalProperties"] = false
		if len(req) > 0 {
			out["required"] = req
		}
	}
	return out
}

// checkDefinitions renders one definition per usage: a oneOf over the
// collectors allowed there, each with its own parameters.
func checkDefinitions() map[string]any {
	defs := map[string]any{}
	for _, usage := range []string{UseCheck, UseTraffic, UseConns, UseUsers, UseProbe, UseMetric} {
		var variants []any
		names := []string{}
		for _, c := range Catalog {
			if !c.UsableAs(usage) {
				continue
			}
			names = append(names, c.Name)
			props := map[string]any{}
			var req []string
			for _, f := range checkBase(usage) {
				props[f.Name] = toJSON(f.S)
			}
			props["collector"] = map[string]any{"const": c.Name, "description": c.Title + ": " + c.Doc}
			req = append(req, "collector")
			for _, p := range c.Params {
				props[p.Name] = toJSON(ParamSchema(p))
				if p.Required && !(usage == UseProbe && p.Name == "route") {
					req = append(req, p.Name)
				}
			}
			sort.Strings(req[1:])
			variants = append(variants, map[string]any{
				"type": "object", "properties": props, "required": req, "additionalProperties": false,
			})
		}
		sort.Strings(names)
		defs["check-"+usage] = map[string]any{
			"description": "Collector spec. Allowed collectors here: " + join(names),
			"oneOf":       variants,
		}
	}
	return defs
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// SchemaFiles returns every generated schema file, keyed by repo path.
func SchemaFiles() (map[string][]byte, error) {
	out := map[string][]byte{}
	type spec struct {
		path, id, title string
		s               *S
	}
	for _, sp := range []spec{
		{"schema/topology.schema.json", "https://vpnmap.local/schema/topology.schema.json", "vpnmap component file (topology/components/*.yaml)", ComponentSchema},
		{"schema/map.schema.json", "https://vpnmap.local/schema/map.schema.json", "vpnmap map file (topology/map.yaml)", MapSchema},
	} {
		b, err := JSONSchema(sp.s, sp.id, sp.title)
		if err != nil {
			return nil, err
		}
		out[sp.path] = append(b, '\n')
	}
	return out, nil
}
