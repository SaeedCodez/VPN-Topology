// Package procfs parses the few /proc files the agent needs. Every parser
// takes an io.Reader so it can be tested against fixtures, and FS reads from
// a configurable root so collectors can be pointed at a testdata tree.
package procfs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FS reads files below a proc mount ("/proc" in production).
type FS struct{ Root string }

// Default is the real /proc.
var Default = FS{Root: "/proc"}

func (fs FS) open(name string) (*os.File, error) {
	return os.Open(filepath.Join(fs.Root, name))
}

func (fs FS) parse(name string, fn func(io.Reader) error) error {
	f, err := fs.open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := fn(f); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// CPUStat is the aggregate "cpu" line of /proc/stat plus a few globals.
type CPUStat struct {
	Total    uint64 // all jiffies except guest time (already counted in user)
	Idle     uint64 // idle + iowait
	NumCPU   int
	BootTime int64 // btime, Unix seconds
}

// Stat reads /proc/stat.
func (fs FS) Stat() (s CPUStat, err error) {
	err = fs.parse("stat", func(r io.Reader) error { s, err = ParseStat(r); return err })
	return s, err
}

// ParseStat parses /proc/stat.
func ParseStat(r io.Reader) (CPUStat, error) {
	var s CPUStat
	found := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			if len(f) < 5 {
				return s, fmt.Errorf("short cpu line")
			}
			// user nice system idle iowait irq softirq steal [guest guest_nice]
			for i := 1; i < len(f) && i <= 8; i++ {
				v, err := strconv.ParseUint(f[i], 10, 64)
				if err != nil {
					return s, fmt.Errorf("cpu field %d: %w", i, err)
				}
				s.Total += v
				if i == 4 || i == 5 {
					s.Idle += v
				}
			}
			found = true
		case strings.HasPrefix(f[0], "cpu"):
			s.NumCPU++
		case f[0] == "btime" && len(f) > 1:
			s.BootTime, _ = strconv.ParseInt(f[1], 10, 64)
		}
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	if !found {
		return s, fmt.Errorf("no aggregate cpu line")
	}
	return s, nil
}

// CPUPercent returns the busy share of all cores between two samples (0..100).
func CPUPercent(prev, cur CPUStat) float64 {
	if cur.Total <= prev.Total {
		return 0
	}
	dt := float64(cur.Total - prev.Total)
	var di float64
	if cur.Idle > prev.Idle {
		di = float64(cur.Idle - prev.Idle)
	}
	p := (1 - di/dt) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// Meminfo holds the fields of /proc/meminfo in bytes.
type Meminfo struct {
	Total     uint64
	Available uint64
}

// Meminfo reads /proc/meminfo.
func (fs FS) Meminfo() (m Meminfo, err error) {
	err = fs.parse("meminfo", func(r io.Reader) error { m, err = ParseMeminfo(r); return err })
	return m, err
}

// ParseMeminfo parses /proc/meminfo. Kernels older than 3.14 lack
// MemAvailable; MemFree+Buffers+Cached is the classic approximation.
func ParseMeminfo(r io.Reader) (Meminfo, error) {
	kv := map[string]uint64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			v *= 1024
		}
		kv[k] = v
	}
	if err := sc.Err(); err != nil {
		return Meminfo{}, err
	}
	m := Meminfo{Total: kv["MemTotal"]}
	if m.Total == 0 {
		return m, fmt.Errorf("MemTotal missing")
	}
	if a, ok := kv["MemAvailable"]; ok {
		m.Available = a
	} else {
		m.Available = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	if m.Available > m.Total {
		m.Available = m.Total
	}
	return m, nil
}

// Loadavg reads /proc/loadavg.
func (fs FS) Loadavg() (l [3]float64, err error) {
	err = fs.parse("loadavg", func(r io.Reader) error { l, err = ParseLoadavg(r); return err })
	return l, err
}

// ParseLoadavg parses "0.10 0.20 0.30 1/234 5678".
func ParseLoadavg(r io.Reader) ([3]float64, error) {
	var l [3]float64
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return l, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return l, fmt.Errorf("short loadavg")
	}
	for i := 0; i < 3; i++ {
		if l[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return l, err
		}
	}
	return l, nil
}

// Uptime reads /proc/uptime and returns whole seconds.
func (fs FS) Uptime() (u int64, err error) {
	err = fs.parse("uptime", func(r io.Reader) error { u, err = ParseUptime(r); return err })
	return u, err
}

// ParseUptime parses "12345.67 54321.00".
func ParseUptime(r io.Reader) (int64, error) {
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, fmt.Errorf("empty uptime")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

// NICStat holds cumulative counters of one interface from /proc/net/dev.
type NICStat struct {
	Name           string
	RxBytes, RxPkt uint64
	TxBytes, TxPkt uint64
}

// NetDev reads /proc/net/dev (the agent's own network namespace).
func (fs FS) NetDev() (n []NICStat, err error) {
	err = fs.parse("net/dev", func(r io.Reader) error { n, err = ParseNetDev(r); return err })
	return n, err
}

// SkipNIC reports interfaces that are noise on the board: loopback and the
// per-container plumbing docker creates.
func SkipNIC(name string) bool {
	return name == "lo" ||
		strings.HasPrefix(name, "veth") ||
		strings.HasPrefix(name, "br-") ||
		strings.HasPrefix(name, "docker")
}

// ParseNetDev parses /proc/net/dev, skipping interfaces per SkipNIC.
// The result is sorted by name.
func ParseNetDev(r io.Reader) ([]NICStat, error) {
	var out []NICStat
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		// Large counters can touch the colon ("eth0:123456"), so split on it.
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		if name == "" || SkipNIC(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 10 {
			return nil, fmt.Errorf("interface %s: short line", name)
		}
		var v [10]uint64
		for _, i := range []int{0, 1, 8, 9} {
			x, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("interface %s: %w", name, err)
			}
			v[i] = x
		}
		out = append(out, NICStat{Name: name, RxBytes: v[0], RxPkt: v[1], TxBytes: v[8], TxPkt: v[9]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
