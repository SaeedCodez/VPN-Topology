package main

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
)

// The systemd units ship inside the binary. install.sh writes them with
// `vpnmap-agent unit <name>`, so they are covered by the binary's sha256
// check instead of being downloaded separately.
//
//go:embed deploy/*.service deploy/*.socket deploy/*.conf
var unitFS embed.FS

func unitNames() []string {
	entries, _ := fs.ReadDir(unitFS, "deploy")
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func cmdUnit(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "--list" {
		if len(args) == 1 {
			for _, n := range unitNames() {
				fmt.Fprintln(stdout, n)
			}
			return 0
		}
		fmt.Fprintln(stderr, "usage: vpnmap-agent unit <name>|--list")
		return 2
	}
	b, err := unitFS.ReadFile(path.Join("deploy", path.Base(args[0])))
	if err != nil {
		fmt.Fprintf(stderr, "vpnmap-agent: no bundled unit %q (try --list)\n", args[0])
		return 1
	}
	stdout.Write(b)
	return 0
}
