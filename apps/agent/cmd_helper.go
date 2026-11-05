package main

import (
	"context"
	"io"
	"os"
	"time"

	"vpnmap/apps/agent/internal/helper"
	"vpnmap/pkg/agentcfg"
	"vpnmap/pkg/sysexec"
)

// agentUser is the unprivileged account allowed to talk to the helper.
const agentUser = "vpnmap-agent"

// cmdHelperServe answers exactly one request on stdin/stdout. systemd starts
// it per connection (Accept=yes) on /run/vpnmap-agent/helper.sock.
func cmdHelperServe(args []string, stderr io.Writer) int {
	fs, cfgPath := newFlags("helper-serve", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := newLogger(stderr)
	reply := func(msg string) int {
		helper.WriteResponse(os.Stdout, helper.Response{OK: false, Error: msg})
		return 1
	}
	if os.Geteuid() != 0 {
		log.Error("helper-serve must run as root (it is started by vpnmap-agent-helper.socket)")
		return reply("helper is not running as root")
	}
	// Hard backstop in case anything below blocks past every deadline.
	time.AfterFunc(helper.MaxCheckTimeout+15*time.Second, func() { os.Exit(3) })

	if err := checkPeer(os.Stdin); err != nil {
		log.Warn("refused connection", "reason", err.Error())
		return reply("peer not allowed")
	}
	// The config decides what the helper may do. It must be root-owned and
	// not writable by the agent, or the agent could widen its own access.
	if err := helper.TrustedFile(*cfgPath, 0); err != nil {
		log.Error("untrusted config", "error", err.Error())
		return reply("helper config is not root-owned: " + err.Error())
	}
	cfg, _, err := agentcfg.Load(*cfgPath)
	if err != nil {
		log.Error("cannot load config", "error", err.Error())
		return reply("helper cannot load agent.yaml")
	}
	s := &helper.Server{Cfg: cfg, Runner: sysexec.Exec{}, TrustedUID: 0, Log: log}
	if err := s.ServeConn(context.Background(), os.Stdin, os.Stdout); err != nil {
		log.Warn("cannot write response", "error", err.Error())
		return 1
	}
	return 0
}
