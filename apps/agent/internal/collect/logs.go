package collect

import (
	"fmt"
	"regexp"

	"vpnmap/apps/agent/internal/guard"
	"vpnmap/apps/agent/internal/tail"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/redact"
)

// Logs tails the configured log files. Every line is redacted before it
// leaves this collector.
type Logs struct {
	cfgs     []agentcfg.LogConfig
	matchers map[string]*regexp.Regexp
	state    *tail.State
}

// NewLogs prepares the tailer; regexps were validated with the config.
func NewLogs(cfgs []agentcfg.LogConfig, state *tail.State) (*Logs, error) {
	l := &Logs{cfgs: cfgs, matchers: map[string]*regexp.Regexp{}, state: state}
	keep := map[string]bool{}
	for _, c := range cfgs {
		keep[c.ID] = true
		if c.Match == "" {
			continue
		}
		re, err := regexp.Compile(c.Match)
		if err != nil {
			return nil, fmt.Errorf("log %s: %w", c.ID, err)
		}
		l.matchers[c.ID] = re
	}
	state.Prune(keep)
	return l, nil
}

// Collect reads new lines of every log. Errors are per log, so one missing
// file does not hide the others.
func (l *Logs) Collect() ([]protocol.LogLines, []protocol.CollectorError) {
	var out []protocol.LogLines
	var errs []protocol.CollectorError
	for _, c := range l.cfgs {
		name := "logs:" + c.ID
		if err := guard.CheckReadable(c.Path); err != nil {
			errs = append(errs, protocol.CollectorError{Collector: name, Error: err.Error()})
			continue
		}
		res, pos, err := tail.Read(tail.Options{
			Path: c.Path, Match: l.matchers[c.ID], Redact: redact.String,
		}, l.state.Get(c.ID))
		if err != nil {
			errs = append(errs, protocol.CollectorError{Collector: name, Error: err.Error()})
			continue
		}
		l.state.Set(c.ID, pos)
		if len(res.Lines) > 0 || res.Truncated {
			lines := res.Lines
			if lines == nil {
				lines = []string{}
			}
			out = append(out, protocol.LogLines{ID: c.ID, Lines: lines, Truncated: res.Truncated})
		}
	}
	if err := l.state.Save(); err != nil {
		errs = append(errs, protocol.CollectorError{Collector: "logs", Error: "saving state: " + err.Error()})
	}
	return out, errs
}
