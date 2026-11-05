// Package agent assembles collectors into the push loop of `vpnmap-agent run`.
//
// Cheap collectors (host, sockets, units, logs, nft, wg) run right before
// every push; slower ones (docker, tcp, http, prom, checks) run on their own
// tickers and the push carries their latest result. A failing collector
// becomes a CollectorError in the push and never stops the agent; a panic
// in a collector is recovered the same way.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"vpnmap/apps/agent/internal/collect"
	"vpnmap/apps/agent/internal/probe"
	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/apps/agent/internal/push"
	"vpnmap/apps/agent/internal/tail"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

// MaxBackoff caps the wait between failed pushes.
const MaxBackoff = 60 * time.Second

// Helper is the privileged helper client (helper.Client in production).
type Helper interface {
	Call(ctx context.Context, op string, args []string, out any) error
}

// Pusher sends a push to the hub (push.Client in production).
type Pusher interface {
	Push(ctx context.Context, p *protocol.Push) error
}

// Deps are the agent's side effects, injectable for tests.
type Deps struct {
	FS       procfs.FS
	Runner   sysexec.Runner
	Helper   Helper
	Pusher   Pusher // nil in --once mode
	State    *tail.State
	Log      *slog.Logger
	Hostname string
	Version  string
	Now      func() time.Time
	// ProbeEnv overrides the sing-box environment (tests only).
	ProbeEnv []string
}

// Agent is one running agent.
type Agent struct {
	cfg   *agentcfg.Config
	sha   string
	d     Deps
	store *Store
	caps  protocol.Capabilities
	seq   uint64

	host    *collect.Host
	units   *collect.Units
	sockets *collect.Sockets
	logs    *collect.Logs
	httpc   *collect.HTTPChecker
	prom    *collect.PromScraper
}

// New builds an agent from a loaded config and its config_sha.
func New(cfg *agentcfg.Config, sha string, d Deps) (*Agent, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.State == nil {
		d.State = tail.LoadState("", true)
	}
	if err := probe.Validate(cfg.Probe); err != nil {
		return nil, err
	}
	ua := "vpnmap-agent/" + d.Version
	a := &Agent{
		cfg: cfg, sha: sha, d: d, store: NewStore(), caps: capabilities(cfg),
		host:    &collect.Host{FS: d.FS},
		sockets: &collect.Sockets{FS: d.FS, SS: cfg.SS},
		httpc:   &collect.HTTPChecker{UserAgent: ua},
		prom:    &collect.PromScraper{UserAgent: ua},
	}
	a.units = &collect.Units{Names: cfg.Units, Runner: d.Runner, BootTime: func() int64 {
		st, err := d.FS.Stat()
		if err != nil {
			return 0
		}
		return st.BootTime
	}}
	logs, err := collect.NewLogs(cfg.Logs, d.State)
	if err != nil {
		return nil, err
	}
	a.logs = logs
	return a, nil
}

func (a *Agent) kind() string {
	if a.cfg.Node == "probe" {
		return "probe"
	}
	return "agent"
}

// assemble builds the next push from the store.
func (a *Agent) assemble() (*protocol.Push, logToken) {
	a.seq++
	p := &protocol.Push{
		V:   protocol.Version,
		TS:  a.d.Now().UTC().Format(time.RFC3339),
		Seq: a.seq,
		Agent: protocol.AgentInfo{
			Node: a.cfg.Node, Kind: a.kind(), Version: a.d.Version,
			Hostname: a.d.Hostname, ConfigSHA: a.sha, Capabilities: a.caps,
		},
	}
	a.store.Fill(p)
	tok := a.store.FillLogs(p)
	return p, tok
}

// collectorErrors lets one task report several named errors (one per log).
type collectorErrors []protocol.CollectorError

func (c collectorErrors) Error() string {
	var s []string
	for _, e := range c {
		s = append(s, e.Collector+": "+e.Error)
	}
	return strings.Join(s, "; ")
}

// runTask executes one task with its timeout and records the outcome.
func (a *Agent) runTask(ctx context.Context, t task) {
	defer func() {
		if r := recover(); r != nil {
			a.d.Log.Error("collector panicked", "collector", t.name, "panic", fmt.Sprint(r))
			a.store.Set(t.name, nil, fmt.Errorf("collector panicked: %v", r))
		}
	}()
	timeout := t.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	apply, err := t.run(tctx)
	var ce collectorErrors
	if errors.As(err, &ce) {
		a.store.SetErrors(t.name, apply, ce)
		return
	}
	a.store.Set(t.name, apply, err)
}

// runAll runs tasks concurrently and waits for all of them.
func (a *Agent) runAll(ctx context.Context, ts []task) {
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.runTask(ctx, t)
		}()
	}
	wg.Wait()
}

// runPeriodic runs t now (staggered by offset) and then every t.interval.
func (a *Agent) runPeriodic(ctx context.Context, t task, offset time.Duration) {
	if !sleep(ctx, offset) {
		return
	}
	for {
		a.runTask(ctx, t)
		if !sleep(ctx, t.interval) {
			return
		}
	}
}

func (a *Agent) reportProbe(r protocol.ProbeResult) {
	a.store.Set("probe:"+r.Route, func(p *protocol.Push) { p.Probes = append(p.Probes, r) }, nil)
}

func (a *Agent) probeManager() *probe.Manager {
	return &probe.Manager{
		Cfg: a.cfg.Probe, UserAgent: "vpnmap-agent/" + a.d.Version,
		Report: a.reportProbe, Log: a.d.Log, Env: a.d.ProbeEnv,
	}
}

// Once collects everything a single time (including slow collectors and
// probes) and returns the push without sending it: `run --once --print`.
func (a *Agent) Once(ctx context.Context) *protocol.Push {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); a.runAll(ctx, a.periodicTasks()) }()
	if a.cfg.Probe != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.probeManager().Once(ctx); err != nil {
				a.store.Set("probe", nil, err)
			}
		}()
	}
	a.runAll(ctx, a.cheapTasks())
	wg.Wait()
	p, _ := a.assemble()
	return p
}

// Run is the main loop. It returns when ctx is cancelled (SIGTERM).
func (a *Agent) Run(ctx context.Context) error {
	if a.d.Pusher == nil {
		return errors.New("no pusher configured")
	}
	interval := a.cfg.Interval.D()
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var wg sync.WaitGroup
	periodic := a.periodicTasks()
	for i, t := range periodic {
		// Spread the first runs over a couple of seconds instead of a burst.
		offset := time.Duration(i) * 2 * time.Second / time.Duration(len(periodic))
		wg.Add(1)
		go func() { defer wg.Done(); a.runPeriodic(ctx, t, offset) }()
	}
	if a.cfg.Probe != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.probeManager().Run(ctx); err != nil {
				a.d.Log.Error("probe disabled", "error", err.Error())
				a.store.Set("probe", nil, err)
			}
		}()
	}
	a.d.Log.Info("agent started", "node", a.cfg.Node, "kind", a.kind(), "config_sha", a.sha,
		"interval", interval.String(), "periodic_collectors", len(periodic))

	cheap := a.cheapTasks()
	var ps pushState
	for {
		started := a.d.Now()
		a.runAll(ctx, cheap)
		p, tok := a.assemble()
		err := a.d.Pusher.Push(ctx, p)
		if ctx.Err() != nil {
			break
		}
		wait := interval - a.d.Now().Sub(started)
		if err == nil {
			a.store.CommitLogs(tok)
			ps.ok(a.d.Log)
		} else {
			wait = push.Backoff(interval, MaxBackoff, ps.fail(a.d.Log, err, interval))
		}
		if !sleep(ctx, wait) {
			break
		}
	}
	wg.Wait()
	a.d.Log.Info("agent stopped")
	return nil
}

// pushState logs push failures once per state change instead of every 5s.
type pushState struct {
	failures int
	kind     string
	since    time.Time
}

func (s *pushState) fail(log *slog.Logger, err error, interval time.Duration) int {
	s.failures++
	kind := push.Kind(err)
	next := push.Backoff(interval, MaxBackoff, s.failures)
	switch {
	case s.failures == 1:
		s.since = time.Now()
		log.Warn("push failed; collecting continues and retries back off", "error", err.Error(), "retry_in", next.String())
	case kind != s.kind:
		log.Warn("push still failing with a different error", "error", err.Error(), "failures", s.failures, "retry_in", next.String())
	}
	s.kind = kind
	return s.failures
}

func (s *pushState) ok(log *slog.Logger) {
	if s.failures > 0 {
		log.Info("push recovered", "failed_attempts", s.failures, "down_for", time.Since(s.since).Round(time.Second).String())
	}
	s.failures, s.kind = 0, ""
}

// sleep waits d (or not at all if d <= 0) and reports whether ctx is alive.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
