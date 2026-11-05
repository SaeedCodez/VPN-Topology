package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/sysexec"
)

func cmdRenderNft(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("render-nft", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, _, err := agentcfg.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if cfg.Nft == nil {
		fmt.Fprintln(stdout, "# no nft counters configured: table inet vpnmap is not needed")
		return 0
	}
	fmt.Fprint(stdout, agentcfg.RenderNft(cfg.Nft.Counters))
	return 0
}

// cmdNftApply runs as root from the unit's ExecStartPre=+. It only ever
// touches table inet vpnmap.
func cmdNftApply(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("nft-apply", stderr)
	dry := fs.Bool("dry-run", false, "print the ruleset and the commands, change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, _, err := agentcfg.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var r sysexec.Runner = sysexec.Exec{}
	if *dry {
		r = dryRunner{w: stdout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := nftApply(ctx, cfg, r, stdout); err != nil {
		fmt.Fprintln(stderr, "nft-apply:", err)
		return 1
	}
	return 0
}

// nftApply installs the counter-only table, or removes it when the config
// has no nft section.
func nftApply(ctx context.Context, cfg *agentcfg.Config, r sysexec.Runner, out io.Writer) error {
	if cfg.Nft == nil {
		// Any failure to list (table absent, nft not installed) means there
		// is nothing of ours to remove.
		if _, err := r.Run(ctx, sysexec.Cmd{Name: "nft", Args: []string{"list", "table", "inet", "vpnmap"}}); err != nil {
			fmt.Fprintln(out, "no nft counters configured and table inet vpnmap is absent: nothing to do")
			return nil
		}
		if _, err := r.Run(ctx, sysexec.Cmd{Name: "nft", Args: []string{"delete", "table", "inet", "vpnmap"}}); err != nil {
			return err
		}
		fmt.Fprintln(out, "no nft counters configured: deleted table inet vpnmap")
		return nil
	}
	script := agentcfg.RenderNft(cfg.Nft.Counters)
	if err := checkNftScript(script); err != nil {
		return fmt.Errorf("refusing to apply rendered ruleset: %w", err)
	}
	if _, err := r.Run(ctx, sysexec.Cmd{Name: "nft", Args: []string{"-f", "-"}, Stdin: []byte(script)}); err != nil {
		return err
	}
	fmt.Fprintf(out, "applied table inet vpnmap (%d counters)\n", len(cfg.Nft.Counters))
	return nil
}

var (
	// Top-level statements allowed in the script: only our table.
	reTopLevel = regexp.MustCompile(`^(table|delete table|flush table) inet vpnmap( \{)?$`)
	verdicts   = map[string]bool{"accept": true, "drop": true, "reject": true, "jump": true, "goto": true,
		"queue": true, "return": true, "continue": true, "masquerade": true, "snat": true, "dnat": true, "redirect": true}
)

// checkNftScript is a second line of defence around agentcfg.RenderNft:
// the script may only address table inet vpnmap, and rules may only count
// (no verdicts, no NAT), so it can never change what other tables do.
func checkNftScript(script string) error {
	depth := 0
	for n, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if depth == 0 && !reTopLevel.MatchString(line) {
			return fmt.Errorf("line %d: only table inet vpnmap may be addressed: %q", n+1, line)
		}
		if depth >= 2 && !strings.HasPrefix(line, "type ") {
			for _, tok := range strings.Fields(line) {
				if verdicts[strings.TrimSuffix(tok, ";")] {
					return fmt.Errorf("line %d: rules must be counter-only, found %q", n+1, tok)
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			return fmt.Errorf("line %d: unbalanced braces", n+1)
		}
	}
	if depth != 0 {
		return fmt.Errorf("unbalanced braces")
	}
	return nil
}

// dryRunner prints commands instead of running them. `nft list table`
// reports "absent" so a dry run never claims to delete anything.
type dryRunner struct{ w io.Writer }

func (d dryRunner) Run(_ context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	fmt.Fprintf(d.w, "[dry-run] %s\n", sysexec.Key(c))
	if len(c.Stdin) > 0 {
		fmt.Fprintf(d.w, "%s", c.Stdin)
	}
	if len(c.Args) > 0 && c.Args[0] == "list" {
		return sysexec.Result{ExitCode: 1}, &sysexec.ExitError{Name: c.Name, Code: 1}
	}
	return sysexec.Result{}, nil
}
