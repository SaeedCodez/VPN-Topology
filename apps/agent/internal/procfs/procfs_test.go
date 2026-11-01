package procfs

import (
	"math"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

var fixture = FS{Root: "testdata/proc"}

func TestStat(t *testing.T) {
	s, err := fixture.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// user+nice+system+idle+iowait+irq+softirq+steal; guest excluded.
	if want := uint64(10000 + 500 + 3000 + 80000 + 1500 + 0 + 200 + 300); s.Total != want {
		t.Errorf("Total = %d, want %d", s.Total, want)
	}
	if s.Idle != 81500 {
		t.Errorf("Idle = %d, want 81500", s.Idle)
	}
	if s.NumCPU != 2 || s.BootTime != 1767225600 {
		t.Errorf("NumCPU=%d BootTime=%d", s.NumCPU, s.BootTime)
	}
}

func TestParseStatErrors(t *testing.T) {
	if _, err := ParseStat(strings.NewReader("btime 1\n")); err == nil {
		t.Error("want error without cpu line")
	}
	if _, err := ParseStat(strings.NewReader("cpu 1 2\n")); err == nil {
		t.Error("want error for short cpu line")
	}
}

func TestCPUPercent(t *testing.T) {
	prev := CPUStat{Total: 1000, Idle: 800}
	cur := CPUStat{Total: 1200, Idle: 850}
	if got := CPUPercent(prev, cur); math.Abs(got-75) > 1e-9 {
		t.Errorf("CPUPercent = %v, want 75", got)
	}
	if got := CPUPercent(cur, prev); got != 0 {
		t.Errorf("counter going backwards: %v, want 0", got)
	}
}

func TestMeminfo(t *testing.T) {
	m, err := fixture.Meminfo()
	if err != nil {
		t.Fatal(err)
	}
	if m.Total != 4028492*1024 || m.Available != 2014246*1024 {
		t.Errorf("got %+v", m)
	}
	// Old kernels: no MemAvailable.
	old := "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 250 kB\n"
	m, err = ParseMeminfo(strings.NewReader(old))
	if err != nil || m.Available != 400*1024 {
		t.Errorf("fallback: %+v %v", m, err)
	}
	if _, err := ParseMeminfo(strings.NewReader("MemFree: 1 kB\n")); err == nil {
		t.Error("want error without MemTotal")
	}
}

func TestLoadavgUptime(t *testing.T) {
	l, err := fixture.Loadavg()
	if err != nil || l != [3]float64{0.52, 0.38, 0.31} {
		t.Errorf("loadavg %v %v", l, err)
	}
	u, err := fixture.Uptime()
	if err != nil || u != 864123 {
		t.Errorf("uptime %v %v", u, err)
	}
}

func TestNetDev(t *testing.T) {
	n, err := fixture.NetDev()
	if err != nil {
		t.Fatal(err)
	}
	want := []NICStat{
		{Name: "eth0", RxBytes: 123456789012, RxPkt: 98765432, TxBytes: 234567890123, TxPkt: 87654321},
		{Name: "wg0", RxBytes: 5555, RxPkt: 55, TxBytes: 6666, TxPkt: 66},
	}
	if !reflect.DeepEqual(n, want) {
		t.Errorf("got %+v\nwant %+v", n, want)
	}
}

func TestScanSockets(t *testing.T) {
	var all []Socket
	if err := fixture.ScanSockets("net/tcp", func(s Socket) { all = append(all, s) }); err != nil {
		t.Fatal(err)
	}
	if len(all) != 8 {
		t.Fatalf("rows = %d, want 8", len(all))
	}
	if all[0] != (Socket{LocalAddr: netip.MustParseAddr("0.0.0.0"), LocalPort: 22, RemotePort: 0, State: TCPListen}) {
		t.Errorf("row 0 = %+v", all[0])
	}
	if all[3] != (Socket{LocalAddr: netip.MustParseAddr("10.0.0.10"), LocalPort: 2053, RemotePort: 51000, State: TCPEstablished}) {
		t.Errorf("row 3 = %+v", all[3])
	}
	if !all[1].LocalLoopback() || all[1].LocalAddr.String() != "127.0.0.1" || all[0].LocalLoopback() {
		t.Errorf("loopback detection: %+v %+v", all[0], all[1])
	}
	var v6 []Socket
	fixture.ScanSockets("net/tcp6", func(s Socket) { v6 = append(v6, s) })
	wantAddrs := []string{"::", "10.0.0.10", "10.0.0.10", "::", "::1", "127.0.0.1"} // v4-mapped are unmapped
	for i, w := range wantAddrs {
		if v6[i].LocalAddr.String() != w {
			t.Errorf("tcp6 row %d addr = %s, want %s", i, v6[i].LocalAddr, w)
		}
	}
	if !v6[4].LocalLoopback() || !v6[5].LocalLoopback() || v6[0].LocalLoopback() {
		t.Error("IPv6 loopback detection")
	}
	// IPv6 disabled: missing file is fine.
	if err := fixture.ScanSockets("net/raw6", func(Socket) {}); err != nil {
		t.Errorf("missing file: %v", err)
	}
	if err := ParseSockets(strings.NewReader("hdr\n 0: zz:zz 00:00 0A\n"), func(Socket) {}); err == nil {
		t.Error("want error on malformed row")
	}
}
