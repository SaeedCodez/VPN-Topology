package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"vpnmap/apps/agent/internal/collect"
	"vpnmap/apps/agent/internal/helper"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

// DefaultPeriodic is the interval of the slower collectors (docker, tcp,
// http, checks, prom) when the config does not set one.
const DefaultPeriodic = 15 * time.Second

// task is one collector invocation. run returns how to add its data to a
// push (nil for none) and an error for the board's "no data" reason.
type task struct {
	name     string
	interval time.Duration // 0: runs on every push (cheap tasks)
	timeout  time.Duration
	run      func(ctx context.Context) (func(*protocol.Push), error)
}

func orDefault(d agentcfg.Duration, def time.Duration) time.Duration {
	if d.D() > 0 {
		return d.D()
	}
	return def
}

// cheapTasks run right before every push so cumulative counters and the
// push timestamp line up (the hub derives rates from them).
func (a *Agent) cheapTasks() []task {
	cfg := a.cfg
	ts := []task{
		{name: "host", timeout: 3 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			h, err := a.host.Collect(ctx)
			return func(p *protocol.Push) { p.Host = h }, err
		}},
		{name: "sockets", timeout: 3 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			r, err := a.sockets.Collect()
			a.store.SetListening(r.Listening)
			return func(p *protocol.Push) { p.SS = append(p.SS, r.SS...) }, err
		}},
		{name: "checks-installed", timeout: time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			a.store.SetChecks(installedChecks(cfg))
			return nil, nil
		}},
	}
	if len(cfg.Units) > 0 {
		ts = append(ts, task{name: "units", timeout: 5 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			u, err := a.units.Collect(ctx)
			return func(p *protocol.Push) { p.Units = append(p.Units, u...) }, err
		}})
	}
	if len(cfg.Logs) > 0 {
		ts = append(ts, task{name: "logs", timeout: 3 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			lines, errs := a.logs.Collect()
			a.store.AddLogs(lines)
			if len(errs) > 0 {
				return nil, collectorErrors(errs) // one error per log file
			}
			return nil, nil
		}})
	}
	if cfg.Nft != nil {
		ts = append(ts, task{name: "nft", timeout: 4 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			var c []protocol.NftCounter
			if err := a.d.Helper.Call(ctx, helper.OpNftCounters, nil, &c); err != nil {
				return nil, err
			}
			return func(p *protocol.Push) { p.Nft = append(p.Nft, c...) }, nil
		}})
	}
	for _, w := range cfg.WG {
		w := w
		ts = append(ts, task{name: "wg:" + wgLabel(w), timeout: 4 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			var wi protocol.WGInterface
			if err := a.d.Helper.Call(ctx, helper.OpWGDump, []string{w.Netns, w.Iface}, &wi); err != nil {
				return nil, err
			}
			return func(p *protocol.Push) { p.WG = append(p.WG, wi) }, nil
		}})
	}
	return ts
}

// periodicTasks run on their own tickers; pushes carry their latest result.
func (a *Agent) periodicTasks() []task {
	cfg := a.cfg
	var ts []task
	if len(cfg.Containers) > 0 {
		ts = append(ts, task{name: "docker", interval: DefaultPeriodic, timeout: 15 * time.Second, run: func(ctx context.Context) (func(*protocol.Push), error) {
			var c []protocol.Container
			if err := a.d.Helper.Call(ctx, helper.OpDockerPS, nil, &c); err != nil {
				return nil, err
			}
			return func(p *protocol.Push) { p.Containers = append(p.Containers, c...) }, nil
		}})
	}
	for _, t := range cfg.TCP {
		t := t
		timeout := orDefault(t.Timeout, 3*time.Second)
		ts = append(ts, task{name: "tcp:" + t.Target, interval: orDefault(t.Interval, DefaultPeriodic), timeout: timeout + time.Second,
			run: func(ctx context.Context) (func(*protocol.Push), error) {
				r := collect.DialTCP(ctx, t.Target, timeout)
				return func(p *protocol.Push) { p.TCP = append(p.TCP, r) }, nil
			}})
	}
	for _, h := range cfg.HTTP {
		h := h
		timeout := orDefault(h.Timeout, 10*time.Second)
		ts = append(ts, task{name: "http:" + h.ID, interval: orDefault(h.Interval, DefaultPeriodic), timeout: timeout + time.Second,
			run: func(ctx context.Context) (func(*protocol.Push), error) {
				r := a.httpc.Check(ctx, h.ID, h.URL, timeout)
				return func(p *protocol.Push) { p.HTTP = append(p.HTTP, r) }, nil
			}})
	}
	for _, pt := range cfg.Prom {
		pt := pt
		ts = append(ts, task{name: "prom:" + pt.ID, interval: orDefault(pt.Interval, DefaultPeriodic), timeout: 11 * time.Second,
			run: func(ctx context.Context) (func(*protocol.Push), error) {
				s, err := a.prom.Scrape(ctx, pt, 10*time.Second)
				return func(p *protocol.Push) { p.Prom = append(p.Prom, s...) }, err
			}})
	}
	for _, c := range cfg.Checks {
		c := c
		// Outlast the helper's own deadline so its answer (even "timed out")
		// arrives before the client gives up.
		helperDeadline := max(helper.OverallTimeout, helper.CheckTimeout(c)+5*time.Second)
		ts = append(ts, task{name: "check:" + c.ID, interval: orDefault(c.Interval, DefaultPeriodic), timeout: helperDeadline + 5*time.Second,
			run: func(ctx context.Context) (func(*protocol.Push), error) {
				var r protocol.CheckResult
				if err := a.d.Helper.Call(ctx, helper.OpCheck, []string{c.ID}, &r); err != nil {
					return nil, err
				}
				return func(p *protocol.Push) { p.Checks = append(p.Checks, r) }, nil
			}})
	}
	return ts
}

func wgLabel(w agentcfg.WGConfig) string {
	if w.Netns != "" {
		return w.Netns + "/" + w.Iface
	}
	return w.Iface
}

// installedChecks lists configured checks whose script exists in checks.d
// (a cheap stat; the helper does the real ownership checks before running).
func installedChecks(cfg *agentcfg.Config) []string {
	var out []string
	for _, c := range cfg.Checks {
		if fi, err := os.Stat(filepath.Join(cfg.ChecksDir, c.ID+".check")); err == nil && fi.Mode().IsRegular() {
			out = append(out, c.ID)
		}
	}
	return out
}

// capabilities summarises the config; listening ports and installed checks
// are filled in from the store at push time.
func capabilities(cfg *agentcfg.Config) protocol.Capabilities {
	var c protocol.Capabilities
	c.Units = append(c.Units, cfg.Units...)
	c.Containers = append(c.Containers, cfg.Containers...)
	if cfg.Nft != nil {
		for _, n := range cfg.Nft.Counters {
			c.NftCounters = append(c.NftCounters, n.Name)
		}
	}
	for _, w := range cfg.WG {
		c.WG = append(c.WG, wgLabel(w))
	}
	for _, s := range cfg.SS {
		c.SSPorts = append(c.SSPorts, s.ID)
	}
	for _, t := range cfg.TCP {
		c.TCPTargets = append(c.TCPTargets, t.Target)
	}
	for _, h := range cfg.HTTP {
		c.HTTPTargets = append(c.HTTPTargets, h.ID)
	}
	for _, l := range cfg.Logs {
		c.Logs = append(c.Logs, l.ID)
	}
	for _, p := range cfg.Prom {
		c.Prom = append(c.Prom, p.ID)
	}
	if cfg.Probe != nil {
		for _, r := range cfg.Probe.Routes {
			c.Routes = append(c.Routes, r.Route)
		}
	}
	return c
}
