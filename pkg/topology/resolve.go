package topology

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// resolve merges the components into the bundle and runs every semantic
// check: references, ids, collector parameters, routes, alerts, runbooks.
func resolve(b *Bundle, ds *Diagnostics) {
	mapFile := "topology/map.yaml"

	// Groups and sources.
	groups := map[string]bool{}
	for _, g := range b.Map.Groups {
		if groups[g.ID] {
			ds.errorf(mapFile, 0, 0, "rename one of them", "group %q is defined twice", g.ID)
		}
		groups[g.ID] = true
	}
	sources := map[string]bool{"hub": true}
	for _, s := range b.Map.Sources {
		if sources[s.ID] {
			ds.errorf(mapFile, 0, 0, "rename it", "source %q is defined twice (\"hub\" is built in)", s.ID)
		}
		sources[s.ID] = true
	}
	if b.Map.Settings.StaleAfter == 0 {
		b.Map.Settings.StaleAfter = Duration(30 * time.Second)
	}
	if b.Map.Settings.RebootWindow == 0 {
		b.Map.Settings.RebootWindow = Duration(30 * time.Minute)
	}

	// Collect entities, detecting duplicates across files.
	nodeAt := map[string]DefinedIn{}
	linkAt := map[string]DefinedIn{}
	routeAt := map[string]DefinedIn{}
	alertAt := map[string]DefinedIn{}
	compAt := map[string]string{}
	dup := func(kind, id string, here, there DefinedIn) {
		ds.errorf(here.File, here.Line, 0, fmt.Sprintf("ids must be unique across all components; rename it or edit the existing one in %s:%d", there.File, there.Line),
			"%s %q is already defined in %s:%d", kind, id, there.File, there.Line)
	}
	for _, c := range b.Components {
		want := strings.TrimSuffix(strings.TrimSuffix(path.Base(c.File), ".yaml"), ".yml")
		if c.Component != want {
			ds.errorf(c.File, 1, 0, fmt.Sprintf("rename the file to %s.yaml or set `component: %s`", c.Component, want),
				"component name %q does not match its file name", c.Component)
		}
		if other, ok := compAt[c.Component]; ok {
			ds.errorf(c.File, 1, 0, "", "component %q is also defined in %s", c.Component, other)
		}
		compAt[c.Component] = c.File
		for _, n := range c.Nodes {
			if there, ok := nodeAt[n.ID]; ok {
				dup("node", n.ID, n.Defined, there)
				continue
			}
			nodeAt[n.ID] = n.Defined
			b.Nodes = append(b.Nodes, n)
		}
		for _, l := range c.Links {
			if there, ok := linkAt[l.ID]; ok {
				dup("link", l.ID, l.Defined, there)
				continue
			}
			linkAt[l.ID] = l.Defined
			b.Links = append(b.Links, l)
		}
		for _, r := range c.Routes {
			if there, ok := routeAt[r.ID]; ok {
				dup("route", r.ID, r.Defined, there)
				continue
			}
			routeAt[r.ID] = r.Defined
			b.Routes = append(b.Routes, r)
		}
		for _, a := range c.Alerts {
			if there, ok := alertAt[a.ID]; ok {
				dup("alert", a.ID, a.Defined, there)
				continue
			}
			alertAt[a.ID] = a.Defined
			b.Alerts = append(b.Alerts, a)
		}
		for _, rb := range c.Runbook {
			checkRunbook(rb, c.File, 0, ds)
		}
	}
	for id := range nodeAt {
		if _, ok := linkAt[id]; ok {
			ds.warnf(linkAt[id].File, linkAt[id].Line, 0, "use different ids for nodes and links to keep CLI lookups unambiguous", "id %q is used by a node and a link", id)
		}
	}

	nodeIDs := sortedKeys(nodeAt)
	agentNodes := map[string]bool{}
	for _, n := range b.Nodes {
		if n.Agent {
			agentNodes[n.ID] = true
		}
	}
	ctx := &checkCtx{b: b, ds: ds, sources: sources, agentNodes: agentNodes, nodeIDs: nodeIDs}

	// Nodes.
	for i := range b.Nodes {
		n := &b.Nodes[i]
		f, line := n.Defined.File, n.Defined.Line
		if !groups[n.Group] {
			ds.errorf(f, n.Defined.At("group"), 0, didYouMean(n.Group, sortedKeys(groups), "group")+" (groups are declared in topology/map.yaml)", "node %q: unknown group %q", n.ID, n.Group)
		}
		owner := "node:" + n.ID
		used := map[string]bool{}
		for j := range n.Checks {
			ctx.resolveCheck(&n.Checks[j], owner, n.ID, "", UseCheck, used, f)
		}
		if n.Agent && !hasCollector(n.Checks, "boot-time") {
			c := Check{ID: "boot", Collector: "boot-time", OnFail: "degraded", Params: map[string]any{"host": n.ID, "window": b.Map.Settings.RebootWindow.String()}, Line: line}
			ctx.resolveCheck(&c, owner, n.ID, "", UseCheck, used, f)
			n.Checks = append(n.Checks, c)
		}
		for j := range n.Metrics {
			m := &n.Metrics[j]
			if m.ID == "" || m.Label == "" {
				ds.errorf(f, m.Line, 0, "add `id:` and `label:` so the card can show it", "node %q: metric needs id and label", n.ID)
			}
			if IsBuiltinMetric(m.ID) {
				ds.errorf(f, m.Line, 0, "pick another id", "node %q: metric id %q is reserved for the built-in agent metric", n.ID, m.ID)
			}
			ctx.resolveCheck(m, owner, n.ID, "", UseMetric, used, f)
		}
		if len(n.Card) > 3 {
			ds.warnf(f, line, 0, "keep at most 3 numbers on a card", "node %q: card lists %d metrics; only the first 3 are shown", n.ID, len(n.Card))
		}
		for _, id := range n.Card {
			if IsBuiltinMetric(id) {
				if !n.Agent {
					ds.errorf(f, line, 0, "set `agent: true` or pick a metric/check id", "node %q: card metric %q needs an agent on the node", n.ID, id)
				}
				continue
			}
			if !used[id] {
				var c []string
				for k := range used {
					c = append(c, k)
				}
				for _, bm := range BuiltinMetrics {
					c = append(c, bm.ID)
				}
				sort.Strings(c)
				ds.errorf(f, line, 0, didYouMean(id, c, "metric or check"), "node %q: card refers to unknown metric %q", n.ID, id)
			}
		}
		for _, rb := range n.Runbook {
			checkRunbook(rb, f, line, ds)
		}
		checkDocs(n.Docs, f, line, ds)
	}

	// Links.
	labels := map[string]string{}
	for i := range b.Links {
		l := &b.Links[i]
		f, line := l.Defined.File, l.Defined.Line
		for _, end := range []struct{ name, id string }{{"from", l.From}, {"to", l.To}} {
			if _, ok := nodeAt[end.id]; !ok {
				ds.errorf(f, l.Defined.At(end.name), 0, didYouMean(end.id, nodeIDs, "node"), "link %q: unknown node %q in `%s`", l.ID, end.id, end.name)
			}
		}
		if l.From == l.To {
			ds.errorf(f, line, 0, "", "link %q goes from %q to itself", l.ID, l.From)
		}
		if l.Label != "" {
			if other, ok := labels[l.Label]; ok {
				ds.warnf(f, line, 0, "give each link its own label", "link %q: label %q is also used by link %q", l.ID, l.Label, other)
			}
			labels[l.Label] = l.ID
		}
		owner := "link:" + l.ID
		used := map[string]bool{}
		if l.Traffic != nil {
			l.Traffic.ID = "traffic"
			ctx.resolveCheck(l.Traffic, owner, "", l.ID, UseTraffic, used, f)
			if l.Traffic.Fallback != nil {
				l.Traffic.Fallback.ID = "traffic-fallback"
				ctx.resolveCheck(l.Traffic.Fallback, owner, "", l.ID, UseTraffic, used, f)
			}
		}
		if l.Conns != nil {
			l.Conns.ID = "conns"
			ctx.resolveCheck(l.Conns, owner, "", l.ID, UseConns, used, f)
		}
		for j := range l.Health {
			ctx.resolveCheck(&l.Health[j], owner, "", l.ID, UseCheck, used, f)
		}
		if to := b.NodeByID(l.To); l.Plane == "data" && len(l.Health) == 0 && l.Traffic == nil && (to == nil || to.Kind != "internet") {
			ds.warnf(f, line, 0, "add a health check or a traffic source", "link %q has no checks: it will always be grey", l.ID)
		}
		for _, rb := range l.Runbook {
			checkRunbook(rb, f, line, ds)
		}
		checkDocs(l.Docs, f, line, ds)
	}

	// Routes.
	for i := range b.Routes {
		r := &b.Routes[i]
		f, line := r.Defined.File, r.Defined.Line
		if r.Impact == "" {
			r.Impact = "outage"
		}
		r.Nodes = nil
		ok := true
		for j, h := range r.Hops {
			l := b.LinkByID(h)
			if l == nil {
				ds.errorf(f, r.Defined.At("hops"), 0, didYouMean(h, sortedKeys(linkAt), "link"), "route %q: hop %d refers to unknown link %q", r.ID, j+1, h)
				ok = false
				continue
			}
			if j == 0 {
				r.Nodes = append(r.Nodes, l.From)
			} else if prev := b.LinkByID(r.Hops[j-1]); prev != nil && prev.To != l.From {
				ds.errorf(f, r.Defined.At("hops"), 0, fmt.Sprintf("hops must chain: link %q ends at %q, so the next hop must start there", prev.ID, prev.To),
					"route %q: hop %q starts at %q, not at %q", r.ID, h, l.From, prev.To)
				ok = false
			}
			r.Nodes = append(r.Nodes, l.To)
		}
		if ok && len(r.Nodes) > 0 {
			if n := b.NodeByID(r.Nodes[0]); n != nil && n.Kind != "users" && n.Kind != "router" {
				ds.warnf(f, line, 0, "routes normally start at the users node", "route %q starts at %q (kind %s)", r.ID, n.ID, n.Kind)
			}
		}
		owner := "route:" + r.ID
		used := map[string]bool{}
		if r.Probe != nil {
			r.Probe.ID = "probe"
			r.Probe.Params["route"] = r.ID
			ctx.resolveCheck(r.Probe, owner, "", "", UseProbe, used, f)
		}
		if r.Users != nil {
			r.Users.ID = "users"
			ctx.resolveCheck(r.Users, owner, "", "", UseUsers, used, f)
		}
		checkDocs(r.Docs, f, line, ds)
	}

	// Critical squads must be served by some route.
	for _, sq := range b.Map.Settings.CriticalSquads {
		used := false
		for _, r := range b.Routes {
			for _, x := range r.Squads {
				if x == sq {
					used = true
				}
			}
		}
		if !used {
			ds.warnf(mapFile, 0, 0, "add the squad to a route's `squads:` or remove it from settings.critical_squads", "critical squad %q is not served by any route", sq)
		}
	}

	// Alerts.
	for i := range b.Alerts {
		a := &b.Alerts[i]
		f, line := a.Defined.File, a.Defined.At("when")
		e, err := ParseExpr(a.When)
		if err != nil {
			ds.errorf(f, line, 0, `write conditions like "link.e3.health == down for 2m"; see CLAUDE.md`, "alert %q: %v", a.ID, err)
			continue
		}
		a.Expr = e
		for _, r := range e.Refs() {
			ctx.checkRef(r, a.ID, f, line)
		}
		if strings.TrimSpace(a.MessageFa) == "" {
			ds.errorf(f, line, 0, "Telegram alerts are in Persian", "alert %q: message_fa is empty", a.ID)
		}
	}

	// Layout.
	for id := range b.Layout {
		if _, ok := nodeAt[id]; !ok {
			ds.warnf("topology/layout.yaml", 0, 0, "it is cleaned up the next time the layout is saved", "layout has a position for unknown node %q", id)
		}
	}

	sort.SliceStable(b.Nodes, func(i, j int) bool { return b.Nodes[i].ID < b.Nodes[j].ID })
	sort.SliceStable(b.Links, func(i, j int) bool { return b.Links[i].ID < b.Links[j].ID })
	sort.SliceStable(b.Routes, func(i, j int) bool { return b.Routes[i].ID < b.Routes[j].ID })
	sort.SliceStable(b.Alerts, func(i, j int) bool { return b.Alerts[i].ID < b.Alerts[j].ID })
}

type checkCtx struct {
	b          *Bundle
	ds         *Diagnostics
	sources    map[string]bool
	agentNodes map[string]bool
	nodeIDs    []string
}

var reCheckID = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// resolveCheck assigns the id/key, fills defaults and checks parameter values.
func (c *checkCtx) resolveCheck(ch *Check, owner, nodeID, linkID, usage string, used map[string]bool, file string) {
	info := Collector(ch.Collector)
	if info == nil {
		return // reported by the structural pass
	}
	if ch.ID == "" {
		ch.ID = ch.Collector
		for n := 2; used[ch.ID]; n++ {
			ch.ID = fmt.Sprintf("%s-%d", ch.Collector, n)
		}
	} else if used[ch.ID] {
		c.ds.errorf(file, ch.Line, 0, "give each check of the same node/link its own id", "%s: check id %q is used twice", owner, ch.ID)
	}
	used[ch.ID] = true
	ch.Key = owner + ":" + ch.ID
	if ch.Params == nil {
		ch.Params = map[string]any{}
	}
	for _, p := range info.Params {
		if _, ok := ch.Params[p.Name]; !ok && p.Default != "" {
			switch p.Type {
			case PInt:
				n, _ := strconv.Atoi(p.Default)
				ch.Params[p.Name] = n
			case PBool:
				ch.Params[p.Name] = p.Default == "true"
			default:
				ch.Params[p.Name] = p.Default
			}
		}
	}
	if ch.OnFail == "" {
		ch.OnFail = "down"
		switch ch.Collector {
		case "boot-time", "tls-expiry", "log-match":
			ch.OnFail = "degraded"
		}
	}
	at := func(fix, format string, a ...any) { c.ds.errorf(file, ch.Line, 0, fix, owner+": "+format, a...) }

	for _, p := range info.Params {
		v, ok := ch.Params[p.Name]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		switch p.Type {
		case PNode:
			if !c.agentNodes[s] {
				if c.b.NodeByID(s) != nil {
					at(fmt.Sprintf("set `agent: true` on node %q (and install the agent there), or collect from another node", s), "%s: node %q has no agent", p.Name, s)
				} else {
					at(didYouMean(s, c.nodeIDs, "node"), "%s: unknown node %q", p.Name, s)
				}
			}
		case PFrom:
			if !c.sources[s] && !c.agentNodes[s] {
				cands := sortedKeys(c.sources)
				for n := range c.agentNodes {
					cands = append(cands, n)
				}
				sort.Strings(cands)
				at(didYouMean(s, cands, "source"), "from: %q is not a source or a node with an agent", s)
			}
		case PTarget:
			if _, port, err := net.SplitHostPort(s); err != nil || port == "" {
				at("write host:port", "target %q is not host:port", s)
			}
		case PRegex:
			if _, err := regexp.Compile(s); err != nil {
				at("use RE2 syntax", "pattern: %v", err)
			}
		case PDuration:
			if _, err := ParseDuration(s); err != nil {
				at("write a duration like 15s, 3m", "%s: %v", p.Name, err)
			}
		case PCheckID:
			if !reCheckID.MatchString(s) {
				at("use lowercase letters, digits, - _ .", "%s: invalid check id %q", p.Name, s)
			}
		case PMatch:
			c.checkMatch(ch, p.Name, owner, file)
		}
	}

	// Collector-specific rules.
	switch ch.Collector {
	case "xray-stats":
		if (ch.Str("outbound") == "") == (ch.Str("inbound") == "") {
			at("set exactly one of `outbound:` or `inbound:`", "xray-stats needs an outbound or an inbound tag")
		}
	case "wg-transfer", "nft-counter":
		if linkID != "" && ch.Collector == "wg-transfer" && ch.Str("up") == "" {
			l := c.b.LinkByID(linkID)
			if l != nil && ch.Str("host") != l.From && ch.Str("host") != l.To {
				at("set `up: rx` or `up: tx`", "host %q is neither end of link %q, so the direction cannot be inferred", ch.Str("host"), linkID)
			}
		}
	case "ss-conns":
		if (ch.Params["port"] == nil) == (ch.Params["ports"] == nil) {
			at("set `port: 2053` or `ports: [443, 8443]`", "ss-conns needs port or ports")
		}
	case "log-match":
		if ch.Str("mode") == "freshness" && ch.Str("max_age") == "" {
			at("add `max_age: 35m`", "log-match in freshness mode needs max_age")
		}
	case "e2e-probe":
		if usage != UseProbe && ch.Str("route") == "" {
			at("put the probe under the route as `probe:`", "e2e-probe must be the `probe:` of a route")
		}
	}
	if ch.Fallback != nil && usage != UseTraffic {
		at("", "fallback is only allowed on traffic")
	}
}

func (c *checkCtx) checkMatch(ch *Check, name, owner, file string) {
	m := ch.Map(name)
	if m == nil {
		return
	}
	hasPort := m["sport"] != nil || m["dport"] != nil
	if hasPort && m["proto"] == nil {
		c.ds.errorf(file, ch.Line, 0, "add `proto: tcp` (or udp)", "%s: %s match uses a port without proto", owner, name)
	}
	if len(m) == 1 {
		c.ds.warnf(file, ch.Line, 0, "add an address, port or interface", "%s: %s match only has a hook and will count all traffic", owner, name)
	}
}

func (c *checkCtx) checkRef(r *Ref, alertID, file string, line int) {
	at := func(fix, format string, a ...any) {
		c.ds.errorf(file, line, 0, fix, "alert %q: "+format, append([]any{alertID}, a...)...)
	}
	switch r.Kind {
	case "collector":
		if Collector(r.ID) == nil {
			at(didYouMean(r.ID, CollectorNames(), "collector"), "unknown collector %q", r.ID)
		}
		return
	case "node":
		if r.ID != "*" && c.b.NodeByID(r.ID) == nil {
			at(didYouMean(r.ID, c.nodeIDs, "node"), "unknown node %q", r.ID)
			return
		}
	case "link":
		if r.ID != "*" && c.b.LinkByID(r.ID) == nil {
			var ids []string
			for _, l := range c.b.Links {
				ids = append(ids, l.ID)
			}
			at(didYouMean(r.ID, ids, "link"), "unknown link %q", r.ID)
			return
		}
	case "route":
		if r.ID != "*" && c.b.RouteByID(r.ID) == nil {
			var ids []string
			for _, x := range c.b.Routes {
				ids = append(ids, x.ID)
			}
			at(didYouMean(r.ID, ids, "route"), "unknown route %q", r.ID)
			return
		}
	}
	if r.Check != "" && r.ID != "*" {
		var checks []Check
		switch r.Kind {
		case "node":
			n := c.b.NodeByID(r.ID)
			checks = append(append(checks, n.Checks...), n.Metrics...)
		case "link":
			l := c.b.LinkByID(r.ID)
			checks = append(checks, l.Health...)
			if l.Traffic != nil {
				checks = append(checks, *l.Traffic)
			}
			if l.Conns != nil {
				checks = append(checks, *l.Conns)
			}
		case "route":
			rt := c.b.RouteByID(r.ID)
			if rt.Probe != nil {
				checks = append(checks, *rt.Probe)
			}
		}
		var ids []string
		for _, ch := range checks {
			ids = append(ids, ch.ID)
			if ch.ID == r.Check {
				return
			}
		}
		at(didYouMean(r.Check, ids, "check"), "%s %q has no check %q", r.Kind, r.ID, r.Check)
	}
}

func hasCollector(cs []Check, name string) bool {
	for _, c := range cs {
		if c.Collector == name {
			return true
		}
	}
	return false
}

func checkDocs(u, file string, line int, ds *Diagnostics) {
	if u != "" && !strings.HasPrefix(u, "https://") {
		ds.errorf(file, line, 0, "use the https:// link of the Notion page", "docs link %q is not https", u)
	}
}

// Runbook commands are shown as read-only diagnostics, so anything that
// changes a server is rejected.
var (
	dangerTokens = map[string]bool{"restart": true, "reboot": true, "shutdown": true, "poweroff": true, "kill": true, "pkill": true, "killall": true,
		"rm": true, "rmdir": true, "mkfs": true, "dd": true, "truncate": true, "reload": true, "tee": true, "shred": true, "chmod": true, "chown": true}
	cliTools  = map[string]bool{"systemctl": true, "docker": true, "relay-ctl": true, "mullvad-ns": true, "xbridger": true, "x-ui": true, "ufw": true, "nft": true, "iptables": true, "ip": true, "wg-quick": true, "wg": true, "compose": true}
	readVerbs = map[string]bool{"status": true, "is-active": true, "is-enabled": true, "show": true, "list": true, "list-units": true, "list-timers": true, "cat": true,
		"ps": true, "logs": true, "inspect": true, "stats": true, "top": true, "images": true, "version": true, "info": true, "exec": true, "doctor": true,
		"health": true, "conn": true, "config": true, "settings": true, "template": true, "nodes": true, "-j": true, "addr": true, "route": true, "rule": true,
		"link": true, "netns": true, "neigh": true, "-s": true, "-4": true, "-6": true, "-br": true, "-d": true, "dump": true, "transfer": true, "latest-handshakes": true, "endpoints": true}
	writeSub = map[string]bool{"start": true, "stop": true, "restart": true, "enable": true, "disable": true, "apply": true, "edit": true, "select": true, "delete": true,
		"del": true, "set": true, "add": true, "remove": true, "rm": true, "up": true, "down": true, "flush": true, "insert": true, "replace": true, "pull": true,
		"start-all": true, "stop-all": true, "mask": true, "kill": true, "allow": true, "deny": true, "reset": true, "create": true, "exec-root": true}
	tokSplit = regexp.MustCompile(`[\s;&|()'"` + "`" + `]+`)
	redirect = regexp.MustCompile(`(^|[^0-9&])>{1,2}\s*([^&\s]+)`)
)

func checkRunbook(rb RunbookCmd, file string, line int, ds *Diagnostics) {
	reason := RunbookProblem(rb.Cmd)
	if reason != "" {
		ds.errorf(file, line, 0, "runbook commands must be read-only diagnostics; keep fixes in Notion 07 · Operations Runbook",
			"runbook %q: %s", rb.Title, reason)
	}
}

// RunbookProblem returns why a command is not read-only, or "".
func RunbookProblem(cmd string) string {
	for _, m := range redirect.FindAllStringSubmatch(cmd, -1) {
		if m[2] != "/dev/null" && !strings.HasPrefix(m[2], "/dev/tcp/") {
			return fmt.Sprintf("writes to %s", m[2])
		}
	}
	toks := tokSplit.Split(cmd, -1)
	for i, t := range toks {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if dangerTokens[t] {
			return fmt.Sprintf("uses %q", t)
		}
		if cliTools[t] {
			// The next few arguments after a known tool must not be write verbs
			// (catches `systemctl restart x`, `xbridger conn start x`).
			seen := 0
			for _, n := range toks[i+1:] {
				if n == "" || strings.HasPrefix(n, "-") {
					continue
				}
				if writeSub[n] {
					return fmt.Sprintf("runs `%s ... %s`", t, n)
				}
				if seen++; seen >= 3 {
					break
				}
			}
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
