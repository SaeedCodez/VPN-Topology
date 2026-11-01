package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// client is a small typed client of the hub API (apps/hub/src/api).
type client struct {
	base  string
	token string
	hc    *http.Client
}

func newClient() (*client, error) {
	hub, tok, err := hubSettings()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(hub, "http://") && !strings.HasPrefix(hub, "https://") {
		return nil, failf("hub URL %q must start with https:// (or http://localhost for a demo hub)", hub)
	}
	return &client{base: hub, token: tok, hc: &http.Client{Timeout: 60 * time.Second}}, nil
}

// atQuery returns ?at=<--at> for read commands.
func atQuery() url.Values {
	q := url.Values{}
	if g.At != "" {
		q.Set("at", g.At)
	}
	return q
}

func (c *client) do(method, path string, q url.Values, body any) ([]byte, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, failf("invalid hub URL %q: %v", c.base, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vpnmap/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, networkError(c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, networkError(c.base, err)
	}
	if resp.StatusCode >= 400 {
		return nil, hubError(resp.StatusCode, raw, method, path)
	}
	if ct := resp.Header.Get("Content-Type"); len(raw) > 0 && !strings.Contains(ct, "json") {
		return nil, failf("%s %s answered with %s, not JSON: is %s really the hub (not the web UI)?", method, path, orDash(ct), c.base)
	}
	return raw, nil
}

func networkError(hub string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	hint := "check --hub / VPNMAP_HUB (demo: http://localhost:4000) or run `vpnmap login --hub <url>`"
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return failf("the hub at %s did not answer in time (%v)\nhint: %s", hub, err, hint)
	}
	return failf("cannot reach the hub at %s: %v\nhint: %s", hub, err, hint)
}

// hubError turns a hub error body ({statusCode, message, error}) into a clean message.
func hubError(code int, raw []byte, method, path string) error {
	var body struct {
		Message any    `json:"message"`
		Error   string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil {
		switch m := body.Message.(type) {
		case string:
			msg = m
		case []any:
			var parts []string
			for _, p := range m {
				parts = append(parts, fmt.Sprint(p))
			}
			msg = strings.Join(parts, "; ")
		}
		if msg == "" {
			msg = body.Error
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
	}
	msg = scrub(msg)
	switch code {
	case http.StatusUnauthorized:
		return failf("the hub refused the request (401): %s\nhint: run `vpnmap login --hub <url>` to save a personal token, or set VPNMAP_TOKEN", msg)
	case http.StatusForbidden:
		return failf("the hub refused the request (403): %s", msg)
	case http.StatusNotFound:
		return failf("%s", msg)
	}
	if code == http.StatusBadRequest {
		return failf("the hub rejected the request: %s", msg)
	}
	return failf("hub error %d on %s %s: %s", code, method, path, msg)
}

func (c *client) getRaw(path string, q url.Values) ([]byte, error) {
	return c.do(http.MethodGet, path, q, nil)
}

func (c *client) get(path string, q url.Values, out any) ([]byte, error) {
	raw, err := c.getRaw(path, q)
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, failf("unexpected answer from %s%s: %v (is the hub the same version as this CLI?)", c.base, path, err)
		}
	}
	return raw, nil
}

func (c *client) post(path string, body, out any) ([]byte, error) {
	raw, err := c.do(http.MethodPost, path, nil, body)
	if err != nil {
		return nil, err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, failf("unexpected answer from %s%s: %v", c.base, path, err)
		}
	}
	return raw, nil
}

// emitRaw prints an API answer as indented JSON with secret-looking strings masked.
func emitRaw(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		_, err = os.Stdout.Write(raw)
		return err
	}
	return printJSON(scrubJSON(v))
}

// readCall fetches path and decodes it into out; with --json it prints the
// raw answer and returns done = true.
func readCall(c *client, path string, q url.Values, out any) (done bool, err error) {
	raw, err := c.get(path, q, out)
	if err != nil {
		return false, err
	}
	if g.JSON {
		return true, emitRaw(raw)
	}
	return false, nil
}
