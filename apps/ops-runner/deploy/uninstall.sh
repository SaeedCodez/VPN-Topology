#!/bin/bash
# Remove the ops action runner (vpnmap-ops) from this server: the hub's key,
# the sudoers entry, the user, the binary and its config. The audit log
# /var/log/vpnmap-ops.log is kept. Run it yourself, as root.
#
#   curl -fsSL "$HUB/agent/ops-uninstall.sh" | sudo bash
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
rm -f /etc/sudoers.d/vpnmap-ops
rm -f /home/vpnmap-ops/.ssh/authorized_keys
if id vpnmap-ops >/dev/null 2>&1; then
	pkill -u vpnmap-ops 2>/dev/null || true
	userdel vpnmap-ops
fi
rm -rf /home/vpnmap-ops
rm -f /usr/local/bin/vpnmap-ops
rm -rf /etc/vpnmap-ops /var/lib/vpnmap-ops
echo "vpnmap-ops removed (audit log kept in /var/log/vpnmap-ops.log)"
