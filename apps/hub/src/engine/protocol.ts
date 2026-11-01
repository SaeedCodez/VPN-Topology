/** TypeScript mirror of pkg/protocol (agent → hub push payload). */

export interface Push {
  v: number;
  agent: {
    node: string;
    kind: 'agent' | 'probe';
    version: string;
    hostname: string;
    config_sha: string;
    capabilities: {
      units?: string[];
      containers?: string[];
      nft_counters?: string[];
      wg?: string[];
      ss?: string[];
      tcp?: string[];
      http?: string[];
      logs?: string[];
      checks?: string[];
      prom?: string[];
      routes?: string[];
      listening?: string[];
    };
  };
  ts: string;
  seq: number;
  host?: {
    boot_time: number;
    uptime_s: number;
    cpu_pct: number;
    num_cpu: number;
    load1: number;
    load5: number;
    load15: number;
    mem_total_b: number;
    mem_used_b: number;
    disks?: { mount: string; total_b: number; used_b: number; used_pct: number }[];
    nics?: { name: string; rx_b: number; tx_b: number; rx_p: number; tx_p: number }[];
  };
  units?: { name: string; active: string; sub: string; since?: string; n_restarts: number }[];
  containers?: { name: string; state: string; health: string; started_at?: string; restart_count: number; image?: string }[];
  nft?: { name: string; bytes: number; packets: number }[];
  wg?: { netns?: string; iface: string; listen_port?: number; peers: { key: string; endpoint?: string; last_handshake: number; rx_b: number; tx_b: number }[] }[];
  ss?: { id: string; port: number; side: string; established: number }[];
  tcp?: { target: string; ok: boolean; rtt_ms: number; error?: string; at: string }[];
  http?: { id: string; url: string; ok: boolean; status: number; latency_ms: number; error?: string; cert_days?: number; at: string }[];
  logs?: { id: string; lines: string[]; truncated?: boolean }[];
  checks?: { id: string; status: string; value?: number; detail?: string; data?: Record<string, any>; exit_code: number; duration_ms: number; at: string }[];
  prom?: { target: string; name: string; labels?: Record<string, string>; value: number }[];
  probes?: { route: string; ok: boolean; latency_ms: number; exit_country?: string; exit_ip?: string; error?: string; at: string }[];
  errors?: { collector: string; error: string }[];
}

/** Results of collectors that run inside the hub (or are simulated in demo mode). */
export interface RemnawaveNode {
  uuid: string;
  name: string;
  isConnected: boolean;
  isNodeOnline: boolean;
  isXrayRunning: boolean;
  isDisabled: boolean;
  lastStatusMessage: string | null;
  xrayUptime: number; // seconds
  usersOnline: number;
}

export interface HubProbeResult {
  ok: boolean;
  status?: number;
  latencyMs: number;
  error?: string;
  certDays?: number;
  at: number; // ms
}

export interface XuiInbound {
  remark: string;
  tag: string;
  enable: boolean;
  up: number; // cumulative bytes
  down: number;
}
