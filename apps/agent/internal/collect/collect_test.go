package collect

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/apps/agent/internal/tail"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
	"vpnmap/pkg/sysexec"
)

var fixtureFS = procfs.FS{Root: "../procfs/testdata/proc"}

func TestHost(t *testing.T) {
	h := &Host{FS: fixtureFS, FirstSample: time.Millisecond}
	got, err := h.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.NumCPU != 2 || got.BootTime != 1767225600 || got.UptimeS != 864123 || got.Load1 != 0.52 {
		t.Errorf("%+v", got)
	}
	if got.MemTotal != 4028492*1024 || got.MemUsed != (4028492-2014246)*1024 {
		t.Errorf("mem %d/%d", got.MemUsed, got.MemTotal)
	}
	if len(got.NICs) != 2 || got.NICs[0].Name != "eth0" || got.NICs[0].RxB != 123456789012 {
		t.Errorf("nics %+v", got.NICs)
	}
	if len(got.Disks) != 1 || got.Disks[0].Mount != "/" || got.Disks[0].Total == 0 {
		t.Errorf("disks %+v", got.Disks)
	}
	// Identical samples: 0% busy, and the second call uses the stored baseline.
	if got.CPUPct != 0 {
		t.Errorf("cpu %v", got.CPUPct)
	}
	if _, err := h.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHostPartialFailure(t *testing.T) {
	h := &Host{FS: procfs.FS{Root: t.TempDir()}, FirstSample: time.Millisecond}
	got, err := h.Collect(context.Background())
	if err == nil {
		t.Fatal("want error for missing /proc files")
	}
	if got == nil || len(got.Disks) != 1 {
		t.Errorf("partial result lost: %+v", got)
	}
}

func TestUnits(t *testing.T) {
	out, _ := os.ReadFile("testdata/systemctl-show.txt")
	names := []string{"xray", "remnanode", "nope"}
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"systemctl show --no-pager --property=Id,LoadState,ActiveState,SubState,ActiveEnterTimestampMonotonic,ActiveEnterTimestamp,NRestarts -- xray remnanode nope": {Stdout: string(out)},
	}}
	u := &Units{Names: names, Runner: f, BootTime: func() int64 { return 1767225600 }}
	got, err := u.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Unit{
		{Name: "xray", ActiveState: "active", SubState: "running", Since: "2026-01-01T01:00:00Z", NRestarts: 2},
		{Name: "remnanode", ActiveState: "failed", SubState: "failed", NRestarts: 14},
		{Name: "nope", ActiveState: "inactive", SubState: "not-found"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if _, err := ParseSystemctlShow(out, []string{"only-one"}, 0); err == nil {
		t.Error("block count mismatch accepted")
	}
	// Without btime the textual timestamp is kept.
	got, _ = ParseSystemctlShow(out, names, 0)
	if got[0].Since != "Thu 2026-01-01 01:00:00 UTC" {
		t.Errorf("since %q", got[0].Since)
	}
}

func TestSockets(t *testing.T) {
	s := &Sockets{FS: fixtureFS, SS: []agentcfg.SSConfig{
		{ID: "inbound-2053", Port: 2053, Side: "local"},
		{ID: "https-in", Port: 443, Side: "local"},
		{ID: "to-upstream-443", Port: 443, Side: "remote"},
		{ID: "unused", Port: 9999, Side: "local"},
	}}
	got, err := s.Collect()
	if err != nil {
		t.Fatal(err)
	}
	// Loopback-only listeners are excluded: 127.0.0.1:3001, [::1]:8080,
	// [::ffff:127.0.0.1]:8081 and 127.0.0.53:53 (udp). 10.0.0.10:9090 counts.
	wantListen := []string{"tcp/22", "tcp/443", "tcp/2053", "tcp/9090", "udp/51820", "udp/51821"}
	if !reflect.DeepEqual(got.Listening, wantListen) {
		t.Errorf("listening %v", got.Listening)
	}
	want := []protocol.SSCount{
		{ID: "inbound-2053", Port: 2053, Side: "local", Established: 2}, // TIME_WAIT not counted
		{ID: "https-in", Port: 443, Side: "local", Established: 1},
		{ID: "to-upstream-443", Port: 443, Side: "remote", Established: 2}, // tcp + tcp6
		{ID: "unused", Port: 9999, Side: "local", Established: 0},
	}
	if !reflect.DeepEqual(got.SS, want) {
		t.Errorf("ss %+v", got.SS)
	}
}

func TestDialTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	r := DialTCP(context.Background(), addr, time.Second)
	if !r.OK || r.Error != "" || r.Target != addr || r.At == "" {
		t.Errorf("open port: %+v", r)
	}
	ln.Close()
	r = DialTCP(context.Background(), addr, time.Second)
	if r.OK || r.Error != "refused" {
		t.Errorf("closed port: %+v", r)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyNetError(t *testing.T) {
	op := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: err}}
	}
	cases := map[string]error{
		"":            nil,
		"refused":     op(syscall.ECONNREFUSED),
		"unreachable": op(syscall.EHOSTUNREACH),
		"reset":       op(syscall.ECONNRESET),
		"dns":         &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}},
		"timeout":     &net.OpError{Op: "dial", Err: timeoutErr{}},
		"weird thing": errors.New("dial tcp: something: weird thing"),
	}
	for want, err := range cases {
		if got := ClassifyNetError(err); got != want {
			t.Errorf("ClassifyNetError(%v) = %q, want %q", err, got, want)
		}
	}
	if got := ClassifyNetError(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("deadline: %q", got)
	}
}

func TestLogsCollector(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app.log")
	os.WriteFile(app, []byte("old\n"), 0o644)
	secret := filepath.Join(dir, "panel.env")
	os.WriteFile(secret, []byte("JWT_SECRET=x\n"), 0o600)
	state := tail.LoadState(filepath.Join(dir, "state.json"), false)
	l, err := NewLogs([]agentcfg.LogConfig{
		{ID: "app", Path: app, Match: "ERR"},
		{ID: "env", Path: secret},
		{ID: "missing", Path: filepath.Join(dir, "missing.log")},
	}, state)
	if err != nil {
		t.Fatal(err)
	}
	lines, errs := l.Collect()
	if len(lines) != 0 || len(errs) != 2 {
		t.Fatalf("first run: lines=%+v errs=%+v", lines, errs)
	}
	if errs[0].Collector != "logs:env" || !strings.Contains(errs[0].Error, "refusing") {
		t.Errorf("secret file: %+v", errs[0])
	}
	f, _ := os.OpenFile(app, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("INFO fine\nERR db password=hunter2 at 10.0.0.9\n")
	f.Close()
	lines, _ = l.Collect()
	if len(lines) != 1 || !reflect.DeepEqual(lines[0].Lines, []string{"ERR db password=[REDACTED] at 10.0.0.9"}) {
		t.Errorf("lines %+v", lines)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Errorf("state not saved: %v", err)
	}
}
