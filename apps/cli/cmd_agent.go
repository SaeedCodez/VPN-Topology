package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/topology"
)

// agentHubURL is the hub URL written into generated agent configs:
// --hub flag > settings.hub in topology/map.yaml > the configured hub.
func agentHubURL(b *topology.Bundle) string {
	if g.Hub != "" {
		return strings.TrimRight(g.Hub, "/")
	}
	if b != nil && b.Map.Settings.Hub != "" {
		return strings.TrimRight(b.Map.Settings.Hub, "/")
	}
	cfg := loadCLIConfig()
	return strings.TrimRight(firstNonEmpty(os.Getenv("VPNMAP_HUB"), cfg.Hub), "/")
}

// genAgentConfig loads the local topology and generates one node's agent.yaml.
func genAgentConfig(node string) (*topology.Bundle, *agentcfg.Config, []byte, string, error) {
	b, _, err := validBundle()
	if err != nil {
		return nil, nil, nil, "", err
	}
	cfg, err := topology.AgentConfig(b, node, agentHubURL(b))
	if err != nil {
		return nil, nil, nil, "", failf("%v", err)
	}
	raw, sha, err := topology.AgentConfigYAML(cfg)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return b, cfg, raw, sha, nil
}

func cmdAgent() *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "Generate agent configs, nft rules, push tokens and install commands",
		Long: `vpnmap-agent runs on each server with agent: true (and on the probe). It
reads only its local /etc/vpnmap-agent/agent.yaml and pushes to the hub; the
hub never sends it anything. These commands print files and commands for you
to install; vpnmap never connects to a server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(agentConfigCmd(), agentNftCmd(), agentTokenCmd(), agentInstallCmd(), getAgents("list", "ls"))
	return c
}

func agentConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config <node>",
		Short: "Print /etc/vpnmap-agent/agent.yaml for a node (generated from the topology)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("agent config"); err != nil {
				return err
			}
			_, cfg, raw, sha, err := genAgentConfig(args[0])
			if err != nil {
				return err
			}
			if g.JSON {
				return printJSON(map[string]any{"node": cfg.Node, "config_sha": sha, "config": cfg, "yaml": string(raw)})
			}
			_, err = os.Stdout.Write(raw)
			if isTTY(os.Stdout) {
				fmt.Fprintf(os.Stderr, "%s\n", dim(fmt.Sprintf("# config_sha %s · save it with: vpnmap agent config %s > /tmp/%s.agent.yaml", sha, cfg.Node, cfg.Node)))
			}
			return err
		},
	}
}

func agentNftCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "nft <node>",
		Short: "Print the counter-only nft ruleset (table inet vpnmap) for a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("agent nft"); err != nil {
				return err
			}
			_, cfg, _, _, err := genAgentConfig(args[0])
			if err != nil {
				return err
			}
			var counters []agentcfg.NftCounter
			if cfg.Nft != nil {
				counters = cfg.Nft.Counters
			}
			if g.JSON {
				out := map[string]any{"node": cfg.Node, "counters": counters}
				if len(counters) > 0 {
					out["ruleset"] = agentcfg.RenderNft(counters)
				}
				return printJSON(out)
			}
			if len(counters) == 0 {
				fmt.Printf("# %s has no nft counters: the agent does not create table inet vpnmap.\n", cfg.Node)
				return nil
			}
			fmt.Print(agentcfg.RenderNft(counters))
			return nil
		},
	}
}

func agentTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token <node>",
		Short: "Create a push token for a node (printed once; only its sha256 goes to the hub)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("agent token"); err != nil {
				return err
			}
			node := args[0]
			if !agentcfg.ValidID(node) {
				return failf("%q is not a node id", node)
			}
			if b, _, err := loadBundle(false); err == nil && b != nil {
				ok := false
				for _, h := range b.AgentHosts() {
					ok = ok || h == node
				}
				if !ok {
					return failf("%q is not a node with agent: true (or the probe) in the topology; agent hosts: %s", node, strings.Join(b.AgentHosts(), ", "))
				}
			}
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			token := base64.RawURLEncoding.EncodeToString(buf)
			sum := sha256.Sum256([]byte(token))
			hash := hex.EncodeToString(sum[:])
			entry := node + "=" + hash
			if g.JSON {
				return printJSON(map[string]any{"node": node, "token": token, "sha256": hash, "agent_tokens_entry": entry})
			}
			if !isTTY(os.Stdout) {
				fmt.Fprintln(os.Stderr, amber("warning: the token is written to a pipe or file; delete that copy after use"))
			}
			fmt.Printf("Push token for %s (shown once; vpnmap does not store it):\n\n  %s\n\n", bold(node), token)
			fmt.Printf("1. Paste it when the installer asks \"Push token for %s (input hidden)\"\n   (or run the installer with VPNMAP_TOKEN set). It ends up in %s on the server.\n", node, topology.AgentTokenFile)
			fmt.Printf("2. Add this entry to the hub's AGENT_TOKENS env (comma-separated node=sha256 list),\n   then restart the hub. The hub keeps only the hash:\n\n  %s\n\n", entry)
			fmt.Println(dim("Replacing a token: run this again, re-run the installer with --new-token, and replace the entry."))
			return nil
		},
	}
}

var reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// agentBinarySHA reads the published checksum from the configured hub.
func agentBinarySHA(arch string) (string, string) {
	hub, tok, err := hubSettings()
	if err != nil {
		return "", ""
	}
	u := hub + "/agent/download/vpnmap-agent-linux-" + arch + ".sha256"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", u
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", u
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 200 {
		return "", u
	}
	f := strings.Fields(string(raw))
	if len(f) > 0 && reSHA256.MatchString(strings.ToLower(f[0])) {
		return strings.ToLower(f[0]), u
	}
	return "", u
}

func agentInstallCmd() *cobra.Command {
	var arch string
	c := &cobra.Command{
		Use:   "install-cmd <node>",
		Short: "Print the exact steps to install (or update) the agent on a node; nothing is run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("agent install-cmd"); err != nil {
				return err
			}
			b, cfg, _, sha, err := genAgentConfig(args[0])
			if err != nil {
				return err
			}
			node, hub := cfg.Node, cfg.Hub
			ip := nodeAddress(b, node)
			binSHA, shaURL := agentBinarySHA(arch)
			shaArg := binSHA
			if shaArg == "" {
				shaArg = "<SHA256>"
			}
			yamlTmp := "/tmp/" + node + ".agent.yaml"
			steps := []string{
				fmt.Sprintf("vpnmap agent config %s > %s", node, yamlTmp),
				fmt.Sprintf("scp %s root@%s:/root/vpnmap-agent.yaml", yamlTmp, ip),
				fmt.Sprintf("ssh -t root@%s 'curl -fsSL %s/agent/install.sh | bash -s -- --hub %s --node %s --config-file /root/vpnmap-agent.yaml --sha256 %s'", ip, hub, hub, node, shaArg),
				fmt.Sprintf("ssh root@%s 'curl -fsSL %s/agent/uninstall.sh | bash'", ip, hub),
			}
			var checks []map[string]string
			for _, id := range topology.LocalCheckIDs(cfg) {
				repoFile := "apps/agent/deploy/checks.d/" + id + ".check"
				root, _ := repoRoot()
				m := map[string]string{"id": id}
				if _, err := os.Stat(root + "/" + repoFile); err == nil {
					m["repo_file"] = repoFile
					m["install"] = fmt.Sprintf("scp %s root@%s:/tmp/%s.check && ssh root@%s 'install -o root -g root -m 0755 /tmp/%s.check %s/'", repoFile, ip, id, ip, id, topology.AgentChecksDir)
				} else {
					m["install"] = fmt.Sprintf("write %s/%s.check yourself (read-only, JSON output), then install it with mode 0755 root:root", topology.AgentChecksDir, id)
				}
				checks = append(checks, m)
			}
			nft := 0
			if cfg.Nft != nil {
				nft = len(cfg.Nft.Counters)
			}
			if g.JSON {
				return printJSON(map[string]any{"node": node, "address": ip, "hub": hub, "config_sha": sha, "binary_sha256": binSHA, "binary_sha256_url": shaURL,
					"steps": map[string]string{"config": steps[0], "copy": steps[1], "install": steps[2], "uninstall": steps[3]}, "local_checks": checks, "nft_counters": nft})
			}
			fmt.Printf("Install vpnmap-agent on %s (%s). %s\n\n", bold(node), ip, dim("Run these yourself; vpnmap never connects to a server."))
			fmt.Printf("1. Generate the config (config_sha %s):\n   %s\n\n", sha, steps[0])
			fmt.Printf("2. Copy it to the server:\n   %s\n\n", steps[1])
			fmt.Printf("3. Create its push token (first install only): vpnmap agent token %s\n   and add the printed node=sha256 entry to the hub's AGENT_TOKENS.\n\n", node)
			fmt.Printf("4. Install or update (downloads the binary from the hub, verifies its sha256, asks for the token, starts the agent):\n   %s\n", steps[2])
			if binSHA == "" {
				where := shaURL
				if where == "" {
					where = hub + "/agent/download/vpnmap-agent-linux-" + arch + ".sha256"
				}
				fmt.Printf("   %s\n", amber("<SHA256>: the checksum was not readable from the hub; take it from "+where+" (verify it out of band)"))
			}
			fmt.Printf("\n5. To remove it later (deletes the agent, its user and table inet vpnmap; nothing else):\n   %s\n", steps[3])
			if len(checks) > 0 {
				fmt.Printf("\n%s needs these local checks in %s/ before its checks turn green:\n", node, topology.AgentChecksDir)
				for _, m := range checks {
					fmt.Printf("  • %s\n    %s\n", bold(m["id"]), m["install"])
				}
			}
			if nft > 0 {
				fmt.Printf("\nThe agent creates nft table %s with %d counter-only rules (no accept/drop; review: vpnmap agent nft %s).\n", bold("inet vpnmap"), nft, node)
			}
			if len(cfg.Prom) > 0 {
				fmt.Printf("\nIt scrapes %s: if the endpoint needs basic auth, put \"user:password\" in %s (mode 0640, root:vpnmap-agent).\n", cfg.Prom[0].URL, cfg.Prom[0].AuthFile)
			}
			if cfg.Probe != nil {
				fmt.Printf("\nProbe mode: install sing-box at %s and write one outbound per route (monitoring user's credentials; never in the repo):\n", cfg.Probe.SingBox)
				for _, r := range cfg.Probe.Routes {
					fmt.Printf("  • %s\n", r.OutboundFile)
				}
			}
			if arch == "amd64" {
				fmt.Println(dim("\nARM server? Use --arch arm64 for the matching checksum."))
			}
			return nil
		},
	}
	c.Flags().StringVar(&arch, "arch", "amd64", "server architecture: amd64 or arm64")
	return c
}

// getAgents lists the agents that report, against the agent hosts of the topology.
func getAgents(use string, aliases ...string) *cobra.Command {
	return &cobra.Command{
		Use: use, Aliases: aliases, Short: "Agents: version, last push, config sha and whether it matches the generated config", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.At != "" {
				return failf("--at is not available for agents: the hub only keeps each agent's latest push")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			var agents []agentInfo
			raw, err := c.get("/api/agents", nil, &agents)
			if err != nil {
				return err
			}
			b, ds, _ := loadBundle(false)
			if b != nil && ds.HasErrors() {
				b = nil
			}
			want := map[string]string{} // node → generated sha
			var hosts []string
			if b != nil {
				for _, h := range b.AgentHosts() {
					hosts = append(hosts, h)
					if cfg, err := topology.AgentConfig(b, h, agentHubURL(b)); err == nil {
						_, sha, _ := topology.AgentConfigYAML(cfg)
						want[h] = sha
					}
				}
			}
			if g.JSON {
				if b == nil {
					return emitRaw(raw)
				}
				type row struct {
					agentInfo
					Reporting    bool   `json:"reporting"`
					GeneratedSHA string `json:"generatedConfigSha,omitempty"`
					Matches      *bool  `json:"configMatches,omitempty"`
				}
				var rows []row
				seen := map[string]bool{}
				for _, a := range agents {
					r := row{agentInfo: a, Reporting: true, GeneratedSHA: want[a.Node]}
					if w, ok := want[a.Node]; ok {
						m := w == a.ConfigSha
						r.Matches = &m
					}
					rows = append(rows, r)
					seen[a.Node] = true
				}
				for _, h := range hosts {
					if !seen[h] {
						rows = append(rows, row{agentInfo: agentInfo{Node: h}, GeneratedSHA: want[h]})
					}
				}
				return printJSON(scrubJSON(toAny(rows)))
			}
			t := newTable("NODE", "KIND", "VERSION", "LAST PUSH", "CONFIG SHA", "MATCHES LOCAL CONFIG", "HOSTNAME")
			seen := map[string]bool{}
			for _, a := range agents {
				seen[a.Node] = true
				match := grey("? (no local topology)")
				if w, ok := want[a.Node]; ok {
					if w == a.ConfigSha {
						match = green("✓ yes")
					} else {
						match = amber("✗ no, generated " + w)
					}
				} else if b != nil {
					match = amber("not an agent host in the topology")
				}
				last := ago(a.LastSeen)
				if a.AgeSec > 60 {
					last = amber(last)
				}
				t.add(bold(a.Node), a.Kind, orDash(a.Version), last, orDash(a.ConfigSha), match, scrub(a.Hostname))
			}
			for _, h := range hosts {
				if !seen[h] {
					t.add(bold(h), "", "", red("not reporting"), "", "generated "+want[h], dim("vpnmap agent install-cmd "+h))
				}
			}
			t.print(os.Stdout)
			return nil
		},
	}
}

// ------------------------------------------------------------------ silence

func cmdSilence() *cobra.Command {
	var instance, reason string
	c := &cobra.Command{
		Use:   "silence <alert> <duration>",
		Short: "Silence an alert (or one instance of it) for a while, e.g. silence disk-high 2h",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := noAt("silence"); err != nil {
				return err
			}
			if _, err := topology.ParseDuration(args[1]); err != nil {
				return failf("duration %q: use e.g. 30m, 2h, 1d", args[1])
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			var resp struct {
				Alert string `json:"alert"`
				Until string `json:"until"`
			}
			raw, err := c.post("/api/silence", map[string]any{"alert": args[0], "instance": instance, "duration": args[1], "reason": reason}, &resp)
			if err != nil {
				return err
			}
			if g.JSON {
				return emitRaw(raw)
			}
			what := resp.Alert
			if instance != "" {
				what += " (" + instance + ")"
			}
			fmt.Printf("%s silenced %s until %s\n", green("✓"), bold(what), localTime(resp.Until))
			return nil
		},
	}
	c.Flags().StringVar(&instance, "instance", "", "only this instance (e.g. link:c4b:origin-tls-pl; see `vpnmap get alerts`)")
	c.Flags().StringVar(&reason, "reason", "", "why (recorded in the event log)")
	return c
}
