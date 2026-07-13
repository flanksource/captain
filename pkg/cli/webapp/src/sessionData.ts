import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
import { apiClient } from "./api";
import { parseServerTiming, type TimingMetric } from "./serverTiming";

export type SourceFilter = "all" | "claude" | "codex";
export const ALL_PROJECTS_SCOPE = "all";
export type ProjectScope = typeof ALL_PROJECTS_SCOPE | string;

export type SessionListResult = {
  sessions: SessionRecord[];
  total: number;
  source: SourceFilter;
  scope: "current" | "all" | "project";
  project?: string;
  summary?: SessionDashboard;
};

export type SessionRecord = {
  key: string;
  id: string;
  source: "claude" | "codex";
  project?: string;
  slug?: string;
  title?: string;
  initialPrompt?: string;
  startedAt?: string;
  endedAt?: string;
  model?: string;
  reasoningEffort?: string;
  version?: string;
  gitBranch?: string;
  provider?: string;
  cwd?: string;
  toolCalls: number;
  messages: number;
  detailAvailable?: boolean;
  tokens?: SessionTokens;
  context?: SessionContext;
  costUsd?: number;
  live?: SessionLive;
  health?: SessionHealth[];
};

// UnifiedSession is captain's canonical session.Session (served by the sessions
// `get` action at /api/v1/sessions/{id}). The detail view consumes it directly —
// its `messages` (SessionUIMessage[]) feed the SessionViewer.
export type UnifiedGit = { branch?: string; commit?: string; worktree?: string; diff?: string };

export type UnifiedUsage = {
  inputTokens?: number;
  outputTokens?: number;
  reasoningTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  totalTokens?: number;
};

export type UnifiedCost = {
  model?: string;
  inputTokens?: number;
  outputTokens?: number;
  reasoningTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  totalTokens?: number;
  inputCost?: number;
  outputCost?: number;
  reasoningCost?: number;
  cacheReadCost?: number;
  cacheWriteCost?: number;
};

export type UnifiedAgent = {
  id?: string;
  parentId?: string;
  type?: string;
  desc?: string;
  isRoot?: boolean;
  historyFile?: string;
  children?: UnifiedAgent[];
  usage?: UnifiedUsage;
  cost?: UnifiedCost;
};

export type UnifiedChangedFiles = {
  read?: string[];
  written?: string[];
};

export type UnifiedPlanEvent = {
  kind: string;
  timestamp?: string;
  reason?: string;
};

export type UnifiedPlan = {
  path?: string;
  slug?: string;
  content?: string;
  explicit?: boolean;
  events?: UnifiedPlanEvent[];
};

export type UnifiedDenial = {
  toolUseId?: string;
  tool?: string;
  reason?: string;
};

export type UnifiedApprovalStats = {
  approved?: number;
  denied?: number;
  denials?: UnifiedDenial[];
};

export type UnifiedBudget = {
  used?: number;
  total?: number;
  remaining?: number;
  updatedAt?: string;
};

export type UnifiedCapabilities = {
  tools?: string[];
  pendingMcpServers?: string[];
  agents?: string[];
  skills?: string[];
};

export type UnifiedMetadataEvent = {
  type: string;
  scope?: string;
  turnId?: string;
  timestamp?: string;
  uuid?: string;
  data?: Record<string, unknown>;
};

export type UnifiedTurn = {
  id: string;
  index: number;
  startedAt?: string;
  endedAt?: string;
  stopReason?: string;
  model?: string;
  messageIds?: string[];
  usage?: Record<string, unknown>;
  cost?: Record<string, unknown>;
  context?: SessionContext;
  budget?: UnifiedBudget;
  events?: UnifiedMetadataEvent[];
};

export type UnifiedSession = {
  id: string;
  source: "claude" | "codex";
  project?: string;
  cwd?: string;
  slug?: string;
  title?: string;
  initialPrompt?: string;
  version?: string;
  provider?: string;
  model?: string;
  historyFile?: string;
  git?: UnifiedGit;
  startedAt?: string;
  endedAt?: string;
  usage?: UnifiedUsage;
  cost?: UnifiedCost;
  toolCosts?: UnifiedCost[];
  context?: SessionContext;
  budget?: UnifiedBudget;
  capabilities?: UnifiedCapabilities;
  events?: UnifiedMetadataEvent[];
  turns?: UnifiedTurn[];
  root?: UnifiedAgent;
  agents?: UnifiedAgent[];
  files?: UnifiedChangedFiles;
  plan?: UnifiedPlan;
  approvals?: UnifiedApprovalStats;
  health?: SessionHealth[];
  live?: SessionLive;
  messages?: SessionUIMessage[];
  prompt?: unknown;
};

export type SessionTokens = {
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  totalTokens?: number;
};

export type SessionContext = {
  usedTokens?: number;
  windowTokens?: number;
  freePercent: number;
};

export type SessionLive = {
  pid?: number;
  status?: string;
  active: boolean;
  cpuPercent?: number;
  memoryPercent?: number;
  startedAt?: string;
  cwd?: string;
  command?: string;
};

export type SessionHealth = {
  kind: string;
  severity: "info" | "warning" | "critical" | string;
  message: string;
};

export type SessionDashboard = {
  totalSessions: number;
  liveSessions: number;
  activeSessions: number;
  stoppedSessions: number;
  alertSessions: number;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  totalTokens?: number;
  costUsd?: number;
  lowestContextFree?: number;
};

export type SessionThroughputPoint = {
  at: string;
  sessionId: string;
  outputTokensPerSecond: number;
  totalTokensPerSecond: number;
  contextTokensPerSecond?: number;
  contextUsedPercent?: number;
};

export type SessionThroughputGroup = {
  key: string;
  source: string;
  model: string;
  reasoningEffort: string;
  sessions: number;
  durationSeconds: number;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  totalTokens?: number;
  contextTokens?: number;
  contextSamples?: number;
  outputTokensPerSecond: number;
  totalTokensPerSecond: number;
  contextTokensPerSecond?: number;
  avgContextUsedPercent?: number;
  points?: SessionThroughputPoint[];
};

export type SessionThroughputResult = {
  groups: SessionThroughputGroup[];
  total: number;
  skipped: number;
  source: SourceFilter;
  scope: "current" | "all" | "project";
  project?: string;
};

export type ProjectOption = {
  value: string;
  label: string;
  path: string;
  sources?: string[];
  sessions?: number;
  lastUsed?: string;
};

export type ProjectOptionsResult = {
  total: number;
  projects: ProjectOption[];
};

export const SOURCE_OPTIONS = [
  { id: "all", label: "All" },
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
] satisfies Array<{ id: SourceFilter; label: string }>;

export async function fetchLiveSessions(params: {
  source: SourceFilter;
  project: ProjectScope;
  query?: string;
  limit?: number;
}): Promise<SessionListResult & { timing?: TimingMetric[] }> {
  const response = await apiClient.executeCommand(
    "/api/captain/sessions/live",
    "GET",
    {
      source: params.source,
      ...projectScopeQuery(params.project),
      q: params.query ?? "",
      limit: String(params.limit ?? 100),
    },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load sessions.");
  }
  const timing = parseServerTiming(response.responseHeaders?.["server-timing"]);
  return { ...(response.parsed as SessionListResult), ...(timing.length ? { timing } : {}) };
}

export async function fetchSessionThroughput(params: {
  source: SourceFilter;
  project: ProjectScope;
  query?: string;
  limit?: number;
}): Promise<SessionThroughputResult & { timing?: TimingMetric[] }> {
  const response = await apiClient.executeCommand(
    "/api/captain/sessions/throughput",
    "GET",
    {
      source: params.source,
      ...projectScopeQuery(params.project),
      q: params.query ?? "",
      limit: String(params.limit ?? 500),
    },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load session throughput.");
  }
  const timing = parseServerTiming(response.responseHeaders?.["server-timing"]);
  return { ...(response.parsed as SessionThroughputResult), ...(timing.length ? { timing } : {}) };
}

export async function fetchProjectOptions(): Promise<ProjectOptionsResult> {
  const response = await apiClient.executeCommand(
    "/api/captain/projects",
    "GET",
    {},
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load projects.");
  }
  return response.parsed as ProjectOptionsResult;
}

export async function fetchSession(
  id: string,
  page?: { offset?: number; limit?: number; tail?: number },
): Promise<UnifiedSession & { timing?: TimingMetric[] }> {
  const params: Record<string, string> = { id };
  if (page?.tail) params.tail = String(page.tail);
  if (page?.offset) params.offset = String(page.offset);
  if (page?.limit) params.limit = String(page.limit);
  const response = await apiClient.executeCommand(
    "/api/v1/sessions/{id}",
    "GET",
    params,
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load session.");
  }
  const timing = parseServerTiming(response.responseHeaders?.["server-timing"]);
  return { ...(response.parsed as UnifiedSession), ...(timing.length ? { timing } : {}) };
}

/** Sum a unified session's per-bucket costs into a total USD. */
export function sessionCostTotal(cost: UnifiedCost | undefined): number {
  if (!cost) return 0;
  return (
    (cost.inputCost ?? 0) +
    (cost.outputCost ?? 0) +
    (cost.reasoningCost ?? 0) +
    (cost.cacheReadCost ?? 0) +
    (cost.cacheWriteCost ?? 0)
  );
}

/** Count tool-call parts across a unified session's messages. */
export function sessionToolCount(messages: SessionUIMessage[] | undefined): number {
  let count = 0;
  for (const message of messages ?? []) {
    for (const part of message.parts) {
      if (part.type === "dynamic-tool" || part.type.startsWith("tool-")) count += 1;
    }
  }
  return count;
}

export function unifiedSessionTitle(session: UnifiedSession): string {
  if (session.title) return session.title;
  if (session.slug) return session.slug;
  if (session.git?.branch) return `${session.git.branch} - ${shortID(session.id)}`;
  if (session.model) return `${session.model} - ${shortID(session.id)}`;
  return shortID(session.id);
}

export function sessionTitle(session: SessionRecord) {
  if (session.detailAvailable === false && session.live?.pid) {
    return `${session.source} process ${session.live.pid}`;
  }
  if (session.title) return session.title;
  if (session.slug) return session.slug;
  if (session.gitBranch) return `${session.gitBranch} - ${shortID(session.id)}`;
  if (session.model) return `${session.model} - ${shortID(session.id)}`;
  return shortID(session.id) || session.key;
}

export function shortID(id: string) {
  return id.length > 12 ? id.slice(0, 12) : id;
}

export function formatTime(value: string | undefined) {
  if (!value) return "No timestamp";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

export function formatCompactNumber(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${Math.round(value / 1_000)}k`;
  return String(value);
}

export function formatCost(value: number) {
  if (!value) return "$0";
  if (value < 0.01) return "<$0.01";
  return `$${value.toFixed(2)}`;
}

export function projectLabel(path: string | undefined) {
  if (!path) return "Unknown project";
  const parts = path.split("/").filter(Boolean);
  return parts.slice(-2).join("/") || path;
}

export function projectScopeQuery(project: ProjectScope): Record<string, string> {
  if (!project || project === ALL_PROJECTS_SCOPE) {
    return { all: "true" };
  }
  return { project };
}

export function commandLabel(command: string | undefined) {
  if (!command) return "No command";
  const first = command.split(/\s+/)[0] ?? command;
  return first.split("/").pop() || first;
}

export function sessionSortTime(session: SessionRecord) {
  const value = session.endedAt ?? session.startedAt;
  if (!value) return 0;
  const time = new Date(value).getTime();
  return Number.isNaN(time) ? 0 : time;
}

export function healthRank(session: SessionRecord) {
  return Math.max(0, ...(session.health ?? []).map((signal) => severityRank(signal.severity)));
}

export function severityRank(severity: string | undefined) {
  if (severity === "critical") return 3;
  if (severity === "warning") return 2;
  if (severity === "info") return 1;
  return 0;
}

export function healthClassName(severity: string) {
  if (severity === "critical") return "border-destructive/50 text-destructive";
  if (severity === "warning") return "border-amber-500/50 text-amber-700";
  return "border-border text-muted-foreground";
}

export function healthDotClassName(severity: string) {
  if (severity === "critical") return "bg-destructive";
  if (severity === "warning") return "bg-amber-500";
  return "bg-muted-foreground";
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
