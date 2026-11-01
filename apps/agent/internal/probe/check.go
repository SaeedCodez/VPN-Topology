package probe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"vpnmap/apps/agent/internal/collect"
	"vpnmap/pkg/protocol"
)

// Checker fetches the probe URLs through a local proxy.
type Checker struct {
	URL       string
	TraceURL  string
	UserAgent string
	Timeout   time.Duration // per request; default 10s
}

// Probe runs one end-to-end check through proxy and reports the route state.
// OK means: the 204 URL answered 204 through the tunnel and, when an exit
// country is expected, the trace confirmed it.
func (c *Checker) Probe(ctx context.Context, proxy *url.URL, route, expectCountry string) protocol.ProbeResult {
	r := protocol.ProbeResult{Route: route, At: collect.Now()}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxy),
			DisableKeepAlives: true, // every probe opens a fresh tunnel connection
			ForceAttemptHTTP2: true,
		},
		Timeout: timeout,
		Jar:     nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // a redirect from a 204 URL means interception
		},
	}
	defer client.CloseIdleConnections()

	start := time.Now()
	status, _, err := c.get(ctx, client, c.URL, 1024)
	r.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		r.Error = "probe: " + collect.ClassifyNetError(err)
		return r
	}
	if status != http.StatusNoContent {
		r.Error = fmt.Sprintf("probe: status %d (want 204)", status)
		return r
	}

	_, body, err := c.get(ctx, client, c.TraceURL, 16<<10)
	if err == nil {
		r.ExitIP, r.ExitCountry = ParseTrace(body)
	}
	want := strings.ToUpper(expectCountry)
	switch {
	case want == "":
		r.OK = true
	case err != nil:
		r.Error = "exit country unknown (trace: " + collect.ClassifyNetError(err) + ")"
	case r.ExitCountry != want:
		r.Error = fmt.Sprintf("exit country %s, want %s", orUnknown(r.ExitCountry), want)
	default:
		r.OK = true
	}
	return r
}

func (c *Checker) get(ctx context.Context, client *http.Client, u string, limit int64) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, body, err
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
