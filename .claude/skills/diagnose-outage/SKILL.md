---
name: diagnose-outage
description: Troubleshoot a VPN outage or degradation from the same facts as the board (status, diagnose, path, events), suggest the runbook fix, and ask the owner before anything that touches production.
---

# Diagnose an outage

You read; the owner acts. Nothing here changes a server.

## 1. Overall picture
```bash
vpnmap status            # exit 0 healthy, 1 degraded, 2 down
vpnmap diagnose          # probable root cause, users affected, evidence, runbook
```
If the owner names a time ("last night at 2am"), add `--at 2026-10-22T02:03Z`
(or `--at -6h`) to every command to replay the board at that moment.

## 2. Confirm hop by hop
```bash
vpnmap path users france-exit            # main route
vpnmap path users mullvad-de             # or --route mullvad-us, game, subscription
vpnmap why <node|link>                   # why an element is red/amber/grey
vpnmap describe <node|link>              # checks, details, recent events, runbook, Notion link
```
The root cause is the first failing hop walking from the users to the exit.
"Affected" elements are downstream consequences. Grey = no data (an agent or
collector is silent), which is not the same as down.

## 3. Timeline
```bash
vpnmap events --since 2h                 # state changes, incidents, alerts, reboots, restarts, watchdog
vpnmap metrics e3 --since 6h             # throughput / latency around the incident
```
Look for a reboot, container restart or watchdog line just before the failure.

## 4. Compare with known incidents (Notion 08 · Known Issues, Changelog)
- :9000 refused after XBridger node 3 reboot → connection mt6snj5mlbnn not
  running (fixed by PR #4 self-healing; check `xbridger doctor`).
- rw-api hung at ~100 % CPU while the container is "healthy" → panel API down,
  users keep working, subscriptions/node control fail; watchdog restarts it.
- Mullvad US handshake old (08 #21), origin TLS certs (08 #2), dead relay
  forwards (08 #4) are long-standing known issues.

## 5. Suggest the fix, then ask
- Quote the read-only diagnostic commands from `vpnmap diagnose`/`describe`
  (they are copy-only; run them only if the owner asks you to and you have
  access, and only the read-only ones).
- Propose the fix from Notion 07 · Operations Runbook (e.g. start the XBridger
  connection in the UI, `docker restart remnawave`, `mullvad-ns select us`),
  state the user impact (restarting remnawave/remnanode/realm disconnects
  users), and **ask the owner before anything that touches production**.
- After the fix: `vpnmap status` / `vpnmap path` to confirm recovery, and note
  the incident in Notion (Changelog / 08).
