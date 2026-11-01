package collect

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/protocol"
)

// unitProps are read with `systemctl show`. The monotonic timestamp is used
// for Since because the textual one is locale/timezone dependent; together
// with btime it gives an exact wall-clock time on every systemd version.
var unitProps = []string{"Id", "LoadState", "ActiveState", "SubState", "ActiveEnterTimestampMonotonic", "ActiveEnterTimestamp", "NRestarts"}

// Units reads the state of configured systemd units (read-only D-Bus query;
// works unprivileged).
type Units struct {
	Names  []string
	Runner sysexec.Runner
	// BootTime returns btime (Unix seconds) to turn monotonic timestamps
	// into wall-clock times; 0 if unknown.
	BootTime func() int64
}

// Collect runs one `systemctl show` for all units.
func (u *Units) Collect(ctx context.Context) ([]protocol.Unit, error) {
	if len(u.Names) == 0 {
		return nil, nil
	}
	args := append([]string{"show", "--no-pager", "--property=" + strings.Join(unitProps, ","), "--"}, u.Names...)
	res, err := u.Runner.Run(ctx, sysexec.Cmd{Name: "systemctl", Args: args, Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	var btime int64
	if u.BootTime != nil {
		btime = u.BootTime()
	}
	return ParseSystemctlShow(res.Stdout, u.Names, btime)
}

// ParseSystemctlShow parses `systemctl show` output: one block of key=value
// lines per unit, in argument order, separated by blank lines.
func ParseSystemctlShow(out []byte, names []string, btime int64) ([]protocol.Unit, error) {
	var blocks []map[string]string
	cur := map[string]string{}
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = map[string]string{}
		}
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		l := strings.TrimSpace(string(line))
		if l == "" {
			flush()
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		cur[k] = v
	}
	flush()
	if len(blocks) != len(names) {
		return nil, fmt.Errorf("systemctl show returned %d blocks for %d units", len(blocks), len(names))
	}
	units := make([]protocol.Unit, len(names))
	for i, b := range blocks {
		u := protocol.Unit{Name: names[i], ActiveState: b["ActiveState"], SubState: b["SubState"]}
		if b["LoadState"] == "not-found" {
			u.SubState = "not-found"
		}
		u.NRestarts, _ = strconv.Atoi(b["NRestarts"])
		u.Since = since(b, btime)
		units[i] = u
	}
	return units, nil
}

func since(b map[string]string, btime int64) string {
	if mono, err := strconv.ParseInt(b["ActiveEnterTimestampMonotonic"], 10, 64); err == nil && mono > 0 && btime > 0 {
		t := time.Unix(btime, 0).Add(time.Duration(mono) * time.Microsecond)
		return t.UTC().Format(time.RFC3339)
	}
	return b["ActiveEnterTimestamp"] // "" when never active
}
