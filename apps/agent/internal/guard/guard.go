// Package guard holds the agent's defensive checks that do not depend on the
// config being correct: URL locality and a denylist of secret files.
//
// agentcfg.Validate checks URLs with string prefixes, which also accepts
// "http://127.0.0.1@evil.example" or "http://localhost.evil.example". The
// agent re-checks every URL it is about to use with a real URL parser.
package guard

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// IsLoopbackHost reports whether host (without port) is a loopback name/IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HubURL validates the hub base URL: https anywhere, or plain http only to a
// loopback address (tests, SSH tunnels). It never carries credentials.
func HubURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("hub URL: %w", err)
	}
	if u.User != nil {
		return nil, fmt.Errorf("hub URL must not contain credentials")
	}
	if u.Host == "" || u.Opaque != "" {
		return nil, fmt.Errorf("hub URL needs a host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !IsLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("hub URL must be https:// (plain http only to 127.0.0.1/localhost)")
		}
	default:
		return nil, fmt.Errorf("hub URL must be https://")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("hub URL must not have a query or fragment")
	}
	return u, nil
}

// LocalHTTPURL validates a URL the agent may send local credentials to
// (Prometheus endpoints): plain http to a loopback host, no userinfo.
func LocalHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%q is not an http URL", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL must not contain credentials (use auth_file)")
	}
	if !IsLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("%s is not a local address", u.Hostname())
	}
	return u, nil
}

// secretDirs are never read by the agent, whatever the config says.
var secretDirs = []string{
	"/etc/wireguard", "/etc/mullvad-ns", "/etc/x-ui", "/root", "/etc/ssh",
	"/etc/ssl/private", "/etc/letsencrypt",
}

var secretFiles = map[string]bool{
	"/etc/shadow": true, "/etc/gshadow": true, "/etc/sudoers": true,
}

var secretSuffixes = []string{".env", ".key", ".pem", ".p12", ".pfx", ".db", ".sqlite", ".sqlite3", ".kdbx"}

// SecretPath reports why a path looks like a secret file ("" if it does not).
// Log tailing refuses such paths even if the root-owned config lists them.
func SecretPath(p string) string {
	clean := filepath.Clean(p)
	if secretFiles[clean] {
		return "system credential file"
	}
	for _, d := range secretDirs {
		if clean == d || strings.HasPrefix(clean, d+"/") {
			return "inside " + d
		}
	}
	base := strings.ToLower(filepath.Base(clean))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return "environment file"
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(base, s) {
			return "secret-looking file type " + s
		}
	}
	if strings.Contains(base, "id_rsa") || strings.Contains(base, "id_ed25519") {
		return "ssh key"
	}
	return ""
}

// CheckReadable returns an error if p, or the file it resolves to through
// symlinks, is a secret path.
func CheckReadable(p string) error {
	if why := SecretPath(p); why != "" {
		return fmt.Errorf("refusing to read %s: %s", p, why)
	}
	if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
		if why := SecretPath(real); why != "" {
			return fmt.Errorf("refusing to read %s (-> %s): %s", p, real, why)
		}
	}
	return nil
}
