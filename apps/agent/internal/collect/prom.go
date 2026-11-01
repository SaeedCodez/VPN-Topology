package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"vpnmap/apps/agent/internal/guard"
	"vpnmap/apps/agent/internal/promtext"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

const (
	maxPromSamples = 2000
	maxPromBody    = 32 << 20
)

// PromScraper reads local Prometheus endpoints and keeps allow-listed
// metrics only. Credentials come from the target's auth_file and are sent
// only to loopback addresses, never logged and never forwarded.
type PromScraper struct {
	UserAgent string
	client    *http.Client
}

func (p *PromScraper) httpClient() *http.Client {
	if p.client == nil {
		p.client = &http.Client{
			Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
			// A redirect could carry basic auth elsewhere: never follow.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return p.client
}

// Scrape fetches one target. A partial result (sample cap reached) is
// returned together with an error describing it.
func (p *PromScraper) Scrape(ctx context.Context, t agentcfg.PromTarget, timeout time.Duration) ([]protocol.PromSample, error) {
	u, err := guard.LocalHTTPURL(t.URL)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.UserAgent)
	// Ask for the classic text format, not protobuf/OpenMetrics.
	req.Header.Set("Accept", "text/plain;version=0.0.4")
	if t.AuthFile != "" {
		user, pass, err := readAuthFile(t.AuthFile)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(user, pass)
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape: %s", ClassifyNetError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("scrape: status %d", resp.StatusCode)
	}
	allow := make(map[string]bool, len(t.Metrics))
	for _, m := range t.Metrics {
		allow[m] = true
	}
	samples, dropped, err := promtext.Parse(io.LimitReader(resp.Body, maxPromBody), allow, maxPromSamples)
	out := make([]protocol.PromSample, 0, len(samples))
	for _, s := range samples {
		out = append(out, protocol.PromSample{Target: t.ID, Name: s.Name, Labels: s.Labels, Value: s.Value})
	}
	if err != nil {
		return out, fmt.Errorf("parse: %w", err)
	}
	if dropped > 0 {
		return out, fmt.Errorf("%d samples dropped (limit %d per target)", dropped, maxPromSamples)
	}
	return out, nil
}

// readAuthFile reads "user:password". Error messages never include content.
func readAuthFile(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("auth_file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", "", fmt.Errorf("auth_file: read failed")
	}
	user, pass, ok := strings.Cut(strings.TrimSpace(string(b)), ":")
	if !ok || user == "" {
		return "", "", fmt.Errorf("auth_file %s must contain user:password", path)
	}
	return user, pass, nil
}
