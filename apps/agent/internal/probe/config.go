// Package probe runs end-to-end checks through real VPN clients. For each
// route a long-lived `sing-box run` child process exposes a `mixed` (HTTP +
// SOCKS5) inbound on 127.0.0.1:<free port> that forwards everything to the
// route's outbound. The probe then fetches a 204 URL and a trace URL through
// that proxy, measuring latency and the exit IP/country.
//
// The outbound file holds a monitoring user's credentials. It is read only
// to build the child's config, which lives in a 0700 runtime directory and is
// deleted on exit; neither is ever logged or sent to the hub.
package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vpnmap/pkg/agentcfg"
)

// Defaults.
const (
	DefaultURL      = "http://cp.cloudflare.com/generate_204"
	DefaultTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	DefaultInterval = 30 * time.Second
	MinInterval     = 10 * time.Second
	OutboundTag     = "probe-out"
	InboundTag      = "probe-in"
)

var reRoute = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// Validate checks the probe section beyond what agentcfg.Validate covers.
func Validate(p *agentcfg.ProbeConfig) error {
	if p == nil {
		return nil
	}
	var errs []string
	if p.SingBox != "" && !filepath.IsAbs(p.SingBox) {
		errs = append(errs, "probe.sing_box must be an absolute path")
	}
	for _, u := range []string{p.URL, p.TraceURL} {
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			errs = append(errs, fmt.Sprintf("probe URL %q must be http(s)", u))
		}
	}
	if len(p.Routes) == 0 {
		errs = append(errs, "probe.routes is empty")
	}
	seen := map[string]bool{}
	for _, r := range p.Routes {
		if !reRoute.MatchString(r.Route) {
			errs = append(errs, fmt.Sprintf("probe route %q is not a valid id", r.Route))
		}
		if seen[r.Route] {
			errs = append(errs, fmt.Sprintf("probe route %q is listed twice", r.Route))
		}
		seen[r.Route] = true
		if !filepath.IsAbs(r.OutboundFile) {
			errs = append(errs, fmt.Sprintf("probe route %q: outbound_file must be an absolute path", r.Route))
		}
		if r.ExpectCountry != "" && !regexp.MustCompile(`^[A-Za-z]{2}$`).MatchString(r.ExpectCountry) {
			errs = append(errs, fmt.Sprintf("probe route %q: expect_country must be a 2-letter code", r.Route))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Settings are the effective probe settings with defaults applied.
type Settings struct {
	SingBox  string
	URL      string
	TraceURL string
	Interval time.Duration
}

// Effective applies defaults to a probe config.
func Effective(p *agentcfg.ProbeConfig) Settings {
	s := Settings{SingBox: p.SingBox, URL: p.URL, TraceURL: p.TraceURL, Interval: p.Interval.D()}
	if s.SingBox == "" {
		s.SingBox = "sing-box" // looked up in the fixed system dirs
	}
	if s.URL == "" {
		s.URL = DefaultURL
	}
	if s.TraceURL == "" {
		s.TraceURL = DefaultTraceURL
	}
	if s.Interval == 0 {
		s.Interval = DefaultInterval
	}
	if s.Interval < MinInterval {
		s.Interval = MinInterval
	}
	return s
}

// skipTypes are helper outbounds found in full client configs.
var skipTypes = map[string]bool{"direct": true, "block": true, "dns": true, "selector": true, "urltest": true}

// ExtractOutbound accepts either a single sing-box outbound object or a full
// client config with an "outbounds" array (the first proxy outbound is used).
// Errors never quote the file, which contains credentials.
func ExtractOutbound(raw []byte) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("outbound file is not a valid JSON object")
	}
	if list, ok := doc["outbounds"].([]any); ok {
		for _, e := range list {
			o, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := o["type"].(string); t != "" && !skipTypes[t] {
				return o, nil
			}
		}
		return nil, errors.New(`outbound file: no proxy outbound in "outbounds"`)
	}
	if t, _ := doc["type"].(string); t == "" {
		return nil, errors.New(`outbound file: missing "type"`)
	}
	return doc, nil
}

// BuildConfig renders the sing-box config for one route: a mixed inbound on
// 127.0.0.1:port, the route's outbound tagged "probe-out", and a route whose
// final outbound is probe-out (no direct fallback: a broken tunnel must fail,
// not silently succeed through the probe's own connection).
func BuildConfig(outboundFile []byte, port int) ([]byte, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	out, err := ExtractOutbound(outboundFile)
	if err != nil {
		return nil, err
	}
	out["tag"] = OutboundTag
	cfg := map[string]any{
		"log": map[string]any{"level": "error", "timestamp": false},
		"inbounds": []any{map[string]any{
			"type": "mixed", "tag": InboundTag,
			"listen": "127.0.0.1", "listen_port": port,
		}},
		"outbounds": []any{out},
		"route":     map[string]any{"final": OutboundTag},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// ParseTrace extracts ip= and loc= from a Cloudflare /cdn-cgi/trace body.
func ParseTrace(body []byte) (ip, loc string) {
	for _, line := range strings.Split(string(body), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ip":
			ip = v
		case "loc":
			loc = strings.ToUpper(v)
		}
	}
	return ip, loc
}
