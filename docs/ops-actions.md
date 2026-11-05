# Ops actions: a closed, server-enforced action catalog (spec)

Status: **approved by the owner 2026-11-05** (interview the same day; revised
to be generic and topology-driven, with no source-IP pinning). Implemented in
`pkg/ops`, `apps/ops-runner`, `apps/hub/src/ops` and the topology `ops:` blocks.

The Telegram assistant may read and change the VPN only through named
actions. There is no shell, no free-form command, path or URL, and no
break-glass. Anything not covered stays manual for the owner.

## 1. Two layers: closed action types (code), open data (topology)

The design follows the collector catalog: the *kinds* of thing are closed and
live in code; *which* servers, containers, units or scripts they apply to is
data in `topology/components/*.yaml`, reviewed with `vpnmap validate` /
`plan` / `apply` like everything else. Nothing in the code names a server,
container, unit, country or relay of today's VPN.

- **Action types** (closed, `pkg/ops/types.go`, one source of truth;
  `vpnmap ops types --write` writes `apps/hub/src/ops/types.gen.json`, CI fails
  if stale). Adding a *type* is a code change reviewed like a new collector.
- **Per-node `ops:` block** (data). Adding a server, node, container, unit,
  log or script is a YAML change: `vpnmap validate` → `plan` → owner approves
  → `apply` → owner installs the regenerated runner config on that server.
- **Script actions** cover server-specific tools (mullvad-ns, xbridger,
  relay-ctl, game-gw, and whatever comes next) without a code change: a
  root-owned script that the owner installs in `/etc/vpnmap-ops/actions.d/`,
  declared in the `ops:` block with typed parameters. This is the same
  contract as `local-check` for the agent. The hub never ships or edits
  scripts. Examples live in `apps/ops-runner/deploy/actions.d/`.
- **Derived, not hand-listed:** verify ids, "cuts paying users", TCP targets,
  ports, WireGuard interfaces and Remnawave node names are computed from the
  live topology where possible, so they follow the map when it changes.

## 2. Action types

`R` = read (no approval), `C` = change (one owner approval in Telegram).

| Type | R/C | Parameters (values come from) | Runs |
|---|---|---|---|
| `container.list` | R | – | `docker ps` for the node's declared containers |
| `container.stats` | R | `container` (ops.containers) | `docker stats --no-stream` |
| `container.logs` | R | `container`, `since` ∈ {5m,15m,1h}, `tail` ∈ {50,100,200} | `docker logs` |
| `container.restart` | C | `container` (ops.containers with `restart: true`) | `docker restart` |
| `unit.status` | R | `unit` (ops.units) | `systemctl show` (state, since, restarts) |
| `unit.restart` | C | `unit` (ops.units with `restart: true`) | `systemctl restart` |
| `log.tail` | R | `log` (ops.logs: id → absolute path), `lines` ∈ {20,50,100} | last lines of that file |
| `disk.usage` | R | – | `df` of `/` |
| `tcp.check` | R | `target` (every `tcp-connect` check with `from: <this node>`, plus ops.tcp_targets) | Go TCP dial, 5 s |
| `conns.count` | R | `port` (every `ss-conns` collector on this host) | established TCP count |
| `wg.handshakes` | R | `iface` (every `wg-handshake`/`wg-transfer` collector on this host, with its netns) | handshake age + bytes, never keys |
| `script.read` | R | `script` (ops.scripts kind read) + its declared params | `/etc/vpnmap-ops/actions.d/<script>` |
| `script.change` | C | `script` (ops.scripts kind change) + its declared params | same |
| `rw.nodes`, `rw.system` | R | – | Remnawave `GET /api/nodes`, `GET /api/system/stats`, fixed fields |
| `rw.node-restart` / `rw.node-disable` / `rw.node-enable` | C | `node` (Remnawave node names from `remnawave-node` checks whose topology node lists the action in `ops.remnawave`) | fixed `POST /api/nodes/{uuid}/actions/…`; uuid looked up by name, never from the model |

Script parameters may be: `enum` (list), `int` (min/max), or `pattern` (an
anchored RE2). Whatever the pattern says, every value must also be a token:
ASCII letters, digits and `. _ @ : -`, at most 64 bytes, never starting with
`-`, never containing `..`. Scripts receive them as argv (`--name value`),
never through a shell.

Each change type has defaults that the `ops:` entry may tighten:

| Field | Meaning | Default |
|---|---|---|
| `cooldown` | per action + args, enforced by hub **and** runner | 10m |
| `disruptive` | interrupts the traffic of this node | true for restarts, false for enable |
| `rollback` | action (+args) run if verify gets worse; `null` = none | none for restarts; disable↔enable |
| `read_before` | a read script whose output is saved and may be passed to the rollback (`{before.<field>}`) | – |
| `verify` | extra map ids (may use `{param}`); always plus the derived ones | – |

**Derived verify ids:** the node itself, every link from/to it and every
route with a hop touching it. `validate` checks that every `{param}`
substitution yields an existing id for every enum value.

**Derived "cuts paying users":** a disruptive change on a node that is a hop
of a route serving a squad in `settings.critical_squads`. The approval then
starts with a red warning and needs a second "yes, cut users" tap. Add a
new server on the main route and its restarts get the warning automatically.

## 3. The `ops:` block (example with today's data)

```yaml
- id: iran-mullvad
  ops:
    containers:
      - { name: remnanode }                      # read only (no restart: true)
    units:
      - { name: "mullvad-ns@de.service" }        # status only
    scripts:
      - id: mullvad-status
        kind: read
        params: [{ name: cc, enum: [de, us, tr, fr, all] }]
      - id: mullvad-exit-check
        kind: read
        params: [{ name: cc, enum: [de, us, tr, fr] }]
      - id: mullvad-select
        kind: change
        params:
          - { name: cc, enum: [de, us, tr, fr] }
          - { name: relay, pattern: "^[a-z]{2}-[a-z]{3}-wg-[0-9]{3}$" }
        read_before: { script: mullvad-status, args: { cc: "{cc}" } }
        rollback: { script: mullvad-select, args: { cc: "{cc}", relay: "{before.relay}" } }
        verify: ["link.e8-{cc}", "route.mullvad-{cc}"]
        cooldown: 15m

- id: panel
  ops:
    containers:
      - { name: remnawave, restart: true }       # rw-api hang (2026-10-20/22)
      - { name: remnawave-db }                   # read only, never restart
    logs:
      - { id: watchdog, path: /var/log/remnawave-watchdog.log }

- id: local-node
  ops:
    remnawave: [restart, disable, enable]        # node name from its remnawave-node check
```

The initial `ops:` blocks encode the interview answers (section 6). They are
data: the owner extends them later the same way as any other map change.

## 4. Server side: `vpnmap-ops` (separate static Go binary, `apps/ops-runner`)

Independent of `vpnmap-agent`; the hub still never sends commands to the
collector agents. Static binary, so it also installs on a server whose apt
is broken (France).

- System user `vpnmap-ops`, locked password, shell `/bin/sh` (sshd runs the
  forced command through the login shell).
- `authorized_keys`, one line:
  `restrict,command="/usr/local/bin/vpnmap-ops serve" ssh-ed25519 AAAA… vpnmap-hub`
- **No `from=` / no source-IP pinning anywhere** (owner decision): the hub
  reaches servers through whatever VPN is in use during an outage, and that
  changes. Security does not depend on the hub's address. Instead:
  - the private key is generated on the hub, stored encrypted (secretbox),
    never shown or exported, and rotatable from the Ops panel;
  - a stolen key still reaches only the catalog in the server's own config,
    with server-side cooldowns and an audit log;
  - the hub pins each server's host key (entered by the owner before the
    first connect), so a changing network path cannot impersonate a server.
- `serve` (unprivileged) parses `SSH_ORIGINAL_COMMAND` = `<type> k=v …`,
  validates it, then runs `sudo -n /usr/local/bin/vpnmap-ops exec` with the
  request on stdin.
- `/etc/sudoers.d/vpnmap-ops`, exactly:
  `vpnmap-ops ALL=(root) NOPASSWD: /usr/local/bin/vpnmap-ops exec`
- `exec` (root) validates **again**, checks the cooldown in
  `/var/lib/vpnmap-ops/`, runs a fixed argv (no shell, minimal env, timeout,
  64 KiB cap), and appends one JSON line per call (time, request id, type,
  args, exit code, duration, output bytes) to `/var/log/vpnmap-ops.log`
  (root, 0600) and the journal.
- **Server-side allowlist:** `/etc/vpnmap-ops/ops.yaml` (root, 0644), generated
  by `vpnmap ops config <node>` from the node's `ops:` block plus the derived
  values (targets, ports, interfaces). The runner refuses everything not in
  it, and refuses to start if the binary, the sudoers file, the config or
  `actions.d` is writable by anyone but root. The hub cannot change it.
- Every reply carries the config's hash. The hub compares it with the hash
  of the applied topology; a mismatch shows as "runner config out of date"
  in the Ops panel and in `vpnmap plan`, and change actions on that server
  are refused until the owner installs the new config.

## 5. Plans, approval and the executor

- Reads: the model calls a read action; output is redacted (`pkg/redact`
  rules), capped at 16 KiB and handed back inside `<untrusted-output>` with a
  note that it is data, not instructions.
- Changes: the model proposes a plan (title, why, impact, 1–5 steps
  `{type, args}`). The hub validates every step **and its rollback** against
  the types and the live `ops:` data, and refuses the whole plan on any error.
- Approval message: every step with every parameter, its rollback, its verify
  ids, any read-before value and backup, untruncated, split over several
  messages if needed. UUID callback data; only approvers in the configured
  chat; expires after 10 minutes.
- `UPDATE ops_tasks SET status='approved' … WHERE id=$1 AND status='pending'
  AND hash=$2`: a double tap or a changed plan runs nothing; the hash is
  checked again right before running.
- Loop: baseline of verify ids → per step: stopped? → re-validate step and
  rollback → read-before → run → settle → compare; worse or failed → roll
  back completed reversible steps in reverse order (re-validated) and stop.
- `/stop` and the board's kill switch halt before the next step.
- No global hourly cap and no quiet hours (owner decisions); only per-action
  cooldowns.
- Ops endpoints require auth even in demo mode; in demo mode the executor
  talks to the simulator, never SSH.

## 6. Initial data (interview 2026-11-05)

These go into the `ops:` blocks, not into code.

- **Change (one approval):** restart container `remnawave` (panel);
  `xbridger-conn` start/restart script (xbridger-node-3, connection ids as an
  enum); `mullvad-select` script (iran-mullvad); restart `x-ui.service`
  (france-exit); Remnawave restart/disable/enable for Local-Node and
  Iran-Mullvad.
- **Never restartable** (entries marked `never_restart: true`; validate
  refuses `restart: true` next to it, so nobody adds it by accident): `realm.service`
  (relay-1), container `backend-app-1` (xbridger-node-3), `remnawave-db`,
  `remnawave-redis`. The `remnanode` container only via `rw.node-restart`.
- **Reads:** containers/CPU/logs/watchdog/backup on the panel; `:9000` check,
  `xbridger doctor`/`conn show` (never `--reveal`), connection counts and
  `relay-ctl status`; Mullvad status, handshakes, exit check, `game-gw
  status`; unit status and disk everywhere.
- **Rollout:** iran-mullvad first, then the Remnawave API actions, then
  panel, xbridger-node-3, relay-1 (reads), france-exit.

## 7. Never possible (no action type exists)

Reboot; any file edit, copy or upload; relay-ctl add/remove/edit/apply; UFW,
iptables, nft; Remnawave config profiles, squads, hosts or users; DB restore;
upgrades, `docker compose up/pull/--build`; package installs; reading files
that are not declared in `ops.logs`; anything with `--reveal`.

## 8. Adding things later

| You add | You do |
|---|---|
| a server | node with `agent`/`ops:` in YAML → `validate`/`plan`/`apply` → Ops panel prints the grant command and runner config for it → enter its host-key fingerprint → switch access on |
| a container/unit/log to an existing server | edit its `ops:` block → `apply` → install the new `ops.yaml` (the panel shows it is out of date) |
| a server-specific tool | write a script (example dir), install it in `actions.d`, declare it under `ops.scripts` |
| a Remnawave node | its `remnawave-node` check + `ops.remnawave: [...]` |
| a new kind of action | code change in `pkg/ops/types.go` + runner + hub, reviewed like a new collector |
