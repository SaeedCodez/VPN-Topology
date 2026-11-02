import { RemnawaveNode } from './protocol';

/**
 * One entry of GET /api/nodes as the checks read it. Remnawave 3.4 no longer
 * sends isNodeOnline, isXrayRunning or xrayVersion: isConnected stands for the
 * node and xrayUptime for the Xray process (seen live on 3.4.5). Reading the
 * missing fields as false reported healthy nodes as "Xray not running".
 * The older fields still win if a panel sends them.
 */
export function toRemnawaveNode(n: any): RemnawaveNode {
  const xrayUptime = Number(n.xrayUptime ?? 0);
  return {
    uuid: n.uuid,
    name: n.name,
    isConnected: !!n.isConnected,
    isNodeOnline: !!(n.isNodeOnline ?? n.isConnected),
    isXrayRunning: !!(n.isXrayRunning ?? (n.xrayVersion || xrayUptime > 0)),
    isDisabled: !!n.isDisabled,
    lastStatusMessage: n.lastStatusMessage ?? null,
    xrayUptime,
    usersOnline: Number(n.usersOnline ?? 0),
  };
}
