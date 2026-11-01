package collect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"vpnmap/pkg/protocol"
)

// DialTCP does one TCP connect to target (host:port) and closes it at once.
// No byte is sent: this only proves the port accepts connections.
func DialTCP(ctx context.Context, target string, timeout time.Duration) protocol.TCPResult {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	r := protocol.TCPResult{Target: target, At: Now()}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", target)
	r.RTTMs = ms(time.Since(start))
	if err != nil {
		r.Error = ClassifyNetError(err)
		return r
	}
	c.Close()
	r.OK = true
	return r
}

// ClassifyNetError maps a dial/HTTP error to a short, stable token the board
// can colour by: refused, timeout, unreachable, dns, reset, cert_expired,
// cert_unknown_authority, cert_hostname, tls, or a shortened raw message.
func ClassifyNetError(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	var certInvalid x509.CertificateInvalidError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var tlsRecord tls.RecordHeaderError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "unreachable"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "reset"
	case errors.As(err, &certInvalid):
		if certInvalid.Reason == x509.Expired {
			return "cert_expired"
		}
		return "cert_invalid"
	case errors.As(err, &unknownAuth):
		return "cert_unknown_authority"
	case errors.As(err, &hostErr):
		return "cert_hostname"
	case errors.As(err, &tlsRecord):
		return "tls"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	}
	return shortError(err)
}

// shortError keeps the most specific part of a wrapped error message.
func shortError(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func ms(d time.Duration) float64 { return round2(float64(d.Microseconds()) / 1000) }

// HTTPChecker performs read-only GET requests from this server.
type HTTPChecker struct {
	UserAgent string
	// RootCAs overrides the system roots (tests only).
	RootCAs *x509.CertPool

	client *http.Client
}

const (
	maxRedirects = 3
	maxBody      = 64 << 10
)

func (c *HTTPChecker) httpClient() *http.Client {
	if c.client == nil {
		tr := &http.Transport{
			Proxy:               nil, // measure the direct path from this server
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			TLSClientConfig:     &tls.Config{RootCAs: c.RootCAs},
			DisableKeepAlives:   true, // every check is a fresh connection
			ForceAttemptHTTP2:   true,
		}
		c.client = &http.Client{
			Transport: tr,
			Jar:       nil, // never store or send cookies
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		}
	}
	return c.client
}

// Check GETs url and reports status, latency (until headers) and the days
// left on the leaf certificate for https.
func (c *HTTPChecker) Check(ctx context.Context, id, url string, timeout time.Duration) protocol.HTTPResult {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	r := protocol.HTTPResult{ID: id, URL: url, At: Now()}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		r.Error = "bad url"
		return r
	}
	req.Header.Set("User-Agent", c.UserAgent)
	start := time.Now()
	resp, err := c.httpClient().Do(req)
	r.LatencyMs = ms(time.Since(start))
	if err != nil {
		r.Error = ClassifyNetError(err)
		if strings.Contains(err.Error(), "redirects") {
			r.Error = "too_many_redirects"
		}
		return r
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	r.Status = resp.StatusCode
	r.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !r.OK {
		r.Error = fmt.Sprintf("status %d", resp.StatusCode)
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		left := time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24
		r.CertDays = round2(left)
	}
	return r
}
