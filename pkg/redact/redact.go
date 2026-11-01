// Package redact masks secrets in free-form text (log lines, check output)
// before anything leaves a server. It is deliberately conservative: when in
// doubt a value is masked. IP addresses and hostnames are kept intact because
// the board needs them to place events on the topology.
//
// What is masked (replaced by "[REDACTED]"):
//   - values of sensitive keys in key=value, key: value and JSON "key":"value"
//     forms (password, secret, token, api_key, private_key, shortIds, jwt, ...)
//   - Authorization header values and "Bearer <token>"
//   - user:password@ in URLs
//   - JWTs, UUIDs (VLESS/VMess user ids are credentials)
//   - long base64 / base64url / hex blobs (>= 32 chars): keys, hashes, tokens
package redact

import (
	"regexp"
	"strings"
)

// Mask replaces every secret value.
const Mask = "[REDACTED]"

// sensitiveSubstrings mark a key as secret when they appear anywhere in it
// (case-insensitive, after removing '-', '_' and '.'): "SECRET_KEY",
// "jwt_secret", "x-api-key", "privateKey" and "client_secret" all match.
var sensitiveSubstrings = []string{
	"password", "passwd", "secret", "token", "apikey", "privatekey",
	"shortid", "jwt", "authorization", "cookie", "credential", "presharedkey",
	"passphrase",
}

// sensitiveExact are short keys that are only secret as a whole word
// (matching "sid" as a substring would also hit "side" or "inside").
// A bare "key" is not listed: it is far too common in ordinary log lines.
var sensitiveExact = map[string]bool{
	"pass": true, "pwd": true, "sid": true, "psk": true, "auth": true,
}

// normKey lower-cases a key and drops separators so "Private-Key",
// "private_key" and "privateKey" compare equal.
func normKey(k string) string {
	k = strings.ToLower(k)
	return strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(k)
}

// IsSensitiveKey reports whether values stored under key k must be masked.
func IsSensitiveKey(k string) bool {
	n := normKey(k)
	if n == "" {
		return false
	}
	if sensitiveExact[n] {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

var (
	// scheme://user:pass@host -> scheme://[REDACTED]@host. The password part
	// is greedy up to the last '@' of the authority so "p@ss" cannot leak.
	reURLUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:"']+:[^\s/"']*@`)

	// Authorization: Bearer xxx / Authorization=Basic xxx / "authorization":"..."
	reAuthHeader = regexp.MustCompile(`(?i)(["']?(?:proxy-)?authorization["']?\s*[:=]\s*["']?)(?:(bearer|basic|token|digest)\s+)?([^\s"',;]+)`)

	// A bare "Bearer <token>" anywhere (curl traces, access logs).
	reBearer = regexp.MustCompile(`(?i)\b(bearer)\s+([A-Za-z0-9._~+/=-]{6,})`)

	// key<sep>value. The key is captured with optional quotes so JSON works.
	// Values: "double quoted", 'single quoted', [array], or a bare token.
	reKeyValue = regexp.MustCompile(`(["']?)([A-Za-z0-9_.-]{2,64})(["']?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|\[[^\]]*\]|[^\s,;&}\]"']+)`)

	reJWT  = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)
	reUUID = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

	// Candidate blobs: runs of base64/base64url characters, optional padding.
	reBlob = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)
)

// String returns s with every secret-looking value replaced by Mask.
func String(s string) string {
	if s == "" {
		return s
	}
	s = reURLUserinfo.ReplaceAllString(s, "${1}"+Mask+"@")
	s = reAuthHeader.ReplaceAllStringFunc(s, func(m string) string {
		sub := reAuthHeader.FindStringSubmatch(m)
		out := sub[1]
		if sub[2] != "" {
			out += sub[2] + " "
		}
		return out + Mask
	})
	s = reBearer.ReplaceAllString(s, "${1} "+Mask)
	s = reKeyValue.ReplaceAllStringFunc(s, redactKeyValue)
	s = reJWT.ReplaceAllString(s, Mask)
	s = reUUID.ReplaceAllString(s, Mask)
	s = reBlob.ReplaceAllStringFunc(s, func(m string) string {
		if looksLikeBlob(m) {
			return Mask
		}
		return m
	})
	return s
}

func redactKeyValue(m string) string {
	sub := reKeyValue.FindStringSubmatch(m)
	q1, key, sep, val := sub[1], sub[2], sub[3], sub[4]
	if !IsSensitiveKey(key) {
		// The value of an innocent key can still hide a secret:
		// msg="login password=x" or next=/cb?token=x. Redact inside it; the
		// value is strictly shorter than m, so the recursion terminates.
		return q1 + key + sep + String(val)
	}
	// Already masked, or an Authorization header handled with its scheme kept.
	if strings.Contains(val, Mask) || strings.Contains(normKey(key), "authorization") {
		return m
	}
	switch {
	case strings.HasPrefix(val, `"`):
		val = `"` + Mask + `"`
	case strings.HasPrefix(val, `'`):
		val = `'` + Mask + `'`
	default:
		val = Mask
	}
	return q1 + key + sep + val
}

// looksLikeBlob decides whether a long base64/hex-looking run is a secret or
// an innocent identifier (a path like /var/lib/vpnmap-agent/state_2024 or a
// long_snake_case_name). Random keys mix character classes and have few
// separators; identifiers are words joined by '/', '-' or '_'.
func looksLikeBlob(m string) bool {
	core := strings.TrimRight(m, "=")
	if len(core) < 32 {
		return false
	}
	var upper, lower, digit, seps int
	hexOnly := true
	for _, r := range core {
		switch {
		case r >= 'A' && r <= 'Z':
			upper++
			if r > 'F' {
				hexOnly = false
			}
		case r >= 'a' && r <= 'z':
			lower++
			if r > 'f' {
				hexOnly = false
			}
		case r >= '0' && r <= '9':
			digit++
		case r == '/' || r == '-' || r == '_':
			seps++
			hexOnly = false
		default: // '+'
			hexOnly = false
		}
	}
	if hexOnly && digit > 0 {
		return true // sha256, api keys, wg keys in hex
	}
	if seps*8 > len(core) {
		return false // word_word_word or a/long/path
	}
	padded := len(m) != len(core)
	if padded && len(core) >= 40 {
		return true // WireGuard / x25519 keys: 43 chars + "="
	}
	return upper > 0 && lower > 0 && digit > 0
}

// Map returns a deep copy of m where values of sensitive keys are replaced
// by Mask and every other string is passed through String.
func Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if IsSensitiveKey(k) {
			out[k] = Mask
			continue
		}
		out[k] = Value(v)
	}
	return out
}

// Value redacts an arbitrary JSON-like value (as produced by encoding/json).
func Value(v any) any {
	switch t := v.(type) {
	case string:
		return String(t)
	case map[string]any:
		return Map(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Value(e)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = String(e)
		}
		return out
	default:
		return v
	}
}
