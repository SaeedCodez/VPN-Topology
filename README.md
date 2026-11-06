# VPN Topology

A private, live, n8n-style map of the VPN system: every server is a node card,
every connection an edge. At a glance it shows what is up, what is down and how
much traffic flows on each link. When something breaks, it names the first
broken hop and how many users are affected.

It is **read-only**. Nothing here changes, restarts or reconfigures a VPN
server. The map is **topology as code**: files in `topology/` describe it, the
`vpnmap` CLI validates, plans and applies them, and every view on the board is
also available as CLI output (`--json`) for an agent.

```
                     topology/*.yaml ──vpnmap validate/plan/apply──┐
                                                                    ▼
 servers: vpnmap-agent ──HTTPS push (outbound only)──►  HUB (NestJS)  ◄── Remnawave API, 3x-ui, HTTP/TLS probes
 probe (Iran): sing-box e2e ─────────────────────────►   resolve → evaluate → root cause → alerts
                                                          │ Postgres (topology versions, events)
                                                          │ VictoriaMetrics (10 s, 180 d)
                                   board (Nuxt) ◄─WS/API──┤
                                   vpnmap CLI   ◄──API────┘   (MCP server in phase 2)
```

## Status

| Phase | Item | State |
|---|---|---|
| 1 | Topology schema, 6 component files for today's system, `vpnmap validate` (file:line:fix) | done |
| 1 | Hub: ingest, collector resolvers, evaluation, root cause + blast radius, alert rules, events, time travel, REST + WebSocket, auth (password + TOTP, CLI tokens, per-agent tokens) | done |
| 1 | Demo mode: simulator that replays the real incidents through the production code path | done |
| 1 | Board: canvas, node cards, traffic edges, group frames, side panel, timeline + replay slider, light/dark | done |
| 1 | CLI: status, get, describe, diagnose, path, why, events, metrics, snapshot, plan, apply, history, rollback, layout pull, docs, agent config/nft/token/install-cmd, silence, login, admin hash-password/totp | done |
| 1 | Agent: unprivileged collector + root helper with fixed read-only ops, counter-only nft table, install/uninstall, probe mode | done, not yet run on a real server |
| 1 | Live collectors in the hub: Remnawave API, 3x-ui, HTTP/TCP/TLS probes | written, untested against the real APIs |
| 2 | Telegram delivery (Persian), dedupe, recovery, maintenance mute; read-only Telegram assistant (OpenRouter): **done**. External heartbeat; `vpnmap discover`; MCP server | next |
| 3 | Operational buttons with confirmation and audit log | later |

## Repository

| Path | What |
|---|---|
| `topology/` | `map.yaml` (groups, settings, probe source), `components/*.yaml` (one feature each), `layout.yaml` (positions, written via the UI) |
| `schema/` | JSON schemas generated from `pkg/topology` (editors + agents) |
| `pkg/topology` | the only YAML loader/validator, collector catalog, alert-expression parser, bundle |
| `pkg/protocol`, `pkg/agentcfg`, `pkg/redact` | push payload, agent config, redaction |
| `pkg/ops`, `pkg/sysexec` | closed ops action types, request format, runner config; shared safe exec |
| `apps/cli` | `vpnmap` |
| `apps/agent` | `vpnmap-agent` + `deploy/` (systemd units, install/uninstall, example local checks) — see `apps/agent/README.md` |
| `apps/ops-runner` | `vpnmap-ops`, the forced-command runner for ops actions + `deploy/` (install/uninstall, example action scripts) |
| `apps/hub` | NestJS hub; `src/engine` is pure evaluation logic, `src/sim` the demo simulator |
| `apps/web` | Nuxt 3 SPA (Vue Flow, Tailwind, ECharts), types from `apps/hub/src/contract.ts` |
| `CLAUDE.md`, `.claude/skills/` | how an agent works here: `add-component`, `diagnose-outage` |

## How it works

**Pipeline (every 5 s).** Agents push raw facts (host metrics, nft counters,
`wg show` dumps, `ss` counts, container/unit states, TCP/HTTP results, local
checks, new log lines, allow-listed Prometheus samples). Hub-side collectors poll
the Remnawave API, 3x-ui and run HTTP/TLS/TCP probes. Everything lands in one
store; one resolver per catalog collector turns it into check results; a pure
evaluator turns check results into the board snapshot. Demo mode feeds the
same store from a simulator, so the demo exercises the production code.

**Root cause.** Each route (declared in the component files) is walked from the
users toward the exit; the first failing hop is the root and the hops after it
are "affected" (unless a healthy route still uses them). A failing link whose
destination node is also failing (or whose agent went silent) points at the
node. Users affected = live online users of the broken routes (or the declared
estimate), deduplicated. Control-plane failures (panel, central, backups) are
roots of their own and mark the control links they serve.

**Overall status.** Red (outage) when a broken route serves a paying squad
(`settings.critical_squads` in `topology/map.yaml`: Premium, Economic), however
few users are online; amber (degraded) for anything else that is not healthy,
e.g. a broken Mullvad-Test or Xbox route; green when everything is healthy.

**Time travel.** Every 10 s the observations are written as series
(`c:<check>:s|v`, `l:<link>:up|down|conns`, `n:<node>:cpu…`, `r:<route>:…`).
`--at` / the slider rebuilds the board at that moment by running the same
evaluator on stored observations; check details and alerts come from events.

**Per-outbound Xray stats (first task: verified).** Remnawave node 3.4.2 always
enables `statsOutboundUplink/Downlink` and exposes `/node/stats/get-all-outbounds-stats`
on :2222 (mTLS + JWT from the panel, not usable by us). The backend resets the
node counters every 30 s and accumulates them in its own Prometheus counters
`remnawave_node_outbound_upload_bytes` / `..._download_bytes{node_uuid,tag}` on
`127.0.0.1:3001/metrics` (basic auth `METRICS_USER/PASS`). So `xray-stats`
is collected by the agent on the panel host scraping that endpoint (only
allow-listed metric names are forwarded; credentials are a copy in
`/etc/vpnmap-agent/remnawave-metrics.auth`, the agent never reads `.env`).
Data arrives in 30 s steps. E3 also declares an nftables fallback
(`daddr 31.56.89.125 dport 9000` on the panel). Confirm on the live panel:

```bash
ssh root@81.12.50.23 'curl -s -u "$METRICS_USER:$METRICS_PASS" http://127.0.0.1:3001/metrics | grep -E "^remnawave_node_(outbound_|basic_info)" | head'
```

Also found: the node's Xray gRPC API listens on an abstract unix socket that any
local user can reach (`HandlerService` without auth) — worth a look separately.

## Try it with Docker (demo, 1 command)

Simulated data and replays of the real incidents; no servers, no secrets.

```bash
git clone -b claude/vpn-topology-dashboard-ylokrt https://github.com/SaeedCodez/VPN-Topology.git
cd VPN-Topology
docker compose -f docker-compose.demo.yml up --build
```

- Board: http://localhost:8080 ("Replay incident" plays 2026-10-13 / 2026-10-20 / 2026-10-22 live)
- CLI without installing Go (the hub image contains `vpnmap`):
  `docker compose -f docker-compose.demo.yml exec -e VPNMAP_HUB=http://localhost:4000 hub vpnmap status`
- From Iran, pass mirrors: `NPM_REGISTRY=... ALPINE_MIRROR=... GOPROXY=... docker compose -f docker-compose.demo.yml up --build`
- Stop: `docker compose -f docker-compose.demo.yml down` (demo data lives in memory and is regenerated on start)

## Running

**Demo without Docker (no servers, no secrets):**

```bash
pnpm install
go build -o bin/vpnmap ./apps/cli
pnpm --filter @vpnmap/hub dev        # :4000, simulates 25 h of history in ~15 s
pnpm --filter @vpnmap/web dev        # :3000
VPNMAP_HUB=http://localhost:4000 ./bin/vpnmap status
```

**Production (Coolify, 171.22.27.214):** `docker-compose.yml` runs `hub`, `web`
(nginx serving the SPA and proxying `/api`, `/ws`, `/agent` to the hub),
`postgres` and `victoriametrics` (180 d retention, 10 s dedup). Point the
domain (map.imdev.ir) at `web:8080`. Build args `NPM_REGISTRY`,
`ALPINE_MIRROR`, `GOPROXY` take the Iranian mirrors. The hub image also
serves `/agent/install.sh`, `/agent/uninstall.sh` and the agent binaries
(+ `.sha256`). The e2e probe runs as the `probe` service for now (see below).

### Environment variables (names only; values in Coolify)

| Name | Purpose |
|---|---|
| `HUB_MODE` | `live` or `demo` |
| `PUBLIC_URL` | e.g. https://map.imdev.ir (enables secure cookies) |
| `DATABASE_URL` | Postgres (without it: embedded PGlite) |
| `VICTORIA_URL` | VictoriaMetrics (without it: in-memory, 27 h) |
| `ADMIN_PASSWORD_HASH`, `ADMIN_TOTP_SECRET`, `SESSION_SECRET` | single admin login (`vpnmap admin hash-password`, `vpnmap admin totp`) |
| `CLI_TOKEN_SHA256` | sha256 of personal CLI token(s) |
| `AGENT_TOKENS` | `node=sha256,...` per-agent push tokens (`vpnmap agent token <node>`) |
| `RW_API_URL`, `RW_API_TOKEN` | Remnawave API, dedicated monitoring user |
| `XUI_FRANCE_URL`, `XUI_FRANCE_USER`, `XUI_FRANCE_PASS` | 3x-ui on the France exit (inbound list only) |
| `PROBE_TOKEN`, `PROBE_OUTBOUND_MAIN`, `PROBE_OUTBOUND_MULLVAD_DE/US/TR`, `PROBE_SUBSCRIPTION_ID` | probe container: push token, and the monitoring user's sing-box outbounds and shortUuid |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Telegram alerts (live mode only). Empty → off. `vpnmap telegram status` / `vpnmap telegram test` |
| `TELEGRAM_API_ROOT` | the Cloudflare Worker relay URL (see below); empty = `https://api.telegram.org` |
| `NOTION_TOKEN`, `NOTION_ROOT_PAGE` | read-only Notion docs for the Telegram assistant (see below). Empty token → off; empty root → the `docs:` link in `topology/map.yaml` |
| `TOPOLOGY_ROOT`, `VPNMAP_BIN`, `AGENT_DIST_DIR` | set in the image |
| `NPM_REGISTRY`, `ALPINE_MIRROR`, `GOPROXY` | build mirrors |

### Telegram alerts

Every alert rule in the component files (`alerts:` with `message_fa`) is sent
to one chat when it fires, and again when it recovers:

- 🔴 critical / 🟠 warning, the Persian message, the board's root cause (critical
  only), the alert id + instance, Tehran time (Jalali) and a link to the board.
- ✅ «برطرف شد» with how long it lasted, sent 2 minutes after recovery; if it
  fires again inside those 2 minutes (flapping) nothing more is sent.
- Silenced alerts (`vpnmap silence <alert> 2h`) are not sent, nor their recovery.
- After a hub restart, alerts that were already firing are not announced again.
- Failed sends are retried with backoff (and Telegram's `retry_after`); the
  token never appears in logs or API answers.

Setup: create a bot with @BotFather, add it to the alert group, get the chat
id (a group id starts with `-100`), put both in Coolify, redeploy, then
`vpnmap telegram test`.

### Telegram relay (Cloudflare Worker)

The Coolify server cannot reach `api.telegram.org`, so the hub talks to
Telegram through a small Cloudflare Worker:
[`apps/hub/deploy/telegram-relay.worker.js`](apps/hub/deploy/telegram-relay.worker.js).
It forwards only `sendMessage`, `sendPhoto`, `sendDocument` (pictures of the
board), `getUpdates` and `sendChatAction`, passing the request's content-type
through (photos are multipart), and stores nothing. Set `TELEGRAM_API_ROOT` to
the Worker URL (its `workers.dev` route must be enabled; Cloudflare answers
`error code: 1042` otherwise). If you deployed the Worker before board
pictures, paste the current file again so the new methods are allowed (an old
Worker answers pictures with `404`).

### Telegram assistant (OpenRouter, read-only)

In the alert group, reply to any bot message (an alert, a recovery, an
earlier answer) or write `/ask <question>`; an OpenRouter model answers in
Persian from the same facts as the board.

- **Read-only by construction**: its only tools are `list_entities`, `status`,
  `diagnose`, `describe`, `why`, `path`, `events`, `metrics` (each with `at`),
  `board_image`, which sends a picture of the board to the same chat, and,
  when connected, `docs_read` / `docs_search` (Notion, below).
  A reply to an alert looks at the moment of that alert; a reply to an answer
  (or to a picture) continues the conversation.
- **Settings on the board** (Assistant button): on/off, OpenRouter key, model,
  allowed Telegram user ids, questions per day, a Test button, today's usage
  and cost. Changes apply at once, no redeploy. The key is stored AES-GCM
  encrypted (derived from `SESSION_SECRET`) and never returned; if
  `SESSION_SECRET` changes it has to be entered again.
- Only allowed user ids get answers; other messages in the group are not read.
  The panel shows the id of the last ignored asker, to find your own.
- Runs only in live mode, with Telegram configured; the hub long-polls
  `getUpdates` (so the bot must have no webhook).
- Privacy: questions and the board facts the model looks up (names, IPs,
  users, events) go to OpenRouter and the model's provider. Secrets are not
  part of the board data.
- **Notion docs (optional, read-only)**: with `NOTION_TOKEN` set it also gets
  `docs_read` (one page as text, in parts; no page = the main page) and
  `docs_search` (full text over every page under the root), for how/why
  questions, history and runbook commands. The live map stays the source for
  the current state; the prompt says so and treats Notion text as data.
  - Setup: notion.so/profile/integrations → New integration (internal) →
    capabilities: **Read content only** (no Update/Insert content, no
    comments, no user info). Then on the "VPN Infrastructure" page: ··· →
    Connections → add the integration (sub-pages inherit). Put the token in
    Coolify as `NOTION_TOKEN`, redeploy, and press **Test Notion** in the
    Assistant panel.
  - Why read-only: the pages are the runbook people and agents act on, and
    the assistant reads text from Telegram and, in ops mode, from server logs;
    an injected edit to the runbook would be a path to a harmful change.
  - Held three ways: the integration has no write capability, the hub only
    sends GET requests (`apps/hub/src/assistant/notion.ts`), and a page is
    served only if its parent chain reaches the root page. Page text goes
    through the same secret masking as ops output before it reaches the model.

### Ops actions (closed catalog, owner-approved)

Spec: [`docs/ops-actions.md`](docs/ops-actions.md). An approver can ask the
assistant to look at or fix something on a server. It has no shell:

- **Closed types, open data.** The action types (`vpnmap ops types`) are code
  in `pkg/ops`: container/unit status and restart, log tail, disk, TCP check,
  connection count, WireGuard handshakes, declared read/change scripts, and
  Remnawave node restart/enable/disable. What each may touch on a server is
  the node's `ops:` block plus what its checks already declare (`vpnmap ops
  show`). A new server, container or script is a YAML change, not code.
- **Enforced on the server.** `vpnmap-ops` is the forced command of the hub's
  key (`restrict,command="/usr/local/bin/vpnmap-ops serve"`; no `from=`,
  because the hub's egress changes with the VPN it uses). It validates every
  request against its root-owned `/etc/vpnmap-ops/ops.yaml`, re-runs itself
  through the single sudoers entry `vpnmap-ops exec`, validates again,
  applies cooldowns, runs a fixed argv (never a shell) and logs each call to
  `/var/log/vpnmap-ops.log`.
- **Reads** (`ops_read`) run at once; output is redacted, capped and handed
  to the model as untrusted data. **Changes** (`ops_propose`) become a plan
  an approver must tap Approve on in Telegram within 10 minutes; plans that
  cut paying users need a second tap. The hub's executor runs it,
  re-validating each step, reading the state its rollback needs first, and
  rolling back when a step fails or the map gets worse. `/stop` and the
  board's kill switch halt it before the next step.
- **Ops panel** on the board: on/off, model, approvers, per-server access,
  host-key fingerprint (required before the first connection), whether the
  runner's config matches the map, and the install commands to run yourself.
- Only the board login (password + TOTP) changes approvers, access, host keys
  or accepts the policy; the CLI token cannot. After any topology apply that
  changes an `ops:` block, change actions stay blocked until you press
  *Accept policy* in the panel.
- The Telegram relay Worker is part of the approval path: protect that
  Cloudflare account and set the Worker's `BOT_ID`.
- Disabled in demo mode (no login there).

### Pictures of the board

The hub draws the board itself (same positions, cards, links, colours and
sparklines as the web board, at any moment) and rasterises it with
[resvg](https://github.com/yisibl/resvg-js) and the board's Geist fonts
(`apps/hub/assets/fonts`, SIL OFL). No browser runs on the server.

- **From Telegram**: ask the assistant, e.g. `/ask یه عکس از بورد کاربرها تا
  سرور فرانسه تا اینترنت بفرست` or `/ask عکس کل بورد رو بفرست`. It picks the
  part (whole board, a route, a path `from → via → to`, chosen nodes or links,
  optionally with the surroundings dimmed, light or dark, at a past moment)
  and replies with the photo. A big picture goes as a PNG file so the labels
  stay readable; ask for "فایل با کیفیت کامل" to always get the file.
- **From the board**: the camera button (bottom left) opens picture mode: click
  nodes to pick them, or choose a route; nothing picked = the whole board.
  Download PNG or Send to Telegram.
- **API**: `GET /api/board/image?route=main` (or `from=users&via=france-exit&to=internet`,
  `nodes=a,b`, `links=e3`, `at=-2h`, `theme=dark`, `context=true`,
  `quality=file`, `format=svg`) returns the picture;
  `POST /api/board/image/telegram` with the same fields sends it to the alert chat.
- Shared with the board: `apps/hub/src/board/shared` (layout, icons, number
  formats); the drawing mirrors `VpnNode.vue`, `TrafficEdge.vue` and
  `GroupFrame.vue`, so change them together.

## Rollout runbook (the owner runs these; nothing here is run for you)

1. **Backup first** (Notion rule 2), then create the dedicated Remnawave
   monitoring user and API token; put it in `RW_API_TOKEN`.
2. Deploy the compose stack on Coolify with `HUB_MODE=live` and the secrets
   above. First start bootstraps topology v1 from the files in the image.
3. `vpnmap login --hub https://map.imdev.ir` (personal token; its sha256 goes in `CLI_TOKEN_SHA256`).
4. Per server (relay-1, panel, xbridger-node-3, iran-mullvad, france-exit, coolify):
   `vpnmap agent token <node>` (add the hash to `AGENT_TOKENS`), then
   `vpnmap agent install-cmd <node>` and run what it prints. The installer
   creates the `vpnmap-agent` user, the counter-only nft table `inet vpnmap`,
   the helper socket and the service; it restarts nothing else and opens no
   ports. `vpnmap agent install-cmd` also lists the local checks that node
   needs (e.g. `xbridger-doctor.check` on node 3, `remnawave-backup-age.check`
   on the panel) with their install commands.
5. `vpnmap plan` should then show no missing agents or checks.
6. Uninstall: the one-liner printed by `install-cmd` (`/agent/uninstall.sh`),
   which removes the units, the binary, `/etc/vpnmap-agent` and `table inet vpnmap`.

Day to day: `vpnmap status`, `vpnmap diagnose`, the board. Changes to the map:
see CLAUDE.md (validate → plan → owner approves → apply → docs → Notion).

## The end-to-end probe (temporarily on Coolify)

The probe acts like a real user: every 30 s it opens each route (main via
Relay 1, Mullvad DE/US/TR) with a real VPN client (sing-box), fetches
`http://cp.cloudflare.com/generate_204`, measures latency and checks the exit
country. It is the only check that sees what users see (on 2026-10-13 every
component looked "Connected" while :9000 was closed). It runs as the `probe`
container of the Coolify stack for now; it should move to an Iranian server
outside the user path, and while Coolify's location is unverified its results
show that server's vantage point.

Setup (owner):
1. **Backup first**, then create a dedicated Remnawave user (e.g. `monitoring-probe`)
   with access to the main and Mullvad inbounds. It counts as one online user.
2. Fetch its subscription in sing-box format and take one outbound per route
   (the tags are the host remarks shown in the panel):
   ```bash
   curl -s -A 'SFA/1.0' "https://wp.api.mohiraweb.ir/api/sub/<SHORT_UUID>" | jq -c '.outbounds[] | select(.type=="vless") | {tag}'
   curl -s -A 'SFA/1.0' "https://wp.api.mohiraweb.ir/api/sub/<SHORT_UUID>" | jq -c '.outbounds[] | select(.tag=="<TAG>")'
   ```
3. Put each one-line JSON in Coolify as `PROBE_OUTBOUND_MAIN`, `PROBE_OUTBOUND_MULLVAD_DE`,
   `PROBE_OUTBOUND_MULLVAD_US`, `PROBE_OUTBOUND_MULLVAD_TR`.
4. `vpnmap agent token probe`: the token goes in `PROBE_TOKEN`, the printed
   `probe=<sha256>` is appended to `AGENT_TOKENS`.
5. Put the user's `shortUuid` in `PROBE_SUBSCRIPTION_ID`: the subscription route
   fetches its real subscription, because every other path on that host answers
   502 by design. Without it the route shows "no data".

The container generates its agent config from the topology in the image, so
new routes with a probe are picked up on the next deploy. A route without an
outbound shows "no data" (grey), never a false outage; the same holds when the
probe's own client fails.

## Open questions

- **Probe server:** runs on Coolify for now; to move to an Iranian server outside the user path.
- **Coolify location:** if the hub is abroad, hub-side probes see the network
  from abroad; Iran-side checks come from agents and the probe.
- I could not read the XBridger repo's CLAUDE.md files (not reachable from
  this environment); style follows the stack described in Notion 09.
