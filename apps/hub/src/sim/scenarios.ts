import { DemoScenario } from '../contract';

/**
 * Demo scenarios: the real incidents from Notion 08 · Known Issues and the
 * Changelog, compressed so they play out in minutes. Times are offsets in
 * seconds from the scenario start.
 */
export interface ScenarioDef extends DemoScenario {
  /** Effects active at offset `s` seconds into the scenario. */
  effects(s: number): Effects;
}

export interface Effects {
  /** Node ids whose agent is silent (rebooting). */
  silent?: string[];
  /** Node ids that rebooted at this offset (boot time set when they come back). */
  bootAt?: Record<string, number>;
  /** XBridger connection mt6snj5mlbnn process not running (:9000 refused). */
  xbridgerConnDown?: boolean;
  /** rw-api hung: API timeouts, container still healthy, +1 core CPU. */
  rwApiHung?: boolean;
  /** Container restarted (restart count +1, started at this moment). */
  containerRestart?: { node: string; container: string; at: number }[];
  /** Log lines that appear at this offset: [node, log check id suffix, line]. */
  logs?: { node: string; match: string; line: string; at: number }[];
  /** France exit rebooting (3x-ui and :443 unreachable). */
  franceDown?: boolean;
  /** wg-game peer silent (no handshakes). */
  gameTunnelDown?: boolean;
  /** Fraction of main-route users that stay online (users give up during outages). */
  mainUsersFactor?: number;
}

const min = 60;

export const SCENARIOS: ScenarioDef[] = [
  {
    id: 'xbridger-reboot-2026-10-13',
    title: ':9000 closed after XBridger node 3 rebooted',
    date: '2026-10-13',
    description:
      'Node 3 rebooted at 13:43 IRST and XBridger did not restart connection mt6snj5mlbnn, so :9000 refused TCP while the DB still said Connected. Fixed at 15:45 by starting the connection; France was restarted around the same time. Real duration 2 h, replayed in 30 min.',
    durationSec: 30 * min,
    effects(s) {
      const e: Effects = {};
      if (s < 75) e.silent = ['xbridger-node-3'];
      e.bootAt = { 'xbridger-node-3': 20 };
      if (s >= 50) e.containerRestart = [{ node: 'xbridger-node-3', container: 'backend-app-1', at: 50 }];
      e.xbridgerConnDown = s < 28 * min;
      if (s >= 26 * min && s < 27 * min) e.franceDown = true;
      if (s >= 26 * min) e.bootAt['france-exit'] = 26 * min + 30;
      if (s >= 26 * min && s < 27 * min) e.silent = [...(e.silent ?? []), 'france-exit'];
      e.mainUsersFactor = s < 28 * min ? Math.max(0.55, 1 - s / (40 * min)) : Math.min(1, 0.6 + (s - 28 * min) / (2 * min));
      return e;
    },
  },
  {
    id: 'rw-api-hang-2026-10-20',
    title: 'rw-api hung while the container showed healthy',
    date: '2026-10-20',
    description:
      'The rw-api process in the remnawave container hung at ~100 % CPU and answered nothing, while Docker still reported the container healthy. Users stayed connected; subscriptions and node control failed. Fixed with docker restart remnawave. Replayed in 20 min.',
    durationSec: 20 * min,
    effects(s) {
      const e: Effects = { rwApiHung: s < 19 * min };
      if (s >= 19 * min) e.containerRestart = [{ node: 'panel', container: 'remnawave', at: 19 * min }];
      return e;
    },
  },
  {
    id: 'rw-api-hang-2026-10-22',
    title: 'rw-api hung again (~8 h, scanner favicon URL)',
    date: '2026-10-22',
    description:
      'Same hang from 02:03 UTC, seconds after scanners sent a 503-char /favicons/ URL (likely ReDoS, UNVERIFIED). It lasted ~8 h until a manual restart at 13:51 UTC; nginx filtering and the watchdog were added afterwards. Replayed in 35 min.',
    durationSec: 35 * min,
    effects(s) {
      const e: Effects = { rwApiHung: s >= 20 && s < 34 * min };
      if (s >= 34 * min) e.containerRestart = [{ node: 'panel', container: 'remnawave', at: 34 * min }];
      return e;
    },
  },
  {
    id: 'watchdog-restart',
    title: 'rw-api hang caught by the watchdog (after 2026-10-22)',
    date: 'now',
    description:
      'How the same hang plays out today: the watchdog sees 3 failed health checks, restarts remnawave and logs it to /var/log/remnawave-watchdog.log. About 4 minutes of panel outage.',
    durationSec: 8 * min,
    effects(s) {
      const e: Effects = { rwApiHung: s < 3.5 * min };
      if (s >= 3.5 * min) {
        e.containerRestart = [{ node: 'panel', container: 'remnawave', at: 3.5 * min }];
        e.logs = [{ node: 'panel', match: 'watchdog', line: 'remnawave health check failed 3/3, restarting remnawave', at: 3.5 * min }];
      }
      return e;
    },
  },
  {
    id: 'game-tunnel-drop',
    title: 'Game tunnel drops (home internet outage)',
    date: 'now',
    description: 'The XRouter Pi loses its uplink for 6 minutes: wg-game stops handshaking and the Xbox is offline (fail closed).',
    durationSec: 8 * min,
    effects(s) {
      return { gameTunnelDown: s < 6 * min };
    },
  },
];

export function scenario(id: string): ScenarioDef | undefined {
  return SCENARIOS.find((s) => s.id === id);
}
