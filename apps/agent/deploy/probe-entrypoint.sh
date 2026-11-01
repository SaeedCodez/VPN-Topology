#!/bin/sh
# Entry point of the probe container (apps/agent/Dockerfile.probe).
#
# Generates agent.yaml for the `probe` source from the topology baked into the
# image (same commit as the deployment), writes the push token and one sing-box
# outbound per route from environment variables, then runs the agent.
#
#   VPNMAP_TOKEN             push token of the probe (its sha256 goes in the hub's AGENT_TOKENS as probe=<sha>)
#   PROBE_HUB_URL            hub URL (default: settings.hub of the topology)
#   PROBE_OUTBOUND_<ROUTE>   sing-box outbound JSON of the monitoring user for a route,
#                            e.g. PROBE_OUTBOUND_MAIN, PROBE_OUTBOUND_MULLVAD_DE
#   PROBE_CHECK_ONLY=1       write and validate the config, then exit (for testing)
#
# A route without an outbound reports "no data" (grey), never a false outage.
# Secrets are written 0600 and removed from the agent's environment.
set -eu

: "${VPNMAP_TOKEN:?set VPNMAP_TOKEN to the probe push token (vpnmap agent token probe)}"
ETC=/etc/vpnmap-agent
umask 077
mkdir -p "$ETC/probe"
printf '%s' "$VPNMAP_TOKEN" > "$ETC/token"

if [ -n "${PROBE_HUB_URL:-}" ]; then
	vpnmap agent config probe --dir /app/repo --hub "$PROBE_HUB_URL" > "$ETC/agent.yaml"
else
	vpnmap agent config probe --dir /app/repo > "$ETC/agent.yaml"
fi

missing=""
for route in $(sed -n 's/^ *- route: *//p' "$ETC/agent.yaml"); do
	var="PROBE_OUTBOUND_$(printf '%s' "$route" | tr 'a-z-' 'A-Z_')"
	val=$(printenv "$var" || true)
	if [ -n "$val" ]; then
		printf '%s' "$val" > "$ETC/probe/$route.json"
		echo "probe: route $route configured"
	else
		rm -f "$ETC/probe/$route.json"
		missing="$missing $var"
	fi
done
[ -z "$missing" ] || echo "probe: not set:$missing (those routes report 'no data' until you add them)"

vpnmap-agent check-config --config "$ETC/agent.yaml"
[ "${PROBE_CHECK_ONLY:-}" = 1 ] && exit 0

# Run without the secrets in the environment.
exec env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/tmp STATE_DIRECTORY=/tmp/vpnmap-agent \
	vpnmap-agent run --config "$ETC/agent.yaml"
