package tail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// State is the persisted set of positions, keyed by log id.
// It lives in /var/lib/vpnmap-agent/state.json (systemd StateDirectory).
type State struct {
	mu       sync.Mutex
	path     string              // "" = in memory only (--once mode)
	Logs     map[string]Position `json:"logs"`
	dirty    bool
	readOnly bool
}

// LoadState reads the state file. A missing or corrupt file yields an empty
// state (worst case: logs restart at their end, which loses nothing old).
// readOnly states never write back; `run --once` uses that so a debugging
// run cannot steal lines from the running agent.
func LoadState(path string, readOnly bool) *State {
	s := &State{path: path, Logs: map[string]Position{}, readOnly: readOnly}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var disk struct {
		Logs map[string]Position `json:"logs"`
	}
	if json.Unmarshal(b, &disk) == nil && disk.Logs != nil {
		s.Logs = disk.Logs
	}
	return s
}

// Get returns the position of a log id (nil if unknown).
func (s *State) Get(id string) *Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Logs[id]
	if !ok {
		return nil
	}
	return &p
}

// Set records a new position.
func (s *State) Set(id string, p Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.Logs[id]; ok && old == p {
		return
	}
	s.Logs[id] = p
	s.dirty = true
}

// Prune forgets ids that are no longer configured.
func (s *State) Prune(keep map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.Logs {
		if !keep[id] {
			delete(s.Logs, id)
			s.dirty = true
		}
	}
}

// Save writes the state atomically (temp file + rename) if it changed.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty || s.readOnly || s.path == "" {
		return nil
	}
	b, err := json.Marshal(struct {
		Logs map[string]Position `json:"logs"`
	}{s.Logs})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}
