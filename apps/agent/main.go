// Command vpnmap-agent is the read-only monitoring agent of VPN Topology.
//
//	vpnmap-agent run          --config /etc/vpnmap-agent/agent.yaml [--once [--print]]
//	vpnmap-agent helper-serve --config /etc/vpnmap-agent/agent.yaml   (root, via systemd socket)
//	vpnmap-agent render-nft   --config ...
//	vpnmap-agent nft-apply    --config ...                            (root, ExecStartPre=+)
//	vpnmap-agent check-config --config ... [--expect-node N] [--get KEY]
//	vpnmap-agent unit         <name>|--list                           (print a bundled systemd unit)
//	vpnmap-agent version
//
// The agent never restarts, stops or reconfigures anything, never listens on
// a network port and never reads secret files. See apps/agent/README.md.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// DefaultConfig is where the installer puts agent.yaml.
const DefaultConfig = "/etc/vpnmap-agent/agent.yaml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "helper-serve":
		return cmdHelperServe(rest, stderr)
	case "render-nft":
		return cmdRenderNft(rest, stdout, stderr)
	case "nft-apply":
		return cmdNftApply(rest, stdout, stderr)
	case "check-config":
		return cmdCheckConfig(rest, stdout, stderr)
	case "unit":
		return cmdUnit(rest, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "vpnmap-agent %s\n", version)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "vpnmap-agent: unknown command %q\n\n", cmd)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: vpnmap-agent <command> [flags]

commands:
  run           collect and push to the hub every interval (main service)
                  --once          collect a single time
                  --print         with --once: print the JSON payload, do not push
  helper-serve  answer one privileged read request on stdin/stdout (root;
                started by vpnmap-agent-helper.socket, not by hand)
  render-nft    print the counter-only nft ruleset for table inet vpnmap
  nft-apply     apply that ruleset with nft -f - (root; only table inet vpnmap)
  check-config  validate agent.yaml and the files it references
  unit          print a bundled systemd unit (used by install.sh)
  version       print the version

common flags:
  --config PATH   agent config (default /etc/vpnmap-agent/agent.yaml)
`)
}

// newFlags returns a FlagSet with --config.
func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := fs.String("config", DefaultConfig, "path of agent.yaml")
	return fs, cfg
}

// newLogger logs to stderr. Under systemd (JOURNAL_STREAM set) the time is
// dropped because journald records it already.
func newLogger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{}
	if os.Getenv("JOURNAL_STREAM") != "" {
		opts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// stateDir is systemd's StateDirectory, or the documented default.
func stateDir() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return d
	}
	return "/var/lib/vpnmap-agent"
}

const onceTimeout = 90 * time.Second
