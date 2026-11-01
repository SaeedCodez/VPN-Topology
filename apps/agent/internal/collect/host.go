// Package collect holds the agent's unprivileged collectors. Each collector
// is a small type with a Collect method; scheduling, error reporting and
// assembling the push live in package agent.
package collect

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"

	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/pkg/protocol"
)

// Host collects CPU, load, memory, disk and NIC counters from /proc.
type Host struct {
	FS     procfs.FS
	Mounts []string // statfs targets; default "/"
	// FirstSample is how long the very first CPU measurement waits between
	// two /proc/stat reads (later pushes use the previous push as baseline).
	FirstSample time.Duration

	mu   sync.Mutex
	prev *procfs.CPUStat
}

// Collect returns whatever it could read; err lists the parts that failed.
func (h *Host) Collect(ctx context.Context) (*protocol.Host, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := &protocol.Host{NumCPU: runtime.NumCPU()}
	var errs []error

	if cur, err := h.FS.Stat(); err != nil {
		errs = append(errs, err)
	} else {
		if cur.NumCPU > 0 {
			out.NumCPU = cur.NumCPU
		}
		out.BootTime = cur.BootTime
		if h.prev == nil {
			// No baseline yet: take a short second sample so the first push
			// shows real utilisation instead of the average since boot.
			first := cur
			h.prev = &first
			wait := h.FirstSample
			if wait == 0 {
				wait = 250 * time.Millisecond
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			if again, err := h.FS.Stat(); err == nil {
				cur = again
			}
		}
		out.CPUPct = round2(procfs.CPUPercent(*h.prev, cur))
		h.prev = &cur
	}
	if l, err := h.FS.Loadavg(); err != nil {
		errs = append(errs, err)
	} else {
		out.Load1, out.Load5, out.Load15 = l[0], l[1], l[2]
	}
	if u, err := h.FS.Uptime(); err != nil {
		errs = append(errs, err)
	} else {
		out.UptimeS = u
	}
	if m, err := h.FS.Meminfo(); err != nil {
		errs = append(errs, err)
	} else {
		out.MemTotal, out.MemUsed = m.Total, m.Total-m.Available
	}
	mounts := h.Mounts
	if len(mounts) == 0 {
		mounts = []string{"/"}
	}
	for _, m := range mounts {
		d, err := statDisk(m)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out.Disks = append(out.Disks, d)
	}
	if nics, err := h.FS.NetDev(); err != nil {
		errs = append(errs, err)
	} else {
		for _, n := range nics {
			out.NICs = append(out.NICs, protocol.NIC{Name: n.Name, RxB: n.RxBytes, TxB: n.TxBytes, RxP: n.RxPkt, TxP: n.TxPkt})
		}
	}
	return out, errors.Join(errs...)
}

// statDisk reports usage like df: used% = used / (used + available to users).
func statDisk(mount string) (protocol.Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return protocol.Disk{}, &pathErr{"statfs", mount, err}
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	used := (st.Blocks - st.Bfree) * bs
	avail := st.Bavail * bs
	d := protocol.Disk{Mount: mount, Total: total, Used: used}
	if used+avail > 0 {
		d.UsedPc = round2(float64(used) / float64(used+avail) * 100)
	}
	return d, nil
}

type pathErr struct {
	op, path string
	err      error
}

func (e *pathErr) Error() string { return e.op + " " + e.path + ": " + e.err.Error() }
func (e *pathErr) Unwrap() error { return e.err }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// Now is the timestamp format used in results (RFC 3339, UTC, seconds).
func Now() string { return time.Now().UTC().Format(time.RFC3339) }
