package topology

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Names shared with the hub (apps/hub/src/engine/ids.ts). The agent config
// generator (agentgen.go) and the hub must derive the same counter and target
// ids from a check key ("link:e3:traffic"), because the hub finds an agent's
// data by these names. Keep both implementations in sync: ids_test.go and the
// hub's tests use the same vectors.

// PromRemnawave is the id of the Remnawave panel metrics scrape on the panel agent.
const PromRemnawave = "remnawave"

// jsLower lower-cases like JavaScript's String.prototype.toLowerCase for the
// characters that can survive the [a-z0-9] filter below: the only
// unconditional special casing that yields ASCII is U+0130 (İ → "i̇").
func jsLower(s string) string {
	if strings.ContainsRune(s, 'İ') {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

// replaceNonAlnum replaces every run of characters outside [a-z0-9] with sep.
func replaceNonAlnum(s string, sep byte) string {
	var b strings.Builder
	run := false
	for i := 0; i < len(s); {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
			run = false
			i++
			continue
		}
		if !run {
			b.WriteByte(sep)
			run = true
		}
		// Skip the whole UTF-8 sequence: it is one character in the run.
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return b.String()
}

// sliceUTF16 returns the first n UTF-16 code units of s (String.slice(0, n)).
func sliceUTF16(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// Slug turns a check key into an id: "link:e3:traffic-fallback" → "link-e3-traffic-fallback".
func Slug(key string) string {
	s := replaceNonAlnum(jsLower(key), '-')
	s = strings.Trim(s, "-")
	return sliceUTF16(s, 63)
}

// NftName is the nft counter name of one direction ("up" or "down") of a
// check: "link:e3:traffic-fallback", "up" → "l_e3_traffic_fallback_up".
func NftName(key, dir string) string {
	parts := strings.Split(key, ":")
	kind, rest := parts[0], parts[1:]
	body := replaceNonAlnum(jsLower(strings.Join(rest, "_")), '_')
	first := "undefined" // kind[0] of an empty string in JavaScript
	if kind != "" {
		u := utf16.Encode([]rune(kind))
		first = string(utf16.Decode(u[:1]))
	}
	name := sliceUTF16(first+"_"+body, 63-len(dir)-1)
	return name + "_" + dir
}

// SSID is the ss-conns id of one port: "link:e1:conns", 2053 → "link-e1-conns-2053".
func SSID(key string, port int) string {
	// Truncate the key part, never the port, so different ports never collide.
	ps := "-" + strconv.Itoa(port)
	return sliceUTF16(Slug(key), 63-len(ps)) + ps
}

// HTTPID is the id of an http-probe run by an agent or the probe.
func HTTPID(key string) string { return Slug(key) }

// LogID is the id of a log-match file tail.
func LogID(key string) string { return Slug(key) }
