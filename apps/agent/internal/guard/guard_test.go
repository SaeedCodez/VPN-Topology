package guard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHubURL(t *testing.T) {
	ok := []string{"https://map.example.com", "https://map.example.com/", "http://127.0.0.1:8080", "http://localhost:3000", "http://[::1]:3000"}
	bad := []string{
		"http://map.example.com",          // plain http over the internet
		"http://127.0.0.1.evil.example",   // prefix trick
		"http://127.0.0.1@evil.example",   // userinfo trick
		"http://localhost.evil.example",   // prefix trick
		"https://user:pw@map.example.com", // credentials
		"ftp://map.example.com",           // scheme
		"https://map.example.com/?x=1",    // query
		"https:map.example.com",           // opaque
		"",                                // empty
	}
	for _, s := range ok {
		if _, err := HubURL(s); err != nil {
			t.Errorf("HubURL(%q) = %v, want ok", s, err)
		}
	}
	for _, s := range bad {
		if _, err := HubURL(s); err == nil {
			t.Errorf("HubURL(%q) accepted, want error", s)
		}
	}
}

func TestLocalHTTPURL(t *testing.T) {
	for _, s := range []string{"http://127.0.0.1:3001/metrics", "http://localhost:9100/metrics", "http://[::1]:1/m"} {
		if _, err := LocalHTTPURL(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"http://127.0.0.1@evil.example/metrics", "http://127.0.0.1.evil.example/", "http://10.0.0.1/metrics", "http://u:p@127.0.0.1/", "file:///etc/passwd"} {
		if _, err := LocalHTTPURL(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestSecretPath(t *testing.T) {
	secret := []string{"/opt/remnawave/.env", "/opt/app/.env.production", "/etc/wireguard/wg0.conf", "/etc/wireguard",
		"/etc/mullvad-ns/account", "/etc/x-ui/x-ui.db", "/usr/local/x-ui/x-ui.db", "/root/notes.txt", "/etc/shadow",
		"/srv/tls/server.key", "/srv/tls/cert.pem", "/home/u/.ssh/id_ed25519", "/opt/../etc/shadow"}
	fine := []string{"/var/log/xray/access.log", "/var/log/syslog", "/opt/remnawave-backup/backup.log", "/var/log/nginx/error.log", "/etc/rootless.log"}
	for _, p := range secret {
		if SecretPath(p) == "" {
			t.Errorf("SecretPath(%q) = \"\", want a reason", p)
		}
	}
	for _, p := range fine {
		if why := SecretPath(p); why != "" {
			t.Errorf("SecretPath(%q) = %q, want \"\"", p, why)
		}
	}
}

func TestCheckReadableFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.env")
	if err := os.WriteFile(target, []byte("X=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "innocent.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckReadable(link); err == nil {
		t.Error("symlink to a .env file must be refused")
	}
	plain := filepath.Join(dir, "plain.log")
	os.WriteFile(plain, nil, 0o600)
	if err := CheckReadable(plain); err != nil {
		t.Errorf("plain log refused: %v", err)
	}
}
