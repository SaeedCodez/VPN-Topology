#!/bin/bash
# Install or upgrade the VPN Topology agent (vpnmap-agent) on this server.
#
#   curl -fsSL "$HUB/agent/install.sh" | bash -s -- \
#     --hub https://map.example.com --node relay-1 --sha256 <hex> \
#     [--config-file /path/agent.yaml] [--arch amd64|arm64] [--dry-run]
#
# What it does (idempotent; safe to re-run for upgrades):
#   1. downloads vpnmap-agent-linux-<arch> from the hub and verifies its sha256
#      (aborts on mismatch, before anything is changed);
#   2. validates agent.yaml with the verified binary;
#   3. creates the system user vpnmap-agent (no login, no home);
#   4. installs /usr/local/bin/vpnmap-agent, /etc/vpnmap-agent/{agent.yaml,token,checks.d};
#   5. grants vpnmap-agent read ACLs on the configured log files;
#   6. installs and starts vpnmap-agent-helper.socket and vpnmap-agent.service.
#
# What it never does: restart or reload any other service, touch ufw/iptables
# or any nft table other than `inet vpnmap`, open a port, or print the token.
#
# Options:
#   --hub URL            hub base URL (https://, or http://127.0.0.1 for tests)
#   --node ID            topology node id of this server (must match agent.yaml)
#   --sha256 HEX         expected sha256 of the agent binary (shown by the hub)
#   --config-file PATH   agent.yaml generated with `vpnmap agent config <node>`
#                        (optional when /etc/vpnmap-agent/agent.yaml exists)
#   --arch amd64|arm64   override the detected architecture
#   --binary PATH        use a local binary instead of downloading (still verified)
#   --new-token          ask for a new push token even if one is installed
#   --dry-run            print every action without executing it
# Environment:
#   VPNMAP_TOKEN         push token (otherwise prompted from /dev/tty, hidden)
set -euo pipefail

BIN=/usr/local/bin/vpnmap-agent
ETC=/etc/vpnmap-agent
CFG=$ETC/agent.yaml
CHECKS_DIR=$ETC/checks.d
UNIT_DIR=/etc/systemd/system
AGENT_USER=vpnmap-agent
UNITS="vpnmap-agent.service vpnmap-agent-helper.socket vpnmap-agent-helper@.service"
DROPIN_DIR=$UNIT_DIR/vpnmap-agent.service.d

HUB="" NODE="" SHA="" CONFIG_FILE="" ARCH="" LOCAL_BINARY="" NEW_TOKEN=0 DRY_RUN=0
TMP=""

say() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

# run prints a command and executes it unless --dry-run.
run() {
	printf '  + %s\n' "$*"
	if [ "$DRY_RUN" = 0 ]; then "$@"; fi
}

# write_file MODE OWNER PATH: atomically write stdin to PATH.
write_file() {
	local mode=$1 owner=$2 path=$3 tmp
	if [ "$DRY_RUN" = 1 ]; then
		cat >/dev/null
		printf '  + write %s (%s, mode %s)\n' "$path" "$owner" "$mode"
		return
	fi
	tmp=$(mktemp "$path.XXXXXX")
	cat >"$tmp"
	chmod "$mode" "$tmp"
	chown "$owner" "$tmp"
	mv -f "$tmp" "$path"
	printf '  + wrote %s (%s, mode %s)\n' "$path" "$owner" "$mode"
}

usage() {
	cat <<'EOF'
usage: install.sh --hub URL --node ID --sha256 HEX [--config-file PATH]
                  [--arch amd64|arm64] [--binary PATH] [--new-token] [--dry-run]
The push token is read from VPNMAP_TOKEN or prompted (hidden) from /dev/tty.
EOF
}

parse_args() {
	while [ $# -gt 0 ]; do
		case $1 in
		--hub) HUB=${2:-}; shift 2 ;;
		--hub=*) HUB=${1#*=}; shift ;;
		--node) NODE=${2:-}; shift 2 ;;
		--node=*) NODE=${1#*=}; shift ;;
		--sha256) SHA=${2:-}; shift 2 ;;
		--sha256=*) SHA=${1#*=}; shift ;;
		--config-file) CONFIG_FILE=${2:-}; shift 2 ;;
		--config-file=*) CONFIG_FILE=${1#*=}; shift ;;
		--arch) ARCH=${2:-}; shift 2 ;;
		--arch=*) ARCH=${1#*=}; shift ;;
		--binary) LOCAL_BINARY=${2:-}; shift 2 ;;
		--binary=*) LOCAL_BINARY=${1#*=}; shift ;;
		--new-token) NEW_TOKEN=1; shift ;;
		--dry-run) DRY_RUN=1; shift ;;
		-h | --help) usage; exit 0 ;;
		*) die "unknown option: $1 (see --help)" ;;
		esac
	done
	HUB=${HUB%/}
	SHA=$(printf '%s' "$SHA" | tr 'A-F' 'a-f')
	[[ $HUB =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ || $HUB =~ ^http://(127\.0\.0\.1|localhost)(:[0-9]+)?$ ]] ||
		die "--hub must be https://host[:port] (got '${HUB}')"
	[[ $NODE =~ ^[a-z0-9][a-z0-9_.-]{0,62}$ ]] || die "--node must be a node id like relay-1"
	[[ $SHA =~ ^[0-9a-f]{64}$ ]] || die "--sha256 must be the 64-hex-digit checksum shown by the hub"
	if [ -z "$ARCH" ]; then
		case $(uname -m) in
		x86_64 | amd64) ARCH=amd64 ;;
		aarch64 | arm64) ARCH=arm64 ;;
		*) die "unsupported architecture $(uname -m); pass --arch" ;;
		esac
	fi
	[[ $ARCH == amd64 || $ARCH == arm64 ]] || die "--arch must be amd64 or arm64"
	if [ -n "$CONFIG_FILE" ] && [ ! -f "$CONFIG_FILE" ]; then die "--config-file $CONFIG_FILE does not exist"; fi
	if [ -n "$LOCAL_BINARY" ] && [ ! -f "$LOCAL_BINARY" ]; then die "--binary $LOCAL_BINARY does not exist"; fi
	return 0
}

preflight() {
	if [ "$DRY_RUN" = 1 ]; then
		say "dry run: nothing will be changed"
	else
		[ "$(id -u)" = 0 ] || die "run as root (sudo bash -s -- ...)"
	fi
	if [ ! -d /run/systemd/system ]; then
		if [ "$DRY_RUN" = 1 ]; then warn "systemd is not running here"; else die "systemd is required"; fi
	fi
	local t
	for t in sha256sum install getent; do
		command -v "$t" >/dev/null 2>&1 || die "$t is required"
	done
	if [ -z "$LOCAL_BINARY" ]; then command -v curl >/dev/null 2>&1 || die "curl is required"; fi
}

# fetch_binary downloads (or copies) the binary into $TMP and verifies it.
# Nothing on the system is changed before this succeeds.
fetch_binary() {
	local url="$HUB/agent/download/vpnmap-agent-linux-$ARCH" got
	TMP=$(mktemp -d)
	trap 'rm -rf "$TMP"' EXIT
	if [ -n "$LOCAL_BINARY" ]; then
		say "using local binary $LOCAL_BINARY"
		cp "$LOCAL_BINARY" "$TMP/vpnmap-agent"
	elif [ "$DRY_RUN" = 1 ]; then
		say "download $url"
		note "+ curl -fsSL -o <tmp>/vpnmap-agent $url  (skipped in dry run)"
		return 0
	else
		say "downloading $url"
		local proto=(--proto '=https' --tlsv1.2)
		[[ $HUB == http://* ]] && proto=()
		curl -fsSL ${proto[@]+"${proto[@]}"} --retry 3 --max-time 120 -o "$TMP/vpnmap-agent" "$url" ||
			die "download failed: $url"
	fi
	got=$(sha256sum "$TMP/vpnmap-agent" | cut -d' ' -f1)
	if [ "$got" != "$SHA" ]; then
		die "sha256 mismatch: expected $SHA, got $got. Nothing was installed."
	fi
	chmod 0755 "$TMP/vpnmap-agent"
	note "sha256 verified: $SHA"
	note "version: $("$TMP/vpnmap-agent" version)"
}

# agent_bin is the verified binary: the downloaded one, or the installed
# one when a dry run skipped the download.
agent_bin() {
	if [ -x "$TMP/vpnmap-agent" ]; then
		echo "$TMP/vpnmap-agent"
	elif [ -x "$BIN" ]; then
		echo "$BIN"
	fi
}

check_config() {
	local src=$CONFIG_FILE vbin
	if [ -z "$src" ]; then
		if [ -f "$CFG" ]; then
			src=$CFG
			say "keeping existing $CFG"
		else
			die "no agent config. On your workstation run:
    vpnmap agent config $NODE > agent.yaml
copy agent.yaml to this server and rerun with --config-file agent.yaml"
		fi
	fi
	vbin=$(agent_bin)
	if [ -z "$vbin" ]; then
		warn "dry run without a binary: config not validated"
		return 0
	fi
	say "validating $src"
	"$vbin" check-config --config "$src" --expect-node "$NODE" --get node >/dev/null ||
		die "$src is not a valid agent config for node $NODE"
	local cfg_hub
	cfg_hub=$("$vbin" check-config --config "$src" --get hub)
	if [ "${cfg_hub%/}" != "$HUB" ]; then
		warn "agent.yaml pushes to $cfg_hub, not to --hub $HUB"
	fi
}

ensure_user() {
	if getent passwd "$AGENT_USER" >/dev/null; then
		say "system user $AGENT_USER exists"
		return 0
	fi
	say "creating system user $AGENT_USER"
	local nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	run useradd --system --user-group --no-create-home --home-dir /nonexistent --shell "$nologin" "$AGENT_USER"
}

install_files() {
	local vbin
	vbin=$(agent_bin)
	say "installing $BIN"
	if [ -x "$TMP/vpnmap-agent" ]; then
		run install -o root -g root -m 0755 "$TMP/vpnmap-agent" "$BIN"
	else
		note "+ install -o root -g root -m 0755 <downloaded binary> $BIN"
	fi
	say "creating $ETC"
	run install -d -o root -g "$AGENT_USER" -m 0750 "$ETC"
	run install -d -o root -g root -m 0755 "$CHECKS_DIR"
	if [ -n "$CONFIG_FILE" ]; then
		run install -o root -g root -m 0644 "$CONFIG_FILE" "$CFG"
	fi
}

install_token() {
	local vbin tf token
	vbin=$(agent_bin)
	tf=$ETC/token
	if [ -n "$vbin" ]; then
		tf=$("$vbin" check-config --config "${CONFIG_FILE:-$CFG}" --get token-file)
	fi
	case $tf in
	"$ETC"/*) ;;
	*) warn "token_file $tf is outside $ETC; make sure only root can write it" ;;
	esac
	if [ -s "$tf" ] && [ -z "${VPNMAP_TOKEN:-}" ] && [ "$NEW_TOKEN" = 0 ]; then
		say "keeping existing push token $tf (use --new-token to replace it)"
		return 0
	fi
	say "installing push token $tf"
	if [ "$DRY_RUN" = 1 ]; then
		if [ -n "${VPNMAP_TOKEN:-}" ]; then
			note "+ take the token from \$VPNMAP_TOKEN (skipped in dry run)"
		else
			note "+ read the token from /dev/tty, hidden (skipped in dry run)"
		fi
		note "+ write $tf (root:$AGENT_USER, mode 0640)"
		return 0
	fi
	token=${VPNMAP_TOKEN:-}
	if [ -z "$token" ]; then
		{ [ -r /dev/tty ] && [ -w /dev/tty ]; } || die "no terminal to ask for the token: set VPNMAP_TOKEN"
		printf 'Push token for %s (input hidden): ' "$NODE" >/dev/tty
		IFS= read -rs token </dev/tty || true
		printf '\n' >/dev/tty
	fi
	[ -n "$token" ] || die "empty token"
	[[ $token =~ ^[[:graph:]]+$ ]] || die "the token must be a single word of printable characters"
	write_file 0640 "root:$AGENT_USER" "$tf" <<<"$token"
	token=""
	unset VPNMAP_TOKEN
}

# can_read PATH: whether the agent user can already read PATH.
can_read() {
	command -v runuser >/dev/null 2>&1 || return 1
	runuser -u "$AGENT_USER" -- test -r "$1" 2>/dev/null
}

can_traverse() {
	command -v runuser >/dev/null 2>&1 || return 1
	runuser -u "$AGENT_USER" -- test -x "$1" 2>/dev/null
}

grant_log_access() {
	local vbin paths p d
	vbin=$(agent_bin)
	[ -n "$vbin" ] || return 0
	paths=$("$vbin" check-config --config "${CONFIG_FILE:-$CFG}" --get log-paths)
	[ -n "$paths" ] || return 0
	say "granting $AGENT_USER read access to configured log files"
	if ! command -v setfacl >/dev/null 2>&1; then
		warn "setfacl not found (package 'acl'): make these files readable by $AGENT_USER yourself:"
		printf '%s\n' "$paths" | sed 's/^/      /' >&2
		return 0
	fi
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		if [ ! -e "$p" ]; then
			warn "log file $p does not exist (yet); rerun the installer once it does"
			continue
		fi
		if [ "$DRY_RUN" = 0 ] && can_read "$p"; then
			note "$p: already readable"
			continue
		fi
		# Traverse permission on every parent directory that lacks it.
		d=$(dirname "$p")
		while [ "$d" != / ]; do
			if [ "$DRY_RUN" = 1 ] || ! can_traverse "$d"; then
				run setfacl -m "u:$AGENT_USER:x" "$d"
			fi
			d=$(dirname "$d")
		done
		run setfacl -m "u:$AGENT_USER:r" "$p"
		# Default ACL so files created by logrotate stay readable.
		run setfacl -d -m "u:$AGENT_USER:r" "$(dirname "$p")"
	done <<<"$paths"
}

install_units() {
	local vbin u has_probe
	vbin=$(agent_bin)
	say "installing systemd units"
	if [ -z "$vbin" ]; then
		for u in $UNITS; do note "+ write $UNIT_DIR/$u (from vpnmap-agent unit $u)"; done
		return 0
	fi
	for u in $UNITS; do
		"$vbin" unit "$u" | write_file 0644 root:root "$UNIT_DIR/$u"
	done
	has_probe=$("$vbin" check-config --config "${CONFIG_FILE:-$CFG}" --get has-probe)
	if [ "$has_probe" = yes ]; then
		run install -d -o root -g root -m 0755 "$DROPIN_DIR"
		"$vbin" unit vpnmap-probe.conf | write_file 0644 root:root "$DROPIN_DIR/probe.conf"
	elif [ -f "$DROPIN_DIR/probe.conf" ]; then
		run rm -f "$DROPIN_DIR/probe.conf"
	fi
}

start_units() {
	local was_active=0 socket_was_active=0
	if [ "$DRY_RUN" = 0 ]; then
		systemctl is-active --quiet vpnmap-agent.service && was_active=1
		systemctl is-active --quiet vpnmap-agent-helper.socket && socket_was_active=1
	fi
	say "starting vpnmap-agent (no other service is touched)"
	run systemctl daemon-reload
	run systemctl enable --now vpnmap-agent-helper.socket
	# Restart only our own units on upgrade so the new binary/config load.
	if [ "$socket_was_active" = 1 ]; then run systemctl restart vpnmap-agent-helper.socket; fi
	if [ "$DRY_RUN" = 0 ] && command -v runuser >/dev/null 2>&1; then
		say "checking the config as $AGENT_USER"
		runuser -u "$AGENT_USER" -- "$BIN" check-config --config "$CFG" | sed 's/^/    /' || true
	fi
	run systemctl enable --now vpnmap-agent.service
	if [ "$was_active" = 1 ]; then run systemctl restart vpnmap-agent.service; fi
}

summary() {
	echo
	say "done"
	if [ "$DRY_RUN" = 1 ]; then note "(dry run: nothing was changed)"; fi
	cat <<EOF
    node:      $NODE
    hub:       $HUB
    binary:    $BIN (sha256 $SHA)
    config:    $CFG
    checks:    $CHECKS_DIR (install local checks with: install -o root -g root -m 0755 <file> $CHECKS_DIR/)
    status:    systemctl status vpnmap-agent; journalctl -u vpnmap-agent -f
    verify:    $BIN run --once --print --config $CFG
    uninstall: curl -fsSL "$HUB/agent/uninstall.sh" | bash -s --   [--keep-config] [--dry-run]
EOF
}

main() {
	parse_args "$@"
	preflight
	fetch_binary
	check_config
	ensure_user
	install_files
	install_token
	grant_log_access
	install_units
	start_units
	summary
}

# Everything is inside main so a truncated `curl | bash` download never runs
# a partial script.
main "$@"
