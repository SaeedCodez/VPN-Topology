package helper

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/pkg/protocol"
)

// serveUnix emulates systemd's Accept=yes socket: one Server per connection.
func serveUnix(t *testing.T, s *Server) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "helper.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				s.ServeConn(context.Background(), c, c)
			}()
		}
	}()
	return sock
}

func TestClientRoundTrip(t *testing.T) {
	f := &sysexec.Fake{Responses: map[string]sysexec.FakeResponse{
		"nft -j list counters table inet vpnmap": {Stdout: fixture(t, "nft-counters.json")},
	}}
	c := &Client{Socket: serveUnix(t, &Server{Cfg: testConfig(), Runner: f})}
	var got []protocol.NftCounter
	if err := c.Call(context.Background(), OpNftCounters, nil, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %+v", got)
	}
	err := c.Call(context.Background(), OpCheck, []string{"../../etc/shadow"}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid check id") {
		t.Errorf("err = %v", err)
	}
	err = c.Call(context.Background(), "exec", []string{"id"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Errorf("err = %v", err)
	}
}

func TestClientUnavailable(t *testing.T) {
	c := &Client{Socket: filepath.Join(t.TempDir(), "missing.sock")}
	if err := c.Call(context.Background(), OpDockerPS, nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestClientTimeout(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "slow.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second) // never answers in time
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := (&Client{Socket: sock}).Call(ctx, OpDockerPS, nil, nil); err == nil {
		t.Error("want timeout error")
	}
	if time.Since(start) > time.Second {
		t.Error("client ignored its deadline")
	}
}
