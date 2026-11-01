package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"vpnmap/apps/agent/internal/agent"
	"vpnmap/apps/agent/internal/helper"
	"vpnmap/apps/agent/internal/procfs"
	"vpnmap/apps/agent/internal/push"
	"vpnmap/apps/agent/internal/sysexec"
	"vpnmap/apps/agent/internal/tail"
	"vpnmap/pkg/agentcfg"
)

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs, cfgPath := newFlags("run", stderr)
	once := fs.Bool("once", false, "collect a single time")
	print := fs.Bool("print", false, "with --once: print the payload instead of pushing")
	socket := fs.String("helper-socket", helper.DefaultSocket, "privileged helper socket")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *print && !*once {
		fmt.Fprintln(stderr, "vpnmap-agent: --print needs --once")
		return 2
	}
	log := newLogger(stderr)
	cfg, sha, err := agentcfg.Load(*cfgPath)
	if err != nil {
		log.Error("cannot load config", "error", err.Error())
		return 1
	}
	hostname, _ := os.Hostname()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	deps := agent.Deps{
		FS: procfs.Default, Runner: sysexec.Exec{}, Helper: &helper.Client{Socket: *socket},
		Log: log, Hostname: hostname, Version: version,
	}
	// A one-off run must not move the log positions of the running service,
	// or the lines it reads would never reach the hub.
	deps.State = tail.LoadState(filepath.Join(stateDir(), "state.json"), *once)

	var pusher *push.Client
	if !*print {
		if pusher, err = push.New(cfg.Hub, cfg.TokenFile, version); err != nil {
			log.Error("cannot set up push", "error", err.Error())
			return 1
		}
		deps.Pusher = pusher
	}
	a, err := agent.New(cfg, sha, deps)
	if err != nil {
		log.Error("invalid config", "error", err.Error())
		return 1
	}

	if !*once {
		if err := a.Run(ctx); err != nil {
			log.Error("agent stopped", "error", err.Error())
			return 1
		}
		return 0
	}

	ctx, cancel := context.WithTimeout(ctx, onceTimeout)
	defer cancel()
	p := a.Once(ctx)
	if *print {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(p); err != nil {
			log.Error("cannot encode payload", "error", err.Error())
			return 1
		}
		return 0
	}
	if err := pusher.Push(ctx, p); err != nil {
		fmt.Fprintf(stderr, "push to %s failed: %v\n", pusher.URL(), err)
		return 1
	}
	fmt.Fprintf(stdout, "pushed seq=%d to %s (%d collector errors)\n", p.Seq, pusher.URL(), len(p.Errors))
	return 0
}
