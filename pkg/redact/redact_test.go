package redact

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The cases live in testdata/cases.json so the hub's TypeScript port
// (apps/hub/src/redact.ts) is tested against exactly the same table.
func TestString(t *testing.T) {
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		String []struct{ Name, In, Want string } `json:"string"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.String) < 30 {
		t.Fatalf("only %d cases in testdata/cases.json", len(file.String))
	}
	for _, c := range file.String {
		t.Run(c.Name, func(t *testing.T) {
			got := String(c.In)
			if got != c.Want {
				t.Errorf("String(%q)\n got  %q\n want %q", c.In, got, c.Want)
			}
		})
	}
}

func TestStringIdempotent(t *testing.T) {
	in := `password=x Authorization: Bearer abc "token":"y" https://u:p@h/x`
	once := String(in)
	if twice := String(once); twice != once {
		t.Fatalf("not idempotent:\n once  %q\n twice %q", once, twice)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	yes := []string{"password", "DB_PASSWORD", "passwd", "secret", "client_secret", "SECRET_KEY", "token", "accessToken",
		"apikey", "api_key", "X-Api-Key", "private_key", "privateKey", "shortIds", "short_id", "JWT", "jwt_secret",
		"Authorization", "Proxy-Authorization", "cookie", "Set-Cookie", "pass", "sid", "psk", "preshared_key", "auth"}
	no := []string{"", "node", "side", "inside", "host", "ip", "port", "status", "key_count", "public", "user", "connections", "id"}
	for _, k := range yes {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", k)
		}
	}
	for _, k := range no {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", k)
		}
	}
}

func TestMap(t *testing.T) {
	var in map[string]any
	raw := `{
		"status": "ok",
		"host": "10.0.0.1",
		"password": "hunter2",
		"nested": {"privateKey": "abc", "endpoint": "relay.example.com:443", "note": "token=zzz"},
		"list": [{"secret": 1}, "Bearer abcdef123456", 42],
		"shortIds": ["a1", "b2"],
		"count": 3
	}`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	out := Map(in)
	b, _ := json.Marshal(out)
	s := string(b)
	for _, leak := range []string{"hunter2", `"abc"`, "zzz", "abcdef123456", `"a1"`} {
		if strings.Contains(s, leak) {
			t.Errorf("Map leaked %q: %s", leak, s)
		}
	}
	for _, keep := range []string{`"status":"ok"`, `"host":"10.0.0.1"`, `"endpoint":"relay.example.com:443"`, `"count":3`, `42`} {
		if !strings.Contains(s, keep) {
			t.Errorf("Map lost %q: %s", keep, s)
		}
	}
	// The input must not be modified.
	if in["password"] != "hunter2" {
		t.Error("Map modified its input")
	}
	if Map(nil) != nil {
		t.Error("Map(nil) != nil")
	}
}
