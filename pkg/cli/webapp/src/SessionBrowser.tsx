import { useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  type AppShellProps,
  type AppShellNavSection,
} from "@flanksource/clicky-ui/components";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY } from "./shellHelpers";
import { SessionDetail } from "./SessionDetail";
import { TimingBadge } from "./TimingBadge";
import type { TimingMetric } from "./serverTiming";
import { SessionListToolbar } from "./SessionListToolbar";
import {
  SessionTable,
  type DashboardSort,
  type SortDirection,
} from "./SessionTable";
import {
  compareSessions,
  defaultSortDirection,
  groupSessionsByProject,
} from "./sessionTableHelpers";
import {
  errorMessage,
  fetchSession,
  mergeSessionListPages,
  type SessionDashboard,
  type SessionRecord,
  type ProjectScope,
} from "./sessionData";
import { fetchSessionListPage } from "./sessionListData";
import {
  sessionActivityBounds,
  sessionListPath,
  type SessionListFilters,
} from "./sessionListFilters";

type Navigate = (to: string, opts?: { replace?: boolean }) => void;

type SessionBrowserProps = {
  selectedId?: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
  projectScope: ProjectScope;
  filters: SessionListFilters;
};

export function SessionBrowser({
  selectedId,
  onNavigate,
  navSections,
  actions,
  search,
  projectScope,
  filters,
}: SessionBrowserProps) {
  return selectedId ? (
    <SessionDetailPage
      selectedId={selectedId}
      onNavigate={onNavigate}
      navSections={navSections}
      actions={actions}
      search={search}
      projectScope={projectScope}
      filters={filters}
    />
  ) : (
    <SessionListPage
      onNavigate={onNavigate}
      navSections={navSections}
      actions={actions}
      search={search}
      projectScope={projectScope}
      filters={filters}
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
  search,
  projectScope,
  filters,
}: {
  selectedId: string;
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
  projectScope: ProjectScope;
  filters: SessionListFilters;
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
      search={search}
      bodyHeader={
        <SessionHeader
          timing={detailQuery.data?.timing}
          loading={detailQuery.isLoading}
        />
      }
      bodyActions={
        <div className="flex items-center gap-density-2">
          <Button
            size="sm"
            variant="ghost"
            onClick={() =>
              onNavigate(sessionListPath("/sessions", projectScope, filters))
            }
          >
            ← Sessions
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => void detailQuery.refetch()}
          >
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
        onRefresh={() => detailQuery.refetch()}
      />
    </AppShell>
  );
}

function SessionListPage({
  onNavigate,
  navSections,
  actions,
  search,
  projectScope,
  filters,
}: {
  onNavigate: Navigate;
  navSections: AppShellNavSection[];
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
  projectScope: ProjectScope;
  filters: SessionListFilters;
}) {
  const activityRange = useMemo(() => {
    try {
      return { bounds: sessionActivityBounds(filters.from, filters.to) };
    } catch (error) {
      return { error };
    }
  }, [filters.from, filters.to]);

  const listQuery = useInfiniteQuery({
    queryKey: [
      "sessions",
      filters.mode,
      filters.source,
      projectScope,
      filters.query,
      filters.from,
      filters.to,
    ],
    queryFn: ({ pageParam }) =>
      fetchSessionListPage({
        mode: filters.mode,
        source: filters.source,
        project: projectScope,
        query: filters.query,
        ...(activityRange.bounds ?? {}),
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.nextCursor,
    enabled: !activityRange.error,
  });
  const result = mergeSessionListPages(listQuery.data?.pages ?? []);
  const sessions = result?.sessions ?? [];

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      search={search}
      bodyHeader={<div className="text-sm font-semibold">Sessions</div>}
      bodyActions={
        <Button
          size="sm"
          variant="outline"
          onClick={() => void listQuery.refetch()}
        >
          Refresh
        </Button>
      }
      contentClassName="p-0 overflow-hidden"
    >
      <SessionList
        filters={filters}
        onFiltersChange={(nextFilters) =>
          onNavigate(
            sessionListPath("/sessions", projectScope, nextFilters),
            { replace: true },
          )
        }
        sessions={sessions}
        summary={result?.summary}
        timing={result?.timing}
        total={result?.total ?? 0}
        loading={listQuery.isLoading}
        loadingMore={listQuery.isFetchingNextPage}
        hasMore={listQuery.hasNextPage}
        onLoadMore={() => listQuery.fetchNextPage()}
        error={activityRange.error ?? listQuery.error}
        onSelect={(session) =>
          onNavigate(
            sessionListPath(
              `/sessions/${encodeURIComponent(session.key)}`,
              projectScope,
              filters,
            ),
          )
        }
      />
    </AppShell>
  );
}

function SessionList({
  filters,
  onFiltersChange,
  sessions,
  summary,
  timing,
  total,
  loading,
  loadingMore,
  hasMore,
  onLoadMore,
  error,
  onSelect,
}: {
  filters: SessionListFilters;
  onFiltersChange: (filters: SessionListFilters) => void;
  sessions: SessionRecord[];
  summary?: SessionDashboard;
  timing?: TimingMetric[];
  total: number;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  onLoadMore: () => Promise<unknown>;
  error: unknown;
  onSelect: (session: SessionRecord) => void;
}) {
  const [sort, setSort] = useState<DashboardSort>("recent");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");

  const groups = useMemo(
    () =>
      groupSessionsByProject(
        [...sessions].sort((left, right) =>
          compareSessions(left, right, sort, sortDirection),
        ),
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
      <SessionListToolbar
        filters={filters}
        onFiltersChange={onFiltersChange}
        summary={summary}
        timing={timing}
        shown={sessions.length}
        total={total}
        loading={loading}
      />

      <div className="min-h-0 flex-1 overflow-y-auto p-density-3">
        {error ? (
          <div className="text-sm text-destructive">{errorMessage(error)}</div>
        ) : sessions.length === 0 && !loading ? (
          <div className="text-sm text-muted-foreground">
            No sessions found.
          </div>
        ) : (
          <div className="space-y-density-3">
            <SessionTable
              groups={groups}
              sort={sort}
              sortDirection={sortDirection}
              onSortChange={toggleSort}
              onOpen={onSelect}
            />
            {hasMore ? (
              <div className="flex justify-center">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={loadingMore}
                  onClick={() => void onLoadMore()}
                >
                  {loadingMore ? "Loading..." : "Load more"}
                </Button>
              </div>
            ) : null}
          </div>
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
    return (
      <div className="text-sm text-muted-foreground">Loading session...</div>
    );
  }
  return (
    <div className="flex items-center gap-density-2">
      <div className="text-sm font-semibold">Session</div>
      <TimingBadge metrics={timing} />
    </div>
  );
}
