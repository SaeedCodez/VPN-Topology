package helper

import (
	"bytes"
	"context"
	"fmt"
	"strconv"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/protocol"
)

// keyPrefix is how many characters of a public key are kept: enough to tell
// peers apart on the board, useless as a credential.
const keyPrefix = 8

// wgDump runs `wg show <iface> dump`, inside `ip netns exec <netns>` when a
// namespace is configured. The first line of the dump carries the interface's
// PRIVATE key and peer lines carry preshared keys: both are dropped while
// parsing, never converted to strings, and the raw buffer is zeroed.
func (s *Server) wgDump(ctx context.Context, netns, iface string) (protocol.WGInterface, error) {
	cmd := sysexec.Cmd{Name: "wg", Args: []string{"show", iface, "dump"}, MaxOutput: MaxResponse}
	if netns != "" {
		cmd.Name = "ip"
		cmd.Args = []string{"netns", "exec", netns, "wg", "show", iface, "dump"}
	}
	res, err := s.Runner.Run(ctx, cmd)
	defer zero(res.Stdout)
	if err != nil {
		return protocol.WGInterface{}, err
	}
	if res.Truncated {
		return protocol.WGInterface{}, fmt.Errorf("dump larger than %d bytes", MaxResponse)
	}
	return ParseWGDump(res.Stdout, netns, iface)
}

// ParseWGDump parses the tab-separated output of `wg show <iface> dump`:
//
//	line 1: private-key public-key listen-port fwmark
//	peers:  public-key preshared-key endpoint allowed-ips latest-handshake rx tx keepalive
//
// Error messages never quote input lines, since they may contain keys.
func ParseWGDump(b []byte, netns, iface string) (protocol.WGInterface, error) {
	out := protocol.WGInterface{Netns: netns, Iface: iface, Peers: []protocol.WGPeer{}}
	lines := bytes.Split(b, []byte{'\n'})
	first := true
	for n, line := range lines {
		f := bytes.Split(bytes.TrimRight(line, "\r"), []byte{'\t'})
		if len(f) == 1 && len(bytes.TrimSpace(f[0])) == 0 {
			continue
		}
		if first {
			first = false
			// f[0] is the private key: never read past this comment.
			if len(f) != 4 {
				return out, fmt.Errorf("unexpected interface line format (%d fields)", len(f))
			}
			if p, err := strconv.Atoi(string(f[2])); err == nil {
				out.Listen = p
			}
			continue
		}
		if len(f) != 8 {
			return out, fmt.Errorf("line %d: unexpected peer format (%d fields)", n+1, len(f))
		}
		// f[1] is the preshared key: skipped.
		peer := protocol.WGPeer{Key: shortKey(f[0])}
		if ep := string(f[2]); ep != "(none)" {
			peer.Endpoint = ep
		}
		hs, err1 := strconv.ParseInt(string(f[4]), 10, 64)
		rx, err2 := strconv.ParseUint(string(f[5]), 10, 64)
		tx, err3 := strconv.ParseUint(string(f[6]), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return out, fmt.Errorf("line %d: bad number", n+1)
		}
		peer.LastHandshake, peer.RxB, peer.TxB = hs, rx, tx
		out.Peers = append(out.Peers, peer)
	}
	if first {
		return out, fmt.Errorf("empty dump")
	}
	return out, nil
}

func shortKey(k []byte) string {
	if len(k) > keyPrefix {
		k = k[:keyPrefix]
	}
	return string(k)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
