package collect

import (
	"errors"
	"fmt"
	"sort"

	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/protocol"
)

// Sockets derives listening ports and per-port established connection
// counts from /proc/net/{tcp,udp}{,6} in a single streaming pass. This is
// what `ss -tan state established '( sport = :2053 )'` would report, without
// running ss.
type Sockets struct {
	FS procfs.FS
	SS []agentcfg.SSConfig
}

// SocketsResult is the outcome of one pass.
type SocketsResult struct {
	// Listening are publicly bindable listeners ("tcp/2053", "udp/51821"),
	// sorted by proto then port. Sockets bound only to loopback (127.0.0.0/8,
	// ::1) are excluded: the hub uses this list to detect drift of services
	// reachable from the network, and a panel on 127.0.0.1:3000 is not one.
	Listening []string
	SS        []protocol.SSCount
}

// Collect reads the socket tables.
func (s *Sockets) Collect() (SocketsResult, error) {
	type lp struct {
		proto string
		port  int
	}
	listen := map[lp]bool{}
	local := map[int]int{}  // established, by local port
	remote := map[int]int{} // established, by remote port
	var errs []error

	for _, f := range []string{"net/tcp", "net/tcp6"} {
		err := s.FS.ScanSockets(f, func(k procfs.Socket) {
			switch k.State {
			case procfs.TCPListen:
				if !k.LocalLoopback() {
					listen[lp{"tcp", k.LocalPort}] = true
				}
			case procfs.TCPEstablished:
				local[k.LocalPort]++
				remote[k.RemotePort]++
			}
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, f := range []string{"net/udp", "net/udp6"} {
		err := s.FS.ScanSockets(f, func(k procfs.Socket) {
			if k.State == procfs.UDPUnconnected && k.RemotePort == 0 && k.LocalPort != 0 && !k.LocalLoopback() {
				listen[lp{"udp", k.LocalPort}] = true
			}
		})
		if err != nil {
			errs = append(errs, err)
		}
	}

	keys := make([]lp, 0, len(listen))
	for k := range listen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].proto != keys[j].proto {
			return keys[i].proto < keys[j].proto
		}
		return keys[i].port < keys[j].port
	})
	var res SocketsResult
	for _, k := range keys {
		res.Listening = append(res.Listening, fmt.Sprintf("%s/%d", k.proto, k.port))
	}
	for _, c := range s.SS {
		n := local[c.Port]
		if c.Side == "remote" {
			n = remote[c.Port]
		}
		res.SS = append(res.SS, protocol.SSCount{ID: c.ID, Port: c.Port, Side: c.Side, Established: n})
	}
	return res, errors.Join(errs...)
}
