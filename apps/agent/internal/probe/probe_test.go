package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

// A fake outbound with obviously fake credentials (never a real server).
const outboundJSON = `{"type":"vless","tag":"whatever","server":"vpn.example.test","server_port":443,
 "uuid":"00000000-1111-2222-3333-444444444444","flow":"xtls-rprx-vision",
 "tls":{"enabled":true,"server_name":"www.example.test","reality":{"enabled":true,"public_key":"FAKEPUBLICKEY","short_id":"abcd"}}}`

func TestBuildConfig(t *testing.T) {
	raw, err := BuildConfig([]byte(outboundJSON), 40123)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Type, Tag, Listen string
			ListenPort        int `json:"listen_port"`
		}
		Outbounds []map[string]any
		Route     struct{ Final string }
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	in := cfg.Inbounds
	if len(in) != 1 || in[0].Type != "mixed" || in[0].Listen != "127.0.0.1" || in[0].ListenPort != 40123 {
		t.Errorf("inbounds %+v", in)
	}
	if len(cfg.Outbounds) != 1 || cfg.Outbounds[0]["tag"] != OutboundTag || cfg.Outbounds[0]["type"] != "vless" ||
		cfg.Outbounds[0]["uuid"] != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("outbounds %+v", cfg.Outbounds)
	}
	if cfg.Route.Final != OutboundTag {
		t.Errorf("route final %q", cfg.Route.Final)
	}
	if _, err := BuildConfig([]byte(outboundJSON), 0); err == nil {
		t.Error("port 0 accepted")
	}
}

func TestExtractOutbound(t *testing.T) {
	full := `{"log":{},"outbounds":[{"type":"direct","tag":"direct"},{"type":"trojan","tag":"proxy","password":"pw"},{"type":"block"}]}`
	o, err := ExtractOutbound([]byte(full))
	if err != nil || o["type"] != "trojan" {
		t.Errorf("%v %v", o, err)
	}
	for _, bad := range []string{`{"outbounds":[{"type":"direct"}]}`, `{"server":"x"}`, `[1,2]`, `not json "password":"leak-me"`} {
		_, err := ExtractOutbound([]byte(bad))
		if err == nil {
			t.Errorf("%s accepted", bad)
		} else if strings.Contains(err.Error(), "leak-me") {
			t.Errorf("error quotes the file: %v", err)
		}
	}
}

func TestParseTrace(t *testing.T) {
	body := "fl=123abc\nh=www.cloudflare.com\nip=203.0.113.77\nts=1767225600.123\nvisit_scheme=https\nuag=vpnmap\ncolo=AMS\nsliver=none\nhttp=http/2\nloc=nl\ntls=TLSv1.3\nsni=plaintext\nwarp=off\ngateway=off\nrbi=off\nkex=X25519\n"
	ip, loc := ParseTrace([]byte(body))
	if ip != "203.0.113.77" || loc != "NL" {
		t.Errorf("ip=%q loc=%q", ip, loc)
	}
	if ip, loc := ParseTrace([]byte("<html>blocked</html>")); ip != "" || loc != "" {
		t.Errorf("garbage: %q %q", ip, loc)
	}
}

func TestValidateAndEffective(t *testing.T) {
	good := &agentcfg.ProbeConfig{SingBox: "/usr/local/bin/sing-box", Routes: []agentcfg.ProbeRoute{
		{Route: "nl-direct", OutboundFile: "/etc/vpnmap-agent/probe/nl.json", ExpectCountry: "NL"},
	}}
	if err := Validate(good); err != nil {
		t.Fatal(err)
	}
	bad := []*agentcfg.ProbeConfig{
		{Routes: nil},
		{SingBox: "sing-box", Routes: good.Routes},
		{Routes: []agentcfg.ProbeRoute{{Route: "../x", OutboundFile: "/a"}}},
		{Routes: []agentcfg.ProbeRoute{{Route: "a", OutboundFile: "rel.json"}}},
		{Routes: []agentcfg.ProbeRoute{{Route: "a", OutboundFile: "/a"}, {Route: "a", OutboundFile: "/b"}}},
		{Routes: []agentcfg.ProbeRoute{{Route: "a", OutboundFile: "/a", ExpectCountry: "Netherlands"}}},
		{URL: "ftp://x", Routes: good.Routes},
	}
	for i, b := range bad {
		if err := Validate(b); err == nil {
			t.Errorf("case %d accepted: %+v", i, b)
		}
	}
	s := Effective(&agentcfg.ProbeConfig{Interval: agentcfg.Duration(time.Second)})
	if s.URL != DefaultURL || s.TraceURL != DefaultTraceURL || s.Interval != MinInterval || s.SingBox != "sing-box" {
		t.Errorf("%+v", s)
	}
}

// fakeProxy is an HTTP proxy that answers the probe URLs itself, so tests
// never touch the network. It plays the role of sing-box's mixed inbound.
func fakeProxy(t *testing.T, status int, loc string) *url.URL {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/generate_204"):
			w.WriteHeader(status)
		case strings.HasSuffix(r.URL.Path, "/cdn-cgi/trace"):
			fmt.Fprintf(w, "ip=203.0.113.9\nloc=%s\n", loc)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return u
}

func testChecker() *Checker {
	return &Checker{URL: "http://cp.example.test/generate_204", TraceURL: "http://trace.example.test/cdn-cgi/trace", UserAgent: "t", Timeout: 2 * time.Second}
}

func TestProbe(t *testing.T) {
	c := testChecker()
	r := c.Probe(context.Background(), fakeProxy(t, 204, "nl"), "nl-direct", "NL")
	if !r.OK || r.ExitCountry != "NL" || r.ExitIP != "203.0.113.9" || r.Error != "" || r.LatencyMs <= 0 || r.Route != "nl-direct" {
		t.Errorf("%+v", r)
	}
	r = c.Probe(context.Background(), fakeProxy(t, 204, "DE"), "nl-direct", "NL")
	if r.OK || r.Error != "exit country DE, want NL" {
		t.Errorf("wrong country: %+v", r)
	}
	r = c.Probe(context.Background(), fakeProxy(t, 204, "DE"), "any", "")
	if !r.OK || r.ExitCountry != "DE" {
		t.Errorf("no expectation: %+v", r)
	}
	r = c.Probe(context.Background(), fakeProxy(t, 200, "NL"), "nl-direct", "NL")
	if r.OK || !strings.Contains(r.Error, "status 200 (want 204)") {
		t.Errorf("intercepted: %+v", r)
	}
	// Dead proxy (sing-box gone): refused.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := &url.URL{Scheme: "http", Host: ln.Addr().String()}
	ln.Close()
	r = c.Probe(context.Background(), dead, "nl-direct", "NL")
	if r.OK || r.Error != "probe: refused" {
		t.Errorf("dead proxy: %+v", r)
	}
}

// TestMain lets the test binary act as a fake sing-box: `<test> run -c cfg -D dir`
// with VPNMAP_FAKE_SINGBOX=1 starts an HTTP proxy on the config's listen port.
func TestMain(m *testing.M) {
	if os.Getenv("VPNMAP_FAKE_SINGBOX") == "1" {
		fakeSingBox()
		return
	}
	os.Exit(m.Run())
}

func fakeSingBox() {
	var cfgPath string
	for i, a := range os.Args {
		if a == "-c" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
		}
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FATAL cannot read config")
		os.Exit(1)
	}
	var cfg struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
		}
		Outbounds []map[string]any
	}
	if json.Unmarshal(raw, &cfg) != nil || len(cfg.Inbounds) != 1 || cfg.Outbounds[0]["tag"] != OutboundTag {
		fmt.Fprintln(os.Stderr, "FATAL bad config")
		os.Exit(1)
	}
	served := 0
	crashAfter := os.Getenv("VPNMAP_FAKE_CRASH_AFTER")
	var mu sync.Mutex
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/generate_204") {
			w.WriteHeader(204)
		} else {
			fmt.Fprint(w, "ip=203.0.113.9\nloc=NL\n")
		}
		mu.Lock()
		served++
		if crashAfter != "" && fmt.Sprint(served) == crashAfter {
			go func() { time.Sleep(50 * time.Millisecond); os.Exit(3) }()
		}
		mu.Unlock()
	})
	http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", cfg.Inbounds[0].ListenPort), h)
}

type collector struct {
	mu  sync.Mutex
	res []protocol.ProbeResult
}

func (c *collector) add(r protocol.ProbeResult) { c.mu.Lock(); c.res = append(c.res, r); c.mu.Unlock() }
func (c *collector) snapshot() []protocol.ProbeResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]protocol.ProbeResult(nil), c.res...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runRoute(t *testing.T, env []string) (*collector, string, context.CancelFunc, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	outbound := filepath.Join(dir, "nl.json")
	os.WriteFile(outbound, []byte(outboundJSON), 0o600)
	runDir := filepath.Join(dir, "run")
	os.Mkdir(runDir, 0o700)
	self, _ := os.Executable()
	col := &collector{}
	r := &RouteRunner{
		Route:      agentcfg.ProbeRoute{Route: "nl-direct", OutboundFile: outbound, ExpectCountry: "NL"},
		Settings:   Settings{SingBox: self, Interval: 50 * time.Millisecond},
		RuntimeDir: runDir, Checker: testChecker(), Report: col.add, Env: env,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	return col, runDir, cancel, done
}

func TestRouteRunnerEndToEnd(t *testing.T) {
	col, runDir, cancel, done := runRoute(t, []string{"VPNMAP_FAKE_SINGBOX=1"})
	waitFor(t, func() bool { return len(col.snapshot()) >= 3 })
	for _, r := range col.snapshot()[:3] {
		if !r.OK || r.ExitCountry != "NL" {
			t.Errorf("%+v", r)
		}
	}
	// The generated config is private while running...
	fi, err := os.Stat(filepath.Join(runDir, "nl-direct.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config file: %v %v", fi, err)
	}
	cancel()
	<-done
	// ...and removed on exit.
	if _, err := os.Stat(filepath.Join(runDir, "nl-direct.json")); !os.IsNotExist(err) {
		t.Errorf("config not removed: %v", err)
	}
}

func TestRouteRunnerRestartsCrashedSingBox(t *testing.T) {
	col, _, cancel, done := runRoute(t, []string{"VPNMAP_FAKE_SINGBOX=1", "VPNMAP_FAKE_CRASH_AFTER=2"})
	defer func() { cancel(); <-done }()
	// The fake exits after two requests (one probe); the runner must notice,
	// restart it and keep producing OK results.
	waitFor(t, func() bool {
		ok := 0
		for _, r := range col.snapshot() {
			if r.OK {
				ok++
			}
		}
		return ok >= 3
	})
}

func TestRouteRunnerReportsStartFailure(t *testing.T) {
	// Without the marker env the test binary just runs tests, never opening
	// the port: start fails and an error result is reported.
	dir := t.TempDir()
	col := &collector{}
	r := &RouteRunner{
		Route:      agentcfg.ProbeRoute{Route: "broken", OutboundFile: filepath.Join(dir, "missing.json")},
		Settings:   Settings{SingBox: "/bin/true", Interval: 50 * time.Millisecond},
		RuntimeDir: dir, Checker: testChecker(), Report: col.add,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	waitFor(t, func() bool { return len(col.snapshot()) >= 1 })
	cancel()
	<-done
	res := col.snapshot()[0]
	if res.OK || !strings.HasPrefix(res.Error, "sing-box: outbound file") {
		t.Errorf("%+v", res)
	}
}

func TestManagerOnce(t *testing.T) {
	dir := t.TempDir()
	outbound := filepath.Join(dir, "nl.json")
	os.WriteFile(outbound, []byte(outboundJSON), 0o600)
	self, _ := os.Executable()
	col := &collector{}
	runDir := filepath.Join(dir, "run")
	m := &Manager{
		Cfg: &agentcfg.ProbeConfig{SingBox: self, URL: "http://cp.example.test/generate_204", TraceURL: "http://trace.example.test/cdn-cgi/trace",
			Routes: []agentcfg.ProbeRoute{{Route: "nl-direct", OutboundFile: outbound, ExpectCountry: "NL"}}},
		Report: col.add, RuntimeDir: runDir, Env: []string{"VPNMAP_FAKE_SINGBOX=1"},
	}
	if err := m.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := col.snapshot()
	if len(res) != 1 || !res[0].OK {
		t.Errorf("%+v", res)
	}
	entries, _ := os.ReadDir(runDir)
	if len(entries) != 0 {
		t.Errorf("runtime dir not cleaned: %v", entries)
	}
	if fi, _ := os.Stat(runDir); fi.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir mode %v", fi.Mode().Perm())
	}
}
