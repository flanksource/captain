import type { ComponentProps, KeyboardEvent, ReactNode } from "react";
import { Button } from "@flanksource/clicky-ui/components";
import {
  CopyBadge,
  Icon,
  ProgressBars,
  UiArrowDown,
  UiArrowUp,
  UiBrain,
  UiChip,
  UiCopy,
  UiHistory,
  UiLogoClaude,
  UiLogoDeepseek,
  UiLogoGemini,
  UiLogoMistral,
  UiLogoOpenai,
  UiMemoryStick,
  UiTerminal,
} from "@flanksource/clicky-ui/data";
import {
  commandLabel,
  formatCompactNumber,
  healthDotClassName,
  projectLabel,
  sessionSortTime,
  sessionTitle,
  shortID,
  type SessionLive,
  type SessionRecord,
} from "./sessionData";

export type DashboardSort =
  | "model"
  | "health"
  | "context"
  | "cpu"
  | "memory"
  | "tokens"
  | "recent";
export type SortDirection = "asc" | "desc";
export type SessionIcon = NonNullable<ComponentProps<typeof Icon>["icon"]>;
export type LiveSessionRecord = SessionRecord & { live: SessionLive };
export type ProjectSessionGroup = {
  key: string;
  label: string;
  detail?: string;
  sessions: SessionRecord[];
};

const PERCENT_UNIT = {
  perBar: 25,
  label: "%",
  barLabel: "25%",
  format: (units: number) => `${Math.round(units * 25)}`,
};

const SESSION_GRID_CLASS =
  "grid grid-cols-[minmax(14rem,1.6fr)_5.25rem_6.25rem] sm:grid-cols-[minmax(16rem,1.7fr)_5.25rem_6.25rem_6.25rem] lg:grid-cols-[minmax(20rem,2fr)_5.5rem_7rem_7rem_7rem_6rem_7rem_5.5rem]";

const SESSION_COLUMNS = [
  { label: "Model", sort: "model" },
  { label: "Status", sort: "health" },
  { label: "CPU", sort: "cpu" },
  { label: "Memory", sort: "memory" },
  { label: "Context", sort: "context" },
  { label: "Tokens", sort: "tokens" },
  { label: "Updated", sort: "recent" },
  { label: "Actions" },
] satisfies Array<{ label: string; sort?: DashboardSort }>;

export function SessionTable({
  groups,
  sort,
  sortDirection,
  onSortChange,
  onOpen,
}: {
  groups: ProjectSessionGroup[];
  sort: DashboardSort;
  sortDirection: SortDirection;
  onSortChange: (sort: DashboardSort) => void;
  onOpen: (session: SessionRecord) => void;
}) {
  return (
    <div className="min-w-0 overflow-hidden rounded border border-border">
      <div className={`${SESSION_GRID_CLASS} border-b border-border bg-muted/40 px-density-3 py-2 text-[11px] font-medium uppercase text-muted-foreground`}>
        {SESSION_COLUMNS.map((column) => (
          <SessionHeaderCell
            key={column.label}
            column={column}
            sort={sort}
            sortDirection={sortDirection}
            onSortChange={onSortChange}
          />
        ))}
      </div>
      <div>
        {groups.map((group) => (
          <section key={group.key} className="border-b border-border last:border-b-0">
            <ProjectGroupHeader group={group} />
            <div className="divide-y divide-border">
              {group.sessions.map((session) => (
                <div
                  key={session.key}
                  role={session.detailAvailable === false ? undefined : "button"}
                  tabIndex={session.detailAvailable === false ? undefined : 0}
                  onClick={() => activateSession(session, onOpen)}
                  onKeyDown={(event) => handleSessionKeyDown(event, session, onOpen)}
                  className={`${SESSION_GRID_CLASS} cursor-pointer items-center gap-density-2 px-density-3 py-density-2 text-sm transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[disabled=true]:cursor-default data-[disabled=true]:hover:bg-transparent`}
                  data-disabled={session.detailAvailable === false ? "true" : undefined}
                >
                  <SessionIdentity session={session} />
                  <StatusCell session={session} />
                  <UsageBarsCell
                    title="CPU"
                    value={session.live?.cpuPercent}
                    icon={UiChip}
                    thresholds={[60, 85]}
                  />
                  <UsageBarsCell
                    title="Memory"
                    value={session.live?.memoryPercent}
                    icon={UiMemoryStick}
                    thresholds={[70, 90]}
                  />
                  <ContextCell session={session} />
                  <MetricText value={formatCompactNumber(session.tokens?.totalTokens ?? 0)} />
                  <MetricText value={formatRelativeTime(session.endedAt ?? session.startedAt)} />
                  <SessionActions session={session} onOpen={onOpen} />
                </div>
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  );
}

function SessionHeaderCell({
  column,
  sort,
  sortDirection,
  onSortChange,
}: {
  column: (typeof SESSION_COLUMNS)[number];
  sort: DashboardSort;
  sortDirection: SortDirection;
  onSortChange: (sort: DashboardSort) => void;
}) {
  if (!column.sort) {
    return <div className="truncate px-1">{column.label}</div>;
  }

  const active = sort === column.sort;
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={() => onSortChange(column.sort)}
      className="inline-flex min-w-0 items-center gap-1 rounded px-1 py-0.5 text-left uppercase transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="truncate">{column.label}</span>
      {active ? (
        <Icon
          icon={sortDirection === "asc" ? UiArrowUp : UiArrowDown}
          className="size-3 shrink-0"
        />
      ) : null}
    </button>
  );
}

export function ProjectGroupHeader({ group }: { group: ProjectSessionGroup }) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-density-2 bg-muted/20 px-density-3 py-2 text-xs">
      <div className="min-w-0">
        <div className="truncate font-semibold">{group.label}</div>
        {group.detail ? (
          <div className="mt-0.5 truncate text-[11px] text-muted-foreground">{group.detail}</div>
        ) : null}
      </div>
      <div className="shrink-0 rounded border border-border bg-background px-2 py-0.5 text-[11px] text-muted-foreground">
        {group.sessions.length}
      </div>
    </div>
  );
}

export function UsageBarsCell({
  title,
  value,
  icon,
  thresholds,
}: {
  title: string;
  value: number | undefined;
  icon: SessionIcon;
  thresholds: [warning: number, danger: number];
}) {
  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      <ProgressBars
        variant="cell"
        title={title}
        icon={icon}
        usage={value}
        max={100}
        unit={PERCENT_UNIT}
        thresholds={thresholds}
        showValue={false}
        orientation="vertical"
        hoverCard={false}
        className="max-w-full"
      />
      <span className="text-[11px] tabular-nums text-muted-foreground">{formatPercent(value)}</span>
    </div>
  );
}

export function SessionIdentity({ session }: { session: SessionRecord }) {
  const model = modelLabel(session);
  const effort = effortLabel(session.reasoningEffort);
  const id = shortID(session.id) || session.key;
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="grid size-6 shrink-0 place-items-center rounded border border-border bg-muted/50 text-muted-foreground">
          <Icon icon={modelIcon(session)} className="size-3.5" />
        </span>
        <span className="min-w-0 truncate text-sm font-medium">{model}</span>
      </div>
      <div className="mt-1 flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
        {effort ? (
          <span className="inline-flex min-w-0 items-center gap-1">
            <Icon icon={UiBrain} className="size-3 shrink-0" />
            <span className="truncate">{effort}</span>
          </span>
        ) : null}
        <span className="truncate">{sessionTitle(session)}</span>
      </div>
      <div
        className="mt-1 flex min-w-0 flex-wrap items-center gap-1"
        onClick={(event) => event.stopPropagation()}
      >
        {id ? <CopyBadge label="id" value={id} /> : null}
        {session.live?.pid ? (
          <CopyBadge label="pid" value={String(session.live.pid)} />
        ) : null}
      </div>
      {session.live?.command ? (
        <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
          {commandLabel(session.live.command)}
        </div>
      ) : null}
    </div>
  );
}

function StatusCell({ session }: { session: SessionRecord }) {
  const signal = session.health?.[0];
  const status = session.live?.status ?? (session.endedAt ? "ended" : "idle");
  const dot = signal
    ? healthDotClassName(signal.severity)
    : session.live
      ? "bg-emerald-500"
      : "bg-muted-foreground";
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <span className={`h-2 w-2 shrink-0 rounded-full ${dot}`} />
        <span className="truncate text-xs font-medium">{status}</span>
      </div>
    </div>
  );
}

export function ContextCell({
  session,
  expanded = false,
}: {
  session: SessionRecord;
  expanded?: boolean;
}) {
  const percent = session.context?.freePercent;
  if (percent === undefined) {
    return <MetricText value="--" />;
  }
  return (
    <div className="min-w-0">
      <div className="flex items-center justify-between gap-2 text-xs">
        <span className={contextTone(percent)}>{percent}% free</span>
        {expanded && session.context?.windowTokens ? (
          <span className="text-muted-foreground">
            {formatCompactNumber(session.context.windowTokens)}
          </span>
        ) : null}
      </div>
      <div className="mt-1 h-1.5 overflow-hidden rounded bg-muted">
        <div
          className={`h-full rounded ${contextBarTone(percent)}`}
          style={{ width: `${Math.max(0, Math.min(100, percent))}%` }}
        />
      </div>
    </div>
  );
}

export function SessionActions({
  session,
  onOpen,
  compact = false,
}: {
  session: SessionRecord;
  onOpen: (session: SessionRecord) => void;
  compact?: boolean;
}) {
  return (
    <div className="flex shrink-0 items-center gap-1">
      <Button
        size="sm"
        variant="ghost"
        disabled={session.detailAvailable === false}
        onClick={(event) => {
          event.stopPropagation();
          onOpen(session);
        }}
        aria-label="Open session history"
      >
        <Icon icon={UiHistory} className="size-4" />
        {compact ? null : "Open"}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        onClick={(event) => {
          event.stopPropagation();
          copySessionRef(session);
        }}
        aria-label="Copy session reference"
      >
        <Icon icon={UiCopy} className="size-4" />
      </Button>
    </div>
  );
}

export function MetricText({ value }: { value: ReactNode }) {
  return <div className="truncate text-xs text-muted-foreground">{value}</div>;
}

export function hasLiveProcess(session: SessionRecord): session is LiveSessionRecord {
  return Boolean(session.live);
}

export function compareSessions(
  left: SessionRecord,
  right: SessionRecord,
  sort: DashboardSort,
  direction: SortDirection,
) {
  if (sort === "model") {
    return directionalCompare(
      modelLabel(left).localeCompare(modelLabel(right)) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "asc",
    );
  }
  if (sort === "context") {
    return directionalCompare(
      (left.context?.freePercent ?? 101) - (right.context?.freePercent ?? 101) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "asc",
    );
  }
  if (sort === "cpu") {
    return directionalCompare(
      (right.live?.cpuPercent ?? -1) - (left.live?.cpuPercent ?? -1) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  if (sort === "memory") {
    return directionalCompare(
      (right.live?.memoryPercent ?? -1) - (left.live?.memoryPercent ?? -1) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  if (sort === "tokens") {
    return directionalCompare(
      tokenTotal(right) - tokenTotal(left) || sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  if (sort === "recent") {
    return directionalCompare(
      sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  return directionalCompare(
    healthRank(right) - healthRank(left) ||
      (left.context?.freePercent ?? 101) - (right.context?.freePercent ?? 101) ||
      (right.live?.cpuPercent ?? -1) - (left.live?.cpuPercent ?? -1) ||
      sessionSortTime(right) - sessionSortTime(left),
    direction,
    "desc",
  );
}

function healthRank(session: SessionRecord) {
  return Math.max(0, ...(session.health ?? []).map((signal) => severityRank(signal.severity)));
}

function severityRank(severity: string | undefined) {
  if (severity === "critical") return 3;
  if (severity === "warning") return 2;
  if (severity === "info") return 1;
  return 0;
}

function directionalCompare(value: number, direction: SortDirection, natural: SortDirection) {
  return direction === natural ? value : -value;
}

export function defaultSortDirection(sort: DashboardSort): SortDirection {
  return sort === "model" || sort === "context" ? "asc" : "desc";
}

function tokenTotal(session: SessionRecord) {
  return session.tokens?.totalTokens ?? 0;
}

export function groupSessionsByProject(sessions: SessionRecord[]): ProjectSessionGroup[] {
  const groups = new Map<string, ProjectSessionGroup>();

  for (const session of sessions) {
    const cwd = session.live?.cwd ?? session.cwd;
    const key = cwd || "unknown";
    const label = projectLabel(cwd);
    const existing = groups.get(key);

    if (existing) {
      existing.sessions.push(session);
      continue;
    }

    groups.set(key, {
      key,
      label,
      detail: cwd && cwd !== label ? cwd : undefined,
      sessions: [session],
    });
  }

  return [...groups.values()];
}

function activateSession(session: SessionRecord, onOpen: (session: SessionRecord) => void) {
  if (session.detailAvailable === false) return;
  onOpen(session);
}

function handleSessionKeyDown(
  event: KeyboardEvent<HTMLDivElement>,
  session: SessionRecord,
  onOpen: (session: SessionRecord) => void,
) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  activateSession(session, onOpen);
}

export function modelLabel(session: { model?: string; provider?: string; source?: string }) {
  return session.model || session.provider || session.source || "";
}

export function modelIcon(session: {
  model?: string;
  provider?: string;
  source?: string;
}): SessionIcon {
  const value = `${session.provider ?? ""} ${session.model ?? ""} ${session.source ?? ""}`.toLowerCase();
  if (value.includes("claude") || value.includes("anthropic")) return UiLogoClaude;
  if (value.includes("codex") || value.includes("openai") || value.includes("gpt")) {
    return UiLogoOpenai;
  }
  if (value.includes("gemini") || value.includes("google")) return UiLogoGemini;
  if (value.includes("deepseek")) return UiLogoDeepseek;
  if (value.includes("mistral")) return UiLogoMistral;
  return UiTerminal;
}

export function effortLabel(value: string | undefined) {
  return value ? value.replace(/_/g, " ") : undefined;
}

function formatPercent(value: number | undefined) {
  return value === undefined ? "--" : `${value.toFixed(1)}%`;
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

function contextTone(percent: number) {
  if (percent <= 10) return "font-medium text-destructive";
  if (percent <= 25) return "font-medium text-amber-700";
  return "font-medium text-emerald-700";
}

function contextBarTone(percent: number) {
  if (percent <= 10) return "bg-destructive";
  if (percent <= 25) return "bg-amber-500";
  return "bg-emerald-500";
}

function copySessionRef(session: SessionRecord) {
  if (!navigator.clipboard) return;
  const value = session.live?.pid ? `${session.source}:${session.live.pid}` : session.key;
  void navigator.clipboard.writeText(value).catch(() => undefined);
}
