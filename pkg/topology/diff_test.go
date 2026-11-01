package topology

import (
	"strings"
	"testing"
)

func loadTest(t *testing.T, comps map[string]string) *Bundle {
	t.Helper()
	b, ds := Load(Options{Root: writeRepo(t, comps)})
	if ds.HasErrors() {
		t.Fatalf("errors: %v", ds)
	}
	return b
}

func findChange(d *BundleDiff, op, kind, id string) *Change {
	for i := range d.Changes {
		c := &d.Changes[i]
		if c.Op == op && c.Kind == kind && c.ID == id {
			return c
		}
	}
	return nil
}

func TestDiffIgnoresLineNumbers(t *testing.T) {
	a := loadTest(t, map[string]string{"base": goodComponent})
	shifted := strings.Replace(goodComponent, "title: Base\n", "title: Base\n# a comment\n\n# that moves every line\n", 1)
	b := loadTest(t, map[string]string{"base": shifted})
	d, err := Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("moving lines must not be a change: %+v", d.Changes)
	}
}

func TestDiffRoundTripThroughJSON(t *testing.T) {
	b := repoBundle(t)
	live, err := ParseBundle(b.JSON())
	if err != nil {
		t.Fatal(err)
	}
	if live.Map.Settings.StaleAfter != b.Map.Settings.StaleAfter {
		t.Errorf("stale_after %v != %v after JSON round trip", live.Map.Settings.StaleAfter, b.Map.Settings.StaleAfter)
	}
	d, err := Diff(live, b)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("round trip must give no diff: %+v", d.Changes)
	}
	d, err = DiffJSON(b.JSON(), b.JSON())
	if err != nil || !d.Empty() {
		t.Fatalf("DiffJSON(same) = %+v, %v", d, err)
	}
}

func TestDiffAddRemoveChange(t *testing.T) {
	old := loadTest(t, map[string]string{"base": goodComponent})
	changed := goodComponent
	// change a node field and a check parameter, add a node + link, remove the alert
	changed = strings.Replace(changed, "{ id: relay, name: Relay, kind: relay, group: dc, agent: true, card: [cpu] }",
		"{ id: relay, name: Relay, kind: relay, group: dc, agent: true, card: [cpu], role: realm, address: [1.2.3.4] }", 1)
	changed = strings.Replace(changed, `target: "relay.example:2053" }`, `target: "relay.example:2054" }
  - { id: e3, from: relay, to: relay2, plane: data, health: { collector: tcp-connect, from: probe, target: "r2:1" } }`, 1)
	changed = strings.Replace(changed, "  - { id: internet,", "  - { id: relay2, name: Relay 2, kind: relay, group: dc }\n  - { id: internet,", 1)
	changed = strings.Replace(changed, `alerts:
  - { id: a1, when: "link.e1.health == down for 2m", severity: warning, message_fa: "قطع" }
`, "", 1)
	newB := loadTest(t, map[string]string{"base": changed})
	d, err := Diff(old, newB)
	if err != nil {
		t.Fatal(err)
	}
	if c := findChange(d, "add", "node", "relay2"); c == nil || c.File != "topology/components/base.yaml" || c.Line == 0 {
		t.Errorf("node relay2 should be added with its file:line: %+v", c)
	}
	if c := findChange(d, "add", "link", "e3"); c == nil || len(c.Checks) != 1 || !strings.Contains(c.Checks[0], "link:e3:tcp-connect") {
		t.Errorf("link e3 should be added with its check: %+v", c)
	}
	if findChange(d, "add", "check", "link:e3:tcp-connect") != nil {
		t.Error("checks of an added link are listed under the link, not separately")
	}
	if findChange(d, "remove", "alert", "a1") == nil {
		t.Error("alert a1 should be removed")
	}
	c := findChange(d, "change", "node", "relay")
	if c == nil {
		t.Fatalf("node relay should change: %+v", d.Changes)
	}
	paths := map[string]FieldChange{}
	for _, f := range c.Fields {
		paths[f.Path] = f
	}
	if f, ok := paths["role"]; !ok || f.Old != nil || f.New != "realm" {
		t.Errorf("role change: %+v", c.Fields)
	}
	if _, ok := paths["address"]; !ok {
		t.Errorf("address change: %+v", c.Fields)
	}
	for p := range paths {
		if strings.Contains(p, "line") {
			t.Errorf("line numbers must be ignored, got %s", p)
		}
	}
	ck := findChange(d, "change", "check", "link:e1:tcp-connect")
	if ck == nil || len(ck.Fields) != 1 || ck.Fields[0].Path != "params.target" || ck.Fields[0].New != "relay.example:2054" || ck.Owner != "link:e1" || ck.Collector != "tcp-connect" {
		t.Errorf("check change: %+v", ck)
	}
	if findChange(d, "change", "link", "e1") != nil {
		t.Error("a check change is reported on the check, not on its link")
	}
	if d.Count("add") != 2 || d.Count("remove") != 1 || d.Count("change") != 2 {
		t.Errorf("counts: +%d -%d ~%d: %+v", d.Count("add"), d.Count("remove"), d.Count("change"), d.Changes)
	}
	if d.OldHash == d.NewHash {
		t.Error("hashes must differ")
	}
}

func TestDiffChecksAddedToExistingOwner(t *testing.T) {
	old := loadTest(t, map[string]string{"base": goodComponent})
	changed := strings.Replace(goodComponent, "agent: true, card: [cpu] }",
		"agent: true, card: [cpu], checks: [{ id: realm, collector: systemd-unit, host: relay, unit: realm.service }] }", 1)
	d, err := Diff(old, loadTest(t, map[string]string{"base": changed}))
	if err != nil {
		t.Fatal(err)
	}
	c := findChange(d, "add", "check", "node:relay:realm")
	if c == nil || c.Collector != "systemd-unit" || c.File != "topology/components/base.yaml" {
		t.Errorf("added check: %+v", d.Changes)
	}
	if FormatValue(nil) != "–" || FormatValue("x") != `"x"` || FormatValue([]any{"a", 1.0}) != `["a",1]` {
		t.Error("FormatValue")
	}
}
