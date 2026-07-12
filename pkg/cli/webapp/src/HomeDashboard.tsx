import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  SearchInput,
  SegmentedControl,
} from "@flanksource/clicky-ui/components";
import {
  Icon,
  TimeseriesPanel,
  UiActivity,
  UiChartBar,
  UiChip,
  UiClock,
  UiHistory,
  UiMemoryStick,
  UiRefresh,
  UiRobotAi,
  UiTerminal,
  type TimeseriesResponse,
  type TimeseriesSeries,
} from "@flanksource/clicky-ui/data";
import {
  SOURCE_OPTIONS,
  commandLabel,
  errorMessage,
  fetchLiveSessions,
  fetchSessionThroughput,
  formatCompactNumber,
  formatCost,
  formatTime,
  healthDotClassName,
  healthRank,
  projectLabel,
  sessionSortTime,
  sessionTitle,
  type SessionDashboard,
  type SessionRecord,
  type SessionThroughputGroup,
  type SessionThroughputResult,
  type ProjectScope,
  type SourceFilter,
} from "./sessionData";
import {
  ContextCell,
  ProjectGroupHeader,
  SessionActions,
  SessionIdentity,
  SessionTable,
  UsageBarsCell,
  compareSessions,
  defaultSortDirection,
  effortLabel,
  formatRelativeTime,
  groupSessionsByProject,
  hasLiveProcess,
  modelIcon,
  type DashboardSort,
  type ProjectSessionGroup,
  type SessionIcon,
  type SortDirection,
} from "./SessionTable";
import { withProjectScope } from "./shellHelpers";

type DashboardView = "list" | "cards";
type Navigate = (to: string, opts?: { replace?: boolean }) => void;
type DashboardIcon = SessionIcon;
type ThroughputMetric = "outputTokensPerSecond" | "contextTokensPerSecond";
type ThroughputChartModel = {
  series: TimeseriesSeries[];
  responses: Record<string, TimeseriesResponse>;
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

const EMPTY_SESSIONS: SessionRecord[] = [];
const EMPTY_THROUGHPUT_GROUPS: SessionThroughputGroup[] = [];

export function HomeDashboard({
  onNavigate,
  projectScope,
}: {
  onNavigate: Navigate;
  projectScope: ProjectScope;
}) {
  const [source, setSource] = useState<SourceFilter>("all");
  const [query, setQuery] = useState("");
  const [view, setView] = useState<DashboardView>("list");
  const [sort, setSort] = useState<DashboardSort>("health");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");

  const sessionsQuery = useQuery({
    queryKey: ["sessions-dashboard", source, projectScope, query],
    queryFn: () => fetchLiveSessions({ source, project: projectScope, query, limit: 200 }),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });
  const throughputQuery = useQuery({
    queryKey: ["sessions-throughput", source, projectScope, query],
    queryFn: () => fetchSessionThroughput({ source, project: projectScope, query, limit: 500 }),
    refetchInterval: false,
  });

  const sessions = sessionsQuery.data?.sessions ?? EMPTY_SESSIONS;
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
    onNavigate(withProjectScope(`/sessions/${encodeURIComponent(session.key)}`, projectScope));
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
        query={query}
        onQueryChange={setQuery}
        view={view}
        onViewChange={setView}
        sort={sort}
        onSortChange={selectSort}
        loading={sessionsQuery.isFetching || throughputQuery.isFetching}
        onRefresh={() => {
          void sessionsQuery.refetch();
          void throughputQuery.refetch();
        }}
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
        <ThroughputPanel
          result={throughputQuery.data}
          loading={throughputQuery.isLoading}
          error={throughputQuery.error}
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

      <div className="mt-density-3 grid gap-density-2 lg:grid-cols-[minmax(14rem,1fr)_auto_auto_auto]">
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

function ThroughputPanel({
  result,
  loading,
  error,
}: {
  result?: SessionThroughputResult;
  loading: boolean;
  error: unknown;
}) {
  const groups = result?.groups ?? EMPTY_THROUGHPUT_GROUPS;
  const outputChart = useMemo(
    () => buildThroughputChart(groups, "outputTokensPerSecond"),
    [groups],
  );
  const contextChart = useMemo(
    () => buildThroughputChart(groups, "contextTokensPerSecond"),
    [groups],
  );

  return (
    <section className="px-density-4 pb-density-4">
      <div className="mb-density-2 flex min-w-0 flex-wrap items-end justify-between gap-density-2">
        <div className="min-w-0">
          <div className="text-sm font-semibold">Token Throughput</div>
          <div className="truncate text-xs text-muted-foreground">
            {loading ? "Loading completed sessions..." : `${result?.total ?? 0} completed sessions sampled`}
          </div>
        </div>
        {result?.skipped ? (
          <div className="shrink-0 text-xs text-muted-foreground">{result.skipped} skipped</div>
        ) : null}
      </div>

      {error ? (
        <div className="rounded border border-destructive/30 px-density-3 py-density-2 text-sm text-destructive">
          {errorMessage(error)}
        </div>
      ) : loading ? (
        <div className="rounded border border-border px-density-3 py-density-6 text-center text-xs text-muted-foreground">
          Loading throughput...
        </div>
      ) : groups.length === 0 ? (
        <div className="rounded border border-dashed border-border px-density-3 py-density-6 text-center text-xs text-muted-foreground">
          No completed sessions with duration and token usage.
        </div>
      ) : (
        <>
          <div className="grid gap-density-3 xl:grid-cols-2">
            <StaticThroughputChart
              title="Output tokens/sec"
              icon={UiTerminal}
              model={outputChart}
              empty="At least two completed sessions per model are needed."
            />
            <StaticThroughputChart
              title="Context tokens/sec"
              icon={UiMemoryStick}
              model={contextChart}
              empty="No completed sessions have enough context samples."
            />
          </div>
          <ThroughputTable groups={groups.slice(0, 8)} />
        </>
      )}
    </section>
  );
}

function StaticThroughputChart({
  title,
  icon,
  model,
  empty,
}: {
  title: string;
  icon: DashboardIcon;
  model: ThroughputChartModel;
  empty: string;
}) {
  const fetcher = useMemo(
    () => staticThroughputFetcher(model.responses),
    [model.responses],
  );
  if (model.series.length === 0) {
    return (
      <div className="flex min-h-[14rem] items-center justify-center rounded-lg border border-border bg-card px-density-3 py-density-6 text-center text-xs text-muted-foreground">
        {empty}
      </div>
    );
  }
  return (
    <TimeseriesPanel
      title={title}
      icon={icon}
      series={model.series}
      fetcher={fetcher}
      refreshMs={0}
      height={180}
      variant="line"
      unit=" tok/s"
    />
  );
}

function ThroughputTable({ groups }: { groups: SessionThroughputGroup[] }) {
  return (
    <div className="mt-density-3 overflow-hidden rounded border border-border">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[48rem] text-left text-sm">
          <thead className="border-b border-border bg-muted/40 text-[11px] uppercase text-muted-foreground">
            <tr>
              <th className="px-density-3 py-2 font-medium">Model</th>
              <th className="px-density-3 py-2 font-medium">Sessions</th>
              <th className="px-density-3 py-2 font-medium">Output</th>
              <th className="px-density-3 py-2 font-medium">Total</th>
              <th className="px-density-3 py-2 font-medium">Context</th>
              <th className="px-density-3 py-2 font-medium">Context Used</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {groups.map((group) => (
              <tr key={group.key}>
                <td className="min-w-0 px-density-3 py-2">
                  <div className="flex min-w-0 items-center gap-1.5">
                    <span className="grid size-6 shrink-0 place-items-center rounded border border-border bg-muted/50 text-muted-foreground">
                      <Icon icon={modelIcon(group)} className="size-3.5" />
                    </span>
                    <span className="min-w-0">
                      <span className="block truncate font-medium">{group.model}</span>
                      <span className="block truncate text-[11px] text-muted-foreground">
                        {group.source} / {effortLabel(group.reasoningEffort) ?? "default"}
                      </span>
                    </span>
                  </div>
                </td>
                <td className="px-density-3 py-2 tabular-nums text-muted-foreground">{group.sessions}</td>
                <td className="px-density-3 py-2 tabular-nums">{formatRate(group.outputTokensPerSecond)}</td>
                <td className="px-density-3 py-2 tabular-nums text-muted-foreground">{formatRate(group.totalTokensPerSecond)}</td>
                <td className="px-density-3 py-2 tabular-nums text-muted-foreground">{formatOptionalRate(group.contextTokensPerSecond)}</td>
                <td className="px-density-3 py-2 tabular-nums text-muted-foreground">{formatOptionalPercent(group.avgContextUsedPercent)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
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
                onClick={() => session.detailAvailable !== false && onOpen(session)}
                onKeyDown={(event) => handleCardKeyDown(event, session, onOpen)}
                className="min-w-0 cursor-pointer rounded border border-border bg-card p-density-3 transition-colors hover:border-muted-foreground/40 hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[disabled=true]:cursor-default data-[disabled=true]:hover:border-border data-[disabled=true]:hover:bg-card"
                data-disabled={session.detailAvailable === false ? "true" : undefined}
              >
                <div className="flex min-w-0 items-start justify-between gap-density-2">
                  <SessionIdentity session={session} />
                  <SessionActions session={session} onOpen={onOpen} compact />
                </div>
                <div className="mt-density-3 grid grid-cols-2 gap-density-2 text-xs">
                  <MetricBox label="Status" value={session.live?.status ?? "active"} />
                  <MetricBox
                    label="Tokens"
                    value={formatCompactNumber(session.tokens?.totalTokens ?? 0)}
                  />
                  <MetricBox
                    label="CPU"
                    value={
                      <UsageBarsCell
                        title="CPU"
                        value={session.live?.cpuPercent}
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
                        value={session.live?.memoryPercent}
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
                {session.live?.command ? (
                  <div className="mt-density-2 truncate text-[11px] text-muted-foreground">
                    {commandLabel(session.live.command)}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function handleCardKeyDown(
  event: React.KeyboardEvent<HTMLDivElement>,
  session: SessionRecord,
  onOpen: (session: SessionRecord) => void,
) {
  if (event.key !== "Enter" && event.key !== " ") return;
  if (session.detailAvailable === false) return;
  event.preventDefault();
  onOpen(session);
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

function buildThroughputChart(
  groups: SessionThroughputGroup[],
  metric: ThroughputMetric,
): ThroughputChartModel {
  const responses: Record<string, TimeseriesResponse> = {};
  const series: TimeseriesSeries[] = [];
  const ranked = [...groups]
    .filter((group) => (group.points?.length ?? 0) >= 2 && (group[metric] ?? 0) > 0)
    .sort((left, right) => (right[metric] ?? 0) - (left[metric] ?? 0))
    .slice(0, 5);

  for (const [index, group] of ranked.entries()) {
    const id = `${metric}-${index}-${hashThroughputGroup(group)}`;
    const points = [];
    for (const point of group.points ?? []) {
      const value = point[metric] ?? 0;
      if (!Number.isFinite(value)) continue;
      points.push({ at: point.at, value });
    }
    if (points.length < 2) continue;
    series.push({
      id,
      label: throughputGroupLabel(group),
      unit: " tok/s",
    });
    responses[id] = { id, points };
  }

  return { series, responses };
}

function staticThroughputFetcher(responses: Record<string, TimeseriesResponse>) {
  return async (url: string): Promise<TimeseriesResponse> => {
    const path = url.split("?")[0] ?? url;
    const id = decodeURIComponent(path.split("/").filter(Boolean).pop() ?? path);
    return responses[id] ?? { id, points: [] };
  };
}

function throughputGroupLabel(group: SessionThroughputGroup) {
  const effort = effortLabel(group.reasoningEffort);
  return effort && effort !== "default" ? `${group.model} / ${effort}` : group.model;
}

function hashThroughputGroup(group: SessionThroughputGroup) {
  const lastPoint = group.points?.[group.points.length - 1];
  const input = [
    group.key,
    group.sessions,
    group.durationSeconds,
    group.outputTokens,
    group.totalTokens,
    group.contextTokens,
    lastPoint?.at,
    lastPoint?.outputTokensPerSecond,
    lastPoint?.contextTokensPerSecond,
  ].join("|");
  let hash = 0;
  for (let i = 0; i < input.length; i++) {
    hash = (hash * 31 + input.charCodeAt(i)) >>> 0;
  }
  return hash.toString(36);
}

function formatRate(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0/s";
  if (value >= 1000) return `${formatCompactNumber(Math.round(value))}/s`;
  if (value >= 10) return `${value.toFixed(0)}/s`;
  return `${value.toFixed(1)}/s`;
}

function formatOptionalRate(value: number | undefined) {
  return value !== undefined && value > 0 ? formatRate(value) : "--";
}

function formatOptionalPercent(value: number | undefined) {
  if (value === undefined || !Number.isFinite(value) || value <= 0) return "--";
  return `${value.toFixed(value >= 10 ? 0 : 1)}%`;
}
