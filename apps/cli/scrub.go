package main

import (
	"regexp"
	"strings"

	"vpnmap/pkg/redact"
)

// Everything the CLI prints from the hub goes through scrub, so a secret
// that slipped into a check detail, an event or a log line is masked on
// screen too. Tokens the CLI is asked to create (agent token) are the only
// secrets it prints, once, on purpose.

func scrub(s string) string { return redact.String(s) }

func scrubAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = scrub(s)
	}
	return out
}

var reShellVar = regexp.MustCompile(`^\$(\{[A-Za-z_][A-Za-z0-9_]*\}|[A-Za-z_][A-Za-z0-9_]*)$`)

// scrubCmd masks secrets in a runbook command but keeps references to
// environment variables ("Authorization: Bearer $TOKEN") intact, so the
// command stays copyable. It aligns the masked text with the original and
// restores only masks that replaced a bare $VAR.
func scrubCmd(cmd string) string {
	masked := redact.String(cmd)
	if masked == cmd || !strings.Contains(masked, redact.Mask) {
		return masked
	}
	parts := strings.Split(masked, redact.Mask)
	var b strings.Builder
	pos := 0
	b.WriteString(parts[0])
	if !strings.HasPrefix(cmd, parts[0]) {
		return masked
	}
	pos = len(parts[0])
	for i := 1; i < len(parts); i++ {
		next := parts[i]
		var hidden string
		if next == "" && i < len(parts)-1 {
			return masked // two masks in a row: cannot align, keep them
		}
		if next == "" {
			hidden = cmd[pos:]
		} else {
			j := strings.Index(cmd[pos:], next)
			if j < 0 {
				return masked
			}
			hidden = cmd[pos : pos+j]
		}
		if reShellVar.MatchString(hidden) {
			b.WriteString(hidden)
		} else {
			b.WriteString(redact.Mask)
		}
		b.WriteString(next)
		pos += len(hidden) + len(next)
	}
	return b.String()
}

// scrubJSON masks secret-looking values in decoded JSON (values of
// sensitive keys and secret-looking strings; runbook commands keep $VARs).
func scrubJSON(v any) any {
	switch t := v.(type) {
	case string:
		return scrub(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = scrubJSON(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			switch s, isStr := e.(string); {
			case k == "cmd" && isStr:
				out[k] = scrubCmd(s)
			case isStr && redact.IsSensitiveKey(k) && s != "":
				out[k] = redact.Mask
			default:
				out[k] = scrubJSON(e)
			}
		}
		return out
	}
	return v
}
