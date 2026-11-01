package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/topology"
)

// noAt rejects --at on commands that change things or read local files.
func noAt(cmd string) error {
	if g.At != "" {
		return failf("--at only applies to read commands (status, get, describe, ...), not to %s", cmd)
	}
	return nil
}

// validBundle loads the local files and fails with exit 3 on errors.
func validBundle() (*topology.Bundle, string, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, "", failf("%v", err)
	}
	b, ds, err := loadBundle(true)
	if err != nil {
		return nil, "", err
	}
	if b == nil || ds.HasErrors() {
		for _, d := range ds {
			if d.Severity == "error" {
				fmt.Fprintln(os.Stderr, red(d.String()))
			}
		}
		return nil, "", exitWith(ExitInvalid, "the topology has errors: fix them (see `vpnmap validate`) before planning or applying")
	}
	return b, root, nil
}

// ------------------------------------------------------------------ plan

type localCheckReq struct {
	ID       string `json:"id"`
	RepoFile string `json:"repo_file,omitempty"`
	Install  string `json:"install"`
}

type agentReq struct {
	Node          string              `json:"node"`
	Address       string              `json:"address"`
	Reporting     bool                `json:"reporting"`
	Version       string              `json:"version,omitempty"`
	Stale         bool                `json:"stale"`
	LastSeen      string              `json:"last_seen,omitempty"`
	ReportedSHA   string              `json:"reported_config_sha,omitempty"`
	GeneratedSHA  string              `json:"generated_config_sha,omitempty"`
	ConfigError   string              `json:"config_error,omitempty"`
	ConfigCurrent bool                `json:"config_current"`
	Missing       map[string][]string `json:"missing_capabilities,omitempty"`
	MissingChecks []localCheckReq     `json:"missing_local_checks,omitempty"`
	NftChanged    bool                `json:"nft_changed"`
	NftReason     string              `json:"nft_reason,omitempty"`
	NftCounters   int                 `json:"nft_counters"`
	Commands      []string            `json:"commands"`
	cfg           *agentcfg.Config
}

type planResult struct {
	Hub           string               `json:"hub"`
	AgentHub      string               `json:"agent_hub"`
	LiveVersion   int                  `json:"live_version"`
	LiveHash      string               `json:"live_hash"`
	LocalHash     string               `json:"local_hash"`
	LocalCommit   string               `json:"local_commit,omitempty"`
	Diff          *topology.BundleDiff `json:"diff"`
	Agents        []agentReq           `json:"agents"`
	UnknownAgents []string             `json:"unknown_agents,omitempty"`
	Note          string               `json:"note"`
}

const planNote = "Nothing is changed on any server by apply; these commands are for you to run."

func computePlan(c *client, b *topology.Bundle, root string) (*planResult, error) {
	var live topologyResp
	if _, err := c.get("/api/topology", nil, &live); err != nil {
		return nil, err
	}
	if len(live.Bundle) == 0 || string(live.Bundle) == "null" {
		return nil, failf("the hub has no active topology")
	}
	diff, err := topology.DiffJSON(live.Bundle, b.JSON())
	if err != nil {
		return nil, failf("cannot compare with the hub's topology: %v", err)
	}
	liveB, _ := topology.ParseBundle(live.Bundle)
	var agents []agentInfo
	if _, err := c.get("/api/agents", nil, &agents); err != nil {
		return nil, err
	}
	byNode := map[string]agentInfo{}
	for _, a := range agents {
		byNode[a.Node] = a
	}
	p := &planResult{Hub: c.base, AgentHub: agentHubURL(b), LiveVersion: live.Version, LocalHash: b.Hash, LocalCommit: b.Source.GitCommit, Diff: diff, Note: planNote}
	if liveB != nil {
		p.LiveHash = liveB.Hash
	}
	hosts := b.AgentHosts()
	known := map[string]bool{}
	for _, h := range hosts {
		known[h] = true
		p.Agents = append(p.Agents, agentRequirements(b, liveB, h, byNode, root, p.AgentHub))
	}
	for _, a := range agents {
		if !known[a.Node] {
			p.UnknownAgents = append(p.UnknownAgents, a.Node)
		}
	}
	sort.Strings(p.UnknownAgents)
	return p, nil
}

func agentRequirements(b, liveB *topology.Bundle, host string, byNode map[string]agentInfo, root, agentHub string) agentReq {
	r := agentReq{Node: host, Address: nodeAddress(b, host)}
	cfg, err := topology.AgentConfig(b, host, agentHub)
	if err != nil {
		r.ConfigError = err.Error()
		r.Commands = append(r.Commands, "fix the topology so `vpnmap agent config "+host+"` works")
		return r
	}
	r.cfg = cfg
	_, sha, _ := topology.AgentConfigYAML(cfg)
	r.GeneratedSHA = sha
	if cfg.Nft != nil {
		r.NftCounters = len(cfg.Nft.Counters)
	}
	a, reporting := byNode[host]
	r.Reporting = reporting
	if !reporting {
		r.Commands = append(r.Commands, "vpnmap agent install-cmd "+host)
	} else {
		r.Version, r.LastSeen, r.ReportedSHA = a.Version, a.LastSeen, a.ConfigSha
		r.Stale = a.AgeSec > 120
		r.ConfigCurrent = a.ConfigSha == sha
		r.Missing = missingCapabilities(cfg, a.Capabilities)
		if !r.ConfigCurrent {
			r.Commands = append(r.Commands, "vpnmap agent install-cmd "+host+"   # steps 1, 2 and 4: re-run the installer with the new config (it keeps the token)")
		}
	}
	// Local checks the agent does not report.
	have := map[string]bool{}
	if reporting {
		for _, id := range a.Capabilities["checks"] {
			have[id] = true
		}
	}
	for _, id := range topology.LocalCheckIDs(cfg) {
		if have[id] {
			continue
		}
		lc := localCheckReq{ID: id}
		repoFile := filepath.Join("apps", "agent", "deploy", "checks.d", id+".check")
		dest := topology.AgentChecksDir + "/"
		if _, err := os.Stat(filepath.Join(root, repoFile)); err == nil {
			lc.RepoFile = repoFile
			lc.Install = fmt.Sprintf("scp %s root@%s:/tmp/%s.check && ssh root@%s 'install -o root -g root -m 0755 /tmp/%s.check %s'", repoFile, r.Address, id, r.Address, id, dest)
		} else {
			lc.Install = fmt.Sprintf("write %s%s.check (a read-only script that prints JSON {\"status\":\"ok|degraded|down\",\"detail\":\"...\"}), then: scp %s.check root@%s:/tmp/ && ssh root@%s 'install -o root -g root -m 0755 /tmp/%s.check %s'", dest, id, id, r.Address, r.Address, id, dest)
		}
		r.MissingChecks = append(r.MissingChecks, lc)
		r.Commands = append(r.Commands, lc.Install)
	}
	// nft rules: the topology changes them, or the agent runs other counters.
	var liveCounters []agentcfg.NftCounter
	if liveB != nil {
		if lc, err := topology.AgentConfig(liveB, host, agentHub); err == nil && lc.Nft != nil {
			liveCounters = lc.Nft.Counters
		}
	}
	var newCounters []agentcfg.NftCounter
	if cfg.Nft != nil {
		newCounters = cfg.Nft.Counters
	}
	switch {
	case agentcfg.RenderNft(liveCounters) != agentcfg.RenderNft(newCounters):
		r.NftChanged, r.NftReason = true, "the topology changes the nft counters"
	case reporting && len(r.Missing["nft_counters"]) > 0:
		r.NftChanged, r.NftReason = true, "the agent does not run every counter yet"
	}
	if r.NftChanged {
		r.Commands = append(r.Commands, "vpnmap agent nft "+host+"   # review the counter-only rules for table inet vpnmap (the agent applies them when it restarts with the new config)")
	}
	return r
}

// missingCapabilities lists what the generated config collects but the agent does not report.
func missingCapabilities(cfg *agentcfg.Config, caps map[string][]string) map[string][]string {
	want := map[string][]string{"units": cfg.Units, "containers": cfg.Containers}
	if cfg.Nft != nil {
		for _, n := range cfg.Nft.Counters {
			want["nft_counters"] = append(want["nft_counters"], n.Name)
		}
	}
	for _, w := range cfg.WG {
		id := w.Iface
		if w.Netns != "" {
			id = w.Netns + "/" + w.Iface
		}
		want["wg"] = append(want["wg"], id)
	}
	for _, s := range cfg.SS {
		want["ss"] = append(want["ss"], s.ID)
	}
	for _, t := range cfg.TCP {
		want["tcp"] = append(want["tcp"], t.Target)
	}
	for _, h := range cfg.HTTP {
		want["http"] = append(want["http"], h.ID)
	}
	for _, l := range cfg.Logs {
		want["logs"] = append(want["logs"], l.ID)
	}
	for _, p := range cfg.Prom {
		want["prom"] = append(want["prom"], p.ID)
	}
	if cfg.Probe != nil {
		for _, r := range cfg.Probe.Routes {
			want["routes"] = append(want["routes"], r.Route)
		}
	}
	out := map[string][]string{}
	for k, ids := range want {
		have := map[string]bool{}
		for _, x := range caps[k] {
			have[x] = true
		}
		for _, id := range ids {
			if !have[id] {
				out[k] = append(out[k], id)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nodeAddress is the first address of a node, for ssh/scp commands.
func nodeAddress(b *topology.Bundle, id string) string {
	if n := b.NodeByID(id); n != nil && len(n.Address) > 0 {
		a := n.Address[0]
		if h, _, ok := strings.Cut(a, ":"); ok && !strings.Contains(h, "[") {
			a = h
		}
		return a
	}
	return "<" + id + "-ip>"
}

func printPlan(p *planResult, b *topology.Bundle) {
	commit := ""
	if p.LocalCommit != "" {
		commit = ", commit " + p.LocalCommit
	}
	fmt.Printf("%s local topology (hash %s%s) → hub %s (v%d, hash %s)\n\n", bold("Plan:"), p.LocalHash, commit, p.Hub, p.LiveVersion, orDash(p.LiveHash))
	fmt.Println(bold("Topology changes"))
	printDiff(p.Diff)
	fmt.Println(bold("\nAgent and server requirements"))
	if p.AgentHub != p.Hub {
		fmt.Println(dim("  agent configs push to " + p.AgentHub))
	}
	for _, r := range p.Agents {
		fmt.Printf("  %s %s\n", bold(r.Node), dim("("+r.Address+")"))
		if r.ConfigError != "" {
			fmt.Println("    " + red("✗ cannot generate its agent config: ") + r.ConfigError)
			continue
		}
		if r.Stale {
			fmt.Printf("    %s the agent last pushed %s: check vpnmap-agent on the server (systemctl status vpnmap-agent)\n", red("✗"), ago(r.LastSeen))
		}
		switch {
		case !r.Reporting:
			fmt.Printf("    %s no agent reporting → %s\n", red("✗"), "vpnmap agent install-cmd "+r.Node)
		case r.ConfigCurrent:
			fmt.Printf("    %s agent v%s reporting, config current (sha %s)\n", green("✓"), orDash(r.Version), r.GeneratedSHA)
		default:
			fmt.Printf("    %s agent config out of date: it runs %s, the topology generates %s\n", amber("!"), orDash(r.ReportedSHA), r.GeneratedSHA)
			for _, k := range sortedMapKeys(r.Missing) {
				fmt.Printf("      not collected yet: %s %s\n", k, strings.Join(r.Missing[k], ", "))
			}
			fmt.Printf("      update: vpnmap agent config %s > /tmp/%s.agent.yaml, copy it, and re-run the installer (`vpnmap agent install-cmd %s`, steps 1, 2 and 4; it keeps the token)\n", r.Node, r.Node, r.Node)
		}
		for _, lc := range r.MissingChecks {
			fmt.Printf("    %s local check %s is not installed\n", amber("!"), bold(lc.ID))
			if lc.RepoFile != "" {
				fmt.Printf("      file:    %s\n", lc.RepoFile)
			}
			fmt.Printf("      install: %s\n", lc.Install)
		}
		if r.NftChanged {
			fmt.Printf("    %s nft counter rules change (%s): review `vpnmap agent nft %s`\n", amber("!"), r.NftReason, r.Node)
		}
	}
	for _, u := range p.UnknownAgents {
		fmt.Printf("  %s an agent reports as %q, which is not a node with agent: true in the topology\n", amber("?"), u)
	}
	fmt.Println("\n" + p.Note)
}

func printDiff(d *topology.BundleDiff) {
	if d.Empty() {
		fmt.Printf("  No differences: the hub already runs this topology (hash %s).\n", d.NewHash)
		return
	}
	sym := map[string]string{"add": green("+"), "remove": red("-"), "change": amber("~")}
	for _, c := range d.Changes {
		title := ""
		if c.Title != "" {
			title = " " + dim(fmt.Sprintf("%q", c.Title))
		}
		where := ""
		if c.File != "" {
			where = "  " + dim(c.File)
			if c.Line > 0 {
				where += dim(":" + itoa(c.Line))
			}
		}
		kind := c.Kind
		if c.Kind == "check" && c.Collector != "" {
			title = " " + dim("("+c.Collector+")")
		}
		fmt.Printf("  %s %s %s%s%s\n", sym[c.Op], kind, bold(c.ID), title, where)
		for _, ck := range c.Checks {
			fmt.Printf("      %s check %s\n", sym[c.Op], ck)
		}
		for _, f := range c.Fields {
			fmt.Printf("      %s: %s → %s\n", f.Path, truncate(scrub(topology.FormatValue(f.Old)), 100), truncate(scrub(topology.FormatValue(f.New)), 100))
		}
	}
	fmt.Printf("  %d to add, %d to change, %d to remove.\n", d.Count("add"), d.Count("change"), d.Count("remove"))
}

func cmdPlan() *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Compare the local topology with the hub and list what servers need",
		Long: `Validates the local files, compares them with the topology the hub runs
(added, removed and changed nodes, links, routes, checks and alerts, field
by field) and lists what the servers need: agents to install, agent configs
to update, local checks to install and nft counter changes.
` + planNote,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("plan"); err != nil {
				return err
			}
			b, root, err := validBundle()
			if err != nil {
				return err
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			p, err := computePlan(c, b, root)
			if err != nil {
				return err
			}
			if g.JSON {
				return printJSON(scrubJSON(toAny(p)))
			}
			printPlan(p, b)
			return nil
		},
	}
}

// toAny converts a struct to generic JSON (for scrubJSON).
func toAny(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return v
	}
	return out
}

// ------------------------------------------------------------------ apply

func gitOut(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// confirm asks a y/N question on the terminal.
func confirm(question string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, failf("no terminal to confirm on: pass --yes to proceed non-interactively")
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [y/N] ", question)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func whoAmI(root string) string {
	if u, err := gitOut(root, "config", "user.name"); err == nil && u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cli"
}

func cmdApply() *cobra.Command {
	var message string
	var yes, allowDirty bool
	c := &cobra.Command{
		Use:   "apply",
		Short: "Make the committed topology the hub's active version (changes only the hub)",
		Long: `Validates the files, shows the plan, asks for confirmation and sends the
bundle to the hub as a new topology version. Refuses uncommitted changes in
topology/ unless --allow-dirty. It changes only the hub: no server is touched.
After applying, regenerate Notion page 01 with ` + "`vpnmap docs`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("apply"); err != nil {
				return err
			}
			root, err := repoRoot()
			if err != nil {
				return failf("%v", err)
			}
			if !allowDirty {
				out, err := gitOut(root, "status", "--porcelain", "--", "topology")
				if err != nil {
					return failf("cannot check git status in %s (%v): apply only committed topology, or pass --allow-dirty", root, err)
				}
				if out != "" {
					return failf("topology/ has uncommitted changes:\n%s\ncommit them first (the hub records the commit), or pass --allow-dirty", out)
				}
			}
			b, root, err := validBundle()
			if err != nil {
				return err
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			p, err := computePlan(c, b, root)
			if err != nil {
				return err
			}
			if !g.JSON {
				printPlan(p, b)
				fmt.Println()
			}
			if p.Diff.Empty() && p.LiveHash == b.Hash {
				if g.JSON {
					return printJSON(map[string]any{"applied": false, "version": p.LiveVersion, "reason": "unchanged", "plan": scrubJSON(toAny(p))})
				}
				fmt.Printf("Nothing to apply: the hub already runs this topology (v%d).\n", p.LiveVersion)
				return nil
			}
			if !yes {
				if g.JSON {
					return failf("pass --yes with --json (apply asks for confirmation otherwise)")
				}
				ok, err := confirm(fmt.Sprintf("Apply this topology to %s?", c.base))
				if err != nil {
					return err
				}
				if !ok {
					return exitWith(ExitInvalid, "aborted: nothing was applied")
				}
			}
			if message == "" {
				message, _ = gitOut(root, "log", "-1", "--format=%s")
			}
			var resp struct {
				Version   int  `json:"version"`
				Unchanged bool `json:"unchanged"`
			}
			body := map[string]any{"bundle": json.RawMessage(b.JSON()), "message": message, "by": whoAmI(root)}
			if _, err := c.post("/api/topology", body, &resp); err != nil {
				return err
			}
			if g.JSON {
				return printJSON(map[string]any{"applied": !resp.Unchanged, "version": resp.Version, "hash": b.Hash, "plan": scrubJSON(toAny(p))})
			}
			if resp.Unchanged {
				fmt.Printf("The hub reports no change: it already runs this topology (v%d).\n", resp.Version)
			} else {
				fmt.Printf("%s topology v%d is active on %s (hash %s).\n", green("✓"), resp.Version, c.base, b.Hash)
			}
			fmt.Println("Next:")
			fmt.Println("  • apply changed only the hub: run the agent and server commands listed above yourself, if any")
			fmt.Println("  • regenerate the architecture page: vpnmap docs -o /tmp/notion-01.md, and update Notion page 01 with it (Notion rule 7)")
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&message, "message", "m", "", "version message (default: the last commit subject)")
	f.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	f.BoolVar(&allowDirty, "allow-dirty", false, "apply even with uncommitted changes in topology/")
	return c
}

// ------------------------------------------------------------------ history / rollback

func cmdHistory() *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "Topology versions applied to the hub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("history"); err != nil {
				return err
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			var vs []topologyVersion
			if done, err := readCall(c, "/api/topology/versions", nil, &vs); done || err != nil {
				return err
			}
			t := newTable("VERSION", "", "APPLIED (IRST)", "BY", "COMMIT", "HASH", "N/L/R/A", "MESSAGE")
			for _, v := range vs {
				active := ""
				if v.Active {
					active = green("active")
				}
				t.add(bold("v"+itoa(v.Version)), active, localTime(v.CreatedAt), scrub(v.AppliedBy), orDash(v.GitCommit), v.Hash,
					fmt.Sprintf("%d/%d/%d/%d", v.Counts.Nodes, v.Counts.Links, v.Counts.Routes, v.Counts.Alerts), truncate(scrub(v.Message), 60))
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

func cmdRollback() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "rollback <version>",
		Short: "Make an earlier topology version active again (changes only the hub)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("rollback"); err != nil {
				return err
			}
			v, err := strconv.Atoi(strings.TrimPrefix(args[0], "v"))
			if err != nil || v <= 0 {
				return failf("version must be a number like 3 (see `vpnmap history`)")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			if !yes {
				if g.JSON {
					return failf("pass --yes with --json")
				}
				ok, err := confirm(fmt.Sprintf("Make topology v%d active on %s?", v, c.base))
				if err != nil {
					return err
				}
				if !ok {
					return exitWith(ExitInvalid, "aborted: nothing changed")
				}
			}
			root, _ := repoRoot()
			var resp struct {
				Version int    `json:"version"`
				Hash    string `json:"hash"`
			}
			raw, err := c.post("/api/topology/rollback", map[string]any{"version": v, "by": whoAmI(root)}, &resp)
			if err != nil {
				return err
			}
			if g.JSON {
				return emitRaw(raw)
			}
			fmt.Printf("%s topology v%d (hash %s) is active again on %s.\n", green("✓"), resp.Version, resp.Hash, c.base)
			fmt.Println("The files in topology/ may now describe a different version: revert the commit too, or the next `vpnmap apply` brings it back.")
			return nil
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return c
}

// ------------------------------------------------------------------ layout

var reBareKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

const layoutHeader = "# written by the UI via vpnmap layout pull; agents never edit this file\n"

func renderLayout(pos map[string]topology.Pos) string {
	var b strings.Builder
	b.WriteString(layoutHeader)
	b.WriteString("positions:\n")
	for _, id := range sortedMapKeys(pos) {
		key := id
		if !reBareKey.MatchString(id) {
			key = strconv.Quote(id)
		}
		p := pos[id]
		fmt.Fprintf(&b, "  %s: { x: %s, y: %s }\n", key, strconv.FormatFloat(p.X, 'f', -1, 64), strconv.FormatFloat(p.Y, 'f', -1, 64))
	}
	return b.String()
}

func cmdLayout() *cobra.Command {
	c := &cobra.Command{
		Use:   "layout pull",
		Short: "Write the node positions saved in the UI to topology/layout.yaml",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(&cobra.Command{
		Use:   "pull",
		Short: "GET /api/layout and write topology/layout.yaml (sorted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("layout pull"); err != nil {
				return err
			}
			root, err := repoRoot()
			if err != nil {
				return failf("%v", err)
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			pos := map[string]topology.Pos{}
			if _, err := c.get("/api/layout", nil, &pos); err != nil {
				return err
			}
			path := filepath.Join(root, "topology", "layout.yaml")
			if len(pos) == 0 {
				if g.JSON {
					return printJSON(map[string]any{"written": false, "positions": 0})
				}
				fmt.Println("The hub has no saved layout yet (move nodes on the board first); topology/layout.yaml was not changed.")
				return nil
			}
			content := renderLayout(pos)
			old, _ := os.ReadFile(path)
			changed := string(old) != content
			if changed {
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					return err
				}
			}
			if g.JSON {
				return printJSON(map[string]any{"written": changed, "file": relTo(root, path), "positions": len(pos)})
			}
			if !changed {
				fmt.Printf("topology/layout.yaml is already up to date (%d positions).\n", len(pos))
				return nil
			}
			fmt.Printf("wrote %s (%d positions)\nnext: review with `git diff topology/layout.yaml`, commit it, then `vpnmap apply`\n", relTo(root, path), len(pos))
			return nil
		},
	})
	return c
}

// ------------------------------------------------------------------ docs

func cmdDocs() *cobra.Command {
	var out string
	var liveStatus bool
	c := &cobra.Command{
		Use:   "docs",
		Short: "Render Notion page 01 (Mermaid map + tables) from the topology files",
		Long: `Prints Markdown for Notion page 01: a Mermaid flowchart (one frame per
datacenter/provider/personal group, solid data links, dotted control links)
and the tables of nodes, links, routes and alerts. --live adds a status
column from the hub. Paste it into Notion after every apply (Notion rule 7).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("docs"); err != nil && !liveStatus {
				return err
			}
			b, root, err := validBundle()
			if err != nil {
				return err
			}
			commit, _, dirty := gitInfo(root)
			opt := topology.DocsOptions{Commit: commit, Dirty: dirty, Now: time.Now()}
			if liveStatus {
				c, err := newClient()
				if err != nil {
					return err
				}
				var s snapshot
				if _, err := c.get("/api/snapshot", atQuery(), &s); err != nil {
					return err
				}
				opt.Status = map[string]string{}
				for id, n := range s.Nodes {
					if n.Observed {
						opt.Status["node:"+id] = n.Status
					}
				}
				for id, l := range s.Links {
					if l.Observed {
						opt.Status["link:"+id] = l.Status
					}
				}
				for id, r := range s.Routes {
					opt.Status["route:"+id] = r.Status
				}
				opt.StatusAt = localTime(s.At)
			}
			md := topology.Docs(b, opt)
			if out != "" {
				if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
					return err
				}
			}
			if g.JSON {
				return printJSON(map[string]any{"file": out, "markdown": md})
			}
			if out != "" {
				fmt.Printf("wrote %s (%d lines): paste it into Notion page 01\n", out, strings.Count(md, "\n"))
				return nil
			}
			fmt.Print(md)
			return nil
		},
	}
	c.Flags().StringVarP(&out, "output", "o", "", "write to this file instead of stdout")
	c.Flags().BoolVar(&liveStatus, "live", false, "add a status column from the hub (honours --at)")
	return c
}
