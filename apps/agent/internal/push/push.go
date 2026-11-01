// Package push sends reports to the hub. Data flows one way: the response
// body is never interpreted beyond its status code, so the hub (or anyone
// impersonating it) cannot make the agent do anything.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"vpnmap/apps/agent/internal/guard"
	"vpnmap/pkg/protocol"
)

// Path is the ingest endpoint below the hub base URL.
const Path = "/api/ingest/v1/push"

// Timeout bounds one push.
const Timeout = 4 * time.Second

// StatusError is a non-2xx answer from the hub.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("hub answered HTTP %d", e.Code) }

// Client posts pushes to the hub.
type Client struct {
	url       string
	tokenFile string
	userAgent string
	http      *http.Client

	mu    sync.Mutex
	token string
}

// New validates the hub URL (https, or http to loopback only) and reads the
// token file.
func New(hub, tokenFile, version string) (*Client, error) {
	u, err := guard.HubURL(hub)
	if err != nil {
		return nil, err
	}
	c := &Client{
		url:       strings.TrimRight(u.String(), "/") + Path,
		tokenFile: tokenFile,
		userAgent: "vpnmap-agent/" + version,
		http: &http.Client{
			Timeout: Timeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				TLSHandshakeTimeout: Timeout,
				MaxIdleConns:        2,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
			// Never follow redirects: the bearer token must only ever be sent
			// to the configured hub URL. A redirect counts as a failure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	if err := c.reloadToken(); err != nil {
		return nil, err
	}
	return c, nil
}

// URL is the full ingest URL.
func (c *Client) URL() string { return c.url }

// ReadToken reads a token file: first line, trimmed, at most 4 KiB.
// Errors never include the file content.
func ReadToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", fmt.Errorf("token file %s: read failed", path)
	}
	tok, _, _ := strings.Cut(string(b), "\n")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	for _, r := range tok {
		if r < 0x21 || r > 0x7e {
			return "", fmt.Errorf("token file %s contains invalid characters", path)
		}
	}
	return tok, nil
}

func (c *Client) reloadToken() error {
	tok, err := ReadToken(c.tokenFile)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.token = tok
	c.mu.Unlock()
	return nil
}

// Push sends one report. On 401/403 the token file is re-read so a token
// rotated on disk is picked up by the next attempt without a restart.
func (c *Client) Push(ctx context.Context, p *protocol.Push) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode push: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return scrubURL(err)
	}
	// Drain a little so the connection can be reused; the content is ignored.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = c.reloadToken()
		}
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}

// scrubURL drops the request URL from *url.Error messages so logs stay short
// (the URL carries no secret, but the message is logged on every state change).
func scrubURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// Kind summarises an error for state-change logging: errors of the same
// kind are logged once, a change of kind is logged again.
func Kind(err error) string {
	if err == nil {
		return ""
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// Backoff returns the wait before the next attempt after n consecutive
// failures: interval, 2x, 4x, ... capped at max.
func Backoff(interval, max time.Duration, failures int) time.Duration {
	if failures <= 0 {
		return interval
	}
	d := interval
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
