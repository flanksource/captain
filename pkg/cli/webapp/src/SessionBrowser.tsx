import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  SearchInput,
  SegmentedControl,
  type AppShellProps,
  type AppShellNavSection,
} from "@flanksource/clicky-ui/components";
import { SessionInspector } from "@flanksource/clicky-ui/ai";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY, withProjectScope } from "./shellHelpers";
import { TimingBadge } from "./TimingBadge";
import type { TimingMetric } from "./serverTiming";
import {
  SessionTable,
  compareSessions,
  defaultSortDirection,
  groupSessionsByProject,
  type DashboardSort,
  type SortDirection,
} from "./SessionTable";
import {
  SOURCE_OPTIONS,
  errorMessage,
  fetchLiveSessions,
  fetchSession,
  formatCompactNumber,
  formatCost,
  type SessionDashboard,
  type SessionGetItem,
  type SessionGetResult,
  type SessionRecord,
  type ProjectScope,
  type SourceFilter,
} from "./sessionData";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type SessionBrowserProps = {
  selectedId?: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  projectScope: ProjectScope;
};

export function SessionBrowser({
  selectedId,
  onNavigate,
  navSections,
  actions,
  projectScope,
}: SessionBrowserProps) {
  return selectedId ? (
    <SessionDetailPage
      selectedId={selectedId}
      onNavigate={onNavigate}
      navSections={navSections}
      actions={actions}
      projectScope={projectScope}
    />
  ) : (
    <SessionListPage
      onNavigate={onNavigate}
      navSections={navSections}
      actions={actions}
      projectScope={projectScope}
    />
  );
}

// The detail page is a listing-free view: the dedicated session listing lives on
// /sessions (SessionListPage) and on the home dashboard, so here we render only
// the selected session's header and transcript.
function SessionDetailPage({
  selectedId,
  onNavigate,
  navSections,
  actions,
  projectScope,
}: {
  selectedId: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  projectScope: ProjectScope;
}) {
  const detailQuery = useQuery({
    queryKey: ["session", selectedId],
    queryFn: () => fetchSession(selectedId),
  });

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      bodyHeader={
        <SessionHeader
          timing={detailQuery.data?.timing}
          loading={detailQuery.isLoading}
        />
      }
      bodyActions={
        <div className="flex items-center gap-density-2">
          <Button size="sm" variant="ghost" onClick={() => onNavigate(withProjectScope("/sessions", projectScope))}>
            ← Sessions
          </Button>
          <Button size="sm" variant="outline" onClick={() => void detailQuery.refetch()}>
            Refresh
          </Button>
        </div>
      }
      contentClassName="p-0 overflow-hidden"
    >
      <SessionDetail
        result={detailQuery.data}
        loading={detailQuery.isLoading}
        error={detailQuery.error}
      />
    </AppShell>
  );
}

function SessionListPage({
  onNavigate,
  navSections,
  actions,
  projectScope,
}: {
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  projectScope: ProjectScope;
}) {
  const [source, setSource] = useState<SourceFilter>("all");
  const [query, setQuery] = useState("");

  const listQuery = useQuery({
    queryKey: ["sessions", source, projectScope, query],
    queryFn: () => fetchLiveSessions({ source, project: projectScope, query }),
  });
  const sessions = listQuery.data?.sessions ?? [];

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      bodyHeader={<div className="text-sm font-semibold">Sessions</div>}
      bodyActions={
        <Button size="sm" variant="outline" onClick={() => void listQuery.refetch()}>
          Refresh
        </Button>
      }
      contentClassName="p-0 overflow-hidden"
    >
      <SessionList
        source={source}
        onSourceChange={setSource}
        query={query}
        onQueryChange={setQuery}
        sessions={sessions}
        summary={listQuery.data?.summary}
        timing={listQuery.data?.timing}
        total={listQuery.data?.total ?? 0}
        loading={listQuery.isLoading}
        error={listQuery.error}
        onSelect={(session) => onNavigate(withProjectScope(`/sessions/${encodeURIComponent(session.key)}`, projectScope))}
      />
    </AppShell>
  );
}

function SessionList({
  source,
  onSourceChange,
  query,
  onQueryChange,
  sessions,
  summary,
  timing,
  total,
  loading,
  error,
  onSelect,
}: {
  source: SourceFilter;
  onSourceChange: (source: SourceFilter) => void;
  query: string;
  onQueryChange: (query: string) => void;
  sessions: SessionRecord[];
  summary?: SessionDashboard;
  timing?: TimingMetric[];
  total: number;
  loading: boolean;
  error: unknown;
  onSelect: (session: SessionRecord) => void;
}) {
  const [sort, setSort] = useState<DashboardSort>("recent");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");

  const groups = useMemo(
    () =>
      groupSessionsByProject(
        [...sessions].sort((left, right) => compareSessions(left, right, sort, sortDirection)),
      ),
    [sessions, sort, sortDirection],
  );

  const toggleSort = (nextSort: DashboardSort) => {
    if (sort === nextSort) {
      setSortDirection((direction) => (direction === "asc" ? "desc" : "asc"));
      return;
    }
    setSort(nextSort);
    setSortDirection(defaultSortDirection(nextSort));
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="shrink-0 space-y-density-2 border-b border-border p-density-3">
        <div className="grid gap-density-2 md:grid-cols-[minmax(14rem,1fr)_auto]">
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
        </div>
        <SessionSummary summary={summary} loading={loading} />
        <div className="flex items-center justify-between gap-density-2 text-xs text-muted-foreground">
          <span>{loading ? "Loading..." : `${sessions.length} shown / ${total} total`}</span>
          <TimingBadge metrics={timing} align="right" />
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-density-3">
        {error ? (
          <div className="text-sm text-destructive">{errorMessage(error)}</div>
        ) : sessions.length === 0 && !loading ? (
          <div className="text-sm text-muted-foreground">No sessions found.</div>
        ) : (
          <SessionTable
            groups={groups}
            sort={sort}
            sortDirection={sortDirection}
            onSortChange={toggleSort}
            onOpen={onSelect}
          />
        )}
      </div>
    </div>
  );
}

function SessionHeader({
  timing,
  loading,
}: {
  timing?: TimingMetric[];
  loading: boolean;
}) {
  if (loading) {
    return <div className="text-sm text-muted-foreground">Loading session...</div>;
  }
  return (
    <div className="flex items-center gap-density-2">
      <div className="text-sm font-semibold">Session</div>
      <TimingBadge metrics={timing} />
    </div>
  );
}

function SessionDetail({
  result,
  loading,
  error,
}: {
  result?: SessionGetResult;
  loading: boolean;
  error: unknown;
}) {
  if (loading) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
        Loading session...
      </div>
    );
  }
  if (error) {
    return (
      <div className="min-h-0 flex-1 overflow-auto p-6 text-sm text-destructive">
        {errorMessage(error)}
      </div>
    );
  }
  if (!result?.sessions.length) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        No matching sessions.
      </div>
    );
  }
  return (
    <div className="h-full min-h-0 overflow-auto">
      {result.sessions.map((item) => (
        <SessionGetItemDetail key={item.captainId} item={item} single={result.sessions.length === 1} />
      ))}
    </div>
  );
}

function SessionGetItemDetail({ item, single }: { item: SessionGetItem; single: boolean }) {
  return (
    <section className={single ? "flex h-full min-h-0 flex-col" : "border-b border-border"}>
      {!single ? (
        <div className="shrink-0 border-b border-border px-density-4 py-density-3 text-xs">
          <div className="font-mono font-semibold text-foreground">{item.captainId}</div>
          <div className="mt-1 flex flex-wrap gap-x-density-3 gap-y-1 text-muted-foreground">
            <span>{item.summary.source}</span>
            {item.providerSessionId && <span>provider={item.providerSessionId}</span>}
            {item.host && <span>host={item.host}</span>}
            {item.summary.project && <span>project={item.summary.project}</span>}
            {item.summary.cwd && <span className="max-w-full truncate">{item.summary.cwd}</span>}
          </div>
        </div>
      ) : null}
      {item.detail ? (
        <div className={single ? "min-h-0 flex-1" : "h-[70vh] min-h-[32rem]"}>
          <SessionInspector session={item.detail} transcriptProps={{ defaultExpanded: false }} />
        </div>
      ) : (
        <div className="p-density-4 text-sm text-muted-foreground">Transcript unavailable.</div>
      )}
    </section>
  );
}

function SessionSummary({
  summary,
  loading,
}: {
  summary?: SessionDashboard;
  loading: boolean;
}) {
  if (!summary && loading) {
    return null;
  }
  const values = [
    ["Live", summary ? `${summary.liveSessions}/${summary.totalSessions}` : "--"],
    ["Active", summary?.activeSessions ?? 0],
    ["Alerts", summary?.alertSessions ?? 0],
    ["Tokens", formatCompactNumber(summary?.totalTokens ?? 0)],
    ["Cost", formatCost(summary?.costUsd ?? 0)],
    ["Context", summary?.lowestContextFree !== undefined ? `${summary.lowestContextFree}%` : "--"],
  ];
  return (
    <div className="grid grid-cols-3 gap-1.5 md:grid-cols-6">
      {values.map(([label, value]) => (
        <div key={label} className="min-w-0 rounded border border-border px-2 py-1">
          <div className="truncate text-[10px] uppercase text-muted-foreground">{label}</div>
          <div className="truncate text-xs font-medium">{value}</div>
        </div>
      ))}
    </div>
  );
}
