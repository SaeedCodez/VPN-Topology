package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// ------------------------------------------------------------------ diagnose

func cmdDiagnose() *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "Probable root causes with evidence, affected users and read-only diagnostics",
		Long: `Explains what is wrong: each probable root cause with its evidence, the
nodes and links it affects, the users affected (≈ for estimates) and the
runbook commands to confirm it. The commands are printed, never executed.
Exit code: 0 ok, 1 degraded or unknown, 2 down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var d diagnosis
			done, err := readCall(c, "/api/diagnose", atQuery(), &d)
			exitFromStatus(d.Status)
			if done || err != nil {
				return err
			}
			live := g.At == ""
			fmt.Printf("%s %s · %s\n", bold("Diagnosis"), statusWord(d.Status), headerTime(d.At, live))
			fmt.Println(scrub(d.Headline))
			for i, rc := range d.RootCauses {
				fmt.Printf("\n%s %s %s\n", red(fmt.Sprintf("Root cause %d:", i+1)), bold(scrub(rc.Title)), dim("("+rc.Kind+" "+rc.ElementID+")"))
				fmt.Printf("  %s\n", scrub(rc.Summary))
				users := thousands(rc.UsersAffected)
				src := rc.UsersSource
				if src != "live" {
					users = "≈" + users
				}
				kv("Impact", fmt.Sprintf("%s · %s users affected (%s)", rc.Impact, users, src))
				kv("Since", sinceText(rc.Since))
				kv("Routes", strings.Join(rc.Routes, ", "))
				if rc.DefinedIn != nil {
					kv("Defined in", rc.DefinedIn.String())
				}
				if len(rc.Evidence) > 0 {
					fmt.Println("  Evidence:")
					for _, e := range rc.Evidence {
						fmt.Println("    • " + scrub(e))
					}
				}
				var aff []string
				if len(rc.Affected.Nodes) > 0 {
					aff = append(aff, "nodes "+strings.Join(rc.Affected.Nodes, ", "))
				}
				if len(rc.Affected.Links) > 0 {
					aff = append(aff, "links "+strings.Join(rc.Affected.Links, ", "))
				}
				if len(aff) > 0 {
					fmt.Println("  Affected downstream: " + strings.Join(aff, " · "))
				}
				if len(rc.Runbook) > 0 {
					printRunbook(rc.Runbook, "  ")
				}
				for _, u := range rc.Docs {
					fmt.Println("  Docs: " + u)
				}
				fmt.Println(dim(fmt.Sprintf("  More: vpnmap describe %s · vpnmap why %s", rc.ElementID, rc.ElementID)))
			}
			if len(d.Degraded) > 0 {
				fmt.Println(bold("\nOther degraded elements"))
				for _, x := range d.Degraded {
					fmt.Printf("  %s %s %s\n", statusText(x.Status), dim(x.Kind), bold(x.ID))
					for _, part := range strings.Split(x.Detail, "; ") {
						if part != "" {
							fmt.Println("      " + truncate(scrub(part), 140))
						}
					}
				}
			}
			printAlerts(d.Alerts)
			if len(d.RecentEvents) > 0 {
				fmt.Println(bold("\nRecent events (2 h)"))
				t := newTable("TIME (IRST)", "KIND", "SEVERITY", "TITLE")
				evs := d.RecentEvents
				for i := len(evs) - 1; i >= 0; i-- { // oldest first
					e := evs[i]
					t.add(eventTime(e.TS), e.Kind, severityText(e.Severity), truncate(scrub(e.Title), 100))
				}
				printIndented(t, "  ")
			}
			return nil
		},
	}
}

// ------------------------------------------------------------------ path

func cmdPath() *cobra.Command {
	var route string
	c := &cobra.Command{
		Use:   "path <from> <to>",
		Short: "Hop-by-hop status between two nodes (along a route)",
		Long: `Walks the declared route from one node to another (the busiest route that
contains both, or --route) and shows every node and link with its status,
metrics and details. Exit code from the path status.
Example: vpnmap path users france-exit`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := atQuery()
			q.Set("from", args[0])
			q.Set("to", args[1])
			if route != "" {
				q.Set("route", route)
			}
			var p pathView
			done, err := readCall(c, "/api/path", q, &p)
			exitFromStatus(p.Status)
			if done || err != nil {
				return err
			}
			via := dim("(shortest data path, no declared route)")
			if p.Route != "" {
				via = "via route " + bold(p.Route)
			}
			fmt.Printf("%s %s → %s %s · %s\n", bold("Path"), p.From, p.To, via, headerTime(p.At, g.At == ""))
			for _, h := range p.Hops {
				if h.Kind == "node" {
					fmt.Printf("%s %s %s %s\n", statusDotOnly(h.Status), bold(h.ID), dim(scrub(h.Name)), impactText(h.Impact))
					if h.Metrics != "" {
						fmt.Printf("│   %s\n", scrub(h.Metrics))
					}
					if h.Detail != "" {
						fmt.Printf("│   %s\n", dim(truncate(scrub(h.Detail), 120)))
					}
					continue
				}
				fmt.Printf("│ %s %s %s %s\n", statusDotOnly(h.Status), h.ID, dim(scrub(h.Name)), impactText(h.Impact))
				fmt.Printf("│     %s\n", scrub(h.Metrics))
				if h.Detail != "" {
					fmt.Printf("│     %s\n", dim(truncate(scrub(h.Detail), 120)))
				}
			}
			fmt.Printf("\n%s %s %s\n", bold("Verdict:"), statusText(p.Status), scrub(p.Verdict))
			return nil
		},
	}
	c.Flags().StringVar(&route, "route", "", "follow this route id")
	return c
}

// ------------------------------------------------------------------ why

func cmdWhy() *cobra.Command {
	return &cobra.Command{
		Use:   "why <node|link|route>",
		Short: "Why an element has its colour: failing checks, root cause, reboots",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var d describeResp
			if _, err := c.get("/api/describe/"+url.PathEscape(args[0]), atQuery(), &d); err != nil {
				return err
			}
			var st struct {
				Status string `json:"status"`
				Impact string `json:"impact"`
			}
			_ = json.Unmarshal(d.State, &st)
			exitFromStatus(st.Status)
			if g.JSON {
				return printJSON(map[string]any{"id": d.ID, "kind": d.Kind, "at": d.At, "status": st.Status, "impact": st.Impact, "reasons": scrubAll(d.Why)})
			}
			fmt.Printf("%s %s %s %s\n", dim(strings.ToUpper(d.Kind)), bold(d.ID), statusText(st.Status), impactText(st.Impact))
			for _, w := range d.Why {
				fmt.Println("  • " + scrub(w))
			}
			if st.Status != "ok" {
				fmt.Println(dim("More: vpnmap describe " + d.ID))
			}
			return nil
		},
	}
}
