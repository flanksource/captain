import { useMemo, useState, type ComponentProps, type KeyboardEvent, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  SearchInput,
  SegmentedControl,
  Switch,
} from "@flanksource/clicky-ui/components";
import {
  Icon,
  ProgressBars,
  UiActivity,
  UiArrowDown,
  UiArrowUp,
  UiBrain,
  UiChartBar,
  UiChip,
  UiClock,
  UiCopy,
  UiHistory,
  UiMemoryStick,
  UiRefresh,
  UiRobotAi,
  UiSparkles,
  UiTerminal,
} from "@flanksource/clicky-ui/data";
import {
  SOURCE_OPTIONS,
  commandLabel,
  errorMessage,
  fetchLiveSessions,
  formatCompactNumber,
  formatCost,
  formatTime,
  healthDotClassName,
  healthRank,
  projectLabel,
  sessionSortTime,
  sessionTitle,
  type SessionDashboard,
  type SessionLive,
  type SessionRecord,
  type SourceFilter,
} from "./sessionData";

type DashboardView = "list" | "cards";
type DashboardSort = "model" | "health" | "context" | "cpu" | "memory" | "tokens" | "recent";
type SortDirection = "asc" | "desc";
type Navigate = (to: string, opts?: { replace?: boolean }) => void;
type DashboardIcon = NonNullable<ComponentProps<typeof Icon>["icon"]>;
type LiveSessionRecord = SessionRecord & { live: SessionLive };
type ProjectSessionGroup = {
  key: string;
  label: string;
  detail?: string;
  sessions: LiveSessionRecord[];
};

const PERCENT_UNIT = {
  perBar: 25,
  label: "%",
  barLabel: "25%",
  format: (units: number) => `${Math.round(units * 25)}`,
};

const VIEW_OPTIONS = [
  { id: "list", label: "List" },
  { id: "cards", label: "Cards" },
] satisfies Array<{ id: DashboardView; label: string }>;

const SORT_OPTIONS = [
  { id: "model", label: "Model" },
  { id: "health", label: "Health" },
  { id: "context", label: "Context" },
  { id: "cpu", label: "CPU" },
  { id: "memory", label: "Memory" },
  { id: "tokens", label: "Tokens" },
  { id: "recent", label: "Recent" },
] satisfies Array<{ id: DashboardSort; label: string }>;

const SESSION_GRID_CLASS =
  "grid grid-cols-[minmax(12rem,1.4fr)_5.25rem_6.25rem] sm:grid-cols-[minmax(13rem,1.5fr)_5.25rem_6.25rem_6.25rem] lg:grid-cols-[minmax(15rem,1.6fr)_5.5rem_7rem_7rem_7rem_6rem_7rem_5.5rem]";

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

export function HomeDashboard({ onNavigate }: { onNavigate: Navigate }) {
  const [source, setSource] = useState<SourceFilter>("all");
  const [allProjects, setAllProjects] = useState(false);
  const [query, setQuery] = useState("");
  const [view, setView] = useState<DashboardView>("list");
  const [sort, setSort] = useState<DashboardSort>("health");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");

  const sessionsQuery = useQuery({
    queryKey: ["sessions-dashboard", source, allProjects, query],
    queryFn: () => fetchLiveSessions({ source, allProjects, query, limit: 200 }),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });

  const sessions = sessionsQuery.data?.sessions ?? [];
  const liveSessions = useMemo(
    () =>
      sessions
        .filter(hasLiveProcess)
        .sort((left, right) => compareSessions(left, right, sort, sortDirection)),
    [sessions, sort, sortDirection],
  );
  const liveProjectGroups = useMemo(
    () => groupSessionsByProject(liveSessions),
    [liveSessions],
  );
  const alertSessions = useMemo(
    () =>
      sessions
        .filter((session) => (session.health?.length ?? 0) > 0)
        .sort((left, right) => healthRank(right) - healthRank(left) || sessionSortTime(right) - sessionSortTime(left)),
    [sessions],
  );
  const recentSessions = useMemo(
    () =>
      sessions
        .filter((session) => !session.live && session.detailAvailable !== false)
        .sort((left, right) => sessionSortTime(right) - sessionSortTime(left))
        .slice(0, 6),
    [sessions],
  );

  const openSession = (session: SessionRecord) => {
    if (session.detailAvailable === false) return;
    onNavigate(`/sessions/${encodeURIComponent(session.key)}`);
  };

  const selectSort = (nextSort: DashboardSort) => {
    setSort(nextSort);
    setSortDirection(defaultSortDirection(nextSort));
  };

  const toggleSort = (nextSort: DashboardSort) => {
    if (sort === nextSort) {
      setSortDirection((direction) => (direction === "asc" ? "desc" : "asc"));
      return;
    }
    selectSort(nextSort);
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <DashboardToolbar
        source={source}
        onSourceChange={setSource}
        allProjects={allProjects}
        onAllProjectsChange={setAllProjects}
        query={query}
        onQueryChange={setQuery}
        view={view}
        onViewChange={setView}
        sort={sort}
        onSortChange={selectSort}
        loading={sessionsQuery.isFetching}
        onRefresh={() => void sessionsQuery.refetch()}
        onNewAgent={() => onNavigate("/agent")}
      />

      <div className="min-h-0 flex-1 overflow-auto">
        {sessionsQuery.error ? (
          <div className="border-b border-border p-density-4 text-sm text-destructive">
            {errorMessage(sessionsQuery.error)}
          </div>
        ) : null}

        <MetricGrid
          summary={sessionsQuery.data?.summary}
          liveCount={liveSessions.length}
          loading={sessionsQuery.isLoading}
        />

        <div className="grid min-h-0 gap-density-4 px-density-4 pb-density-4 xl:grid-cols-[minmax(0,1fr)_22rem]">
          <section className="min-w-0">
            <div className="mb-density-2 flex min-w-0 items-center justify-between gap-density-2">
              <div className="min-w-0">
                <div className="text-sm font-semibold">Running Sessions</div>
                <div className="truncate text-xs text-muted-foreground">
                  {sessionsQuery.isFetching ? "Refreshing..." : `${liveSessions.length} live / ${sessions.length} shown`}
                </div>
              </div>
            </div>

            {liveSessions.length === 0 && !sessionsQuery.isLoading ? (
              <EmptyLiveSessions onNewAgent={() => onNavigate("/agent")} />
            ) : view === "cards" ? (
              <SessionCardGrid groups={liveProjectGroups} onOpen={openSession} />
            ) : (
              <SessionTable
                groups={liveProjectGroups}
                sort={sort}
                sortDirection={sortDirection}
                onSortChange={toggleSort}
                onOpen={openSession}
              />
            )}
          </section>

          <aside className="min-w-0 space-y-density-4">
            <HealthPanel sessions={alertSessions} onOpen={openSession} />
            <RecentPanel sessions={recentSessions} onOpen={openSession} />
          </aside>
        </div>
      </div>
    </div>
  );
}

function DashboardToolbar({
  source,
  onSourceChange,
  allProjects,
  onAllProjectsChange,
  query,
  onQueryChange,
  view,
  onViewChange,
  sort,
  onSortChange,
  loading,
  onRefresh,
  onNewAgent,
}: {
  source: SourceFilter;
  onSourceChange: (source: SourceFilter) => void;
  allProjects: boolean;
  onAllProjectsChange: (enabled: boolean) => void;
  query: string;
  onQueryChange: (query: string) => void;
  view: DashboardView;
  onViewChange: (view: DashboardView) => void;
  sort: DashboardSort;
  onSortChange: (sort: DashboardSort) => void;
  loading: boolean;
  onRefresh: () => void;
  onNewAgent: () => void;
}) {
  return (
    <div className="shrink-0 border-b border-border px-density-4 py-density-3">
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-density-3">
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold leading-tight">Running Sessions</h1>
          <div className="mt-1 text-xs text-muted-foreground">
            {loading ? "Refreshing live process data" : "Live agent process dashboard"}
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-density-2">
          <Button size="sm" variant="outline" onClick={onRefresh}>
            <Icon icon={UiRefresh} className="size-4" />
            Refresh
          </Button>
          <Button size="sm" onClick={onNewAgent}>
            <Icon icon={UiRobotAi} className="size-4" />
            New Agent
          </Button>
        </div>
      </div>

      <div className="mt-density-3 grid gap-density-2 lg:grid-cols-[minmax(14rem,1fr)_auto_auto_auto_auto]">
        <SearchInput
          value={query}
          onChange={onQueryChange}
          placeholder="Search sessions"
          shortcut={null}
        />
        <SegmentedControl
          value={source}
          options={SOURCE_OPTIONS}
          onChange={onSourceChange}
          size="sm"
          aria-label="Session source"
        />
        <Switch
          checked={allProjects}
          onChange={onAllProjectsChange}
          label="All projects"
        />
        <SegmentedControl
          value={view}
          options={VIEW_OPTIONS}
          onChange={onViewChange}
          size="sm"
          aria-label="Dashboard view"
        />
        <label className="flex min-w-[8rem] items-center gap-2 text-xs text-muted-foreground">
          Sort
          <select
            value={sort}
            onChange={(event) => onSortChange(event.target.value as DashboardSort)}
            className="h-8 rounded border border-border bg-background px-2 text-xs text-foreground"
          >
            {SORT_OPTIONS.map((option) => (
              <option key={option.id} value={option.id}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </div>
    </div>
  );
}

function MetricGrid({
  summary,
  liveCount,
  loading,
}: {
  summary?: SessionDashboard;
  liveCount: number;
  loading: boolean;
}) {
  const stats = [
    {
      label: "Live",
      value: summary ? `${summary.liveSessions}/${summary.totalSessions}` : loading ? "--" : "0/0",
      detail: `${summary?.activeSessions ?? liveCount} active`,
      icon: UiActivity,
    },
    {
      label: "Alerts",
      value: summary?.alertSessions ?? 0,
      detail: `${summary?.stoppedSessions ?? 0} stopped`,
      icon: UiChartBar,
    },
    {
      label: "Context",
      value: summary?.lowestContextFree !== undefined ? `${summary.lowestContextFree}%` : "--",
      detail: "lowest free",
      icon: UiMemoryStick,
    },
    {
      label: "Tokens",
      value: formatCompactNumber(summary?.totalTokens ?? 0),
      detail: `${formatCompactNumber(summary?.outputTokens ?? 0)} output`,
      icon: UiTerminal,
    },
    {
      label: "Cache",
      value: formatCompactNumber(summary?.cacheReadTokens ?? 0),
      detail: `${formatCompactNumber(summary?.cacheCreationTokens ?? 0)} created`,
      icon: UiHistory,
    },
    {
      label: "Cost",
      value: formatCost(summary?.costUsd ?? 0),
      detail: "estimated",
      icon: UiClock,
    },
  ] satisfies Array<{
    label: string;
    value: ReactNode;
    detail: string;
    icon: DashboardIcon;
  }>;

  return (
    <section className="grid grid-cols-2 gap-density-2 p-density-4 md:grid-cols-3 2xl:grid-cols-6">
      {stats.map((stat) => (
        <MetricTile key={stat.label} {...stat} />
      ))}
    </section>
  );
}

function MetricTile({
  label,
  value,
  detail,
  icon,
}: {
  label: string;
  value: ReactNode;
  detail: string;
  icon: DashboardIcon;
}) {
  return (
    <div className="min-w-0 rounded border border-border bg-card px-density-3 py-density-2">
      <div className="flex items-center justify-between gap-density-2">
        <div className="truncate text-[10px] font-medium uppercase text-muted-foreground">{label}</div>
        <Icon icon={icon} className="size-4 shrink-0 text-muted-foreground" />
      </div>
      <div className="mt-1 truncate text-2xl font-semibold leading-tight">{value}</div>
      <div className="mt-1 truncate text-xs text-muted-foreground">{detail}</div>
    </div>
  );
}

function SessionTable({
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
    <div className="min-w-0 max-w-[72rem] overflow-hidden rounded border border-border">
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
                    value={session.live.cpuPercent}
                    icon={UiChip}
                    thresholds={[60, 85]}
                  />
                  <UsageBarsCell
                    title="Memory"
                    value={session.live.memoryPercent}
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
      aria-sort={active ? (sortDirection === "asc" ? "ascending" : "descending") : "none"}
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

function SessionCardGrid({
  groups,
  onOpen,
}: {
  groups: ProjectSessionGroup[];
  onOpen: (session: SessionRecord) => void;
}) {
  return (
    <div className="space-y-density-4">
      {groups.map((group) => (
        <section key={group.key} className="min-w-0">
          <ProjectGroupHeader group={group} />
          <div className="mt-density-2 grid gap-density-3 md:grid-cols-2 2xl:grid-cols-3">
            {group.sessions.map((session) => (
              <div
                key={session.key}
                role={session.detailAvailable === false ? undefined : "button"}
                tabIndex={session.detailAvailable === false ? undefined : 0}
                onClick={() => activateSession(session, onOpen)}
                onKeyDown={(event) => handleSessionKeyDown(event, session, onOpen)}
                className="min-w-0 cursor-pointer rounded border border-border bg-card p-density-3 transition-colors hover:border-muted-foreground/40 hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[disabled=true]:cursor-default data-[disabled=true]:hover:border-border data-[disabled=true]:hover:bg-card"
                data-disabled={session.detailAvailable === false ? "true" : undefined}
              >
                <div className="flex min-w-0 items-start justify-between gap-density-2">
                  <SessionIdentity session={session} />
                  <SessionActions session={session} onOpen={onOpen} compact />
                </div>
                <div className="mt-density-3 grid grid-cols-2 gap-density-2 text-xs">
                  <MetricBox label="Status" value={session.live.status ?? "active"} />
                  <MetricBox
                    label="Tokens"
                    value={formatCompactNumber(session.tokens?.totalTokens ?? 0)}
                  />
                  <MetricBox
                    label="CPU"
                    value={
                      <UsageBarsCell
                        title="CPU"
                        value={session.live.cpuPercent}
                        icon={UiChip}
                        thresholds={[60, 85]}
                      />
                    }
                  />
                  <MetricBox
                    label="Memory"
                    value={
                      <UsageBarsCell
                        title="Memory"
                        value={session.live.memoryPercent}
                        icon={UiMemoryStick}
                        thresholds={[70, 90]}
                      />
                    }
                  />
                  <MetricBox label="Updated" value={formatRelativeTime(session.endedAt ?? session.startedAt)} />
                  <MetricBox label="Branch" value={session.gitBranch ?? "--"} />
                </div>
                <div className="mt-density-3">
                  <ContextCell session={session} expanded />
                </div>
                <div className="mt-density-2 truncate text-[11px] text-muted-foreground">
                  {commandLabel(session.live.command)}
                </div>
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function ProjectGroupHeader({ group }: { group: ProjectSessionGroup }) {
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

function UsageBarsCell({
  title,
  value,
  icon,
  thresholds,
}: {
  title: string;
  value: number | undefined;
  icon: DashboardIcon;
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
      <span className="text-[11px] tabular-nums text-muted-foreground">
        {formatPercent(value)}
      </span>
    </div>
  );
}

function SessionIdentity({ session }: { session: SessionRecord }) {
  const model = modelLabel(session);
  const effort = effortLabel(session.reasoningEffort);
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="grid size-6 shrink-0 place-items-center rounded border border-border bg-muted/50 text-muted-foreground">
          <Icon icon={modelIcon(session)} className="size-3.5" />
        </span>
        <span className="min-w-0 truncate text-sm font-medium">{model}</span>
        <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[10px] uppercase text-muted-foreground">
          {session.source}
        </span>
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
      <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
        {session.live?.pid ? `pid ${session.live.pid} - ` : ""}
        {commandLabel(session.live?.command)}
      </div>
    </div>
  );
}

function StatusCell({ session }: { session: SessionRecord & { live: SessionLive } }) {
  const signal = session.health?.[0];
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 items-center gap-1.5">
        <span
          className={`h-2 w-2 shrink-0 rounded-full ${signal ? healthDotClassName(signal.severity) : "bg-emerald-500"}`}
        />
        <span className="truncate text-xs font-medium">{session.live.status ?? "active"}</span>
      </div>
    </div>
  );
}

function ContextCell({
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

function SessionActions({
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

function HealthPanel({
  sessions,
  onOpen,
}: {
  sessions: SessionRecord[];
  onOpen: (session: SessionRecord) => void;
}) {
  return (
    <SidePanel title="Health" count={sessions.length}>
      {sessions.length === 0 ? (
        <PanelEmpty>Clear</PanelEmpty>
      ) : (
        <div className="divide-y divide-border">
          {sessions.slice(0, 6).map((session) => {
            const signal = session.health?.[0];
            return (
              <button
                key={session.key}
                type="button"
                disabled={session.detailAvailable === false}
                onClick={() => onOpen(session)}
                className="grid w-full min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-density-2 py-density-2 text-left disabled:cursor-default"
              >
                <span
                  className={`mt-1 h-2 w-2 rounded-full ${healthDotClassName(signal?.severity ?? "info")}`}
                />
                <span className="min-w-0">
                  <span className="block truncate text-xs font-medium">
                    {signal?.kind.replace(/_/g, " ") ?? "health"}
                  </span>
                  <span className="block truncate text-[11px] text-muted-foreground">
                    {sessionTitle(session)}
                  </span>
                  <span className="block truncate text-[11px] text-muted-foreground">
                    {signal?.message ?? formatTime(session.endedAt ?? session.startedAt)}
                  </span>
                </span>
              </button>
            );
          })}
        </div>
      )}
    </SidePanel>
  );
}

function RecentPanel({
  sessions,
  onOpen,
}: {
  sessions: SessionRecord[];
  onOpen: (session: SessionRecord) => void;
}) {
  return (
    <SidePanel title="Recent History" count={sessions.length}>
      {sessions.length === 0 ? (
        <PanelEmpty>None</PanelEmpty>
      ) : (
        <div className="divide-y divide-border">
          {sessions.map((session) => (
            <button
              key={session.key}
              type="button"
              onClick={() => onOpen(session)}
              className="block w-full min-w-0 py-density-2 text-left"
            >
              <span className="block truncate text-xs font-medium">{sessionTitle(session)}</span>
              <span className="block truncate text-[11px] text-muted-foreground">
                {formatTime(session.endedAt ?? session.startedAt)}
              </span>
              <span className="block truncate text-[11px] text-muted-foreground">
                {projectLabel(session.cwd)}
              </span>
            </button>
          ))}
        </div>
      )}
    </SidePanel>
  );
}

function SidePanel({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section className="min-w-0 rounded border border-border bg-card px-density-3 py-density-2">
      <div className="flex items-center justify-between gap-density-2">
        <div className="truncate text-xs font-semibold uppercase text-muted-foreground">{title}</div>
        <div className="shrink-0 text-xs text-muted-foreground">{count}</div>
      </div>
      <div className="mt-1 min-h-[2.5rem]">{children}</div>
    </section>
  );
}

function EmptyLiveSessions({ onNewAgent }: { onNewAgent: () => void }) {
  return (
    <div className="flex min-h-[16rem] flex-col items-center justify-center rounded border border-dashed border-border p-density-6 text-center">
      <div className="text-sm font-medium">No running sessions.</div>
      <div className="mt-1 text-xs text-muted-foreground">Start an agent or switch scope.</div>
      <Button className="mt-density-3" size="sm" onClick={onNewAgent}>
        <Icon icon={UiRobotAi} className="size-4" />
        New Agent
      </Button>
    </div>
  );
}

function PanelEmpty({ children }: { children: ReactNode }) {
  return <div className="py-density-2 text-xs text-muted-foreground">{children}</div>;
}

function MetricBox({ label, value }: { label: string; value: ReactNode }) {
  const isTextValue = typeof value === "string" || typeof value === "number";
  return (
    <div className="min-w-0 rounded border border-border px-2 py-1.5">
      <div className="truncate text-[10px] uppercase text-muted-foreground">{label}</div>
      <div className={isTextValue ? "truncate text-xs font-medium" : "mt-1 min-w-0"}>
        {value}
      </div>
    </div>
  );
}

function MetricText({ value }: { value: ReactNode }) {
  return <div className="truncate text-xs text-muted-foreground">{value}</div>;
}

function hasLiveProcess(session: SessionRecord): session is LiveSessionRecord {
  return Boolean(session.live);
}

function compareSessions(
  left: LiveSessionRecord,
  right: LiveSessionRecord,
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
      (right.live.cpuPercent ?? -1) - (left.live.cpuPercent ?? -1) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  if (sort === "memory") {
    return directionalCompare(
      (right.live.memoryPercent ?? -1) - (left.live.memoryPercent ?? -1) ||
        sessionSortTime(right) - sessionSortTime(left),
      direction,
      "desc",
    );
  }
  if (sort === "tokens") {
    return directionalCompare(
      tokenTotal(right) - tokenTotal(left) ||
        sessionSortTime(right) - sessionSortTime(left),
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
    (right.live.cpuPercent ?? -1) - (left.live.cpuPercent ?? -1) ||
      sessionSortTime(right) - sessionSortTime(left),
    direction,
    "desc",
  );
}

function directionalCompare(value: number, direction: SortDirection, natural: SortDirection) {
  return direction === natural ? value : -value;
}

function defaultSortDirection(sort: DashboardSort): SortDirection {
  return sort === "model" || sort === "context" ? "asc" : "desc";
}

function tokenTotal(session: SessionRecord) {
  return session.tokens?.totalTokens ?? 0;
}

function groupSessionsByProject(sessions: LiveSessionRecord[]): ProjectSessionGroup[] {
  const groups = new Map<string, ProjectSessionGroup>();

  for (const session of sessions) {
    const cwd = session.live.cwd ?? session.cwd;
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

function modelLabel(session: SessionRecord) {
  return session.model || session.provider || session.source;
}

function modelIcon(session: SessionRecord): DashboardIcon {
  const value = `${session.provider ?? ""} ${session.model ?? ""} ${session.source}`.toLowerCase();
  if (value.includes("claude") || value.includes("anthropic")) return UiSparkles;
  if (value.includes("codex") || value.includes("openai") || value.includes("gpt")) return UiRobotAi;
  return UiTerminal;
}

function effortLabel(value: string | undefined) {
  return value ? value.replace(/_/g, " ") : undefined;
}

function formatPercent(value: number | undefined) {
  return value === undefined ? "--" : `${value.toFixed(1)}%`;
}

function formatRelativeTime(value: string | undefined) {
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
