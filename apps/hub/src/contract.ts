/**
 * The hub API contract. The web board imports these types; the Go CLI mirrors
 * them in apps/cli/api.go. One API, three clients (web, CLI, MCP): nothing the
 * board shows is computed in the browser.
 */

export type Health = 'ok' | 'degraded' | 'down' | 'unknown';
export type Impact = 'root' | 'affected' | null;

// ---------------------------------------------------------------- topology

export interface DefinedIn {
  component: string;
  file: string;
  line: number;
}

export interface CheckSpec {
  id: string;
  title?: string;
  collector: string;
  on_fail: 'down' | 'degraded';
  label?: string;
  unit?: string;
  params: Record<string, any>;
  fallback?: CheckSpec;
  key: string;
  line: number;
}

export interface RunbookCmd {
  title: string;
  cmd: string;
  notes?: string;
}

export interface TopoNode {
  id: string;
  name: string;
  kind: string;
  group: string;
  address?: string[];
  role?: string;
  agent: boolean;
  docs?: string;
  facts?: { label: string; value: string }[];
  checks?: CheckSpec[];
  metrics?: CheckSpec[];
  card?: string[];
  runbook?: RunbookCmd[];
  tags?: string[];
  listens?: string[];
  defined_in: DefinedIn;
}

export interface TopoLink {
  id: string;
  label?: string;
  title?: string;
  from: string;
  to: string;
  plane: 'data' | 'control';
  protocol?: string;
  traffic?: CheckSpec;
  conns?: CheckSpec;
  health?: CheckSpec[];
  docs?: string;
  notes?: string;
  runbook?: RunbookCmd[];
  defined_in: DefinedIn;
}

export interface TopoRoute {
  id: string;
  title: string;
  hops: string[];
  nodes: string[];
  users?: CheckSpec;
  users_estimate?: number;
  squads?: string[];
  probe?: CheckSpec;
  impact: 'outage' | 'degraded';
  docs?: string;
  defined_in: DefinedIn;
}

export interface AlertExpr {
  op: 'and' | 'or' | 'not' | 'cmp';
  args?: AlertExpr[];
  ref?: { kind: string; id: string; check?: string; field: string; text: string };
  cmp?: '==' | '!=' | '>' | '>=' | '<' | '<=';
  value?: { type: 'status' | 'bool' | 'number'; status?: string; bool?: boolean; number?: number; text: string };
  for?: { seconds?: number; samples?: number };
}

export interface TopoAlert {
  id: string;
  when: string;
  severity: 'critical' | 'warning' | 'info';
  title?: string;
  message_fa: string;
  message_en?: string;
  expr: AlertExpr;
  defined_in: DefinedIn;
}

export interface Group {
  id: string;
  title: string;
  kind: 'datacenter' | 'provider' | 'external' | 'personal';
  frame?: boolean;
  notes?: string;
}

export interface Bundle {
  schema: string;
  map: {
    schema: string;
    name: string;
    docs?: string;
    settings: { timezone?: string; stale_after: number; reboot_window: number; hub?: string; critical_squads?: string[] };
    groups: Group[];
    sources?: { id: string; title: string; location?: string; notes?: string }[];
  };
  components: { name: string; title: string; docs?: string; description?: string; file: string; runbook?: RunbookCmd[] }[];
  nodes: TopoNode[];
  links: TopoLink[];
  routes: TopoRoute[];
  alerts: TopoAlert[];
  layout: Record<string, { x: number; y: number }>;
  catalog: any[];
  source: { git_commit?: string; git_branch?: string; dirty: boolean; generated_at: string; generator: string };
  hash: string;
}

export interface TopologyVersion {
  version: number;
  hash: string;
  createdAt: string;
  message: string;
  gitCommit?: string;
  appliedBy: string;
  active: boolean;
  counts: { nodes: number; links: number; routes: number; alerts: number };
}

// ---------------------------------------------------------------- live state

/** Result of one check at one moment. */
export interface CheckState {
  key: string;
  id: string;
  title: string;
  collector: string;
  status: Health;
  /** Contribution to the owner's status after on_fail capping. */
  effective: Health;
  value: number | null;
  unit: string;
  detail: string;
  observedAt: string | null;
  source: string; // "agent:panel", "hub", "probe"
}

export interface NodeState {
  id: string;
  status: Health; // own status from its checks
  observed: boolean; // false when the node has no checks at all
  impact: Impact;
  checks: CheckState[];
  /** Card numbers, keyed by metric id: built-ins (cpu, mem, ...), metric ids and check ids. */
  metrics: Record<string, { value: number | null; unit: string; label: string }>;
  bootTime: string | null;
  uptimeSec: number | null;
  rebootedRecently: boolean;
  agent: { lastSeen: string | null; version?: string; configSha?: string; ageSec: number | null } | null;
  since: string; // when status last changed
}

export interface LinkState {
  id: string;
  status: Health;
  observed: boolean;
  impact: Impact;
  upBps: number | null;
  downBps: number | null;
  conns: number | null;
  trafficSource: 'primary' | 'fallback' | null;
  checks: CheckState[];
  since: string;
}

export interface RouteState {
  id: string;
  status: Health; // down = broken
  broken: boolean;
  probe: { status: Health; latencyMs: number | null; exitCountry: string | null; failures: number; detail: string } | null;
  users: number | null;
  usersSource: 'live' | 'estimate' | null;
  root: { kind: 'node' | 'link' | 'route'; id: string } | null;
  /** Hop-by-hop: alternating nodes and links from start to end. */
  hops: { kind: 'node' | 'link'; id: string; status: Health; impact: Impact }[];
  since: string;
}

export interface RootCause {
  id: string; // stable incident key (root element)
  kind: 'node' | 'link' | 'route';
  elementId: string;
  title: string; // "XBridger node 3"
  summary: string; // "connection mt6snj5mlbnn not running"
  impact: 'outage' | 'degraded';
  usersAffected: number;
  usersSource: 'live' | 'estimate' | 'mixed';
  /** True when a broken route serves a paying squad (settings.critical_squads). */
  critical: boolean;
  squads: string[];
  routes: string[];
  affected: { nodes: string[]; links: string[] };
  since: string;
  evidence: string[];
  runbook: (RunbookCmd & { from: string })[];
  docs: string[];
  definedIn: DefinedIn | null;
}

export interface ActiveAlert {
  id: string; // alert id
  instance: string; // entity the alert fired for ("" when global)
  severity: 'critical' | 'warning' | 'info';
  title: string;
  message: string; // English
  messageFa: string;
  since: string;
  silencedUntil: string | null;
  definedIn: DefinedIn;
}

export interface Overall {
  status: Health;
  usersOnline: number | null;
  throughput: { upBps: number; downBps: number };
  activeAlerts: number;
  rootCauses: RootCause[];
  counts: Record<Health, number>;
}

export interface Snapshot {
  at: string;
  live: boolean;
  mode: 'demo' | 'live';
  topology: { version: number; hash: string; gitCommit?: string };
  overall: Overall;
  nodes: Record<string, NodeState>;
  links: Record<string, LinkState>;
  routes: Record<string, RouteState>;
  alerts: ActiveAlert[];
  drift: DriftItem[];
}

export interface DriftItem {
  kind: 'node' | 'port' | 'agent' | 'check';
  id: string;
  title: string;
  detail: string;
  host?: string;
}

// ---------------------------------------------------------------- events, metrics

export type EventKind =
  | 'state'
  | 'incident'
  | 'alert'
  | 'reboot'
  | 'restart'
  | 'watchdog'
  | 'log'
  | 'agent'
  | 'topology'
  | 'demo';

export interface HubEvent {
  id: number;
  ts: string;
  kind: EventKind;
  severity: 'info' | 'warning' | 'critical' | 'ok';
  entityKind: 'node' | 'link' | 'route' | 'check' | 'alert' | 'topology' | 'agent' | null;
  entityId: string | null;
  title: string;
  detail: string;
  data?: Record<string, any>;
}

export interface Series {
  key: string;
  label: string;
  unit: string;
  points: [number, number | null][]; // [unix ms, value]
}

// ---------------------------------------------------------------- views

export interface Describe {
  kind: 'node' | 'link' | 'route';
  id: string;
  at: string;
  spec: TopoNode | TopoLink | TopoRoute;
  state: NodeState | LinkState | RouteState;
  definedIn: DefinedIn;
  upstream: { kind: 'node' | 'link'; id: string; status: Health }[];
  downstream: { kind: 'node' | 'link'; id: string; status: Health }[];
  routes: string[];
  events: HubEvent[];
  runbook: (RunbookCmd & { from: string })[];
  docs: string[];
  why: string[];
}

export interface PathView {
  at: string;
  from: string;
  to: string;
  route: string | null;
  hops: {
    kind: 'node' | 'link';
    id: string;
    name: string;
    status: Health;
    impact: Impact;
    detail: string;
    metrics: string;
  }[];
  verdict: string;
  status: Health;
}

export interface Diagnosis {
  at: string;
  status: Health;
  headline: string;
  rootCauses: RootCause[];
  degraded: { kind: 'node' | 'link'; id: string; status: Health; detail: string }[];
  alerts: ActiveAlert[];
  recentEvents: HubEvent[];
}

export interface AgentInfo {
  node: string;
  kind: string;
  version: string;
  hostname: string;
  configSha: string;
  lastSeen: string;
  ageSec: number;
  capabilities: Record<string, string[]>;
}

export interface DemoScenario {
  id: string;
  title: string;
  date: string;
  description: string;
  durationSec: number;
}

// ---------------------------------------------------------------- Telegram assistant

export interface AssistantSettingsView {
  enabled: boolean;
  model: string;
  allowedUserIds: string[];
  dailyLimit: number;
  hasKey: boolean;
  /** "sk-or-…ab12"; the key itself is never returned. */
  keyHint: string | null;
  /** A key is stored but cannot be decrypted (SESSION_SECRET changed): enter it again. */
  keyUnreadable: boolean;
  telegram: { enabled: boolean; reason?: string };
  polling: { running: boolean; lastPollAt: string | null; error: string | null };
  usage: { day: string; questions: number; promptTokens: number; completionTokens: number; costUsd: number };
  lastError: { at: string; error: string } | null;
  /** Last message for the bot from a Telegram user who is not allowed (to find your own id). */
  lastIgnored: { at: string; fromId: string; fromName: string } | null;
}

export interface AssistantModel {
  id: string;
  name: string;
  contextLength: number | null;
  /** USD per million tokens. */
  promptPrice: number | null;
  completionPrice: number | null;
  tools: boolean;
}

export interface AssistantTestResult {
  ok: boolean;
  model: string;
  ms: number;
  reply?: string;
  costUsd?: number | null;
  error?: string;
}

// -------------------------------------------------------------------- ops agent

export interface OpsServerView {
  id: string;
  host: string;
  port: number;
  user: string;
  nodeId: string | null;
  access: 'on' | 'off' | 'window' | 'expired';
  accessUntil: string | null;
  hostKey: string | null;
  lastOkAt: string | null;
  note: string | null;
}

export interface OpsSettingsView {
  enabled: boolean;
  actionModel: string;
  approverUserIds: string[];
  hubIp: string;
  hasKey: boolean;
  /** OpenSSH one-line public key to install on a server (the private key is never returned). */
  publicKey: string | null;
  servers: OpsServerView[];
}

export type OpsTaskStatus = 'pending' | 'approved' | 'running' | 'done' | 'failed' | 'rolled-back' | 'cancelled' | 'expired';

/** One step of an ops plan. `run` is a shell command (on `target` server id) or a Remnawave API call (target "remnawave"). */
export interface OpsStep {
  target: string;
  run: string | { method: string; path: string; body?: unknown };
  purpose?: string;
  /** Files copied aside before the step so it can be undone. */
  backup?: string[];
  /** How to undo the step: a command / API call, or "restore" to put the backups back. */
  rollback?: string | { method: string; path: string; body?: unknown } | 'restore' | null;
  critical?: boolean;
}

export interface OpsPlan {
  title: string;
  why: string;
  impact: string;
  steps: OpsStep[];
  /** Node/link/route ids to re-check after each step; worse than baseline → rollback. */
  verify: string[];
}

export interface OpsStepLog {
  index: number;
  target: string;
  summary: string;
  status: 'ok' | 'failed' | 'rolled-back' | 'skipped';
  output: string;
  at: string;
}

export interface OpsTaskView {
  id: string;
  status: OpsTaskStatus;
  title: string;
  why: string;
  impact: string;
  steps: { target: string; summary: string; rollback: string }[];
  verify: string[];
  requestedBy: string;
  approvedBy: string | null;
  createdAt: string;
  finishedAt: string | null;
  log: OpsStepLog[];
  outcome: string | null;
}
