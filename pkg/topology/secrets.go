package topology

import (
	"os"
	"regexp"
	"strings"
)

// Topology files are committed to git, so they must never hold secrets.
// This is a coarse scan; it errs on the side of flagging.
var secretPatterns = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|private[_-]?key|privatekey|short_?ids?|secret_key|jwt[a-z_]*)\s*[:=]\s*["']?[^\s"'$<{][^\s"']{5,}`), "a credential value"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{16,}`), "a bearer token"},
	{regexp.MustCompile(`(?i)\btoken\s*[:=]\s*["']?[A-Za-z0-9._~+/-]{16,}`), "a token"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "a private key"},
	{regexp.MustCompile(`://[^/\s:@]+:[^/\s@]{3,}@`), "credentials in a URL"},
	{regexp.MustCompile(`\b[A-Za-z0-9+/]{43}=`), "a WireGuard/REALITY key"},
}

func scanSecrets(path, rel string, ds *Diagnostics) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for i, line := range strings.Split(string(raw), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		for _, p := range secretPatterns {
			if p.re.MatchString(line) {
				ds.errorf(rel, i+1, 0, "remove it: secrets live only in hub env vars or on the servers; reference them by name (e.g. $RW_TOKEN)",
					"this line looks like it contains %s", p.what)
				break
			}
		}
	}
}
