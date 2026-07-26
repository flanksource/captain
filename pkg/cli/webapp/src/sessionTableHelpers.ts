import { providerIcon } from "@flanksource/clicky-ui/chat";
import { UiTerminal } from "@flanksource/clicky-ui/data";
import {
  projectLabel,
  sessionSortTime,
  type SessionLive,
  type SessionRecord,
} from "./sessionData";
import type {
  DashboardSort,
  ProjectSessionGroup,
  SessionIcon,
  SortDirection,
} from "./SessionTable";

export type LiveSessionRecord = SessionRecord & { live: SessionLive };

export function hasLiveProcess(
  session: SessionRecord,
): session is LiveSessionRecord {
  return Boolean(session.live);
}

export function compareSessions(
  left: SessionRecord,
  right: SessionRecord,
  sort: DashboardSort,
  direction: SortDirection,
) {
  const recent = () => sessionSortTime(right) - sessionSortTime(left);
  if (sort === "model")
    return directionalCompare(
      modelLabel(left).localeCompare(modelLabel(right)) || recent(),
      direction,
      "asc",
    );
  if (sort === "context")
    return directionalCompare(
      (left.context?.freePercent ?? 101) - (right.context?.freePercent ?? 101) ||
        recent(),
      direction,
      "asc",
    );
  if (sort === "cpu")
    return directionalCompare(
      (right.live?.cpuPercent ?? -1) - (left.live?.cpuPercent ?? -1) || recent(),
      direction,
      "desc",
    );
  if (sort === "memory")
    return directionalCompare(
      (right.live?.memoryPercent ?? -1) -
        (left.live?.memoryPercent ?? -1) ||
        recent(),
      direction,
      "desc",
    );
  if (sort === "tokens")
    return directionalCompare(
      (right.tokens?.totalTokens ?? 0) - (left.tokens?.totalTokens ?? 0) ||
        recent(),
      direction,
      "desc",
    );
  if (sort === "recent")
    return directionalCompare(recent(), direction, "desc");
  return directionalCompare(
    healthRank(right) - healthRank(left) ||
      (left.context?.freePercent ?? 101) -
        (right.context?.freePercent ?? 101) ||
      (right.live?.cpuPercent ?? -1) - (left.live?.cpuPercent ?? -1) ||
      recent(),
    direction,
    "desc",
  );
}

function healthRank(session: SessionRecord) {
  return Math.max(
    0,
    ...(session.health ?? []).map((signal) => {
      if (signal.severity === "critical") return 3;
      if (signal.severity === "warning") return 2;
      if (signal.severity === "info") return 1;
      return 0;
    }),
  );
}

function directionalCompare(
  value: number,
  direction: SortDirection,
  natural: SortDirection,
) {
  return direction === natural ? value : -value;
}

export function defaultSortDirection(sort: DashboardSort): SortDirection {
  return sort === "model" || sort === "context" ? "asc" : "desc";
}

export function groupSessionsByProject(
  sessions: SessionRecord[],
): ProjectSessionGroup[] {
  const groups = new Map<string, ProjectSessionGroup>();
  for (const session of sessions) {
    const cwd = session.live?.cwd ?? session.cwd;
    const key = cwd || "unknown";
    const label = projectLabel(cwd);
    const existing = groups.get(key);
    if (existing) {
      existing.sessions.push(session);
    } else {
      groups.set(key, {
        key,
        label,
        detail: cwd && cwd !== label ? cwd : undefined,
        sessions: [session],
      });
    }
  }
  return [...groups.values()];
}

export function modelLabel(session: {
  model?: string;
  provider?: string;
  source?: string;
}) {
  return session.model || session.provider || session.source || "";
}

export function modelIcon(session: {
  model?: string;
  provider?: string;
  source?: string;
}): SessionIcon {
  return (
    providerIcon(session.provider) ??
    providerIcon(session.source) ??
    providerIcon(providerFromModel(session.model)) ??
    UiTerminal
  );
}

function providerFromModel(model?: string): string | undefined {
  const value = (model ?? "").toLowerCase();
  if (value.includes("claude") || value.includes("anthropic"))
    return "anthropic";
  if (value.includes("codex") || value.includes("gpt") || /\bo\d/.test(value))
    return "openai";
  if (value.includes("gemini") || value.includes("google")) return "google";
  if (value.includes("deepseek")) return "deepseek";
  if (value.includes("mistral")) return "mistral";
  return undefined;
}

export function effortLabel(value: string | undefined) {
  return value ? value.replace(/_/g, " ") : undefined;
}

export function formatRelativeTime(value: string | undefined) {
  if (!value) return "--";
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return value;
  const delta = Date.now() - time;
  if (delta < 60_000) return "now";
  if (delta < 3_600_000) return `${Math.round(delta / 60_000)}m ago`;
  if (delta < 86_400_000) return `${Math.round(delta / 3_600_000)}h ago`;
  return `${Math.round(delta / 86_400_000)}d ago`;
}
