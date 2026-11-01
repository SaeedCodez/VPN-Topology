package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"vpnmap/pkg/topology"
)

func findRoot(dir string) (string, error) { return topology.FindRoot(dir) }

// loadBundle loads and validates the files. It returns ExitInvalid on errors.
func loadBundle(printWarnings bool) (*topology.Bundle, topology.Diagnostics, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, nil, failf("%v", err)
	}
	b, ds := topology.Load(topology.Options{Root: root})
	if b != nil {
		commit, branch, dirty := gitInfo(root)
		b.Stamp(commit, branch, dirty, "vpnmap "+version)
	}
	if printWarnings && !g.JSON {
		for _, d := range ds {
			if d.Severity == "warning" {
				fmt.Fprintln(os.Stderr, amber(d.String()))
			}
		}
	}
	return b, ds, nil
}

func gitInfo(root string) (commit, branch string, dirty bool) {
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	commit = run("rev-parse", "--short=12", "HEAD")
	branch = run("rev-parse", "--abbrev-ref", "HEAD")
	dirty = run("status", "--porcelain", "--", "topology") != ""
	return
}

func cmdValidate() *cobra.Command {
	var strict bool
	c := &cobra.Command{
		Use:   "validate",
		Short: "Check the topology files (schema, references, collectors, alerts, secrets)",
		Long: `Validates topology/map.yaml, topology/components/*.yaml and topology/layout.yaml.
Every error names the file, the line and a fix. Runs in CI on every PR.
Exit code 0 = valid, 3 = errors (or warnings with --strict).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, ds, err := loadBundle(false)
			if err != nil {
				return err
			}
			errs, warns := ds.Count()
			if g.JSON {
				out := map[string]any{"valid": !ds.HasErrors() && !(strict && warns > 0), "errors": errs, "warnings": warns, "diagnostics": ds}
				if b != nil {
					out["hash"] = b.Hash
					out["counts"] = map[string]int{"components": len(b.Components), "nodes": len(b.Nodes), "links": len(b.Links), "routes": len(b.Routes), "alerts": len(b.Alerts)}
				}
				_ = printJSON(out)
			} else {
				for _, d := range ds {
					s := d.String()
					if d.Severity == "error" {
						s = red(s)
					} else {
						s = amber(s)
					}
					fmt.Println(s)
				}
				if b != nil && !ds.HasErrors() {
					checks := 0
					for _, n := range b.Nodes {
						checks += len(n.Checks) + len(n.Metrics)
					}
					for _, l := range b.Links {
						checks += len(l.Health)
						if l.Traffic != nil {
							checks++
						}
						if l.Conns != nil {
							checks++
						}
					}
					fmt.Printf("%s topology is valid · %d components, %d nodes, %d links, %d routes, %d checks, %d alerts · hash %s\n",
						green("✓"), len(b.Components), len(b.Nodes), len(b.Links), len(b.Routes), checks, len(b.Alerts), b.Hash)
				}
				if errs+warns > 0 {
					fmt.Printf("%d error(s), %d warning(s)\n", errs, warns)
				}
			}
			if ds.HasErrors() || (strict && warns > 0) {
				return exitWith(ExitInvalid, "")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&strict, "strict", false, "treat warnings as errors")
	return c
}

func cmdSchema() *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "schema",
		Short: "Regenerate schema/*.schema.json from the code (or --check that they are current)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return failf("%v", err)
			}
			files, err := topology.SchemaFiles()
			if err != nil {
				return err
			}
			stale := false
			for p, content := range files {
				full := filepath.Join(root, p)
				old, _ := os.ReadFile(full)
				if bytes.Equal(old, content) {
					continue
				}
				if check {
					fmt.Printf("%s is out of date: run `vpnmap schema`\n", p)
					stale = true
					continue
				}
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(full, content, 0o644); err != nil {
					return err
				}
				fmt.Println("wrote", p)
			}
			if stale {
				return exitWith(ExitInvalid, "")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "fail if the generated schema files are out of date")
	return c
}

func cmdBundle() *cobra.Command {
	return &cobra.Command{
		Use:   "bundle",
		Short: "Print the resolved topology bundle (the JSON that apply sends to the hub)",
		RunE: func(cmd *cobra.Command, args []string) error {
			b, ds, err := loadBundle(false)
			if err != nil {
				return err
			}
			if ds.HasErrors() {
				for _, d := range ds {
					fmt.Fprintln(os.Stderr, d.String())
				}
				return exitWith(ExitInvalid, "topology has errors; run `vpnmap validate`")
			}
			_, err = os.Stdout.Write(b.JSON())
			return err
		},
	}
}

func cmdExplain() *cobra.Command {
	c := &cobra.Command{
		Use:   "explain collector [name]",
		Short: "Describe the collector catalog, or one collector's parameters and an example",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "collector" && args[0] != "collectors" {
				return failf("usage: vpnmap explain collector [name]")
			}
			if len(args) == 1 {
				if g.JSON {
					return printJSON(topology.Catalog)
				}
				t := newTable("COLLECTOR", "RUNS ON", "USE AS", "WHAT")
				for _, c := range topology.Catalog {
					t.add(bold(c.Name), c.RunsOn, strings.Join(c.Usage, ","), c.Title)
				}
				t.print(os.Stdout)
				fmt.Println(dim("\nBuilt-in metrics of every node with agent: true: cpu, mem, disk, load, uptime, net (+ heartbeat and boot-time)."))
				fmt.Println(dim("Run `vpnmap explain collector <name>` for parameters and an example."))
				return nil
			}
			info := topology.Collector(args[1])
			if info == nil {
				return failf("unknown collector %q; known: %s", args[1], strings.Join(topology.CollectorNames(), ", "))
			}
			if g.JSON {
				return printJSON(info)
			}
			fmt.Printf("%s — %s\n\n%s\n\n", bold(info.Name), info.Title, wrap(info.Doc, 78))
			fmt.Printf("Runs on:   %s\nProduces:  %s\nUse as:    %s\n\n", info.RunsOn, strings.Join(info.Produces, ", "), strings.Join(info.Usage, ", "))
			t := newTable("PARAMETER", "TYPE", "REQUIRED", "DEFAULT", "DESCRIPTION")
			for _, p := range info.Params {
				req := ""
				if p.Required {
					req = "yes"
				}
				typ := p.Type
				if len(p.Enum) > 0 {
					typ = strings.Join(p.Enum, "|")
				}
				t.add(p.Name, typ, req, orDash(p.Default), p.Doc)
			}
			t.print(os.Stdout)
			fmt.Printf("\nExample:\n  %s\n", info.Example)
			if info.Notes != "" {
				fmt.Printf("\nNotes: %s\n", wrap(info.Notes, 78))
			}
			return nil
		},
	}
	return c
}

func wrap(s string, width int) string {
	var lines []string
	var cur string
	for _, w := range strings.Fields(s) {
		if len(cur)+len(w)+1 > width && cur != "" {
			lines = append(lines, cur)
			cur = w
		} else if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}

var reComponent = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func cmdNew() *cobra.Command {
	return &cobra.Command{
		Use:   "new component <name>",
		Short: "Scaffold topology/components/<name>.yaml",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "component" {
				return failf("usage: vpnmap new component <name>")
			}
			name := args[1]
			if !reComponent.MatchString(name) {
				return failf("component names use lowercase letters, digits and dashes, e.g. relay-2")
			}
			root, err := repoRoot()
			if err != nil {
				return failf("%v", err)
			}
			path := filepath.Join(root, "topology", "components", name+".yaml")
			if _, err := os.Stat(path); err == nil {
				return failf("%s already exists; edit it instead", relTo(root, path))
			}
			if err := os.WriteFile(path, []byte(componentTemplate(name)), 0o644); err != nil {
				return err
			}
			fmt.Printf("created %s\nnext: fill it in, then run `vpnmap validate` and `vpnmap plan`\n", relTo(root, path))
			return nil
		},
	}
}

func relTo(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

func componentTemplate(name string) string {
	return fmt.Sprintf(`# yaml-language-server: $schema=../../schema/topology.schema.json
component: %[1]s
title: TODO human title
docs: https://app.notion.com/p/TODO   # the Notion page that documents this feature
description: >-
  TODO: what this feature is, how traffic flows, and anything an operator
  must know. Read the Notion pages first (see CLAUDE.md).

nodes:
  # - id: relay-2
  #   name: Relay 2
  #   kind: relay            # %[2]s
  #   group: iran-dc         # a group from topology/map.yaml
  #   address: [1.2.3.4, relay-2.example.ir]
  #   role: realm TCP forwarder
  #   agent: true            # vpnmap-agent installed (vpnmap agent install-cmd relay-2)
  #   checks:
  #     - { collector: systemd-unit, host: relay-2, unit: realm.service }
  #   card: [cpu, net]

links:
  # - id: e10
  #   label: E10
  #   from: users
  #   to: relay-2
  #   plane: data            # data (solid) | control (dashed)
  #   protocol: VLESS+REALITY tcp/2053
  #   traffic: { collector: nft-counter, host: relay-2, up: { hook: input, proto: tcp, dport: 2053 }, down: { hook: output, proto: tcp, sport: 2053 } }
  #   health: { collector: tcp-connect, from: probe, target: "relay-2.example.ir:2053" }

routes:
  # - id: main-2
  #   title: Main route via Relay 2
  #   hops: [e10, ...]       # link ids from the users to the exit
  #   probe: { collector: e2e-probe, expect_country: FR }

alerts:
  # - id: relay-2-down
  #   when: "link.e10.health == down for 2m"
  #   severity: critical
  #   message_fa: "رله ۲ در دسترس نیست."
`, name, strings.Join(topology.NodeKinds, " | "))
}
