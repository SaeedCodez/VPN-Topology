package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"vpnmap/apps/agent/internal/collect"
	"vpnmap/apps/agent/internal/helper"
	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/apps/agent/internal/push"
	"vpnmap/apps/agent/internal/tail"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

type fakeHelper struct {
	mu        sync.Mutex
	responses map[string]any
	panicOn   string
}

func (f *fakeHelper) Call(_ context.Context, op string, args []string, out any) error {
	key := strings.TrimSpace(op + " " + strings.Join(args, " "))
	if key == f.panicOn {
		panic("boom")
	}
	f.mu.Lock()
	v, ok := f.responses[key]
	f.mu.Unlock()
	if !ok {
		return helper.ErrUnavailable
	}
	if err, isErr := v.(error); isErr {
		return err
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}

type fakePusher struct {
	mu     sync.Mutex
	pushes []*protocol.Push
	fail   int // fail the first n pushes with HTTP 500
	onPush func(n int)
}

func (f *fakePusher) Push(_ context.Context, p *protocol.Push) error {
	f.mu.Lock()
	f.pushes = append(f.pushes, p)
	n := len(f.pushes)
	cb := f.onPush
	f.mu.Unlock()
	if cb != nil {
		cb(n)
	}
	if n <= f.fail {
		return &push.StatusError{Code: 500}
	}
	return nil
}

func (f *fakePusher) all() []*protocol.Push {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*protocol.Push(nil), f.pushes...)
}

func baseConfig(t *testing.T) (*agentcfg.Config, string) {
	dir := t.TempDir()
	checks := filepath.Join(dir, "checks.d")
	os.Mkdir(checks, 0o755)
	os.WriteFile(filepath.Join(checks, "xbridger-doctor.check"), []byte("#!/bin/sh\n"), 0o755)
	logPath := filepath.Join(dir, "app.log")
	os.WriteFile(logPath, []byte("old line\n"), 0o644)
	return &agentcfg.Config{
		Hub: "https://map.example.com", Node: "relay-1", TokenFile: "/x", Interval: agentcfg.Duration(20 * time.Millisecond),
		Units:      []string{"xray"},
		Containers: []string{"backend-app-1"},
		Nft:        &agentcfg.NftConfig{Counters: []agentcfg.NftCounter{{Name: "relay_in", Hook: "input"}}},
		WG:         []agentcfg.WGConfig{{Iface: "wg0"}, {Netns: "mullvad", Iface: "wg-mv"}},
		SS:         []agentcfg.SSConfig{{ID: "in-2053", Port: 2053, Side: "local"}},
		Logs:       []agentcfg.LogConfig{{ID: "app", Path: logPath}},
		ChecksDir:  checks,
		Checks:     []agentcfg.LocalCheck{{ID: "xbridger-doctor"}, {ID: "relay-forwards"}},
	}, logPath
}

func systemctlFake() *sysexec.Fake {
	return &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"systemctl show --no-pager --property=Id,LoadState,ActiveState,SubState,ActiveEnterTimestampMonotonic,ActiveEnterTimestamp,NRestarts -- xray": {
			Stdout: "Id=xray.service\nLoadState=loaded\nActiveState=active\nSubState=running\nActiveEnterTimestampMonotonic=1000000\nNRestarts=0\n"},
	}}
}

func newTestAgent(t *testing.T, cfg *agentcfg.Config, h Helper, p Pusher, log *slog.Logger) *Agent {
	t.Helper()
	a, err := New(cfg, "abc123def456", Deps{
		FS: procfs.FS{Root: "../procfs/testdata/proc"}, Runner: systemctlFake(), Helper: h, Pusher: p,
		State: tail.LoadState(filepath.Join(t.TempDir(), "state.json"), false), Log: log,
		Hostname: "relay-1.example", Version: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	a.host.FirstSample = time.Millisecond
	return a
}

func TestOnceAssemblesPush(t *testing.T) {
	cfg, _ := baseConfig(t)
	h := &fakeHelper{responses: map[string]any{
		"nft-counters":          []protocol.NftCounter{{Name: "relay_in", Bytes: 10, Packets: 1}},
		"wg-dump  wg0":          protocol.WGInterface{Iface: "wg0", Peers: []protocol.WGPeer{{Key: "abcdefgh"}}},
		"docker-ps":             []protocol.Container{{Name: "backend-app-1", State: "running"}},
		"check xbridger-doctor": protocol.CheckResult{ID: "xbridger-doctor", Status: "ok"},
		// wg-mv and relay-forwards are missing: helper "not available" for them.
	}}
	a := newTestAgent(t, cfg, h, nil, nil)
	p := a.Once(context.Background())

	if p.V != protocol.Version || p.Seq != 1 || p.TS == "" {
		t.Errorf("header %+v", p)
	}
	ag := p.Agent
	if ag.Node != "relay-1" || ag.Kind != "agent" || ag.Version != "1.0.0" || ag.Hostname != "relay-1.example" || ag.ConfigSHA != "abc123def456" {
		t.Errorf("agent %+v", ag)
	}
	c := ag.Capabilities
	if !reflect.DeepEqual(c.WG, []string{"wg0", "mullvad/wg-mv"}) || !reflect.DeepEqual(c.NftCounters, []string{"relay_in"}) ||
		!reflect.DeepEqual(c.Checks, []string{"xbridger-doctor"}) || !reflect.DeepEqual(c.Logs, []string{"app"}) {
		t.Errorf("capabilities %+v", c)
	}
	if !reflect.DeepEqual(c.ListeningPorts, []string{"tcp/22", "tcp/443", "tcp/2053", "tcp/9090", "udp/51820", "udp/51821"}) {
		t.Errorf("listening %v", c.ListeningPorts)
	}
	if p.Host == nil || p.Host.NumCPU != 2 || len(p.Units) != 1 || p.Units[0].ActiveState != "active" {
		t.Errorf("host/units %+v %+v", p.Host, p.Units)
	}
	if len(p.Nft) != 1 || len(p.WG) != 1 || len(p.Containers) != 1 || len(p.Checks) != 1 || len(p.SS) != 1 {
		t.Errorf("nft=%d wg=%d containers=%d checks=%d ss=%d", len(p.Nft), len(p.WG), len(p.Containers), len(p.Checks), len(p.SS))
	}
	errs := map[string]string{}
	for _, e := range p.Errors {
		errs[e.Collector] = e.Error
	}
	if errs["wg:mullvad/wg-mv"] != "helper not available" || errs["check:relay-forwards"] != "helper not available" || len(errs) != 2 {
		t.Errorf("errors %+v", p.Errors)
	}
	if len(p.Logs) != 0 {
		t.Errorf("first run must not backfill logs: %+v", p.Logs)
	}
	// The payload is valid JSON (no NaN etc.).
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorPanicIsContained(t *testing.T) {
	cfg, _ := baseConfig(t)
	h := &fakeHelper{responses: map[string]any{}, panicOn: "nft-counters"}
	p := newTestAgent(t, cfg, h, nil, nil).Once(context.Background())
	found := false
	for _, e := range p.Errors {
		if e.Collector == "nft" && strings.Contains(e.Error, "panicked") {
			found = true
		}
	}
	if !found || p.Host == nil {
		t.Errorf("panic not contained: %+v", p.Errors)
	}
}

func TestProbeKind(t *testing.T) {
	cfg := &agentcfg.Config{Hub: "https://h.example", Node: "probe", TokenFile: "/x"}
	a, err := New(cfg, "sha", Deps{FS: procfs.FS{Root: "../procfs/testdata/proc"}})
	if err != nil {
		t.Fatal(err)
	}
	a.host.FirstSample = time.Millisecond
	if p := a.Once(context.Background()); p.Agent.Kind != "probe" {
		t.Errorf("kind %q", p.Agent.Kind)
	}
}

func TestRunBackoffLogsOnceAndDeliversLogs(t *testing.T) {
	cfg, logPath := baseConfig(t)
	var logBuf bytes.Buffer
	var logMu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(b []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.Write(b)
	}), nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pusher := &fakePusher{fail: 4}
	pusher.onPush = func(n int) {
		if n == 2 { // new log lines while the hub is failing
			f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("ERROR token=zzSECRETzz upstream down\n")
			f.Close()
		}
		if n == 7 {
			cancel()
		}
	}
	a := newTestAgent(t, cfg, &fakeHelper{responses: map[string]any{}}, pusher, log)
	done := make(chan error)
	go func() { done <- a.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop")
	}

	pushes := pusher.all()
	if len(pushes) != 7 {
		t.Fatalf("pushes = %d", len(pushes))
	}
	for i, p := range pushes {
		if p.Seq != uint64(i+1) {
			t.Errorf("push %d seq %d", i, p.Seq)
		}
	}
	// The line written during the outage is retried until a push succeeds
	// (push 5), then never sent again. It is redacted.
	var carried []int
	for i, p := range pushes {
		for _, l := range p.Logs {
			if len(l.Lines) > 0 {
				carried = append(carried, i+1)
				if l.Lines[0] != "ERROR token=[REDACTED] upstream down" {
					t.Errorf("line %q", l.Lines[0])
				}
			}
		}
	}
	if !reflect.DeepEqual(carried, []int{3, 4, 5}) {
		t.Errorf("log line carried by pushes %v, want [3 4 5]", carried)
	}
	logMu.Lock()
	out := logBuf.String()
	logMu.Unlock()
	if n := strings.Count(out, "push failed"); n != 1 {
		t.Errorf("failure logged %d times:\n%s", n, out)
	}
	if strings.Contains(out, "still failing") {
		t.Errorf("same error logged again:\n%s", out)
	}
	if n := strings.Count(out, "push recovered"); n != 1 {
		t.Errorf("recovery logged %d times:\n%s", n, out)
	}
	if strings.Contains(out, "zzSECRETzz") {
		t.Error("secret in agent log")
	}
	// Backoff timing itself is covered by push.TestBackoff (TS has only
	// second resolution).
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

func TestStoreLogQueue(t *testing.T) {
	s := NewStore()
	s.AddLogs([]protocol.LogLines{{ID: "a", Lines: []string{"1", "2"}}})
	p := &protocol.Push{}
	tok := s.FillLogs(p)
	// More lines arrive while the push is in flight.
	s.AddLogs([]protocol.LogLines{{ID: "a", Lines: []string{"3"}}})
	s.CommitLogs(tok)
	p2 := &protocol.Push{}
	s.FillLogs(p2)
	if len(p2.Logs) != 1 || !reflect.DeepEqual(p2.Logs[0].Lines, []string{"3"}) {
		t.Errorf("after commit %+v", p2.Logs)
	}

	// Overflow while failing: keep the newest, mark truncated.
	s = NewStore()
	var many []string
	for i := 0; i < 450; i++ {
		many = append(many, "x")
	}
	s.AddLogs([]protocol.LogLines{{ID: "b", Lines: many}})
	p = &protocol.Push{}
	tok = s.FillLogs(p)
	if len(p.Logs[0].Lines) != maxQueuedLines || !p.Logs[0].Truncated {
		t.Errorf("len=%d truncated=%v", len(p.Logs[0].Lines), p.Logs[0].Truncated)
	}
	// Trimmed again before the commit: the commit must not drop new lines.
	s.AddLogs([]protocol.LogLines{{ID: "b", Lines: []string{"new"}}})
	s.CommitLogs(tok)
	p = &protocol.Push{}
	s.FillLogs(p)
	if len(p.Logs) != 1 || !reflect.DeepEqual(p.Logs[0].Lines, []string{"new"}) {
		t.Errorf("after trim+commit %+v", p.Logs)
	}
}

func TestStoreFillOrderAndErrors(t *testing.T) {
	s := NewStore()
	s.Set("b", func(p *protocol.Push) { p.TCP = append(p.TCP, protocol.TCPResult{Target: "b"}) }, nil)
	s.Set("a", func(p *protocol.Push) { p.TCP = append(p.TCP, protocol.TCPResult{Target: "a"}) }, errors.New("partial"))
	s.Set("b", func(p *protocol.Push) { p.TCP = append(p.TCP, protocol.TCPResult{Target: "b2"}) }, nil) // replaces
	p := &protocol.Push{}
	s.Fill(p)
	if len(p.TCP) != 2 || p.TCP[0].Target != "b2" || p.TCP[1].Target != "a" {
		t.Errorf("tcp %+v", p.TCP)
	}
	if len(p.Errors) != 1 || p.Errors[0] != (protocol.CollectorError{Collector: "a", Error: "partial"}) {
		t.Errorf("errors %+v", p.Errors)
	}
	s.Set("long", nil, errors.Join(errors.New("one"), errors.New(strings.Repeat("x", 500))))
	p = &protocol.Push{}
	s.Fill(p)
	last := p.Errors[len(p.Errors)-1].Error
	if strings.Contains(last, "\n") || len(last) > maxErrorLen+3 || !strings.HasPrefix(last, "one; ") {
		t.Errorf("error formatting %q", last)
	}
}

func TestHTTPPathFileStaysLocal(t *testing.T) {
	const secret = "s3cr3t-short-uuid"
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Path }))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &collect.HTTPChecker{UserAgent: "vpnmap-agent/test"}
	h := agentcfg.HTTPTarget{ID: "sub", URL: srv.URL + "/api/sub/", PathFile: file}

	r, err := checkHTTP(context.Background(), c, h, time.Second)
	if err != nil || !r.OK {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if got != "/api/sub/"+secret {
		t.Errorf("requested %q", got)
	}
	if r.URL != h.URL {
		t.Errorf("result carries %q; the pushed URL must stay the configured one", r.URL)
	}

	// A failure must not carry it either.
	h.URL = "http://127.0.0.1:1/api/sub/"
	r, err = checkHTTP(context.Background(), c, h, time.Second)
	if err != nil || r.OK || strings.Contains(fmt.Sprintf("%+v", r), secret) {
		t.Errorf("failed check: r=%+v err=%v", r, err)
	}

	// No file: no result, so the hub shows no data instead of a false outage.
	h.PathFile = filepath.Join(t.TempDir(), "missing")
	if _, err := checkHTTP(context.Background(), c, h, time.Second); err == nil {
		t.Error("missing file: want an error")
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.PathFile = file
	if _, err := checkHTTP(context.Background(), c, h, time.Second); err == nil {
		t.Error("empty file: want an error")
	}
}
