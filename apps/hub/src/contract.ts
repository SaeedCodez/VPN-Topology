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
  /** Resolved ops policy per node with an `ops:` block (pkg/ops NodePolicy). Older bundles have none. */
  ops?: OpsNodePolicy[];
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
  | 'demo'
  | 'ops';

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
  /** Read-only Notion documentation (NOTION_TOKEN); `root` is the page id the assistant may read under. */
  docs: { enabled: boolean; reason?: string; root?: string };
}

export interface AssistantDocsTestResult {
  ok: boolean;
  ms: number;
  /** Title of the root page and how many sub-pages it has. */
  title?: string;
  subpages?: number;
  error?: string;
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

// ---------------------------------------------------------------- ops actions (docs/ops-actions.md)

/** pkg/ops Param. */
export interface OpsParam {
  name: string;
  source?: string;
  enum?: string[];
  pattern?: string;
  min?: number;
  max?: number;
  doc?: string;
}

/** pkg/ops Type (apps/hub/src/ops/types.gen.json). */
export interface OpsType {
  name: string;
  kind: 'read' | 'change';
  where: 'server' | 'remnawave';
  title: string;
  doc: string;
  params: OpsParam[] | null;
  disruptive: boolean;
  cooldown?: string;
  rollback?: string;
}

/** pkg/ops RunnerConfig: what the runner on one server allows. */
export interface OpsRunnerConfig {
  node: string;
  containers?: { name: string; restart?: boolean; cooldown?: string }[];
  units?: { name: string; restart?: boolean; cooldown?: string }[];
  logs?: { id: string; path: string }[];
  tcp_targets?: string[];
  ports?: number[];
  wg?: { iface: string; netns?: string }[];
  scripts?: { id: string; kind: 'read' | 'change'; params?: OpsParam[]; timeout?: string; cooldown?: string }[];
}

export interface OpsCall {
  script: string;
  args?: Record<string, string>;
}

/** pkg/ops NodePolicy. */
export interface OpsNodePolicy {
  node: string;
  runner: boolean;
  address?: string;
  config?: OpsRunnerConfig;
  config_hash?: string;
  verify: string[];
  cuts_users: boolean;
  cut_routes?: string[];
  scripts?: { id: string; title?: string; disruptive: boolean; read_before?: OpsCall; rollback?: OpsCall; verify?: string[] }[];
  never_restart?: string[];
  remnawave?: { name: string; actions: string[] };
}

/** One step of a plan as the model proposes it. */
export interface OpsStepInput {
  node: string;
  type: string;
  args?: Record<string, string>;
}

export interface OpsPlanInput {
  title: string;
  why: string;
  impact: string;
  steps: OpsStepInput[];
}

/** A validated step: everything the approval message shows and the executor runs. */
export interface OpsStep {
  node: string;
  type: string;
  args: Record<string, string>;
  /** Canonical request line sent to the runner (server types), or the Remnawave call. */
  line: string;
  where: 'server' | 'remnawave';
  title: string;
  verify: string[];
  cutsUsers: boolean;
  cutRoutes: string[];
  cooldownS: number;
  reversible: boolean;
  /** Rollback as a step template; "{before.x}" is filled from readBefore output. */
  rollback: { type: string; args: Record<string, string> } | null;
  readBefore: { type: string; args: Record<string, string> } | null;
}

export interface OpsPlan {
  title: string;
  why: string;
  impact: string;
  steps: OpsStep[];
  /** Union of the steps' verify ids. */
  verify: string[];
  cutsUsers: boolean;
}

export type OpsTaskStatus = 'pending' | 'confirming' | 'approved' | 'running' | 'done' | 'failed' | 'rolled-back' | 'cancelled' | 'expired';

export interface OpsStepLog {
  index: number;
  node: string;
  line: string;
  /** not-run: refused before anything was sent to the server (does not count for cooldowns). */
  status: 'ok' | 'failed' | 'rolled-back' | 'skipped' | 'not-run';
  output: string;
  at: string;
}

export interface OpsTaskView {
  id: string;
  status: OpsTaskStatus;
  title: string;
  why: string;
  impact: string;
  steps: OpsStep[];
  verify: string[];
  cutsUsers: boolean;
  requestedBy: string;
  approvedBy: string | null;
  createdAt: string;
  finishedAt: string | null;
  log: OpsStepLog[];
  outcome: string | null;
}

export interface OpsServerView {
  node: string;
  name: string;
  /** SSH host: the override, or the node's first address. */
  host: string;
  port: number;
  access: 'on' | 'off' | { until: string };
  reachable: boolean;
  /** Pinned host-key fingerprint ("SHA256:..."), entered by the owner. */
  hostKey: string | null;
  /** Hash the topology expects vs. the one the runner last reported. */
  expectedConfig: string | null;
  reportedConfig: string | null;
  configCurrent: boolean;
  lastOkAt: string | null;
  lastError: string | null;
  cutsUsers: boolean;
  changes: string[];
}

export interface OpsSettingsView {
  enabled: boolean;
  actionModel: string;
  approverUserIds: string[];
  sshPublicKey: string | null;
  servers: OpsServerView[];
  remnawave: { node: string; name: string; actions: string[] }[];
  running: string | null;
  /** Ops is disabled in demo mode (no login). */
  demo: boolean;
  /** Hash of the applied ops policy, and whether the owner accepted it (changes need that). */
  policyHash: string;
  policyAccepted: boolean;
}
