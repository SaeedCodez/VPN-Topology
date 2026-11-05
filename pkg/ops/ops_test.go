package ops

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type requestCases struct {
	Config RunnerConfig `json:"config"`
	Cases  []struct {
		Name      string `json:"name"`
		Line      string `json:"line"`
		OK        bool   `json:"ok"`
		CooldownS int    `json:"cooldown_s"`
	} `json:"cases"`
}

func loadCases(t *testing.T) requestCases {
	t.Helper()
	raw, err := os.ReadFile("testdata/requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc requestCases
	if err := json.Unmarshal(raw, &rc); err != nil {
		t.Fatal(err)
	}
	if len(rc.Cases) < 40 {
		t.Fatalf("expected at least 40 shared cases, got %d", len(rc.Cases))
	}
	return rc
}

// The same file is replayed by the hub's tests, so both sides agree.
func TestSharedRequestCases(t *testing.T) {
	rc := loadCases(t)
	if err := rc.Config.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	for _, c := range rc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			r, err := ParseRequest(c.Line)
			var a *Allowed
			if err == nil {
				a, err = rc.Config.Allow(r)
			}
			if c.OK != (err == nil) {
				t.Fatalf("%q: ok=%v, err=%v", c.Line, c.OK, err)
			}
			if err == nil && a.Cooldown != time.Duration(c.CooldownS)*time.Second {
				t.Fatalf("cooldown %s, want %ds", a.Cooldown, c.CooldownS)
			}
		})
	}
}

func TestCanonicalString(t *testing.T) {
	r, err := ParseRequest("script.change script=select relay=de-dus-wg-103 cc=de req=00ff00ff")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.String(); got != "script.change cc=de relay=de-dus-wg-103 script=select req=00ff00ff" {
		t.Fatal(got)
	}
	if strings.Contains(r.Key(), "req=") {
		t.Fatal("cooldown key must not include the request id")
	}
}

func TestConfigValidateRefusesOddValues(t *testing.T) {
	bad := []RunnerConfig{
		{Node: "n", Containers: []Container{{Name: "a b"}}},
		{Node: "n", Containers: []Container{{Name: "-rm"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "relative.log"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/var/log/../../etc/shadow"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/etc/shadow"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/etc/vpnmap-ops/ops.yaml"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/var/log/a b.log"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/opt/app/.env"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/etc/mullvad-ns/de.log"}}},
		{Node: "n", Logs: []Log{{ID: "x", Path: "/root/x.log"}}},
		{Node: "n", TCPTargets: []string{"evil:99999"}},
		{Node: "n", TCPTargets: []string{"a;b:22"}},
		{Node: "n", WG: []WG{{Iface: "wg0"}, {Iface: "wg0", Netns: "x"}}},
		{Node: "n", Scripts: []Script{{ID: "../x", Kind: Read}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: "write"}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: Read, Params: []Param{{Name: "p", Pattern: "[a-z]+"}}}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: Read, Params: []Param{{Name: "script", Enum: []string{"a"}}}}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: Read, Params: []Param{{Name: "p", Enum: []string{"a;b"}}}}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: Read, Params: []Param{{Name: "p", Enum: []string{"a"}, Pattern: "^a$"}}}}},
		{Node: "n", Scripts: []Script{{ID: "x", Kind: Read, Timeout: "10m"}}},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected an error for %+v", i, c)
		}
	}
}

func TestPatternCannotWidenTokens(t *testing.T) {
	p := Param{Name: "x", Pattern: "^.*$"}
	for _, v := range []string{"a;b", "$(id)", "-x", "a b", "..", "é"} {
		if p.Accepts(v) {
			t.Errorf("pattern ^.*$ accepted %q", v)
		}
	}
	if !p.Accepts("fine-1") {
		t.Error("a plain token should pass")
	}
}

func TestHashIsOrderIndependent(t *testing.T) {
	a := RunnerConfig{Node: "n", Ports: []int{2, 1}, Containers: []Container{{Name: "b"}, {Name: "a"}}}
	b := RunnerConfig{Node: "n", Ports: []int{1, 2}, Containers: []Container{{Name: "a"}, {Name: "b"}}}
	if a.Hash() != b.Hash() {
		t.Fatal("hash depends on order")
	}
	b.Ports = append(b.Ports, 3)
	if a.Hash() == b.Hash() {
		t.Fatal("hash ignores content")
	}
}

func TestEveryTypeIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range Types {
		if seen[ty.Name] {
			t.Errorf("type %s twice", ty.Name)
		}
		seen[ty.Name] = true
		if ty.Kind != Read && ty.Kind != Change {
			t.Errorf("%s: kind %q", ty.Name, ty.Kind)
		}
		if ty.Where != OnServer && ty.Where != OnRemnawave {
			t.Errorf("%s: where %q", ty.Name, ty.Where)
		}
		if ty.Kind == Change && ty.Cooldown == "" {
			t.Errorf("%s: change types need a default cooldown", ty.Name)
		}
		if ty.Rollback != "" && TypeByName(ty.Rollback) == nil {
			t.Errorf("%s: unknown rollback %s", ty.Name, ty.Rollback)
		}
		for _, p := range ty.Params {
			if p.Source == "" {
				t.Errorf("%s.%s: no source", ty.Name, p.Name)
			}
		}
	}
}
