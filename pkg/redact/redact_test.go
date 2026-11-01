package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "connection accepted from 10.0.0.5:51234", "connection accepted from 10.0.0.5:51234"},
		{"ipv4 and host kept", "dial tcp 81.0.0.1:443 via relay.example.com:8443 ok", "dial tcp 81.0.0.1:443 via relay.example.com:8443 ok"},
		{"ipv6 kept", "listen [2001:db8::1]:443", "listen [2001:db8::1]:443"},
		{"time kept", "2026/11/01 12:00:00 started", "2026/11/01 12:00:00 started"},
		{"password kv", "login user=admin password=hunter2 ok", "login user=admin password=[REDACTED] ok"},
		{"passwd colon", "passwd: s3cr3t", "passwd: [REDACTED]"},
		{"pass exact", "pass=abc&user=x", "pass=[REDACTED]&user=x"},
		{"secret upper", "SECRET_KEY=abcdef123", "SECRET_KEY=[REDACTED]"},
		{"jwt secret", "JWT_AUTH_SECRET=xyz", "JWT_AUTH_SECRET=[REDACTED]"},
		{"jwt key", "jwt: abc", "jwt: [REDACTED]"},
		{"token json", `{"token":"abc.def","node":"relay-1"}`, `{"token":"[REDACTED]","node":"relay-1"}`},
		{"access_token query", "GET /cb?access_token=abc123&x=1", "GET /cb?access_token=[REDACTED]&x=1"},
		{"apikey", "apikey=zzz api_key=yyy x-api-key: www", "apikey=[REDACTED] api_key=[REDACTED] x-api-key: [REDACTED]"},
		{"private_key", "private_key = 'abc'", "private_key = '[REDACTED]'"},
		{"privateKey json", `"privateKey": "abc"`, `"privateKey": "[REDACTED]"`},
		{"shortIds array", `"shortIds": ["ab12", "cd34"], "dest": "example.com:443"`, `"shortIds": [REDACTED], "dest": "example.com:443"`},
		{"short_id", "short_id=0a1b", "short_id=[REDACTED]"},
		{"sid exact only", "sid=0a1b side=local", "sid=[REDACTED] side=local"},
		{"authorization bearer", "Authorization: Bearer abc.def.ghi", "Authorization: Bearer [REDACTED]"},
		{"authorization basic", "authorization=Basic dXNlcjpwYXNz", "authorization=Basic [REDACTED]"},
		{"authorization json", `"Authorization":"Bearer xyz123"`, `"Authorization":"Bearer [REDACTED]"`},
		{"bare bearer", "curl -H bearer abcdef123456 http://x", "curl -H bearer [REDACTED] http://x"},
		{"url userinfo", "connect postgres://admin:p4ss@db.local:5432/x", "connect postgres://[REDACTED]@db.local:5432/x"},
		{"url userinfo with at in password", "https://u:p@ss@host.example.com/x", "https://[REDACTED]@host.example.com/x"},
		{"url without userinfo kept", "GET https://map.example.com/api/ingest/v1/push", "GET https://map.example.com/api/ingest/v1/push"},
		{"secret inside innocent quoted value", `level=info msg="login ok password=hunter2"`, `level=info msg="login ok password=[REDACTED]"`},
		{"secret inside innocent bare value", "next=/login?token=abc", "next=/login?token=[REDACTED]"},
		{"jwt blob", "got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.abcDEF123_- here", "got [REDACTED] here"},
		{"uuid", "user 3f2a1b4c-5d6e-7f80-91a2-b3c4d5e6f708 connected", "user [REDACTED] connected"},
		{"vless link", "vless://3f2a1b4c-5d6e-7f80-91a2-b3c4d5e6f708@relay.example.com:443?security=reality", "vless://[REDACTED]@relay.example.com:443?security=reality"},
		{"wg key", "peer aGVsbG8gd29ybGQgdGhpcyBpcyBhIHRlc3Qga2V5IDE=", "peer [REDACTED]"},
		{"hex sha256", "image sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "image sha256:[REDACTED]"},
		{"base64url key", "pbk=Zx9_yQ3kLm8NpR2sTv5WxA7bCd1EfGh4IjKl6MnOp0Q tail", "pbk=[REDACTED] tail"},
		{"long path kept", "open /var/lib/vpnmap-agent/state_file_2024_Backup.json", "open /var/lib/vpnmap-agent/state_file_2024_Backup.json"},
		{"snake identifier kept", "unit remnawave_backup_age_check_for_node_1 ok", "unit remnawave_backup_age_check_for_node_1 ok"},
		{"long word kept", "Supercalifragilisticexpialidociouslyx", "Supercalifragilisticexpialidociouslyx"},
		{"short hex kept", "connection mt6snj5mlbnn status ok", "connection mt6snj5mlbnn status ok"},
		{"cookie", "Cookie: session=abc; theme=dark", "Cookie: [REDACTED]; theme=dark"},
		{"natural language kept", "no secret found, token expired: please re-login", "no secret found, token expired: please re-login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := String(c.in)
			if got != c.want {
				t.Errorf("String(%q)\n got  %q\n want %q", c.in, got, c.want)
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
