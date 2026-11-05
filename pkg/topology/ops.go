package topology

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"vpnmap/pkg/ops"
)

// NodeOps is the `ops:` block of a node: the data side of the ops catalog.
// The action types themselves are closed (pkg/ops); this only says what they
// may touch on this node.
type NodeOps struct {
	// Runner: vpnmap-ops is installed on this server (SSH actions).
	Runner     bool        `yaml:"runner,omitempty" json:"runner,omitempty"`
	Containers []OpsItem   `yaml:"containers,omitempty" json:"containers,omitempty"`
	Units      []OpsItem   `yaml:"units,omitempty" json:"units,omitempty"`
	Logs       []ops.Log   `yaml:"logs,omitempty" json:"logs,omitempty"`
	TCPTargets []string    `yaml:"tcp_targets,omitempty" json:"tcp_targets,omitempty"`
	Scripts    []OpsScript `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	// Remnawave lists allowed panel actions for this node: restart, disable, enable.
	Remnawave []string `yaml:"remnawave,omitempty" json:"remnawave,omitempty"`
	// Verify adds map ids to the derived verify set of every change here.
	Verify []string `yaml:"verify,omitempty" json:"verify,omitempty"`
}

// OpsItem is a container or a systemd unit.
type OpsItem struct {
	Name         string `yaml:"name" json:"name"`
	Restart      bool   `yaml:"restart,omitempty" json:"restart,omitempty"`
	NeverRestart bool   `yaml:"never_restart,omitempty" json:"never_restart,omitempty"`
	Cooldown     string `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
}

// OpsScript declares a script in /etc/vpnmap-ops/actions.d on the server.
type OpsScript struct {
	ID         string      `yaml:"id" json:"id"`
	Kind       string      `yaml:"kind" json:"kind"`
	Title      string      `yaml:"title,omitempty" json:"title,omitempty"`
	Params     []ops.Param `yaml:"params,omitempty" json:"params,omitempty"`
	Timeout    string      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Cooldown   string      `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`
	Disruptive *bool       `yaml:"disruptive,omitempty" json:"disruptive,omitempty"`
	ReadBefore *ops.Call   `yaml:"read_before,omitempty" json:"read_before,omitempty"`
	Rollback   *ops.Call   `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	Verify     []string    `yaml:"verify,omitempty" json:"verify,omitempty"`
}

var sOpsDur = &S{Type: "string", Pattern: `^[0-9]+(ms|s|m|h)$`, Desc: "Duration such as 30s, 10m, 1h."}

var sOpsItem = &S{Type: "object", Fields: []F{
	{Name: "name", S: &S{Type: "string", Desc: "Container or unit name."}, Required: true},
	{Name: "restart", S: &S{Type: "boolean", Desc: "Allow the restart action (one owner approval)."}},
	{Name: "never_restart", S: &S{Type: "boolean", Desc: "Documented as never restartable; validate refuses restart: true next to it."}},
	{Name: "cooldown", S: sOpsDur},
}}

var sOpsCall = &S{Type: "object", Fields: []F{
	{Name: "script", S: sID, Required: true},
	{Name: "args", S: &S{Type: "object", Desc: "param → literal, {param} or {before.<field>}"}},
}}

var sOps = &S{Type: "object", Desc: "What ops actions may read or change on this node (docs/ops-actions.md).", Fields: []F{
	{Name: "runner", S: &S{Type: "boolean", Desc: "vpnmap-ops is installed on this server."}},
	{Name: "containers", S: &S{Type: "array", Items: sOpsItem}},
	{Name: "units", S: &S{Type: "array", Items: sOpsItem}},
	{Name: "logs", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "path", S: &S{Type: "string", Pattern: `^/`, Desc: "Absolute log file path (stays on the server)."}, Required: true},
	}}}},
	{Name: "tcp_targets", S: &S{Type: "array", Desc: "Extra host:port targets for tcp.check (tcp-connect checks from this node are added automatically).", Items: &S{Type: "string"}}},
	{Name: "scripts", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
		{Name: "id", S: sID, Required: true},
		{Name: "kind", S: &S{Type: "string", Enum: []string{ops.Read, ops.Change}}, Required: true},
		{Name: "title", S: sText},
		{Name: "params", S: &S{Type: "array", Items: &S{Type: "object", Fields: []F{
			{Name: "name", S: &S{Type: "string", Pattern: `^[a-z][a-z0-9_]{0,31}$`}, Required: true},
			{Name: "enum", S: &S{Type: "array", Items: &S{Type: "string"}}},
			{Name: "pattern", S: &S{Type: "string", Desc: "Anchored RE2; values must also be tokens ([A-Za-z0-9._@:-])."}},
			{Name: "min", S: &S{Type: "integer"}},
			{Name: "max", S: &S{Type: "integer"}},
			{Name: "doc", S: sText},
		}}}},
		{Name: "timeout", S: sOpsDur},
		{Name: "cooldown", S: sOpsDur},
		{Name: "disruptive", S: &S{Type: "boolean", Desc: "Interrupts traffic through the node (default true for change scripts)."}},
		{Name: "read_before", S: sOpsCall},
		{Name: "rollback", S: sOpsCall},
		{Name: "verify", S: &S{Type: "array", Items: &S{Type: "string"}, Desc: "Extra map ids such as link.e8-{cc}."}},
	}}}},
	{Name: "remnawave", S: &S{Type: "array", Items: &S{Type: "string", Enum: []string{"restart", "disable", "enable"}}}},
	{Name: "verify", S: &S{Type: "array", Items: &S{Type: "string"}}},
}}

var reVerifyRef = regexp.MustCompile(`^(node|link|route)\.([a-z0-9{}][a-z0-9{}_-]*)$`)
var reTemplate = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// resolveOps validates every `ops:` block and fills b.Ops.
func resolveOps(b *Bundle, ds *Diagnostics) {
	b.Ops = nil
	for i := range b.Nodes {
		n := &b.Nodes[i]
		if n.Ops == nil {
			continue
		}
		if p := resolveNodeOps(b, n, ds); p != nil {
			b.Ops = append(b.Ops, *p)
		}
	}
	sort.Slice(b.Ops, func(i, j int) bool { return b.Ops[i].Node < b.Ops[j].Node })
}

// OpsPolicy returns the resolved policy of a node, or nil.
func (b *Bundle) OpsPolicy(node string) *ops.NodePolicy {
	for i := range b.Ops {
		if b.Ops[i].Node == node {
			return &b.Ops[i]
		}
	}
	return nil
}

// OpsConfig returns the runner config for a node with `ops: {runner: true}`.
func OpsConfig(b *Bundle, node string) (*ops.RunnerConfig, error) {
	if b == nil {
		return nil, fmt.Errorf("no topology")
	}
	p := b.OpsPolicy(node)
	if p == nil || !p.Runner || p.Config == nil {
		var ids []string
		for _, x := range b.Ops {
			if x.Runner {
				ids = append(ids, x.Node)
			}
		}
		return nil, fmt.Errorf("node %q has no `ops: {runner: true}` block: %s", node, didYouMean(node, ids, "runner node"))
	}
	return p.Config.Normalized(), nil
}

func resolveNodeOps(b *Bundle, n *Node, ds *Diagnostics) *ops.NodePolicy {
	o := n.Ops
	file, line := n.Defined.File, n.Defined.At("ops")
	errf := func(fix, format string, a ...any) {
		ds.errorf(file, line, 0, fix, "node %q ops: "+format, append([]any{n.ID}, a...)...)
	}
	p := &ops.NodePolicy{Node: n.ID, Runner: o.Runner}
	if len(n.Address) > 0 {
		p.Address = strings.TrimSpace(n.Address[0])
	}

	// Derived verify set and whether a disruption reaches paying users.
	verify, cut := derivedVerify(b, n.ID)
	p.CutRoutes = cut
	p.CutsUsers = len(cut) > 0
	p.Verify = verify
	for _, v := range o.Verify {
		if err := checkVerifyRef(b, v, nil); err != "" {
			errf("use node.<id>, link.<id> or route.<id> of the map", "verify %q: %s", v, err)
			continue
		}
		p.Verify = appendUnique(p.Verify, v)
	}

	hasServerData := len(o.Containers)+len(o.Units)+len(o.Logs)+len(o.TCPTargets)+len(o.Scripts) > 0
	if hasServerData && !o.Runner {
		errf("add `runner: true` (vpnmap-ops on this server) or remove the server entries", "containers, units, logs, tcp_targets and scripts need runner: true")
	}
	if o.Runner {
		if p.Address == "" {
			ds.warnf(file, line, 0, "add an address, or set the SSH host in the board's Ops panel", "node %q ops: runner without an address", n.ID)
		}
		cfg := runnerConfig(b, n, p, errf)
		if err := cfg.Validate(); err != nil {
			errf("fix the value in the ops: block", "%v", err)
		} else {
			p.Config = cfg.Normalized()
			p.ConfigHash = p.Config.Hash()
		}
	}

	// Remnawave node actions.
	if len(o.Remnawave) > 0 {
		name := ""
		for _, c := range n.Checks {
			if c.Collector == "remnawave-node" {
				name = c.Str("node")
			}
		}
		if name == "" {
			errf("add `{ collector: remnawave-node, node: <name> }` to this node's checks", "remnawave actions need the node's remnawave-node check (it names the node in the panel)")
		} else {
			rp := &ops.RemnawavePolicy{Name: name}
			for _, a := range o.Remnawave {
				t, ok := ops.RemnawaveActions[a]
				if !ok {
					errf("use restart, disable or enable", "unknown remnawave action %q", a)
					continue
				}
				rp.Actions = appendUnique(rp.Actions, t)
			}
			sort.Strings(rp.Actions)
			p.Remnawave = rp
		}
	}
	return p
}

// runnerConfig merges the explicit ops data with what the checks already say
// about this server, so new checks become readable without extra YAML.
func runnerConfig(b *Bundle, n *Node, p *ops.NodePolicy, errf func(fix, format string, a ...any)) *ops.RunnerConfig {
	o := n.Ops
	cfg := &ops.RunnerConfig{Node: n.ID}
	containers := map[string]*ops.Container{}
	units := map[string]*ops.Unit{}
	logs := map[string]string{}
	tcp := map[string]bool{}
	ports := map[int]bool{}
	wg := map[string]ops.WG{}

	for _, it := range o.Containers {
		if it.Restart && it.NeverRestart {
			errf("remove one of them", "container %q has both restart and never_restart", it.Name)
		}
		if it.NeverRestart {
			p.NeverRestart = appendUnique(p.NeverRestart, "container:"+it.Name)
		}
		if _, dup := containers[it.Name]; dup {
			errf("list it once", "container %q is listed twice", it.Name)
		}
		containers[it.Name] = &ops.Container{Name: it.Name, Restart: it.Restart && !it.NeverRestart, Cooldown: it.Cooldown}
	}
	for _, it := range o.Units {
		if it.Restart && it.NeverRestart {
			errf("remove one of them", "unit %q has both restart and never_restart", it.Name)
		}
		if it.NeverRestart {
			p.NeverRestart = appendUnique(p.NeverRestart, "unit:"+it.Name)
		}
		if _, dup := units[it.Name]; dup {
			errf("list it once", "unit %q is listed twice", it.Name)
		}
		units[it.Name] = &ops.Unit{Name: it.Name, Restart: it.Restart && !it.NeverRestart, Cooldown: it.Cooldown}
	}
	for _, l := range o.Logs {
		if _, dup := logs[l.ID]; dup {
			errf("use unique log ids", "log %q is listed twice", l.ID)
		}
		logs[l.ID] = l.Path
	}
	for _, t := range o.TCPTargets {
		tcp[t] = true
	}

	for _, c := range b.AllChecks() {
		host, from := c.Str("host"), c.Str("from")
		switch c.Collector {
		case "docker-container":
			if host == n.ID {
				if _, ok := containers[c.Str("container")]; !ok {
					containers[c.Str("container")] = &ops.Container{Name: c.Str("container")}
				}
			}
		case "systemd-unit":
			if host == n.ID {
				u := SystemdUnitName(c)
				if _, ok := units[u]; !ok {
					units[u] = &ops.Unit{Name: u}
				}
			}
		case "log-match":
			if host == n.ID && c.ID != "" {
				if old, ok := logs[c.ID]; ok && old != c.Str("path") {
					errf("give the ops log another id", "log id %q is also a log-match check with another path", c.ID)
				}
				logs[c.ID] = c.Str("path")
			}
		case "tcp-connect":
			if from == n.ID {
				tcp[c.Str("target")] = true
			}
		case "ss-conns":
			if host == n.ID {
				ps := intList(c.Params["ports"])
				if len(ps) == 0 {
					ps = intList(c.Params["port"])
				}
				for _, x := range ps {
					ports[x] = true
				}
			}
		case "wg-transfer", "wg-handshake":
			if host == n.ID {
				w := ops.WG{Iface: c.Str("interface"), Netns: c.Str("netns")}
				wg[w.Netns+"/"+w.Iface] = w
			}
		}
	}

	for _, c := range containers {
		cfg.Containers = append(cfg.Containers, *c)
	}
	for _, u := range units {
		cfg.Units = append(cfg.Units, *u)
	}
	for id, path := range logs {
		cfg.Logs = append(cfg.Logs, ops.Log{ID: id, Path: path})
	}
	for t := range tcp {
		cfg.TCPTargets = append(cfg.TCPTargets, t)
	}
	for x := range ports {
		cfg.Ports = append(cfg.Ports, x)
	}
	for _, w := range wg {
		cfg.WG = append(cfg.WG, w)
	}

	scripts := map[string]*OpsScript{}
	for i := range o.Scripts {
		s := &o.Scripts[i]
		scripts[s.ID] = s
		cfg.Scripts = append(cfg.Scripts, ops.Script{ID: s.ID, Kind: s.Kind, Params: s.Params, Timeout: s.Timeout, Cooldown: s.Cooldown})
	}
	for i := range o.Scripts {
		s := &o.Scripts[i]
		sp := ops.ScriptPolicy{ID: s.ID, Title: s.Title, Disruptive: s.Kind == ops.Change, ReadBefore: s.ReadBefore, Rollback: s.Rollback}
		if s.Disruptive != nil {
			sp.Disruptive = *s.Disruptive
		}
		if s.Kind == ops.Read && (s.Rollback != nil || s.ReadBefore != nil || len(s.Verify) > 0) {
			errf("only change scripts have read_before, rollback and verify", "script %q is a read script", s.ID)
		}
		params := map[string]*ops.Param{}
		for j := range s.Params {
			params[s.Params[j].Name] = &s.Params[j]
		}
		if s.ReadBefore != nil {
			checkCall(s, s.ReadBefore, ops.Read, scripts, params, false, errf)
		}
		if s.Rollback != nil {
			checkCall(s, s.Rollback, ops.Change, scripts, params, s.ReadBefore != nil, errf)
		}
		for _, v := range s.Verify {
			if err := checkVerifyRef(b, v, params); err != "" {
				errf("use node.<id>, link.<id> or route.<id>; {param} only with enum params", "script %q verify %q: %s", s.ID, v, err)
				continue
			}
			sp.Verify = append(sp.Verify, v)
		}
		p.Scripts = append(p.Scripts, sp)
	}
	sort.Slice(p.Scripts, func(i, j int) bool { return p.Scripts[i].ID < p.Scripts[j].ID })
	return cfg
}

// checkCall validates a read_before or rollback call template.
func checkCall(s *OpsScript, c *ops.Call, kind string, scripts map[string]*OpsScript, params map[string]*ops.Param, before bool, errf func(fix, format string, a ...any)) {
	what := "rollback"
	if kind == ops.Read {
		what = "read_before"
	}
	target, ok := scripts[c.Script]
	if !ok || target.Kind != kind {
		errf("point it at a "+kind+" script declared on this node", "script %q %s: %q is not a %s script here", s.ID, what, c.Script, kind)
		return
	}
	want := map[string]*ops.Param{}
	for j := range target.Params {
		want[target.Params[j].Name] = &target.Params[j]
	}
	for k, v := range c.Args {
		tp, ok := want[k]
		if !ok {
			errf("remove it", "script %q %s: %s has no parameter %s", s.ID, what, c.Script, k)
			continue
		}
		switch {
		case strings.HasPrefix(v, "{before.") && strings.HasSuffix(v, "}"):
			if !before {
				errf("add read_before", "script %q %s: %s uses {before.*} without read_before", s.ID, what, k)
			}
		case strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}"):
			if _, ok := params[strings.Trim(v, "{}")]; !ok {
				errf("use a parameter of the script", "script %q %s: %s=%s is not a parameter", s.ID, what, k, v)
			}
		default:
			if !tp.Accepts(v) {
				errf("use a value the parameter accepts", "script %q %s: %s=%s is not accepted by %s", s.ID, what, k, v, c.Script)
			}
		}
	}
	for k := range want {
		if _, ok := c.Args[k]; !ok {
			errf("give every parameter", "script %q %s: missing %s for %s", s.ID, what, k, c.Script)
		}
	}
}

// checkVerifyRef checks a verify id; "{p}" placeholders need enum params and
// must give an existing id for every value.
func checkVerifyRef(b *Bundle, ref string, params map[string]*ops.Param) string {
	if !reVerifyRef.MatchString(ref) {
		return "not a node.<id>, link.<id> or route.<id> reference"
	}
	refs := []string{ref}
	for _, m := range reTemplate.FindAllStringSubmatch(ref, -1) {
		p, ok := params[m[1]]
		if !ok || len(p.Enum) == 0 {
			return fmt.Sprintf("{%s} must be an enum parameter of the script", m[1])
		}
		var next []string
		for _, r := range refs {
			for _, v := range p.Enum {
				next = append(next, strings.ReplaceAll(r, m[0], v))
			}
		}
		refs = next
	}
	for _, r := range refs {
		kind, id, _ := strings.Cut(r, ".")
		ok := false
		switch kind {
		case "node":
			ok = b.NodeByID(id) != nil
		case "link":
			ok = b.LinkByID(id) != nil
		case "route":
			ok = b.RouteByID(id) != nil
		}
		if !ok {
			return fmt.Sprintf("%s does not exist", r)
		}
	}
	return ""
}

// derivedVerify: the node, every link from or to it, every route with such a
// hop; and those routes that serve a critical squad.
func derivedVerify(b *Bundle, node string) (verify, cut []string) {
	verify = []string{"node." + node}
	touch := map[string]bool{}
	for _, l := range b.Links {
		if l.From == node || l.To == node {
			touch[l.ID] = true
			verify = append(verify, "link."+l.ID)
		}
	}
	critical := map[string]bool{}
	for _, s := range b.Map.Settings.CriticalSquads {
		critical[s] = true
	}
	for _, r := range b.Routes {
		through := false
		for _, h := range r.Hops {
			if touch[h] {
				through = true
			}
		}
		if !through {
			continue
		}
		verify = append(verify, "route."+r.ID)
		for _, s := range r.Squads {
			if critical[s] {
				cut = appendUnique(cut, r.ID)
			}
		}
	}
	return verify, cut
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
