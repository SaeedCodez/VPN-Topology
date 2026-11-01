package collect

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpnmap/pkg/agentcfg"
)

func TestHTTPCheck(t *testing.T) {
	var sawCookie bool
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			sawCookie = true
		}
		if r.Header.Get("User-Agent") != "vpnmap-agent/test" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(strings.Repeat("x", 200000))) // larger than the read cap
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	mux.HandleFunc("/setcookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	n := 0
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Redirect(w, r, fmt.Sprintf("/loop?%d", n), http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &HTTPChecker{UserAgent: "vpnmap-agent/test"}
	r := c.Check(context.Background(), "panel", srv.URL+"/ok", time.Second)
	if !r.OK || r.Status != 200 || r.Error != "" || r.CertDays != 0 || r.ID != "panel" {
		t.Errorf("ok: %+v", r)
	}
	r = c.Check(context.Background(), "panel", srv.URL+"/fail", time.Second)
	if r.OK || r.Status != 503 || r.Error != "status 503" {
		t.Errorf("fail: %+v", r)
	}
	r = c.Check(context.Background(), "panel", srv.URL+"/setcookie", time.Second)
	if !r.OK || sawCookie {
		t.Errorf("cookie handling: %+v sawCookie=%v", r, sawCookie)
	}
	r = c.Check(context.Background(), "panel", srv.URL+"/loop", time.Second)
	if r.OK || r.Error != "too_many_redirects" || n > maxRedirects+1 {
		t.Errorf("loop: %+v after %d hops", r, n)
	}
}

func TestHTTPCheckTLSCertDays(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	c := &HTTPChecker{UserAgent: "t", RootCAs: pool}
	r := c.Check(context.Background(), "tls", srv.URL, time.Second)
	want := time.Until(srv.Certificate().NotAfter).Hours() / 24
	if !r.OK || r.CertDays <= 0 || r.CertDays-want > 0.01 || want-r.CertDays > 0.01 {
		t.Errorf("%+v (want ~%.2f days)", r, want)
	}
	// Without the test CA the certificate is rejected.
	r = (&HTTPChecker{UserAgent: "t"}).Check(context.Background(), "tls", srv.URL, time.Second)
	if r.OK || !strings.HasPrefix(r.Error, "cert_") {
		t.Errorf("untrusted: %+v", r)
	}
}

func TestPromScrape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "metrics" || p != "s3cret-pass" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.invalid/steal", http.StatusFound)
			return
		}
		fmt.Fprint(w, "# HELP x\nremnawave_users_online 137\nsecret_metric 1\nremnawave_users_total{status=\"active\"} 1200\n")
	}))
	defer srv.Close()
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth")
	os.WriteFile(auth, []byte("metrics:s3cret-pass\n"), 0o640)

	p := &PromScraper{UserAgent: "t"}
	target := agentcfg.PromTarget{ID: "panel", URL: srv.URL + "/metrics", AuthFile: auth, Metrics: []string{"remnawave_users_online", "remnawave_users_total"}}
	got, err := p.Scrape(context.Background(), target, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Target != "panel" || got[0].Value != 137 || got[1].Labels["status"] != "active" {
		t.Errorf("%+v", got)
	}

	// Redirects are not followed (basic auth must not travel).
	target.URL = srv.URL + "/redirect"
	if _, err := p.Scrape(context.Background(), target, time.Second); err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Errorf("redirect: %v", err)
	}
	// Wrong credentials: error mentions the status, never the password.
	os.WriteFile(auth, []byte("metrics:wrong-pass"), 0o640)
	target.URL = srv.URL + "/metrics"
	_, err = p.Scrape(context.Background(), target, time.Second)
	if err == nil || strings.Contains(err.Error(), "wrong-pass") {
		t.Errorf("bad auth: %v", err)
	}
	// Malformed auth file: content never echoed.
	os.WriteFile(auth, []byte("no-colon-secret"), 0o640)
	if _, err = p.Scrape(context.Background(), target, time.Second); err == nil || strings.Contains(err.Error(), "no-colon-secret") {
		t.Errorf("malformed auth: %v", err)
	}
	// Non-local targets are refused even if the config validator let them in.
	for _, u := range []string{"http://127.0.0.1@example.invalid/metrics", "http://127.0.0.1.example.invalid/metrics"} {
		target.URL = u
		if _, err := p.Scrape(context.Background(), target, time.Second); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
}
