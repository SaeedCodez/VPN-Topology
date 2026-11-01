package probe

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"vpnmap/apps/agent/internal/collect"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

const maxRestartBackoff = 60 * time.Second

// Manager runs one RouteRunner per configured route.
type Manager struct {
	Cfg       *agentcfg.ProbeConfig
	UserAgent string
	Report    func(protocol.ProbeResult)
	Log       *slog.Logger
	// RuntimeDir holds generated configs (0700). Empty: $RUNTIME_DIRECTORY
	// (systemd RuntimeDirectory=) or a private temp dir.
	RuntimeDir string
	// Env overrides the child environment (tests).
	Env []string
}

// Run blocks until ctx is done, then stops every sing-box and removes the
// generated configs.
func (m *Manager) Run(ctx context.Context) error {
	if err := Validate(m.Cfg); err != nil {
		return err
	}
	dir, cleanup, err := runtimeDir(m.RuntimeDir)
	if err != nil {
		return err
	}
	defer cleanup()
	s := Effective(m.Cfg)
	var wg sync.WaitGroup
	for _, rt := range m.Cfg.Routes {
		r := &RouteRunner{
			Route: rt, Settings: s, RuntimeDir: dir, Report: m.Report, Log: m.Log, Env: m.Env,
			Checker: &Checker{URL: s.URL, TraceURL: s.TraceURL, UserAgent: m.UserAgent},
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(ctx)
		}()
	}
	wg.Wait()
	return nil
}

// runtimeDir returns a 0700 directory for generated configs.
func runtimeDir(dir string) (string, func(), error) {
	if dir == "" {
		dir = os.Getenv("RUNTIME_DIRECTORY")
	}
	if dir == "" {
		d, err := os.MkdirTemp("", "vpnmap-probe-")
		if err != nil {
			return "", nil, err
		}
		return d, func() { os.RemoveAll(d) }, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", nil, err
	}
	return dir, func() {}, nil
}

// RouteRunner supervises the sing-box of one route and probes through it.
type RouteRunner struct {
	Route      agentcfg.ProbeRoute
	Settings   Settings
	RuntimeDir string
	Checker    *Checker
	Report     func(protocol.ProbeResult)
	Log        *slog.Logger
	Env        []string
}

// Run loops until ctx is done.
func (r *RouteRunner) Run(ctx context.Context) {
	cfgPath := filepath.Join(r.RuntimeDir, r.Route.Route+".json")
	defer removeQuietly(cfgPath)
	var proc *Process
	defer func() {
		if proc != nil {
			proc.Stop()
		}
	}()
	backoff := time.Second
	wasDown := false
	for {
		if proc == nil || !proc.Running() {
			if proc != nil {
				if !wasDown {
					r.logWarn("sing-box exited, restarting", "last_output", proc.LastOutput())
				}
				wasDown = true
				proc = nil
			}
			p, err := r.start(ctx, cfgPath)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if !wasDown {
					r.logWarn("sing-box failed to start", "error", err.Error())
				}
				wasDown = true
				r.report(protocol.ProbeResult{Route: r.Route.Route, Error: "sing-box: " + err.Error(), At: collect.Now()})
				if !sleep(ctx, backoff) {
					return
				}
				backoff = min(backoff*2, maxRestartBackoff)
				continue
			}
			proc = p
		}
		proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(proc.Port)}
		res := r.Checker.Probe(ctx, proxy, r.Route.Route, r.Route.ExpectCountry)
		if ctx.Err() != nil {
			return
		}
		if res.OK {
			backoff = time.Second
			if wasDown {
				r.logInfo("probe route recovered")
			}
			wasDown = false
		}
		r.report(res)
		if !sleep(ctx, r.Settings.Interval) {
			return
		}
	}
}

// start writes a fresh config (re-reading the outbound file, so edits are
// picked up on restart) and launches sing-box on a free loopback port.
func (r *RouteRunner) start(ctx context.Context, cfgPath string) (*Process, error) {
	outbound, err := os.ReadFile(r.Route.OutboundFile)
	if err != nil {
		return nil, fmt.Errorf("outbound file: %w", err)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfg, err := BuildConfig(outbound, port)
	if err != nil {
		return nil, err
	}
	if err := writePrivate(cfgPath, cfg); err != nil {
		return nil, err
	}
	p := &Process{Bin: r.Settings.SingBox, ConfigPath: cfgPath, WorkDir: r.RuntimeDir, Port: port, Env: r.Env}
	if err := p.Start(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

// writePrivate writes a 0600 file atomically.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cfg-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (r *RouteRunner) report(res protocol.ProbeResult) {
	if r.Report != nil {
		r.Report(res)
	}
}

func (r *RouteRunner) logWarn(msg string, args ...any) {
	if r.Log != nil {
		r.Log.Warn(msg, append([]any{"route", r.Route.Route}, args...)...)
	}
}

func (r *RouteRunner) logInfo(msg string, args ...any) {
	if r.Log != nil {
		r.Log.Info(msg, append([]any{"route", r.Route.Route}, args...)...)
	}
}

// sleep waits d or until ctx is done; it reports whether to continue.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Once probes every route a single time (`run --once --print`): start
// sing-box, probe, stop, delete the generated config.
func (m *Manager) Once(ctx context.Context) error {
	if err := Validate(m.Cfg); err != nil {
		return err
	}
	dir, cleanup, err := runtimeDir(m.RuntimeDir)
	if err != nil {
		return err
	}
	defer cleanup()
	s := Effective(m.Cfg)
	var wg sync.WaitGroup
	for _, rt := range m.Cfg.Routes {
		r := &RouteRunner{
			Route: rt, Settings: s, RuntimeDir: dir, Report: m.Report, Log: m.Log, Env: m.Env,
			Checker: &Checker{URL: s.URL, TraceURL: s.TraceURL, UserAgent: m.UserAgent},
		}
		wg.Add(1)
		go func() { defer wg.Done(); r.Once(ctx) }()
	}
	wg.Wait()
	return nil
}

// Once runs a single probe of this route with a temporary sing-box.
func (r *RouteRunner) Once(ctx context.Context) {
	cfgPath := filepath.Join(r.RuntimeDir, r.Route.Route+".json")
	defer removeQuietly(cfgPath)
	p, err := r.start(ctx, cfgPath)
	if err != nil {
		r.report(protocol.ProbeResult{Route: r.Route.Route, Error: "sing-box: " + err.Error(), At: collect.Now()})
		return
	}
	defer p.Stop()
	proxy := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(p.Port)}
	r.report(r.Checker.Probe(ctx, proxy, r.Route.Route, r.Route.ExpectCountry))
}
