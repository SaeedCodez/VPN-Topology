package agent

import (
	"errors"
	"sync"

	"vpnmap/pkg/protocol"
)

// Store keeps the latest result of every collector task. A push is
// assembled from it, so slow collectors (docker, http, checks) never delay
// a push: it carries whatever they produced last.
//
// Log lines are different: they are events, not state. They queue up until
// a push that carried them succeeds, bounded per log like the tailer.
type Store struct {
	mu    sync.Mutex
	order []string
	slots map[string]slot

	listening []string
	checks    []string

	logOrder []string
	logs     map[string]*pendingLog
}

type slot struct {
	apply func(*protocol.Push)
	errs  []protocol.CollectorError
}

const maxErrorLen = 300

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{slots: map[string]slot{}, logs: map[string]*pendingLog{}}
}

// Set records the latest outcome of a task. apply adds the task's data to a
// push (nil: no data); err, if any, becomes a CollectorError named after the
// task. Partial results (apply and err) are allowed.
func (s *Store) Set(name string, apply func(*protocol.Push), err error) {
	var errs []protocol.CollectorError
	if err != nil {
		errs = []protocol.CollectorError{{Collector: name, Error: clipErr(err)}}
	}
	s.SetErrors(name, apply, errs)
}

// SetErrors is Set with ready-made collector errors (one task, several
// sources, e.g. one per log file).
func (s *Store) SetErrors(name string, apply func(*protocol.Push), errs []protocol.CollectorError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.slots[name]; !ok {
		s.order = append(s.order, name)
	}
	s.slots[name] = slot{apply: apply, errs: errs}
}

// SetListening records the listening ports for the capabilities.
func (s *Store) SetListening(l []string) {
	s.mu.Lock()
	s.listening = l
	s.mu.Unlock()
}

// SetChecks records the configured checks that are installed.
func (s *Store) SetChecks(c []string) {
	s.mu.Lock()
	s.checks = c
	s.mu.Unlock()
}

// Fill adds every task's latest data and errors to p, in task order.
func (s *Store) Fill(p *protocol.Push) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range s.order {
		sl := s.slots[name]
		if sl.apply != nil {
			sl.apply(p)
		}
		p.Errors = append(p.Errors, sl.errs...)
	}
	p.Agent.Capabilities.ListeningPorts = append([]string(nil), s.listening...)
	p.Agent.Capabilities.Checks = append([]string(nil), s.checks...)
}

func clipErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var j interface{ Unwrap() []error }
	if errors.As(err, &j) {
		// errors.Join uses newlines; keep it on one line for the board.
		msg = ""
		for i, e := range j.Unwrap() {
			if i > 0 {
				msg += "; "
			}
			msg += e.Error()
		}
	}
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen] + "..."
	}
	return msg
}

// pendingLog holds lines not yet delivered. start is the absolute index of
// lines[0], so a commit can drop exactly what a push carried even if the
// queue was trimmed in between.
type pendingLog struct {
	start     uint64
	lines     []string
	bytes     int
	truncated bool
}

// Per-log queue bounds, matching what one push may carry.
const (
	maxQueuedLines = 200
	maxQueuedBytes = 64 << 10
)

// AddLogs queues new lines. When the hub is unreachable the queue keeps the
// newest lines and marks the log truncated.
func (s *Store) AddLogs(batch []protocol.LogLines) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range batch {
		q := s.logs[b.ID]
		if q == nil {
			q = &pendingLog{}
			s.logs[b.ID] = q
			s.logOrder = append(s.logOrder, b.ID)
		}
		q.lines = append(q.lines, b.Lines...)
		for _, l := range b.Lines {
			q.bytes += len(l) + 1
		}
		q.truncated = q.truncated || b.Truncated
		for len(q.lines) > 0 && (len(q.lines) > maxQueuedLines || q.bytes > maxQueuedBytes) {
			q.bytes -= len(q.lines[0]) + 1
			q.lines = q.lines[1:]
			q.start++
			q.truncated = true
		}
	}
}

// logToken remembers how far each queue was included in a push.
type logToken map[string]uint64

// FillLogs copies the queued lines into p and returns a token for CommitLogs.
func (s *Store) FillLogs(p *protocol.Push) logToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := logToken{}
	for _, id := range s.logOrder {
		q := s.logs[id]
		if len(q.lines) == 0 && !q.truncated {
			continue
		}
		p.Logs = append(p.Logs, protocol.LogLines{ID: id, Lines: append([]string{}, q.lines...), Truncated: q.truncated})
		tok[id] = q.start + uint64(len(q.lines))
	}
	return tok
}

// CommitLogs drops the lines a successful push delivered.
func (s *Store) CommitLogs(tok logToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, end := range tok {
		q := s.logs[id]
		if q == nil || end <= q.start {
			continue
		}
		n := int(end - q.start)
		if n > len(q.lines) {
			n = len(q.lines)
		}
		for _, l := range q.lines[:n] {
			q.bytes -= len(l) + 1
		}
		q.lines = q.lines[n:]
		q.start += uint64(n)
		if len(q.lines) == 0 {
			q.truncated = false
			q.lines = nil
		}
	}
}
