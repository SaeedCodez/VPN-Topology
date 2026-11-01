package tail

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"vpnmap/pkg/redact"
)

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// reader wraps Read with a persistent position, like the collector does.
type reader struct {
	t   *testing.T
	o   Options
	pos *Position
}

func (r *reader) read() Result {
	r.t.Helper()
	res, pos, err := Read(r.o, r.pos)
	if err != nil {
		r.t.Fatal(err)
	}
	r.pos = &pos
	return res
}

func TestFirstRunStartsAtEnd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "old 1\nold 2\n")
	r := &reader{t: t, o: Options{Path: p, Redact: redact.String}}
	if res := r.read(); len(res.Lines) != 0 {
		t.Fatalf("first run backfilled %v", res.Lines)
	}
	appendFile(t, p, "new 1\nnew 2\n")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"new 1", "new 2"}) || res.Truncated {
		t.Errorf("got %+v", res)
	}
	if res := r.read(); len(res.Lines) != 0 {
		t.Errorf("re-read %v", res.Lines)
	}
}

func TestPartialLineWaits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "")
	r := &reader{t: t, o: Options{Path: p}}
	r.read()
	appendFile(t, p, "complete\npart")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"complete"}) {
		t.Errorf("got %v", res.Lines)
	}
	appendFile(t, p, "ial\r\n")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"partial"}) {
		t.Errorf("got %v", res.Lines)
	}
}

func TestRotationByInode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.log")
	appendFile(t, p, "before\n")
	r := &reader{t: t, o: Options{Path: p}}
	r.read()
	// logrotate: rename the old file, create a new one with fresh lines.
	if err := os.Rename(p, p+".1"); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "rotated 1\nrotated 2\n")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"rotated 1", "rotated 2"}) {
		t.Errorf("got %v", res.Lines)
	}
}

func TestTruncationCopytruncate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "a long line that makes the file big\nanother long line\n")
	r := &reader{t: t, o: Options{Path: p}}
	r.read()
	// copytruncate keeps the inode but the size shrinks below our offset.
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, "fresh\n")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"fresh"}) {
		t.Errorf("got %v", res.Lines)
	}
}

func TestMaxLinesKeepsNewest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "")
	r := &reader{t: t, o: Options{Path: p, MaxLines: 200}}
	r.read()
	var b strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	appendFile(t, p, b.String())
	res := r.read()
	if len(res.Lines) != 200 || !res.Truncated || res.Lines[0] != "line 50" || res.Lines[199] != "line 249" {
		t.Errorf("len=%d truncated=%v first=%q last=%q", len(res.Lines), res.Truncated, res.Lines[0], res.Lines[len(res.Lines)-1])
	}
	if res := r.read(); len(res.Lines) != 0 || res.Truncated {
		t.Errorf("second read %+v", res)
	}
}

func TestMaxBytesSkipsOldestAndPartial(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "")
	r := &reader{t: t, o: Options{Path: p, MaxBytes: 64 << 10}}
	r.read()
	line := strings.Repeat("x", 99) + "\n" // 100 bytes
	appendFile(t, p, strings.Repeat(line, 1000)+"tail\n")
	res := r.read()
	total := 0
	for _, l := range res.Lines {
		total += len(l) + 1
	}
	if !res.Truncated || total > 64<<10 || res.Lines[len(res.Lines)-1] != "tail" {
		t.Errorf("truncated=%v total=%d last=%q", res.Truncated, total, res.Lines[len(res.Lines)-1])
	}
	for _, l := range res.Lines[:len(res.Lines)-1] {
		if len(l) != 99 {
			t.Fatalf("partial line leaked: %d bytes", len(l))
		}
	}
	// Position is at the end: nothing more.
	if res := r.read(); len(res.Lines) != 0 {
		t.Errorf("leftover %d lines", len(res.Lines))
	}
}

func TestHugeLineWithoutNewlineIsSkipped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "")
	r := &reader{t: t, o: Options{Path: p, MaxBytes: 1024}}
	r.read()
	appendFile(t, p, strings.Repeat("y", 5000))
	if res := r.read(); len(res.Lines) != 0 || !res.Truncated {
		t.Errorf("got %+v", res)
	}
	appendFile(t, p, "\nok\n")
	if res := r.read(); !reflect.DeepEqual(res.Lines, []string{"ok"}) {
		t.Errorf("got %v", res.Lines)
	}
}

func TestMatchAndRedact(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	appendFile(t, p, "")
	r := &reader{t: t, o: Options{Path: p, Match: regexp.MustCompile(`ERROR|WARN`), Redact: redact.String}}
	r.read()
	appendFile(t, p, "INFO all good\nERROR login failed password=hunter2 from 10.1.2.3\nWARN Authorization: Bearer abc.def.ghi\n")
	res := r.read()
	want := []string{"ERROR login failed password=[REDACTED] from 10.1.2.3", "WARN Authorization: Bearer [REDACTED]"}
	if !reflect.DeepEqual(res.Lines, want) {
		t.Errorf("got %q", res.Lines)
	}
}

func TestMissingFileAndNotRegular(t *testing.T) {
	if _, _, err := Read(Options{Path: filepath.Join(t.TempDir(), "nope.log")}, nil); err == nil {
		t.Error("missing file: want error")
	}
	if _, _, err := Read(Options{Path: t.TempDir()}, nil); err == nil {
		t.Error("directory: want error")
	}
}

func TestStatePersistence(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	s := LoadState(sp, false)
	s.Set("xray", Position{Path: "/var/log/xray/error.log", Ino: 7, Offset: 42})
	s.Set("old", Position{Path: "/x", Offset: 1})
	s.Prune(map[string]bool{"xray": true})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sp)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi.Mode(), err)
	}
	s2 := LoadState(sp, false)
	if p := s2.Get("xray"); p == nil || p.Offset != 42 || p.Ino != 7 {
		t.Errorf("reloaded %+v", p)
	}
	if s2.Get("old") != nil {
		t.Error("pruned id survived")
	}
	// Read-only state never writes.
	ro := LoadState(sp, true)
	ro.Set("xray", Position{Offset: 99})
	if err := ro.Save(); err != nil {
		t.Fatal(err)
	}
	if p := LoadState(sp, false).Get("xray"); p.Offset != 42 {
		t.Errorf("read-only state was written: %+v", p)
	}
	// Corrupt file: start empty, no panic.
	os.WriteFile(sp, []byte("{not json"), 0o600)
	if len(LoadState(sp, false).Logs) != 0 {
		t.Error("corrupt state not reset")
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.log")
	sp := filepath.Join(dir, "state.json")
	appendFile(t, p, "history\n")

	run := func() []string {
		st := LoadState(sp, false)
		res, pos, err := Read(Options{Path: p}, st.Get("app"))
		if err != nil {
			t.Fatal(err)
		}
		st.Set("app", pos)
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
		return res.Lines
	}
	if l := run(); len(l) != 0 {
		t.Fatalf("first run %v", l)
	}
	appendFile(t, p, "while down\n")
	if l := run(); !reflect.DeepEqual(l, []string{"while down"}) {
		t.Errorf("after restart %v", l)
	}
}
