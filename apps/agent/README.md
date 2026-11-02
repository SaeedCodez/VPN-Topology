# vpnmap-agent

The read-only monitoring agent of VPN Topology. It runs on every server
of the topology (and on the probe host). Every 5 seconds it pushes one JSON
report (`pkg/protocol.Push`) to the hub at `POST <hub>/api/ingest/v1/push`.

Data only flows from the server to the hub. The agent ignores the response
body apart from its HTTP status, so nothing the hub (or anyone impersonating
it) sends back can make the agent act.

## What is collected

| Collector | Source | Runs as | Interval |
|---|---|---|---|
| host | `/proc/stat` (CPU %, boot time), `/proc/loadavg`, `/proc/meminfo`, `/proc/uptime`, `statfs("/")`, `/proc/net/dev` (skips `lo`, `veth*`, `br-*`, `docker*`) | agent | every push |
| units | `systemctl show -p ActiveState,SubState,…` for the configured units (a read-only D-Bus query) | agent | every push |
| ss | count of ESTABLISHED sockets per configured port, from `/proc/net/tcp{,6}` | agent | every push |
| listening ports | listening TCP and unconnected UDP sockets from `/proc/net/{tcp,udp}{,6}`. Sockets bound only to loopback (127.0.0.0/8, ::1) are excluded. | agent | every push |
| logs | new lines of the configured files. Every line is redacted (see `pkg/redact`). | agent | every push |
| nft | `nft -j list counters table inet vpnmap` | helper | every push |
| wg | `wg show <iface> dump`, or inside `ip netns exec <ns>`. Public keys are cut to 8 characters. Private and preshared keys are dropped. | helper | every push |
| docker | `docker inspect` of the configured containers, keeping only name, state, health, started_at, restart count and image | helper | 15 s |
| tcp | TCP connect to each target, then close at once. Reports rtt or `refused`, `timeout`, `unreachable`, `dns`. | agent | 15 s (configurable) |
| http | GET with at most 3 redirects and no cookies. At most 64 KiB of the body is read, then discarded. Reports status, latency and days left on the certificate. | agent | 15 s (configurable) |
| prom | local Prometheus endpoints (loopback only). Only allow-listed metric names are forwarded. | agent | 15 s (configurable) |
| checks | `/etc/vpnmap-agent/checks.d/<id>.check` (see below) | helper | 15 s (configurable) |
| probe | end-to-end probes through sing-box (probe host only) | agent + sing-box child | 30 s (configurable) |

Counters stay cumulative; the hub computes rates. Cheap collectors run right
before each push so counters line up with the push timestamp. Slow
collectors run on their own tickers, and each push carries their latest
result.

When a collector fails (nft is missing, a log file is unreadable, the helper
is not running), the push gets a `CollectorError` entry (for example
`{"collector":"nft","error":"helper not available"}`) and the board shows
"no data" with that reason. A failing collector never stops the agent; even
a panic is recovered.

When the hub cannot be reached, the agent keeps collecting. Retries back
off from 5 s up to 60 s. It logs once when pushes start failing, once if the
kind of error changes, and once when they recover. Log lines read during an
outage are queued (at most 200 lines / 64 KiB per log, newest kept) until a
push succeeds.

## Privilege model

```
          vpnmap-agent.service                 vpnmap-agent-helper.socket (root)
 ┌──────────────────────────────────┐        /run/vpnmap-agent/helper.sock 0660 root:vpnmap-agent
 │ user vpnmap-agent                │  JSON  ┌─────────────────────────────────────────────┐
 │ no capabilities, NoNewPrivileges │ ─────▶ │ vpnmap-agent-helper@.service (one per call)  │
 │ ProtectSystem=strict, …          │ ◀───── │ fixed ops: nft-counters, wg-dump,            │
 │ reads /proc, logs (ACL), pushes  │        │ docker-ps, check; args must be in agent.yaml │
 └──────────────────────────────────┘        └─────────────────────────────────────────────┘
```

* **The agent** runs as the system user `vpnmap-agent`. It has an empty
  capability set, `NoNewPrivileges`, a read-only filesystem
  (`ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`), no
  namespaces and only AF_INET/AF_INET6/AF_UNIX sockets. Its limits are
  `MemoryMax=64M`, `CPUQuota=10%` and `Nice=10`. Well-known secret paths
  (`/opt/remnawave/.env`, `/opt/remnanode/.env`, `/etc/wireguard`,
  `/etc/mullvad-ns`, `/etc/x-ui`, `/root`, …) are `InaccessiblePaths`, and
  the log collector refuses secret-looking files (`.env`, `.key`, `.pem`,
  `.db`, …) even when agent.yaml lists them.
* **The helper** is the only privileged part. systemd starts it once per
  connection on a unix socket that only root and the `vpnmap-agent` group
  can open. It reads one request line (at most 4 KiB) and runs one of four
  fixed read-only operations. Every argument must match an entry of
  `/etc/vpnmap-agent/agent.yaml`, which is owned by root. The helper refuses
  to run if that file, or any directory above it, can be written by anyone
  other than root. The agent therefore cannot widen what the helper does.
  Unknown ops, path tricks like `../x`, ids that are not configured and
  oversized requests are rejected before any command runs. Everything ends
  within 20 s; a check gets its own timeout plus 5 s, at most 35 s.
* **`nft-apply`** runs as root from `ExecStartPre=-+` and only ever loads
  table `inet vpnmap`. That table has counter rules only: every chain has
  policy accept and no rule has a verdict, so it cannot change what other
  tables accept or drop. Before loading, the rendered script is checked a
  second time.

## Files

| Path | Owner / mode | What |
|---|---|---|
| `/usr/local/bin/vpnmap-agent` | root 0755 | the binary (agent, helper, tools) |
| `/etc/vpnmap-agent/` | root:vpnmap-agent 0750 | config directory |
| `/etc/vpnmap-agent/agent.yaml` | root 0644 | generated by `vpnmap agent config <node>` |
| `/etc/vpnmap-agent/token` | root:vpnmap-agent 0640 | push token |
| `/etc/vpnmap-agent/checks.d/` | root 0755 | local checks (root-owned, 0755) |
| `/var/lib/vpnmap-agent/state.json` | vpnmap-agent 0600 | log positions (inode + offset) |
| `/run/vpnmap-agent/helper.sock` | root:vpnmap-agent 0660 | helper socket |
| `/etc/systemd/system/vpnmap-agent.service` | | main unit |
| `/etc/systemd/system/vpnmap-agent-helper.socket`, `vpnmap-agent-helper@.service` | | helper |
| `/etc/systemd/system/vpnmap-agent.service.d/probe.conf` | | probe host only |

The unit files ship inside the binary (`vpnmap-agent unit --list`). The
installer writes them from there, so the binary's sha256 check covers them.

## Install, upgrade, uninstall

1. On your workstation, generate the config: `vpnmap agent config relay-1 > agent.yaml`.
   Then copy it to the server.
2. On the server, as root:

   ```sh
   curl -fsSL "https://map.example.com/agent/install.sh" | bash -s -- \
     --hub https://map.example.com --node relay-1 --sha256 <hex shown by the hub> \
     --config-file ./agent.yaml
   ```

   The installer asks for the push token on the terminal without echoing
   it. You can also pass it in `VPNMAP_TOKEN`. Add `--dry-run` to print
   every action without changing anything.

The installer verifies the binary's sha256 before it changes anything. It
then validates agent.yaml with the verified binary, creates the user, and
installs the binary, config, token and units. It grants read ACLs on the
configured log files, plus a default ACL on their directory so rotated files
stay readable. Finally it starts the socket and the service.

Re-running the installer upgrades in place. It keeps the token unless you
pass `--new-token`, and it restarts only vpnmap-agent's own units. It never
restarts or reloads any other service, never touches ufw, iptables or other
nft tables, and never opens a port.

To uninstall:

```sh
curl -fsSL "https://map.example.com/agent/uninstall.sh" | bash -s --   # [--keep-config] [--dry-run]
```

The uninstaller stops and removes the units, deletes nft table
`inet vpnmap` (and no other table), and removes the ACL entries it granted.
It also removes the binary, `/var/lib/vpnmap-agent`, `/etc/vpnmap-agent`
(unless `--keep-config`) and the user.

## Adding a local check

A check is an executable `/etc/vpnmap-agent/checks.d/<id>.check` that prints
one JSON object:

```json
{"status": "ok|degraded|down", "value": 12.5, "detail": "short text", "data": {"any": "json"}}
```

* `id` must match `^[a-z0-9][a-z0-9_.-]{0,62}$` and be listed in agent.yaml
  under `checks:`, optionally with `interval` (default 15s) and `timeout`
  (default 10s, at most 30s).
* The file must be a regular file owned by root, not group- or
  world-writable, in a directory chain with the same property. Install it
  with `install -o root -g root -m 0755 <file> /etc/vpnmap-agent/checks.d/`.
* The helper runs it as root with only `PATH=/usr/sbin:/usr/bin:/sbin:/bin`
  and `LANG=C` set, stdin set to `/dev/null`, the timeout, and a 64 KiB
  stdout limit.
* Output that is not valid JSON, or has a status outside ok/degraded/down,
  becomes status `unknown` with a reason. `detail` and every string in
  `data` are redacted again by the helper.
* Checks must be read-only: report, never fix.

There are examples in `deploy/checks.d/`:

* `xbridger-doctor.check`: maps `xbridger doctor --json` to the contract
  and folds its per-connection checks into `data.connections`. Without
  python3 it falls back to jq or the exit code, which give the overall
  status only, so the hub then sees no connection.
* `relay-forwards.check`: reports whether realm is active and how many
  forwards the `relay-ctl status` table lists.
* `remnawave-backup-age.check`: reports the backup age in minutes
  (degraded above 35, down above 45).
* `rw-api.check`: GETs the panel API on loopback with the reverse-proxy
  headers Remnawave requires and reports the latency in ms (degraded above
  2 s, down on a timeout, no answer or a 5xx).

## Verify

```sh
vpnmap-agent check-config --config /etc/vpnmap-agent/agent.yaml       # validate config and files
runuser -u vpnmap-agent -- vpnmap-agent check-config                   # ...as the agent user
vpnmap-agent run --once --print --config /etc/vpnmap-agent/agent.yaml  # one payload, printed, not pushed
vpnmap-agent run --once --config /etc/vpnmap-agent/agent.yaml          # push one payload
vpnmap-agent render-nft --config /etc/vpnmap-agent/agent.yaml          # the counter-only ruleset
journalctl -u vpnmap-agent -f
```

`run --once` never moves the stored log positions, so it cannot take lines
away from the running service.

## Probe mode

When agent.yaml has a `probe:` section (on the probe host, `node: probe`, so
the push has `kind: "probe"`), the agent also runs one `sing-box run` child
per route.

* Each child has a `mixed` inbound on `127.0.0.1:<free port>`, the only
  listener the agent ever creates.
* The route's `outbound_file` (a local file with the monitoring user's
  credentials) is tagged `probe-out` and used as the final outbound.
* Every interval (default 30s) the agent fetches `url` (default
  `http://cp.cloudflare.com/generate_204`, which must answer 204) and then
  `trace_url` through that proxy.
* The probe reports latency, exit IP and exit country, and compares the
  country with `expect_country`.
* A crashed sing-box is restarted with backoff.
* The generated configs live in `/run/vpnmap-probe` (0700) and are deleted
  on exit. Neither the outbound file nor the generated config is ever
  logged or sent.

The installer adds `deploy/vpnmap-probe.conf` as a drop-in. It sets
`RuntimeDirectory=`, raises the memory and CPU limits, allows netlink and
turns `MemoryDenyWriteExecute` off for sing-box. Recent sing-box versions
may also want a `domain_resolver` in the outbound when its `server` is a
hostname; put it in the outbound file.

## What the agent never does

* It never restarts, stops, reloads or reconfigures any service, container
  or firewall. Its only write to the system is table `inet vpnmap`, from
  `nft-apply`.
* It never listens on a network port. Probe mode's sing-box binds
  127.0.0.1 only.
* It never reads `.env` files, keys, `x-ui.db`, `/etc/wireguard`,
  `/etc/mullvad-ns` or `/root`. These are blocked by systemd and by the
  agent itself.
* It never sends secrets. Log lines and check output are redacted.
  WireGuard private and preshared keys are dropped while parsing. Docker
  env, mounts, labels and args are never read. Prometheus credentials go
  only to loopback and redirects are not followed.
* It never sends the push token anywhere but the configured hub, and never
  in clear text. Plain http is allowed only to loopback, and redirects are
  refused.
* It never acts on anything the hub replies.

## Development

```sh
go test ./apps/agent/... ./pkg/redact/...
go vet ./apps/agent/... ./pkg/redact/...
go build -ldflags "-X main.version=1.0.0" -o vpnmap-agent-linux-amd64 ./apps/agent
GOARCH=arm64 go build -ldflags "-X main.version=1.0.0" -o vpnmap-agent-linux-arm64 ./apps/agent
```

The tests use fixtures and temp dirs only: `/proc` files, nft JSON, a wg
dump with a fake private key, docker output and Prometheus text. The HTTP
tests use `httptest` servers, the probe tests use a fake sing-box (the test
binary itself) and the helper tests use a unix socket in a temp dir. The
installer is tested in `--dry-run` with stubs that fail on any
system-changing command.
