package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/scrypt"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/topology"
)

func fp(v float64) *float64 { return &v }

func TestSparkline(t *testing.T) {
	got := sparkline([]*float64{fp(0), fp(1), fp(2), fp(3), fp(4), fp(5), fp(6), fp(7)}, 60)
	if got != "▁▂▃▄▅▆▇█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline([]*float64{fp(5), nil, fp(5)}, 60); got != "▄ ▄" {
		t.Errorf("flat with gap = %q", got)
	}
	long := make([]*float64, 240)
	for i := range long {
		long[i] = fp(float64(i))
	}
	if n := len([]rune(sparkline(long, 60))); n != 60 {
		t.Errorf("bucketed width = %d", n)
	}
	mn, avg, mx, last := stats([]*float64{fp(2), nil, fp(4), fp(9)})
	if *mn != 2 || *mx != 9 || *avg != 5 || *last != 9 {
		t.Errorf("stats = %v %v %v %v", *mn, *avg, *mx, *last)
	}
}

func TestScrubCmdKeepsShellVariables(t *testing.T) {
	cmd := `curl -s -H "Authorization: Bearer $TOKEN" https://pl.api.example.ir/api/nodes`
	if got := scrubCmd(cmd); got != cmd {
		t.Errorf("a $VAR reference must survive: %q", got)
	}
	leak := `curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789" https://x`
	if got := scrubCmd(leak); strings.Contains(got, "abcdefghij") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("a literal token must be masked: %q", got)
	}
	if got := scrub("password=hunter2hunter2 ok"); strings.Contains(got, "hunter2") {
		t.Errorf("scrub: %q", got)
	}
	v := scrubJSON(map[string]any{
		"runbook": []any{map[string]any{"cmd": `ssh x "Authorization: Bearer ${RW_TOKEN}"`}},
		"token":   "plain-secret-value",
		"detail":  "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijk end",
	}).(map[string]any)
	if v["token"] != "[REDACTED]" {
		t.Errorf("sensitive key: %v", v["token"])
	}
	if c := v["runbook"].([]any)[0].(map[string]any)["cmd"]; c != `ssh x "Authorization: Bearer ${RW_TOKEN}"` {
		t.Errorf("cmd: %v", c)
	}
	if strings.Contains(v["detail"].(string), "eyJ") {
		t.Errorf("jwt not masked: %v", v["detail"])
	}
}

// RFC 6238 appendix B (SHA-1), truncated to 6 digits as the hub does.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := totpCode(secret, time.Unix(ts, 0)); got != want {
			t.Errorf("totp(%d) = %s, want %s", ts, got, want)
		}
	}
	if s := b32.EncodeToString(secret); strings.Contains(s, "=") {
		t.Errorf("secret must be unpadded base32: %s", s)
	}
}

func TestHashPasswordFormat(t *testing.T) {
	salt := []byte("0123456789abcdef")
	h, err := hashPassword("correct horse", salt)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[0] != "scrypt" || parts[1] != "32768" || parts[2] != "8" || parts[3] != "1" {
		t.Fatalf("format: %s", h)
	}
	gotSalt, _ := base64.StdEncoding.DecodeString(parts[4])
	key, _ := base64.StdEncoding.DecodeString(parts[5])
	want, _ := scrypt.Key([]byte("correct horse"), salt, 32768, 8, 1, 32)
	if string(gotSalt) != string(salt) || len(key) != 32 || string(key) != string(want) {
		t.Errorf("salt/key mismatch: %s", h)
	}
}

func TestRenderLayout(t *testing.T) {
	got := renderLayout(map[string]topology.Pos{"relay-1": {X: 120, Y: 80.5}, "users": {X: -40, Y: 200}, "Odd Id": {X: 1, Y: 2}})
	want := layoutHeader + "positions:\n  \"Odd Id\": { x: 1, y: 2 }\n  relay-1: { x: 120, y: 80.5 }\n  users: { x: -40, y: 200 }\n"
	if got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}
}

func TestMissingCapabilities(t *testing.T) {
	cfg := &agentcfg.Config{
		Units: []string{"realm.service"},
		Nft:   &agentcfg.NftConfig{Counters: []agentcfg.NftCounter{{Name: "l_e1_traffic_up"}}},
		WG:    []agentcfg.WGConfig{{Netns: "mv-de", Iface: "wg-de"}, {Iface: "wg-game"}},
	}
	got := missingCapabilities(cfg, map[string][]string{"units": {"realm.service"}, "wg": {"wg-game"}})
	if len(got) != 2 || got["nft_counters"][0] != "l_e1_traffic_up" || got["wg"][0] != "mv-de/wg-de" {
		t.Errorf("missing = %v", got)
	}
	if missingCapabilities(cfg, map[string][]string{"units": {"realm.service"}, "wg": {"wg-game", "mv-de/wg-de"}, "nft_counters": {"l_e1_traffic_up"}}) != nil {
		t.Error("nothing should be missing")
	}
}

func TestNodeAddress(t *testing.T) {
	b := &topology.Bundle{Nodes: []topology.Node{{ID: "edge", Address: topology.StringList{"chatgpt.com:443"}}, {ID: "r", Address: topology.StringList{"1.2.3.4", "r.example"}}}}
	if nodeAddress(b, "edge") != "chatgpt.com" || nodeAddress(b, "r") != "1.2.3.4" || nodeAddress(b, "probe") != "<probe-ip>" {
		t.Error("nodeAddress")
	}
}

func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/status":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(401)
				w.Write([]byte(`{"statusCode":401,"message":"login required"}`))
				return
			}
			w.Write([]byte(`{"overall":{"status":"down"}}`))
		case "/api/describe/x":
			w.WriteHeader(404)
			w.Write([]byte(`{"statusCode":404,"message":"no node, link or route \"x\""}`))
		case "/api/silence":
			w.WriteHeader(400)
			w.Write([]byte(`{"statusCode":400,"message":["alert must be a string","duration too long"]}`))
		}
	}))
	defer srv.Close()
	code := func(err error) int {
		var ee *exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		return -1
	}
	c := &client{base: srv.URL, token: "bad", hc: srv.Client()}
	if _, err := c.get("/api/status", nil, nil); err == nil || code(err) != ExitInvalid || !strings.Contains(err.Error(), "vpnmap login") || strings.Contains(err.Error(), "bad") {
		t.Errorf("401: %v", err)
	}
	c.token = "good"
	var st statusResp
	if _, err := c.get("/api/status", nil, &st); err != nil || st.Overall.Status != "down" {
		t.Errorf("ok: %v %+v", err, st)
	}
	if _, err := c.get("/api/describe/x", nil, nil); err == nil || err.Error() != `no node, link or route "x"` {
		t.Errorf("404: %v", err)
	}
	if _, err := c.post("/api/silence", map[string]string{}, nil); err == nil || !strings.Contains(err.Error(), "alert must be a string; duration too long") {
		t.Errorf("400: %v", err)
	}
	dead := &client{base: "http://127.0.0.1:1", hc: &http.Client{Timeout: time.Second}}
	if _, err := dead.get("/api/status", nil, nil); code(err) != ExitInvalid || !strings.Contains(err.Error(), "hint:") {
		t.Errorf("network error: %v", err)
	}
}

func TestExitFromStatus(t *testing.T) {
	defer func() { exitCode = ExitOK }()
	for s, want := range map[string]int{"ok": 0, "degraded": 1, "unknown": 1, "down": 2} {
		exitFromStatus(s)
		if exitCode != want {
			t.Errorf("%s → %d, want %d", s, exitCode, want)
		}
	}
}
