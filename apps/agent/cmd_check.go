package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"vpnmap/apps/agent/internal/guard"
	"vpnmap/apps/agent/internal/helper"
	"vpnmap/apps/agent/internal/probe"
	"vpnmap/apps/agent/internal/push"
	"vpnmap/pkg/agentcfg"
)

// cmdCheckConfig validates agent.yaml and reports, without printing any
// secret, whether the files it references are usable by the current user.
// install.sh runs it as the vpnmap-agent user. Only an invalid config (or a
// --expect-node mismatch) fails; unreadable files are warnings.
func cmdCheckConfig(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("check-config", stderr)
	expectNode := fs.String("expect-node", "", "fail unless the config is for this node")
	get := fs.String("get", "", "print one value for scripts: node, hub, token-file, checks-dir, log-paths, has-probe, has-nft")
	socket := fs.String("helper-socket", helper.DefaultSocket, "privileged helper socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, sha, err := agentcfg.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var fatal []string
	if _, err := guard.HubURL(cfg.Hub); err != nil {
		fatal = append(fatal, err.Error())
	}
	if err := probe.Validate(cfg.Probe); err != nil {
		fatal = append(fatal, err.Error())
	}
	if *expectNode != "" && cfg.Node != *expectNode {
		fatal = append(fatal, fmt.Sprintf("config is for node %q, not %q", cfg.Node, *expectNode))
	}
	if len(fatal) > 0 {
		fmt.Fprintf(stderr, "%s: invalid:\n  - %s\n", *cfgPath, strings.Join(fatal, "\n  - "))
		return 1
	}
	if *get != "" {
		return printValue(cfg, *get, stdout, stderr)
	}

	kind := "agent"
	if cfg.Node == "probe" {
		kind = "probe"
	}
	w := func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) }
	w("config      %s (config_sha %s)", *cfgPath, sha)
	w("node        %s (kind %s)", cfg.Node, kind)
	w("hub         %s%s", strings.TrimRight(cfg.Hub, "/"), push.Path)
	w("interval    %s", cfg.Interval.D())
	list := func(name string, items []string) {
		if len(items) > 0 {
			w("%-11s %d: %s", name, len(items), strings.Join(items, ", "))
		}
	}
	list("units", cfg.Units)
	list("containers", cfg.Containers)
	if cfg.Nft != nil {
		var n []string
		for _, c := range cfg.Nft.Counters {
			n = append(n, c.Name)
		}
		list("nft", n)
	}
	var wg, ss, tcp, httpT, logs, prom, checks, routes []string
	for _, x := range cfg.WG {
		if x.Netns != "" {
			wg = append(wg, x.Netns+"/"+x.Iface)
		} else {
			wg = append(wg, x.Iface)
		}
	}
	for _, x := range cfg.SS {
		ss = append(ss, fmt.Sprintf("%s(%s %d)", x.ID, x.Side, x.Port))
	}
	for _, x := range cfg.TCP {
		tcp = append(tcp, x.Target)
	}
	for _, x := range cfg.HTTP {
		httpT = append(httpT, x.ID)
	}
	for _, x := range cfg.Logs {
		logs = append(logs, x.ID)
	}
	for _, x := range cfg.Prom {
		prom = append(prom, x.ID)
	}
	for _, x := range cfg.Checks {
		checks = append(checks, x.ID)
	}
	if cfg.Probe != nil {
		for _, x := range cfg.Probe.Routes {
			routes = append(routes, x.Route)
		}
	}
	list("wg", wg)
	list("ss", ss)
	list("tcp", tcp)
	list("http", httpT)
	list("logs", logs)
	list("prom", prom)
	list("checks", checks)
	list("probe", routes)

	w("")
	w("files (as uid %d):", os.Getuid())
	warnings := 0
	report := func(err error, okMsg string) {
		if err != nil {
			warnings++
			w("  [warn] %v", err)
			return
		}
		w("  [ok]   %s", okMsg)
	}
	_, err = push.ReadToken(cfg.TokenFile)
	report(err, "token file "+cfg.TokenFile+" is readable")
	for _, l := range cfg.Logs {
		report(readable(l.Path), "log "+l.ID+": "+l.Path)
	}
	for _, p := range cfg.Prom {
		if p.AuthFile != "" {
			report(readable(p.AuthFile), "prom "+p.ID+": auth_file "+p.AuthFile)
		}
	}
	for _, c := range cfg.Checks {
		_, err := helper.CheckPath(cfg.ChecksDir, c.ID, 0)
		if err != nil {
			err = fmt.Errorf("check %s: %v", c.ID, err)
		}
		report(err, "check "+c.ID+" installed and root-owned")
	}
	if cfg.Probe != nil {
		for _, r := range cfg.Probe.Routes {
			b, err := os.ReadFile(r.OutboundFile)
			if err == nil {
				_, err = probe.ExtractOutbound(b)
			}
			if err != nil {
				err = fmt.Errorf("probe %s: %s: %v", r.Route, r.OutboundFile, err)
			}
			report(err, "probe "+r.Route+": outbound "+r.OutboundFile)
		}
	}
	needsHelper := cfg.Nft != nil || len(cfg.WG) > 0 || len(cfg.Containers) > 0 || len(cfg.Checks) > 0
	if needsHelper {
		var err error
		if fi, statErr := os.Stat(*socket); statErr != nil || fi.Mode()&os.ModeSocket == 0 {
			err = fmt.Errorf("helper socket %s missing (is vpnmap-agent-helper.socket running?)", *socket)
		}
		report(err, "helper socket "+*socket)
	}
	w("")
	if warnings > 0 {
		w("config is valid; %d warning(s): those collectors will report errors on the board", warnings)
	} else {
		w("config is valid")
	}
	return 0
}

func readable(path string) error {
	if err := guard.CheckReadable(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

func printValue(cfg *agentcfg.Config, key string, stdout, stderr io.Writer) int {
	switch key {
	case "node":
		fmt.Fprintln(stdout, cfg.Node)
	case "hub":
		fmt.Fprintln(stdout, cfg.Hub)
	case "token-file":
		fmt.Fprintln(stdout, cfg.TokenFile)
	case "checks-dir":
		fmt.Fprintln(stdout, cfg.ChecksDir)
	case "log-paths":
		for _, l := range cfg.Logs {
			fmt.Fprintln(stdout, l.Path)
		}
	case "has-probe":
		fmt.Fprintln(stdout, yesNo(cfg.Probe != nil))
	case "has-nft":
		fmt.Fprintln(stdout, yesNo(cfg.Nft != nil))
	default:
		fmt.Fprintf(stderr, "check-config: unknown --get key %q\n", key)
		return 2
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
