package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"vpnmap/pkg/topology"
)

// ------------------------------------------------------------------ status

func cmdStatus() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Overall status, probable root causes, routes and active alerts",
		Long: `Shows what the board shows: the overall status, users online, throughput,
each probable root cause with the users it affects, every route and the
active alerts. Exit code: 0 ok, 1 degraded or unknown, 2 down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var st statusResp
			done, err := readCall(c, "/api/status", atQuery(), &st)
			exitFromStatus(st.Overall.Status)
			if done || err != nil {
				return err
			}
			printHeader(st.Mode, st.Topology, st.At, st.Live)
			printOverall(st.Overall)
			printRootCauses(st.Overall.RootCauses)
			fmt.Println()
			t := newTable("ROUTE", "STATUS", "USERS", "PROBE", "ROOT")
			for _, r := range st.Routes {
				t.add(bold(r.ID)+" "+dim(scrub(r.Title)), statusText(r.Status), usersText(r.Users, r.UsersSource), probeText(r.Probe), rootText(r.Root))
			}
			t.print(os.Stdout)
			printAlerts(st.Alerts)
			printDrift(st.Drift)
			return nil
		},
	}
}

func printHeader(mode string, topo topoRef, at string, live bool) {
	commit := ""
	if topo.GitCommit != "" {
		commit = " @ " + topo.GitCommit
	}
	fmt.Printf("%s · %s mode · topology v%d (%s%s) · %s\n", bold("VPN Topology"), orDash(mode), topo.Version, topo.Hash, commit, headerTime(at, live))
}

func printOverall(o overall) {
	users := grey("–")
	if o.UsersOnline != nil {
		users = thousands(*o.UsersOnline)
	}
	up, down := o.Throughput.UpBps, o.Throughput.DownBps
	fmt.Printf("%s · %s users online · ↑ %s ↓ %s · %s\n", statusWord(o.Status), users, bps(&up), bps(&down), plural(o.ActiveAlerts, "active alert", "active alerts"))
}

func printRootCauses(rcs []rootCause) {
	for _, rc := range rcs {
		users := thousands(rc.UsersAffected)
		if rc.UsersSource != "live" {
			users = "≈" + users
		}
		impact := ""
		if rc.Impact == "degraded" {
			impact = " (degraded service)"
		}
		fmt.Printf("%s %s (%s %s): %s, %s users affected%s (since %s)\n",
			red("Probable root cause:"), bold(scrub(rc.Title)), rc.Kind, rc.ElementID, scrub(rc.Summary), users, impact, sinceText(rc.Since))
	}
}

func probeText(p *routeProbe) string {
	if p == nil {
		return grey("no probe")
	}
	s := scrub(p.Detail)
	if p.Status != "ok" {
		return statusDotOnly(p.Status) + " " + s
	}
	return s
}

func statusDotOnly(s string) string {
	switch s {
	case "ok":
		return green("●")
	case "degraded":
		return amber("●")
	case "down":
		return red("●")
	}
	return grey("○")
}

func rootText(r *elementRef) string {
	if r == nil {
		return ""
	}
	return red(r.Kind + " " + r.ID)
}

func printAlerts(alerts []activeAlert) {
	if len(alerts) == 0 {
		fmt.Println(dim("\nNo active alerts."))
		return
	}
	fmt.Println(bold("\nActive alerts"))
	t := newTable("SEVERITY", "ALERT", "FOR", "SINCE", "MESSAGE")
	for _, a := range alerts {
		msg := scrub(a.Message)
		if a.SilencedUntil != "" {
			msg += dim(" (silenced until " + shortTime(a.SilencedUntil) + ")")
		}
		t.add(severityText(a.Severity), a.ID, orDash(a.Instance), shortTime(a.Since), msg)
	}
	t.print(os.Stdout)
}

func printDrift(items []driftItem) {
	if len(items) == 0 {
		return
	}
	fmt.Println(dim(fmt.Sprintf("\nDrift: %d item(s) the servers report but the topology does not declare:", len(items))))
	for _, d := range items {
		fmt.Println(dim("  · " + scrub(d.Title) + ": " + scrub(d.Detail)))
	}
}

// ------------------------------------------------------------------ get

func cmdGet() *cobra.Command {
	c := &cobra.Command{
		Use:   "get nodes|links|routes|alerts|components|agents",
		Short: "List nodes, links, routes, alerts, components or agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	c.AddCommand(getNodes(), getLinks(), getRoutes(), getAlerts(), getComponents(), getAgents("agents", "agent"))
	return c
}

func getNodes() *cobra.Command {
	return &cobra.Command{
		Use: "nodes", Aliases: []string{"node", "n"}, Short: "Nodes with status, impact, card metrics and uptime", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var items []nodeItem
			if done, err := readCall(c, "/api/nodes", atQuery(), &items); done || err != nil {
				return err
			}
			t := newTable("NODE", "NAME", "STATUS", "IMPACT", "CARD", "UPTIME", "DEFINED IN")
			for _, it := range items {
				n, s := it.Spec, it.State
				status := statusText(s.Status)
				if !s.Observed {
					status = grey("○ not observed")
				}
				t.add(bold(n.ID), scrub(n.Name), status, impactText(s.Impact), cardText(n.Card, s.Metrics), uptimeText(s), fmt.Sprintf("%s:%d", n.Defined.File, n.Defined.Line))
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

func cardText(card []string, m map[string]metricValue) string {
	var parts []string
	for i, k := range card {
		if i >= 3 {
			break
		}
		v, ok := m[k]
		if !ok {
			continue
		}
		parts = append(parts, v.Label+" "+fmtMetric(v.Value, v.Unit))
	}
	return strings.Join(parts, " · ")
}

func uptimeText(s nodeState) string {
	if s.UptimeSec == nil {
		return ""
	}
	u := humanDur(time.Duration(*s.UptimeSec * float64(time.Second)))
	if s.RebootedRecently {
		return amber("⟳ rebooted " + u + " ago")
	}
	return u
}

func getLinks() *cobra.Command {
	return &cobra.Command{
		Use: "links", Aliases: []string{"link", "l"}, Short: "Links with plane, status, throughput and connections", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var items []linkItem
			if done, err := readCall(c, "/api/links", atQuery(), &items); done || err != nil {
				return err
			}
			t := newTable("LINK", "LABEL", "FROM → TO", "PLANE", "STATUS", "↑ Mbps", "↓ Mbps", "CONNS", "IMPACT")
			for _, it := range items {
				l, s := it.Spec, it.State
				status := statusText(s.Status)
				if !s.Observed {
					status = grey("○ not observed")
				}
				plane := l.Plane
				if plane == "control" {
					plane = dim("control")
				}
				t.add(bold(l.ID), orDash(l.Label), l.From+" → "+l.To, plane, status, mbps(s.UpBps), mbps(s.DownBps), num(s.Conns), impactText(s.Impact))
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

func mbps(v *float64) string {
	if v == nil {
		return grey("–")
	}
	return fmt.Sprintf("%.1f", *v/1e6)
}

func getRoutes() *cobra.Command {
	return &cobra.Command{
		Use: "routes", Aliases: []string{"route", "r"}, Short: "Routes with status, users, probe and root cause", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var items []routeItem
			if done, err := readCall(c, "/api/routes", atQuery(), &items); done || err != nil {
				return err
			}
			t := newTable("ROUTE", "TITLE", "STATUS", "USERS", "PROBE", "ROOT", "HOPS")
			for _, it := range items {
				r, s := it.Spec, it.State
				t.add(bold(r.ID), scrub(r.Title), statusText(s.Status), usersText(s.Users, s.UsersSource), probeText(s.Probe), rootText(s.Root), strings.Join(r.Hops, " → "))
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

func getAlerts() *cobra.Command {
	var rules bool
	c := &cobra.Command{
		Use: "alerts", Aliases: []string{"alert"}, Short: "Active alerts (or --rules for the alert rules)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var ar alertsResp
			if done, err := readCall(c, "/api/alerts", atQuery(), &ar); done || err != nil {
				return err
			}
			if !rules {
				if len(ar.Active) == 0 {
					fmt.Println("No active alerts.")
					return nil
				}
				printAlerts(ar.Active)
				return nil
			}
			t := newTable("ALERT", "SEVERITY", "WHEN", "TITLE", "DEFINED IN")
			for _, a := range ar.Rules {
				t.add(bold(a.ID), severityText(a.Severity), a.When, scrub(a.Title), fmt.Sprintf("%s:%d", a.Defined.File, a.Defined.Line))
			}
			t.print(os.Stdout)
			return nil
		},
	}
	c.Flags().BoolVar(&rules, "rules", false, "list the alert rules instead of the active alerts")
	return c
}

func getComponents() *cobra.Command {
	return &cobra.Command{
		Use: "components", Aliases: []string{"component", "comp"}, Short: "Components (topology files) and what they define", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var items []componentItem
			if done, err := readCall(c, "/api/components", nil, &items); done || err != nil {
				return err
			}
			t := newTable("COMPONENT", "TITLE", "FILE", "NODES", "LINKS", "ROUTES", "ALERTS")
			for _, it := range items {
				t.add(bold(it.Name), scrub(it.Title), it.File, itoa(len(it.Nodes)), itoa(len(it.Links)), itoa(len(it.Routes)), itoa(len(it.Alerts)))
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

// ------------------------------------------------------------------ describe

func cmdDescribe() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <node|link|route>",
		Short: "Everything about one node, link or route: spec, checks, metrics, why, events, runbook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var d describeResp
			done, err := readCall(c, "/api/describe/"+url.PathEscape(args[0]), atQuery(), &d)
			if err != nil {
				return err
			}
			var head struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(d.State, &head)
			exitFromStatus(head.Status)
			if done {
				return nil
			}
			return printDescribe(&d)
		},
	}
}

func printDescribe(d *describeResp) error {
	switch d.Kind {
	case "node":
		var spec topology.Node
		var st nodeState
		if err := json.Unmarshal(d.Spec, &spec); err != nil {
			return err
		}
		if err := json.Unmarshal(d.State, &st); err != nil {
			return err
		}
		titleLine("NODE", spec.ID, scrub(spec.Name), statusText(st.Status), impactText(st.Impact))
		kv("Kind", spec.Kind+" · group "+spec.Group)
		kv("Address", strings.Join(spec.Address, ", "))
		kv("Role", scrub(spec.Role))
		agent := "no"
		if spec.Agent {
			agent = "yes"
			if st.Agent != nil {
				agent += fmt.Sprintf(" · v%s · config %s · last push %s", orDash(st.Agent.Version), orDash(st.Agent.ConfigSha), ago(st.Agent.LastSeen))
			} else {
				agent += " · " + amber("not reporting") + " (vpnmap agent install-cmd " + spec.ID + ")"
			}
		}
		kv("Agent", agent)
		kv("Defined in", d.DefinedIn.String()+dim(" (component "+d.DefinedIn.Component+")"))
		kv("Status", statusText(st.Status)+" since "+sinceText(st.Since))
		if st.UptimeSec != nil {
			kv("Uptime", uptimeText(st)+dim(" (boot "+localTime(st.BootTime)+")"))
		}
		if len(spec.Facts) > 0 {
			fmt.Println(bold("\nFacts"))
			t := newTable("", "")
			for _, f := range spec.Facts {
				t.add(f.Label, scrub(f.Value))
			}
			t.hideHead = true
			printIndented(t, "  ")
		}
		printChecks(st.Checks)
		printMetrics(st.Metrics, spec.Card)
	case "link":
		var spec topology.Link
		var st linkState
		if err := json.Unmarshal(d.Spec, &spec); err != nil {
			return err
		}
		if err := json.Unmarshal(d.State, &st); err != nil {
			return err
		}
		label := spec.ID
		if spec.Label != "" {
			label += " (" + spec.Label + ")"
		}
		titleLine("LINK", label, scrub(spec.Title), statusText(st.Status), impactText(st.Impact))
		kv("From → To", spec.From+" → "+spec.To)
		kv("Plane", spec.Plane)
		kv("Protocol", scrub(spec.Protocol))
		if spec.Traffic != nil {
			tr := topology.CheckSummary(spec.Traffic)
			if spec.Traffic.Fallback != nil {
				tr += " (fallback: " + topology.CheckSummary(spec.Traffic.Fallback) + ")"
			}
			if st.TrafficSource != "" {
				tr += dim(" · using " + st.TrafficSource)
			}
			kv("Traffic", tr)
		}
		kv("Throughput", "↑ "+bps(st.UpBps)+"  ↓ "+bps(st.DownBps))
		if spec.Conns != nil {
			kv("Connections", num(st.Conns)+dim(" · "+topology.CheckSummary(spec.Conns)))
		}
		kv("Defined in", d.DefinedIn.String()+dim(" (component "+d.DefinedIn.Component+")"))
		kv("Status", statusText(st.Status)+" since "+sinceText(st.Since))
		if spec.Notes != "" {
			kv("Notes", scrub(spec.Notes))
		}
		printChecks(st.Checks)
	case "route":
		var spec topology.Route
		var st routeState
		if err := json.Unmarshal(d.Spec, &spec); err != nil {
			return err
		}
		if err := json.Unmarshal(d.State, &st); err != nil {
			return err
		}
		fmt.Printf("%s %s · %s   %s\n", dim("ROUTE"), bold(spec.ID), scrub(spec.Title), statusText(st.Status))
		kv("Hops", strings.Join(spec.Hops, " → "))
		kv("Nodes", strings.Join(spec.Nodes, " → "))
		kv("Squads", strings.Join(spec.Squads, ", "))
		users := usersText(st.Users, st.UsersSource)
		if st.UsersSource != "" {
			users += dim(" (" + st.UsersSource + ")")
		}
		kv("Users", users)
		kv("If broken", spec.Impact)
		kv("Probe", probeText(st.Probe))
		if st.Root != nil {
			kv("Root cause", rootText(st.Root))
		}
		kv("Defined in", d.DefinedIn.String()+dim(" (component "+d.DefinedIn.Component+")"))
		kv("Status", statusText(st.Status)+" since "+sinceText(st.Since))
		fmt.Println(bold("\nHop by hop"))
		for _, h := range st.Hops {
			fmt.Printf("  %s %s %s %s\n", statusText(h.Status), dim(h.Kind), h.ID, impactText(h.Impact))
		}
	default:
		return failf("unexpected kind %q from the hub", d.Kind)
	}
	if len(d.Upstream)+len(d.Downstream) > 0 {
		fmt.Println(bold("\nNeighbours"))
		for _, u := range d.Upstream {
			fmt.Printf("  ↑ upstream    %s %s %s\n", statusText(u.Status), dim(u.Kind), u.ID)
		}
		for _, u := range d.Downstream {
			fmt.Printf("  ↓ downstream  %s %s %s\n", statusText(u.Status), dim(u.Kind), u.ID)
		}
	}
	if len(d.Routes) > 0 && d.Kind != "route" {
		fmt.Printf("%s %s\n", bold("\nRoutes through it:"), strings.Join(d.Routes, ", "))
	}
	fmt.Println(bold("\nWhy"))
	for _, w := range d.Why {
		fmt.Println("  • " + scrub(w))
	}
	if len(d.Events) > 0 {
		fmt.Println(bold("\nRecent events"))
		t := newTable("TIME (IRST)", "KIND", "SEVERITY", "TITLE")
		for i := len(d.Events) - 1; i >= 0; i-- { // oldest first, like `vpnmap events`
			e := d.Events[i]
			t.add(eventTime(e.TS), e.Kind, severityText(e.Severity), truncate(scrub(e.Title), 100))
		}
		printIndented(t, "  ")
	}
	if len(d.Runbook) > 0 {
		fmt.Println()
		printRunbook(d.Runbook, "")
	}
	if len(d.Docs) > 0 {
		fmt.Println(bold("\nDocs"))
		for _, u := range d.Docs {
			fmt.Println("  " + u)
		}
	}
	return nil
}

func kv(k, v string) {
	if v == "" {
		return
	}
	fmt.Printf("  %-12s %s\n", k, v)
}

// titleLine prints "KIND id · name   status impact" without trailing blanks.
func titleLine(kind, id, name, status, impact string) {
	fmt.Println(strings.TrimRight(fmt.Sprintf("%s %s · %s   %s %s", dim(kind), bold(id), name, status, impact), " "))
}

func printChecks(cs []checkState) {
	if len(cs) == 0 {
		return
	}
	fmt.Println(bold("\nChecks"))
	t := newTable("STATUS", "CHECK", "DETAIL", "SOURCE", "OBSERVED")
	for _, c := range cs {
		st := statusText(c.Status)
		if c.Effective != c.Status && c.Effective != "" {
			st += dim(" → " + c.Effective)
		}
		observed := grey("never")
		if c.ObservedAt != "" {
			observed = ago(c.ObservedAt)
		}
		t.add(st, scrub(c.Title)+dim(" ["+c.Collector+"]"), truncate(scrub(c.Detail), 90), c.Source, observed)
	}
	printIndented(t, "  ")
}

func printMetrics(m map[string]metricValue, card []string) {
	if len(m) == 0 {
		return
	}
	onCard := map[string]bool{}
	for _, k := range card {
		onCard[k] = true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if onCard[keys[i]] != onCard[keys[j]] {
			return onCard[keys[i]]
		}
		return keys[i] < keys[j]
	})
	fmt.Println(bold("\nMetrics"))
	t := newTable("METRIC", "VALUE", "")
	for _, k := range keys {
		v := m[k]
		mark := ""
		if onCard[k] {
			mark = dim("on card")
		}
		name := v.Label
		if !strings.EqualFold(v.Label, k) {
			name += dim(" (" + k + ")")
		}
		t.add(name, fmtMetric(v.Value, v.Unit), mark)
	}
	printIndented(t, "  ")
}

// printIndented prints a table with every line indented.
func printIndented(t *table, indent string) {
	var b strings.Builder
	t.print(&b)
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		fmt.Println(indent + l)
	}
}

func eventTime(ts string) string {
	tm, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return orDash(ts)
	}
	return tm.In(tehran).Format("01-02 15:04:05")
}

// ------------------------------------------------------------------ events

func cmdEvents() *cobra.Command {
	var since, entity, kinds string
	var limit int
	var checks bool
	c := &cobra.Command{
		Use:   "events",
		Short: "Timeline of incidents, alerts, state changes, reboots and topology changes",
		Long: `Lists hub events in time order (IRST). --at moves the end of the window
into the past: vpnmap events --since 2h --at 2026-10-13T12:00Z.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("since", since)
			if g.At != "" {
				q.Set("until", g.At)
			}
			if entity != "" {
				q.Set("entity", entity)
			}
			if kinds != "" {
				q.Set("kind", strings.ReplaceAll(kinds, " ", ""))
			}
			if limit > 0 {
				q.Set("limit", itoa(limit))
			}
			if checks {
				q.Set("checks", "true")
			}
			var evs []hubEvent
			if done, err := readCall(c, "/api/events", q, &evs); done || err != nil {
				return err
			}
			if len(evs) == 0 {
				fmt.Printf("No events in the last %s.\n", since)
				return nil
			}
			sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })
			t := newTable("TIME (IRST)", "KIND", "SEVERITY", "TITLE", "DETAIL")
			for _, e := range evs {
				t.add(eventTime(e.TS), e.Kind, severityText(e.Severity), truncate(scrub(e.Title), 90), truncate(scrub(e.Detail), 70))
			}
			t.print(os.Stdout)
			fmt.Println(dim(fmt.Sprintf("%s in the last %s", plural(len(evs), "event", "events"), since)))
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&since, "since", "2h", "how far back (e.g. 30m, 2h, 1d)")
	f.StringVar(&entity, "entity", "", "only events of this node, link or route id")
	f.StringVar(&kinds, "kind", "", "comma-separated kinds: state,incident,alert,reboot,restart,watchdog,log,agent,topology,demo")
	f.IntVar(&limit, "limit", 0, "maximum number of events (hub default 500)")
	f.BoolVar(&checks, "checks", false, "include per-check state changes")
	return c
}

// ------------------------------------------------------------------ metrics

func cmdMetrics() *cobra.Command {
	var since, step, keys string
	c := &cobra.Command{
		Use:   "metrics <node|link|route>",
		Short: "Time series of one element with sparklines and min/avg/max",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && keys == "" {
				return failf("usage: vpnmap metrics <node|link|route> [--since 1h] [--step 1m] (or --keys a,b)")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			if len(args) == 1 {
				q.Set("entity", args[0])
			}
			if keys != "" {
				q.Set("keys", keys)
			}
			q.Set("since", since)
			if step != "" {
				q.Set("step", step)
			}
			if g.At != "" {
				q.Set("until", g.At)
			}
			var ss []series
			if done, err := readCall(c, "/api/metrics", q, &ss); done || err != nil {
				return err
			}
			title := strings.Join(args, "")
			if title == "" {
				title = keys
			}
			fmt.Printf("%s · last %s%s\n", bold(title), since, dim(stepNote(ss)))
			for _, s := range ss {
				printSeries(s)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&since, "since", "1h", "window length (e.g. 30m, 1h, 1d)")
	f.StringVar(&step, "step", "", "resolution (e.g. 1m); default about 240 points")
	f.StringVar(&keys, "keys", "", "explicit series keys (comma-separated) instead of an element's defaults")
	return c
}

func stepNote(ss []series) string {
	for _, s := range ss {
		if len(s.Points) > 1 && s.Points[0][0] != nil && s.Points[1][0] != nil {
			d := time.Duration((*s.Points[1][0] - *s.Points[0][0]) * float64(time.Millisecond))
			return " · step " + d.String()
		}
	}
	return ""
}

func printSeries(s series) {
	vals := make([]*float64, len(s.Points))
	for i, p := range s.Points {
		vals[i] = p[1]
	}
	mn, avg, mx, last := stats(vals)
	unit := s.Unit
	fmt.Printf("\n%s %s\n", bold(scrub(s.Label)), dim("("+s.Key+")"))
	if mn == nil {
		fmt.Println("  " + grey("no data in this window"))
		return
	}
	fmt.Printf("  %s\n", sparkline(vals, 60))
	suffix := ""
	if unit == "users" || unit == "conns" {
		suffix = " " + unit
	}
	fmt.Printf("  min %s · avg %s · max %s · last %s%s\n", fmtMetric(mn, unit), fmtMetric(avg, unit), fmtMetric(mx, unit), fmtMetric(last, unit), suffix)
	// A compact table: about 6 evenly spaced samples.
	n := len(s.Points)
	stepIdx := n / 6
	if stepIdx < 1 {
		stepIdx = 1
	}
	t := newTable("TIME (IRST)", "VALUE")
	for i := 0; i < n; i += stepIdx {
		p := s.Points[i]
		if p[0] == nil {
			continue
		}
		ts := time.UnixMilli(int64(*p[0])).In(tehran).Format("01-02 15:04")
		t.add(ts, fmtMetric(p[1], unit))
	}
	if (n-1)%stepIdx != 0 && s.Points[n-1][0] != nil {
		ts := time.UnixMilli(int64(*s.Points[n-1][0])).In(tehran).Format("01-02 15:04")
		t.add(ts, fmtMetric(s.Points[n-1][1], unit))
	}
	printIndented(t, "  ")
}

// ------------------------------------------------------------------ snapshot

func cmdSnapshot() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot",
		Short: "The full board snapshot (now, or --at a past moment)",
		Long: `Prints the snapshot the board renders. --json gives the complete JSON
(nodes, links, routes, alerts, drift); the human view summarises it.
Example: vpnmap snapshot --at -20h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var s snapshot
			done, err := readCall(c, "/api/snapshot", atQuery(), &s)
			exitFromStatus(s.Overall.Status)
			if done || err != nil {
				return err
			}
			printHeader(s.Mode, s.Topology, s.At, s.Live)
			printOverall(s.Overall)
			printRootCauses(s.Overall.RootCauses)
			fmt.Println(bold("\nNodes"))
			t := newTable("NODE", "STATUS", "IMPACT", "DETAIL")
			for _, id := range sortedMapKeys(s.Nodes) {
				n := s.Nodes[id]
				st := statusText(n.Status)
				if !n.Observed {
					st = grey("○ not observed")
				}
				t.add(id, st, impactText(n.Impact), truncate(failingChecks(n.Checks), 90))
			}
			printIndented(t, "  ")
			fmt.Println(bold("\nLinks"))
			t = newTable("LINK", "STATUS", "IMPACT", "↑ Mbps", "↓ Mbps", "CONNS", "DETAIL")
			for _, id := range sortedMapKeys(s.Links) {
				l := s.Links[id]
				st := statusText(l.Status)
				if !l.Observed {
					st = grey("○ not observed")
				}
				t.add(id, st, impactText(l.Impact), mbps(l.UpBps), mbps(l.DownBps), num(l.Conns), truncate(failingChecks(l.Checks), 70))
			}
			printIndented(t, "  ")
			fmt.Println(bold("\nRoutes"))
			t = newTable("ROUTE", "STATUS", "USERS", "PROBE", "ROOT")
			for _, id := range sortedMapKeys(s.Routes) {
				r := s.Routes[id]
				t.add(id, statusText(r.Status), usersText(r.Users, r.UsersSource), probeText(r.Probe), rootText(r.Root))
			}
			printIndented(t, "  ")
			printAlerts(s.Alerts)
			return nil
		},
	}
}

func failingChecks(cs []checkState) string {
	var parts []string
	for _, c := range cs {
		if c.Effective != "ok" && c.Effective != "unknown" && c.Effective != "" {
			parts = append(parts, scrub(c.Title)+": "+scrub(c.Detail))
		}
	}
	return strings.Join(parts, "; ")
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
