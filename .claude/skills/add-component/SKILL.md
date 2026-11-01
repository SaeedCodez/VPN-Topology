---
name: add-component
description: Add or change something on the VPN map ("I added X to the VPN, add it to the map"). Reads Notion, edits one topology component file, validates, plans, and applies only after the owner approves. Never touches production servers.
---

# Add a component to the VPN map

Goal: the map describes the new reality, safely, through files and the CLI.
You never change, restart or install anything on a VPN server.

## 1. Understand what changed
- Ask what was added (new relay, XBridger node, tunnel, route, exit…) if unclear.
- Read the Notion "VPN Infrastructure" page and the relevant children:
  02 · Servers & Nodes (the server page), 01 · Architecture & Traffic Flows
  (the flow), 08 · Known Issues, 09 · XBridger when a bridge is involved.
  Follow its "Rules for agents". Never copy secrets out of Notion or servers.
- Write down: node ids/names, addresses, group (datacenter), roles, the links
  (from → to, protocol/port, data or control), which users/squads use it,
  and how each hop can be observed (see `vpnmap explain collector`).

## 2. See what the system already reports
- `vpnmap discover` (phase 2) drafts YAML for live things the files don't declare.
- Until then: `vpnmap status`, `vpnmap get agents --json` (capabilities incl.
  listening ports), the Drift list in `vpnmap status --json` / the board.

## 3. Edit exactly one component file
- New feature: `vpnmap new component <name>` then fill it in.
- Change to an existing feature: edit its file in `topology/components/`.
- Reuse node ids from other components (e.g. `users`, `internet`, `panel`).
- Every data link gets a traffic source and a health check where possible;
  add the route (hops from `users` to the exit) with users and a probe.
- Add alert rules with Persian `message_fa`.
- Runbook entries: read-only diagnostics only.
- Do not touch `topology/layout.yaml`.

## 4. Validate and plan
```bash
vpnmap validate          # fix every error: each names file:line and the fix
vpnmap plan              # diff vs the live map + server-side requirements
```

## 5. Show the owner and wait
Show the component diff (`git diff topology/`) and the plan output: added /
removed / changed nodes, links, checks, alerts, plus the agents, local checks,
agent config and nft rules that must be installed. Ask for approval. Do not
continue without an explicit yes.

## 6. Apply (after approval)
```bash
git add topology/ && git commit -m "topology: <what>"
vpnmap apply -m "<what and why>"
```

## 7. Hand over the server-side steps (owner runs them)
For each requirement from the plan, give the exact commands, e.g.
`vpnmap agent token <node>`, `vpnmap agent install-cmd <node>`,
`vpnmap agent config <node>`, the `install -o root ... checks.d/` line for a
local check. Explain what each does. Never run them yourself.

## 8. Update the docs
`vpnmap docs` → paste into Notion page 01, and add a Changelog line on the
VPN Infrastructure page (Notion rule 7).
