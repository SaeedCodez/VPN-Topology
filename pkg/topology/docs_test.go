package topology

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDocsRepoTopology(t *testing.T) {
	b := repoBundle(t)
	md := Docs(b, DocsOptions{Commit: "abc123", Now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)})
	for _, want := range []string{
		"# VPN Infrastructure — topology",
		"commit abc123", "2026-11-01 15:30 IRST",
		"```mermaid\nflowchart LR\n",
		`subgraph g_iran_dc["Iran DC · same LAN"]`,
		`subgraph g_home["Home · personal"]`,
		`n_users -->|"E1 · VLESS+REALITY tcp/2053"| n_relay_1`,
		`n_panel -.->|"C1 · node API 172.18.0.1:2222"| n_local_node`,
		"## Nodes", "## Links", "## Routes", "## Alerts",
		"| `relay-1` | Relay 1 | 31.56.89.121, relay-1.aitopology.ir |",
		"tcp-connect from probe → relay-1.aitopology.ir:2053",
		"xray-stats @ panel Local-Node proxy_MAIN (fallback: nft-counter @ panel)",
		"live (remnawave-online Local-Node), ≈1,742",
		"`xbridger-connection-not-running`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("docs missing %q", want)
		}
	}
	// External groups are not framed.
	if strings.Contains(md, "subgraph g_external") {
		t.Error("the external group must not be a frame")
	}
	// Every Mermaid line is a statement we generate: no raw quotes or
	// brackets leak out of a label.
	mer := md[strings.Index(md, "flowchart LR"):]
	mer = mer[:strings.Index(mer, "```")]
	line := regexp.MustCompile(`^(flowchart LR|  subgraph g_[A-Za-z0-9_]+\["[^"]*"\]|  end|\s+n_[A-Za-z0-9_]+(\(\["[^"]*"\]\)|\(\("[^"]*"\)\)|\["[^"]*"\])|  n_[A-Za-z0-9_]+ (-->|-\.->)\|"[^"|]*"\| n_[A-Za-z0-9_]+)$`)
	for _, l := range strings.Split(strings.TrimRight(mer, "\n"), "\n") {
		if !line.MatchString(l) {
			t.Errorf("unexpected mermaid line: %q", l)
		}
	}
	// Each subgraph is closed.
	if strings.Count(mer, "subgraph ") != strings.Count(mer, "\n  end\n") {
		t.Error("unbalanced subgraph/end")
	}
	// Table rows keep their column count even with | in values.
	if !strings.Contains(md, `regexp:.*\.ir$`) {
		t.Error("protocol text with regex should be kept")
	}
}

func TestMermaidLabelEscaping(t *testing.T) {
	got := MermaidLabel(`a "quoted" <b> | x & y #1` + "`\nz")
	want := `a #quot;quoted#quot; #lt;b#gt; #124; x #amp; y #35;1#96; z`
	if got != want {
		t.Errorf("MermaidLabel = %q, want %q", got, want)
	}
	b := loadTest(t, map[string]string{"base": strings.Replace(goodComponent, "plane: data\n    health", "plane: data\n    protocol: 'say \"hi\" <tcp>'\n    health", 1)})
	m := Mermaid(b)
	if !strings.Contains(m, `n_users -->|"e1 · say #quot;hi#quot; #lt;tcp#gt;"| n_relay`) {
		t.Errorf("escaped edge label missing:\n%s", m)
	}
	if strings.Contains(m, "subgraph g_ext") || !strings.Contains(m, `subgraph g_dc["DC"]`) {
		t.Errorf("frames:\n%s", m)
	}
}

func TestDocsLiveStatusColumn(t *testing.T) {
	b := loadTest(t, map[string]string{"base": goodComponent})
	md := Docs(b, DocsOptions{Status: map[string]string{"node:relay": "down", "link:e1": "ok", "route:main": "degraded"}, StatusAt: "2026-11-01 12:00 IRST"})
	for _, want := range []string{"| Status |", "🔴 down", "🟢 ok", "🟠 degraded", "live snapshot"} {
		if !strings.Contains(md, want) {
			t.Errorf("live docs missing %q", want)
		}
	}
}
