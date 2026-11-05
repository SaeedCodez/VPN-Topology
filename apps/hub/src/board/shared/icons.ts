/**
 * Small line icons per node kind (drawn in-house, 24x24 grid, stroke only).
 * Shared by the board (RoleIcon.vue) and the hub's board image renderer.
 */
export const ROLE_ICONS: Record<string, string[]> = {
  users: ['M16 19v-1.5a3.5 3.5 0 0 0-3.5-3.5h-5A3.5 3.5 0 0 0 4 17.5V19', 'M10 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M20 19v-1.5a3.5 3.5 0 0 0-2.5-3.35', 'M15.5 5.13a3 3 0 0 1 0 5.74'],
  relay: ['M4 8h12', 'M13 5l3 3-3 3', 'M20 16H8', 'M11 13l-3 3 3 3'],
  panel: ['M4 5h16v10H4z', 'M8 19h8', 'M12 15v4', 'M7 9h4', 'M7 12h7'],
  'vpn-node': ['M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z', 'M9.5 12l2 2 3.5-4'],
  bridge: ['M3 16h18', 'M5 16V9', 'M19 16V9', 'M5 9c3 4 11 4 14 0', 'M9 16v-4.2', 'M15 16v-4.2', 'M12 16v-3.5'],
  exit: ['M14 4h5v16h-5', 'M10 8l-4 4 4 4', 'M6 12h10'],
  cdn: ['M7 18a4 4 0 0 1-.6-7.96A5.5 5.5 0 0 1 17 8.5a4.5 4.5 0 0 1 .5 9H7z'],
  edge: ['M7 18a4 4 0 0 1-.6-7.96A5.5 5.5 0 0 1 17 8.5a4.5 4.5 0 0 1 .5 9H7z', 'M12 11v4', 'M10 13h4'],
  'vpn-provider': ['M6 11V8a6 6 0 0 1 12 0v3', 'M5 11h14v9H5z', 'M12 15v2'],
  internet: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z', 'M3 12h18', 'M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z'],
  router: ['M4 14h16v5H4z', 'M8 16.5h.01', 'M12 16.5h.01', 'M16 14V9', 'M13 6.5a4 4 0 0 1 6 0', 'M11 4a7.5 7.5 0 0 1 10 0'],
  server: ['M4 4h16v7H4z', 'M4 13h16v7H4z', 'M8 7.5h.01', 'M8 16.5h.01'],
  storage: ['M12 8c4.4 0 8-1.3 8-3s-3.6-3-8-3-8 1.3-8 3 3.6 3 8 3z', 'M4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5', 'M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3'],
  service: ['M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z', 'M19.4 15a7.8 7.8 0 0 0 0-6l2-1.5-2-3.4-2.4 1a8 8 0 0 0-5-3V0h-4v2.1a8 8 0 0 0-5 3l-2.4-1-2 3.4L2.6 9a7.8 7.8 0 0 0 0 6'],
};

export function roleIcon(kind: string): string[] {
  return ROLE_ICONS[kind] ?? ROLE_ICONS.server;
}
