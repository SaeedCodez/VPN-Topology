// Package tail follows log files across pushes. For every file it remembers
// (device, inode, offset) in a small JSON state file so a restart neither
// re-sends old lines nor loses its place.
//
// Semantics, chosen for a live board rather than a log archive:
//   - first sight of a file starts at its END (no backfill of history);
//   - rotation (new inode) or truncation (size < offset) restarts at 0;
//   - at most MaxLines lines / MaxBytes bytes per read; when more was written
//     the OLDEST lines are skipped (the board wants what happens now) and
//     Truncated is set;
//   - only complete lines are consumed; a partial last line waits;
//   - every line is passed through the redactor before it is returned.
package tail

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
)

// Defaults per log per push.
const (
	DefaultMaxLines = 200
	DefaultMaxBytes = 64 << 10
)

// Position is where reading stopped in one file.
type Position struct {
	Path   string `json:"path"`
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
	Offset int64  `json:"offset"`
}

// Options configure one Read.
type Options struct {
	Path     string
	Match    *regexp.Regexp      // nil: all lines
	Redact   func(string) string // required in production; nil means identity
	MaxLines int
	MaxBytes int
}

// Result is the outcome of one Read.
type Result struct {
	Lines     []string
	Truncated bool
}

// Read returns the complete lines written since prev and the new position.
// prev == nil (or a different path) means first sight: start at the end.
// On error the caller keeps prev unchanged.
func Read(o Options, prev *Position) (Result, Position, error) {
	if o.MaxLines <= 0 {
		o.MaxLines = DefaultMaxLines
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	f, err := os.Open(o.Path)
	if err != nil {
		return Result{}, Position{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Result{}, Position{}, err
	}
	if !fi.Mode().IsRegular() {
		return Result{}, Position{}, fmt.Errorf("%s is not a regular file", o.Path)
	}
	pos := Position{Path: o.Path, Offset: fi.Size()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		pos.Dev, pos.Ino = uint64(st.Dev), uint64(st.Ino)
	}
	size := fi.Size()

	if prev == nil || prev.Path != o.Path {
		return Result{}, pos, nil // first sight: no backfill
	}
	start := prev.Offset
	if prev.Dev != pos.Dev || prev.Ino != pos.Ino || size < start {
		start = 0 // rotated or truncated
	}
	if size == start {
		pos.Offset = start
		return Result{}, pos, nil
	}

	var res Result
	from := start
	if size-start > int64(o.MaxBytes) {
		from = size - int64(o.MaxBytes)
		res.Truncated = true
	}
	buf := make([]byte, size-from)
	n, err := f.ReadAt(buf, from)
	if err != nil && err != io.EOF {
		return Result{}, Position{}, err
	}
	buf = buf[:n]
	end := from + int64(n)

	if res.Truncated {
		// We jumped into the middle of a line: drop up to the first newline.
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			// One giant line without newline: skip it entirely.
			pos.Offset = end
			return res, pos, nil
		}
		buf, from = buf[i+1:], from+int64(i+1)
	}
	last := bytes.LastIndexByte(buf, '\n')
	if last < 0 {
		pos.Offset = from // only a partial line so far: wait for the rest
		return res, pos, nil
	}
	pos.Offset = from + int64(last) + 1

	redact := o.Redact
	if redact == nil {
		redact = func(s string) string { return s }
	}
	for _, raw := range bytes.Split(buf[:last], []byte{'\n'}) {
		line := strings.ToValidUTF8(strings.TrimRight(string(raw), "\r"), "�")
		if line == "" {
			continue
		}
		if o.Match != nil && !o.Match.MatchString(line) {
			continue
		}
		res.Lines = append(res.Lines, redact(line))
	}
	if len(res.Lines) > o.MaxLines {
		res.Lines = res.Lines[len(res.Lines)-o.MaxLines:]
		res.Truncated = true
	}
	// Redaction can lengthen lines; keep the byte budget after it too.
	total := 0
	for _, l := range res.Lines {
		total += len(l) + 1
	}
	for total > o.MaxBytes && len(res.Lines) > 0 {
		total -= len(res.Lines[0]) + 1
		res.Lines = res.Lines[1:]
		res.Truncated = true
	}
	return res, pos, nil
}
