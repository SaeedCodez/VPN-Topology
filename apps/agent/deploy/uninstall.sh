#!/bin/bash
# Remove the VPN Topology agent (vpnmap-agent) from this server.
#
#   curl -fsSL "$HUB/agent/uninstall.sh" | bash -s -- [--keep-config] [--dry-run]
#
# Stops and removes vpnmap-agent.service and vpnmap-agent-helper.socket,
# deletes nft table `inet vpnmap` (and no other table), removes the ACL
# entries granted to vpnmap-agent on log files, the binary, /etc/vpnmap-agent
# (kept with --keep-config), /var/lib/vpnmap-agent and the vpnmap-agent user.
# Idempotent: running it twice is harmless. No other service is touched.
set -euo pipefail

BIN=/usr/local/bin/vpnmap-agent
ETC=/etc/vpnmap-agent
CFG=$ETC/agent.yaml
UNIT_DIR=/etc/systemd/system
AGENT_USER=vpnmap-agent
KEEP_CONFIG=0 DRY_RUN=0

say() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}
run() {
	printf '  + %s\n' "$*"
	if [ "$DRY_RUN" = 0 ]; then "$@"; fi
}
# try is run for steps whose failure is fine (already removed).
try() {
	printf '  + %s\n' "$*"
	if [ "$DRY_RUN" = 0 ]; then "$@" 2>/dev/null || true; fi
}

parse_args() {
	while [ $# -gt 0 ]; do
		case $1 in
		--keep-config) KEEP_CONFIG=1; shift ;;
		--dry-run) DRY_RUN=1; shift ;;
		-h | --help)
			echo "usage: uninstall.sh [--keep-config] [--dry-run]"
			exit 0
			;;
		*) die "unknown option: $1" ;;
		esac
	done
}

stop_units() {
	command -v systemctl >/dev/null 2>&1 || return 0
	say "stopping vpnmap-agent units"
	local u
	for u in vpnmap-agent.service vpnmap-agent-helper.socket; do
		if [ -f "$UNIT_DIR/$u" ] || systemctl cat "$u" >/dev/null 2>&1; then
			try systemctl disable --now "$u"
		fi
	done
	# Per-connection helper instances that may still be running.
	try systemctl stop 'vpnmap-agent-helper@*.service'
}

remove_acls() {
	command -v setfacl >/dev/null 2>&1 || return 0
	getent passwd "$AGENT_USER" >/dev/null || return 0
	[ -x "$BIN" ] && [ -f "$CFG" ] || return 0
	local paths p d
	paths=$("$BIN" check-config --config "$CFG" --get log-paths 2>/dev/null) || return 0
	[ -n "$paths" ] || return 0
	say "removing $AGENT_USER ACL entries from log files"
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		if [ -e "$p" ]; then try setfacl -x "u:$AGENT_USER" "$p"; fi
		d=$(dirname "$p")
		try setfacl -d -x "u:$AGENT_USER" "$d"
		while [ "$d" != / ]; do
			try setfacl -x "u:$AGENT_USER" "$d"
			d=$(dirname "$d")
		done
	done <<<"$paths"
}

remove_nft() {
	command -v nft >/dev/null 2>&1 || return 0
	if nft list table inet vpnmap >/dev/null 2>&1; then
		say "deleting nft table inet vpnmap (counters only; no other table is touched)"
		run nft delete table inet vpnmap
	fi
}

remove_files() {
	say "removing units and files"
	try rm -f "$UNIT_DIR/vpnmap-agent.service" "$UNIT_DIR/vpnmap-agent-helper.socket" "$UNIT_DIR/vpnmap-agent-helper@.service"
	try rm -rf "$UNIT_DIR/vpnmap-agent.service.d"
	if command -v systemctl >/dev/null 2>&1; then
		try systemctl daemon-reload
		try systemctl reset-failed vpnmap-agent.service vpnmap-agent-helper.socket
	fi
	try rm -f "$BIN"
	try rm -rf /var/lib/vpnmap-agent /var/lib/private/vpnmap-agent /run/vpnmap-agent /run/vpnmap-probe
	if [ "$KEEP_CONFIG" = 1 ]; then
		say "keeping $ETC (--keep-config)"
		# The group is about to disappear: do not leave files to a stale gid.
		if [ -d "$ETC" ]; then try chgrp -R root "$ETC"; fi
	else
		try rm -rf "$ETC"
	fi
}

remove_user() {
	if getent passwd "$AGENT_USER" >/dev/null; then
		say "removing user $AGENT_USER"
		run userdel "$AGENT_USER"
	fi
	if getent group "$AGENT_USER" >/dev/null; then
		try groupdel "$AGENT_USER"
	fi
}

main() {
	parse_args "$@"
	if [ "$DRY_RUN" = 1 ]; then
		say "dry run: nothing will be changed"
	else
		[ "$(id -u)" = 0 ] || die "run as root"
	fi
	stop_units
	remove_acls
	remove_nft
	remove_files
	remove_user
	say "vpnmap-agent removed"
	if [ "$DRY_RUN" = 1 ]; then echo "    (dry run: nothing was changed)"; fi
}

main "$@"
