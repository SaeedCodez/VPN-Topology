#!/bin/bash
# Install or upgrade the ops action runner (vpnmap-ops) on this server.
# Run it yourself, as root; the hub never runs it (CLAUDE.md rule 3). The
# board's Ops panel prints the exact command for each server.
#
#   curl -fsSL "$HUB/agent/ops-install.sh" | sudo bash -s -- \
#     --hub https://map.example.com --node iran-mullvad --sha256 <hex> \
#     --key 'ssh-ed25519 AAAA… vpnmap-hub' --config-file /tmp/ops.yaml \
#     [--actions mullvad-status:<sha256>,mullvad-select:<sha256>] [--arch amd64|arm64] [--dry-run]
#
# What it does (idempotent):
#   1. downloads vpnmap-ops-linux-<arch> from the hub, verifies its sha256;
#   2. validates the runner config (from `vpnmap ops config <node>`);
#   3. creates the system user vpnmap-ops (locked password, shell /bin/sh);
#   4. installs /usr/local/bin/vpnmap-ops, /etc/vpnmap-ops/ops.yaml and the
#      listed action scripts in /etc/vpnmap-ops/actions.d (each sha256-checked),
#      all root-owned;
#   5. writes ~vpnmap-ops/.ssh/authorized_keys (root-owned) with exactly
#        restrict,command="/usr/local/bin/vpnmap-ops serve" <key>
#   6. writes /etc/sudoers.d/vpnmap-ops (checked with visudo) with exactly
#        vpnmap-ops ALL=(root) NOPASSWD: /usr/local/bin/vpnmap-ops exec
#
# What it never does: restart or reload any service (sshd included), touch
# the firewall, give vpnmap-ops any other sudo right, or add `from=` (the
# hub's egress address changes with the VPN it uses).
set -euo pipefail

BIN=/usr/local/bin/vpnmap-ops
ETC=/etc/vpnmap-ops
ACTIONS=$ETC/actions.d
SUDOERS=/etc/sudoers.d/vpnmap-ops
USER_NAME=vpnmap-ops
HOME_DIR=/home/vpnmap-ops

HUB="" NODE="" SHA="" KEY="" CONFIG_FILE="" ACTION_LIST="" ARCH="" DRY_RUN=0 TMP=""

say() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
run() { printf '  + %s\n' "$*"; if [ "$DRY_RUN" = 0 ]; then "$@"; fi; }
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT

while [ $# -gt 0 ]; do
	case "$1" in
	--hub) HUB=${2%/}; shift 2 ;;
	--node) NODE=$2; shift 2 ;;
	--sha256) SHA=$2; shift 2 ;;
	--key) KEY=$2; shift 2 ;;
	--config-file) CONFIG_FILE=$2; shift 2 ;;
	--actions) ACTION_LIST=$2; shift 2 ;;
	--arch) ARCH=$2; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
	*) die "unknown option $1" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "run as root (sudo bash -s -- …)"
[[ $HUB =~ ^https?:// ]] || die "--hub URL is required"
[[ $NODE =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]] || die "--node <topology id> is required"
[[ $SHA =~ ^[0-9a-f]{64}$ ]] || die "--sha256 <64 hex> is required (shown by the hub's Ops panel)"
[[ $KEY =~ ^ssh-ed25519\ [A-Za-z0-9+/=]{60,80}(\ [A-Za-z0-9._@-]{1,40})?$ ]] || die "--key must be the hub's ssh-ed25519 public key line"
[ -f "$CONFIG_FILE" ] || die "--config-file <path to ops.yaml> is required (vpnmap ops config $NODE)"
if [ -z "$ARCH" ]; then
	case "$(uname -m)" in
	x86_64) ARCH=amd64 ;; aarch64 | arm64) ARCH=arm64 ;; *) die "unknown architecture $(uname -m); pass --arch" ;;
	esac
fi
command -v visudo >/dev/null || die "visudo is missing (install sudo first)"

TMP=$(mktemp -d)
say "downloading vpnmap-ops-linux-$ARCH"
curl -fsSL "$HUB/agent/download/vpnmap-ops-linux-$ARCH" -o "$TMP/vpnmap-ops"
echo "$SHA  $TMP/vpnmap-ops" | sha256sum -c --quiet - || die "sha256 mismatch: not installing"
chmod 0755 "$TMP/vpnmap-ops"

say "checking the runner config"
"$TMP/vpnmap-ops" check "$CONFIG_FILE" | grep -q "node=$NODE " || die "$CONFIG_FILE is not the config of node $NODE"

declare -a SCRIPTS=()
if [ -n "$ACTION_LIST" ]; then
	IFS=',' read -r -a items <<<"$ACTION_LIST"
	for it in "${items[@]}"; do
		id=${it%%:*} sum=${it#*:}
		[[ $id =~ ^[a-z0-9][a-z0-9-]{0,62}$ && $sum =~ ^[0-9a-f]{64}$ ]] || die "bad --actions entry $it (want id:sha256)"
		curl -fsSL "$HUB/agent/download/ops-action-$id" -o "$TMP/action-$id"
		echo "$sum  $TMP/action-$id" | sha256sum -c --quiet - || die "sha256 mismatch for action $id"
		SCRIPTS+=("$id")
	done
fi

say "user $USER_NAME"
if ! id "$USER_NAME" >/dev/null 2>&1; then
	run useradd --system --create-home --home-dir "$HOME_DIR" --shell /bin/sh "$USER_NAME"
fi
run passwd -l "$USER_NAME" >/dev/null

say "files"
run install -o root -g root -m 0755 "$TMP/vpnmap-ops" "$BIN"
run install -d -o root -g root -m 0755 "$ETC" "$ACTIONS"
run install -o root -g root -m 0644 "$CONFIG_FILE" "$ETC/ops.yaml"
for id in "${SCRIPTS[@]}"; do
	run install -o root -g root -m 0755 "$TMP/action-$id" "$ACTIONS/$id"
done
run install -d -o root -g root -m 0700 /var/lib/vpnmap-ops
if [ "$DRY_RUN" = 0 ]; then
	touch /var/log/vpnmap-ops.log && chown root:root /var/log/vpnmap-ops.log && chmod 0600 /var/log/vpnmap-ops.log
fi

say "authorized_keys (forced command, root-owned so the user cannot edit it)"
run install -d -o root -g root -m 0755 "$HOME_DIR/.ssh"
printf 'restrict,command="%s serve" %s\n' "$BIN" "$KEY" >"$TMP/authorized_keys"
run install -o root -g root -m 0644 "$TMP/authorized_keys" "$HOME_DIR/.ssh/authorized_keys"

say "sudoers: only \`$BIN exec\`"
printf '# vpnmap-ops: the runner may re-run itself as root, nothing else.\n%s ALL=(root) NOPASSWD: %s exec\n' "$USER_NAME" "$BIN" >"$TMP/sudoers"
visudo -cf "$TMP/sudoers" >/dev/null || die "sudoers check failed"
run install -o root -g root -m 0440 "$TMP/sudoers" "$SUDOERS"

if grep -qiE '^\s*(AllowUsers|AllowGroups)\b' /etc/ssh/sshd_config /etc/ssh/sshd_config.d/*.conf 2>/dev/null; then
	echo "note: sshd has AllowUsers/AllowGroups; add $USER_NAME yourself if the hub cannot log in (this script does not edit sshd)."
fi
say "done. Host key fingerprint to enter in the Ops panel:"
for f in /etc/ssh/ssh_host_ed25519_key.pub; do [ -f "$f" ] && ssh-keygen -lf "$f"; done
"$BIN" check || true
