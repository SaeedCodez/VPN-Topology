package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var version = "0.1.0-dev"

// globals holds the flags shared by every command.
type globals struct {
	JSON    bool
	At      string
	Hub     string
	Token   string
	Dir     string
	NoColor bool
}

var g globals

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "vpnmap",
		Short: "Topology-as-code and read-only diagnostics for the VPN map",
		Long: `vpnmap validates and applies the topology files (topology/) and reads the
live board from the hub. It never changes a VPN server.

Exit codes: 0 healthy, 1 degraded, 2 down, 3 invalid input.
Every command takes --json; read commands take --at <time> for time travel.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&g.JSON, "json", false, "machine-readable JSON output")
	pf.StringVar(&g.At, "at", "", "read the board at a past `time` (RFC 3339, e.g. 2026-10-22T02:03Z, or -2h)")
	pf.StringVar(&g.Hub, "hub", "", "`URL` of the hub (env VPNMAP_HUB, or save it with vpnmap login)")
	pf.StringVar(&g.Token, "token", "", "personal API `token` (env VPNMAP_TOKEN; prefer vpnmap login)")
	pf.StringVar(&g.Dir, "dir", "", "repository root containing topology/ (default: search upwards from the current `directory`)")
	pf.BoolVar(&g.NoColor, "no-color", false, "disable colors (also NO_COLOR env)")

	root.AddGroup(&cobra.Group{ID: "see", Title: "See the board:"},
		&cobra.Group{ID: "diag", Title: "Diagnose:"},
		&cobra.Group{ID: "change", Title: "Change the map (topology as code):"},
		&cobra.Group{ID: "agents", Title: "Agents and alerts:"},
		&cobra.Group{ID: "other", Title: "Other:"})

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("see", cmdStatus(), cmdGet(), cmdDescribe(), cmdEvents(), cmdMetrics(), cmdSnapshot())
	add("diag", cmdDiagnose(), cmdPath(), cmdWhy())
	add("change", cmdValidate(), cmdPlan(), cmdApply(), cmdHistory(), cmdRollback(), cmdNew(), cmdLayout(), cmdDocs())
	add("agents", cmdAgent(), cmdSilence())
	add("other", cmdExplain(), cmdSchema(), cmdBundle(), cmdLogin(), cmdDemo(), cmdAdmin())
	root.SetHelpCommandGroupID("other")
	root.SetCompletionCommandGroupID("other")
	return root
}

// repoRoot finds the repository root for file commands.
func repoRoot() (string, error) {
	if g.Dir != "" {
		return filepath.Abs(g.Dir)
	}
	wd, _ := os.Getwd()
	return findRoot(wd)
}

// cliConfig is ~/.config/vpnmap/config.yaml.
type cliConfig struct {
	Hub   string `yaml:"hub"`
	Token string `yaml:"token"`
}

func configPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "vpnmap", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vpnmap", "config.yaml")
}

func loadCLIConfig() cliConfig {
	var c cliConfig
	raw, err := os.ReadFile(configPath())
	if err == nil {
		_ = yaml.Unmarshal(raw, &c)
	}
	return c
}

// hubSettings resolves the hub URL and token: flag > env > config file.
func hubSettings() (string, string, error) {
	cfg := loadCLIConfig()
	hub := firstNonEmpty(g.Hub, os.Getenv("VPNMAP_HUB"), cfg.Hub)
	tok := firstNonEmpty(g.Token, os.Getenv("VPNMAP_TOKEN"), cfg.Token)
	if hub == "" {
		return "", "", exitWith(ExitInvalid, "no hub configured: run `vpnmap login --hub https://map.imdev.ir` or set VPNMAP_HUB (demo: http://localhost:4000)")
	}
	return strings.TrimRight(hub, "/"), tok, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func failf(format string, a ...any) error { return exitWith(ExitInvalid, format, a...) }

var _ = fmt.Sprintf
