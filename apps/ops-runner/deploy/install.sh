#!/bin/bash
# Install or upgrade the ops action runner (vpnmap-ops) on this server.
# Run it yourself, as root; the hub never runs it (CLAUDE.md rule 3). The
# board's Ops panel prints the exact command for each server.
#
#   sudo bash ops-install.sh \
#     --hub https://map.example.com --node iran-mullvad --sha256 <hex> \
#     --key 'ssh-ed25519 AAAA… vpnmap-hub' --config-file <file> --config-hash <16 hex> \
#     [--actions mullvad-status:<sha256>,mullvad-select:<sha256>] [--arch amd64|arm64] [--dry-run]
#
# The Ops panel prints one block that downloads this script into a private
# temp dir, checks its sha256, writes the config there and runs it.
#
# What it does (idempotent):
#   1. downloads vpnmap-ops-linux-<arch> from the hub, verifies its sha256;
#   2. validates the runner config (from `vpnmap ops config <node>`) and checks
#      that its hash is the one the Ops panel showed (--config-hash);
#   3. creates the system user vpnmap-ops (no password, shell /bin/sh) with a
#      root-owned home, so the user cannot swap its own ~/.ssh;
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

HUB="" NODE="" SHA="" KEY="" CONFIG_FILE="" CONFIG_HASH="" ACTION_LIST="" ARCH="" DRY_RUN=0 TMP=""

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
	--config-hash) CONFIG_HASH=$2; shift 2 ;;
	--actions) ACTION_LIST=$2; shift 2 ;;
	--arch) ARCH=$2; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
	*) die "unknown option $1" ;;
	esac
done

[ "$(id -u)" = 0 ] || die "run as root (sudo bash -s -- …)"
[[ $HUB =~ ^https:// || $HUB =~ ^http://127\.0\.0\.1(:[0-9]+)?$ ]] || die "--hub must be an https:// URL"
[[ $NODE =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]] || die "--node <topology id> is required"
[[ $SHA =~ ^[0-9a-f]{64}$ ]] || die "--sha256 <64 hex> is required (shown by the hub's Ops panel)"
[[ $KEY =~ ^ssh-ed25519\ [A-Za-z0-9+/=]{60,80}(\ [A-Za-z0-9._@-]{1,40})?$ ]] || die "--key must be the hub's ssh-ed25519 public key line"
[ -f "$CONFIG_FILE" ] || die "--config-file <path to ops.yaml> is required (vpnmap ops config $NODE)"
[[ $CONFIG_HASH =~ ^[0-9a-f]{16}$ ]] || die "--config-hash <16 hex> is required (shown by the Ops panel and vpnmap ops config)"
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
cp "$CONFIG_FILE" "$TMP/ops.yaml"
"$TMP/vpnmap-ops" check "$TMP/ops.yaml" | grep -qx "ok node=$NODE config=$CONFIG_HASH" || die "the config is not the one the Ops panel showed for $NODE (hash $CONFIG_HASH)"

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
	run useradd --system --no-create-home --home-dir "$HOME_DIR" --shell /bin/sh "$USER_NAME"
fi
# No password at all ('*', not a '!' lock: with UsePAM no, sshd refuses keys for locked accounts).
run usermod -p '*' "$USER_NAME"
# Root-owned home: the user can neither rename ~/.ssh nor write authorized_keys.
run install -d -o root -g root -m 0755 "$HOME_DIR"

say "files"
run install -o root -g root -m 0755 "$TMP/vpnmap-ops" "$BIN"
run install -d -o root -g root -m 0755 "$ETC" "$ACTIONS"
run install -o root -g root -m 0644 "$TMP/ops.yaml" "$ETC/ops.yaml"
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
