package procfs

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// Socket states as printed (hex) in /proc/net/{tcp,udp}{,6}.
const (
	TCPEstablished = 0x01
	TCPListen      = 0x0A
	UDPUnconnected = 0x07 // TCP_CLOSE: a bound UDP socket with no peer
)

// Socket is the part of a /proc/net/{tcp,udp} row the agent uses.
type Socket struct {
	LocalAddr  netip.Addr // unmapped: ::ffff:127.0.0.1 becomes 127.0.0.1
	LocalPort  int
	RemotePort int
	State      uint8
}

// LocalLoopback reports a socket bound only to 127.0.0.0/8 or ::1, i.e. one
// that is not reachable from the network.
func (s Socket) LocalLoopback() bool { return s.LocalAddr.IsLoopback() }

// ScanSockets reads the socket table file (e.g. "net/tcp6") and calls fn for
// each row. A missing file (IPv6 disabled) is not an error. Busy VPN servers
// have tens of thousands of rows, so rows are streamed, never stored.
func (fs FS) ScanSockets(name string, fn func(Socket)) error {
	f, err := fs.open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := ParseSockets(f, fn); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ParseSockets parses the /proc/net/{tcp,udp}{,6} format:
//
//	sl  local_address rem_address   st tx_queue ...
//	0: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 ...
//
// Ports are big-endian hex after the last ':'. The header is skipped.
func ParseSockets(r io.Reader, fn func(Socket)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		la, lp, err1 := hexAddrPort(f[1])
		_, rp, err2 := hexAddrPort(f[2])
		st, err3 := strconv.ParseUint(f[3], 16, 8)
		if err1 != nil || err2 != nil || err3 != nil {
			return fmt.Errorf("malformed row %q", f[0])
		}
		fn(Socket{LocalAddr: la, LocalPort: lp, RemotePort: rp, State: uint8(st)})
	}
	return sc.Err()
}

// hexAddrPort decodes "0100007F:0CEA" (IPv4) or a 32-digit IPv6 address.
// The kernel prints each 32-bit word of the address with %08X in host byte
// order, so the words are converted back with the native endianness.
func hexAddrPort(s string) (netip.Addr, int, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.Addr{}, 0, fmt.Errorf("no port in %q", s)
	}
	p, err := strconv.ParseUint(s[i+1:], 16, 16)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	h := s[:i]
	if len(h) != 8 && len(h) != 32 {
		return netip.Addr{}, 0, fmt.Errorf("bad address in %q", s)
	}
	var b [16]byte // parsed word by word: no allocation per row
	for w := 0; w*8 < len(h); w++ {
		v, err := strconv.ParseUint(h[w*8:w*8+8], 16, 32)
		if err != nil {
			return netip.Addr{}, 0, fmt.Errorf("bad address in %q", s)
		}
		binary.NativeEndian.PutUint32(b[w*4:], uint32(v))
	}
	if len(h) == 8 {
		return netip.AddrFrom4([4]byte(b[:4])), int(p), nil
	}
	return netip.AddrFrom16(b).Unmap(), int(p), nil
}
