import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  SearchInput,
  SegmentedControl,
  Switch,
  type AppShellNavSection,
} from "@flanksource/clicky-ui/components";
import { SessionViewer } from "@flanksource/clicky-ui/ai";
import { CAPTAIN_SIDEBAR_COLLAPSE_KEY } from "./shell";
import {
  SOURCE_OPTIONS,
  errorMessage,
  fetchLiveSessions,
  fetchSession,
  formatCompactNumber,
  formatCost,
  formatTime,
  healthClassName,
  sessionTitle,
  type SessionDashboard,
  type SessionRecord,
  type SourceFilter,
} from "./sessionData";

type SessionBrowserProps = {
  selectedId?: string;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
  navSections: AppShellNavSection[];
  actions: ReactNode;
};

export function SessionBrowser({
  selectedId,
  onNavigate,
  navSections,
  actions,
}: SessionBrowserProps) {
  const [source, setSource] = useState<SourceFilter>("all");
  const [allProjects, setAllProjects] = useState(false);
  const [query, setQuery] = useState("");

  const listQuery = useQuery({
    queryKey: ["sessions", source, allProjects, query],
    queryFn: () => fetchLiveSessions({ source, allProjects, query }),
  });
  const sessions = listQuery.data?.sessions ?? [];

  useEffect(() => {
    if (selectedId || sessions.length === 0) return;
    const firstDetail = sessions.find((session) => session.detailAvailable !== false);
    if (!firstDetail) return;
    onNavigate(`/sessions/${encodeURIComponent(firstDetail.key)}`, { replace: true });
  }, [onNavigate, selectedId, sessions]);

  const selectedSummary = useMemo(
    () => sessions.find((session) => session.key === selectedId || session.id === selectedId),
    [selectedId, sessions],
  );

  const detailQuery = useQuery({
    queryKey: ["session", selectedId],
    queryFn: () => fetchSession(String(selectedId)),
    enabled: Boolean(selectedId),
  });
  const selected = detailQuery.data ?? selectedSummary;

  return (
    <AppShell
      className="h-screen"
      brand={<div className="text-sm font-semibold">Captain</div>}
      navSections={navSections}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      bodySidebar={
        <SessionSidebar
          source={source}
          onSourceChange={setSource}
          allProjects={allProjects}
          onAllProjectsChange={setAllProjects}
          query={query}
          onQueryChange={setQuery}
          sessions={sessions}
          summary={listQuery.data?.summary}
          selectedId={selectedId}
          total={listQuery.data?.total ?? 0}
          loading={listQuery.isLoading}
          error={listQuery.error}
          onSelect={(session) => onNavigate(`/sessions/${encodeURIComponent(session.key)}`)}
          onRefresh={() => void listQuery.refetch()}
        />
      }
      bodyHeader={<SessionHeader session={selected} loading={detailQuery.isLoading} />}
      bodyActions={
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void listQuery.refetch();
            if (selectedId) void detailQuery.refetch();
          }}
        >
          Refresh
        </Button>
      }
      bodySplit={28}
      contentClassName="p-0 overflow-hidden"
    >
      <SessionDetail
        session={detailQuery.data}
        loading={detailQuery.isLoading}
        error={detailQuery.error}
        hasSelection={Boolean(selectedId)}
      />
    </AppShell>
  );
}

function SessionSidebar({
  source,
  onSourceChange,
  allProjects,
  onAllProjectsChange,
  query,
  onQueryChange,
  sessions,
  summary,
  selectedId,
  total,
  loading,
  error,
  onSelect,
  onRefresh,
}: {
  source: SourceFilter;
  onSourceChange: (source: SourceFilter) => void;
  allProjects: boolean;
  onAllProjectsChange: (enabled: boolean) => void;
  query: string;
  onQueryChange: (query: string) => void;
  sessions: SessionRecord[];
  summary?: SessionDashboard;
  selectedId?: string;
  total: number;
  loading: boolean;
  error: unknown;
  onSelect: (session: SessionRecord) => void;
  onRefresh: () => void;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="shrink-0 space-y-density-2 border-b border-border p-density-3">
        <div className="flex items-center justify-between gap-density-2">
          <div className="text-sm font-semibold">Sessions</div>
          <Button size="sm" variant="ghost" onClick={onRefresh}>
            Refresh
          </Button>
        </div>
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
          className="w-full"
        />
        <Switch
          checked={allProjects}
          onChange={onAllProjectsChange}
          label="All projects"
        />
        <SessionSummary summary={summary} loading={loading} />
        <div className="text-xs text-muted-foreground">
          {loading ? "Loading..." : `${sessions.length} shown / ${total} total`}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {error ? (
          <div className="p-density-3 text-sm text-destructive">{errorMessage(error)}</div>
        ) : sessions.length === 0 && !loading ? (
          <div className="p-density-3 text-sm text-muted-foreground">No sessions found.</div>
        ) : (
          <div className="divide-y divide-border">
            {sessions.map((session) => {
              const active = session.key === selectedId || session.id === selectedId;
              const detailAvailable = session.detailAvailable !== false;
              return (
                <button
                  key={session.key}
                  type="button"
                  onClick={() => detailAvailable && onSelect(session)}
                  disabled={!detailAvailable}
                  className={[
                    "block w-full px-density-3 py-density-2 text-left transition-colors",
                    active ? "bg-accent text-accent-foreground" : "hover:bg-muted/60",
                    detailAvailable ? "" : "cursor-default opacity-75",
                  ].join(" ")}
                >
                  <div className="flex min-w-0 items-center justify-between gap-density-2">
                    <span className="min-w-0 truncate text-sm font-medium">
                      {sessionTitle(session)}
                    </span>
                    <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
                      {session.source}
                    </span>
                  </div>
                  <SessionBadges session={session} />
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {formatTime(session.endedAt ?? session.startedAt)}
                    {session.model ? ` - ${session.model}` : ""}
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">
                    {session.cwd ?? session.id}
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

function SessionHeader({
  session,
  loading,
}: {
  session?: SessionRecord;
  loading: boolean;
}) {
  if (loading && !session) {
    return <div className="text-sm text-muted-foreground">Loading session...</div>;
  }
  if (!session) {
    return (
      <div>
        <div className="text-sm font-semibold">Session Browser</div>
        <div className="text-xs text-muted-foreground">Select a session to inspect activity.</div>
      </div>
    );
  }
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 flex-wrap items-center gap-density-2">
        <div className="truncate text-sm font-semibold">{sessionTitle(session)}</div>
        <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
          {session.source}
        </span>
        {session.live && (
          <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
            {session.live.status || "live"}
          </span>
        )}
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-density-3 gap-y-1 text-xs text-muted-foreground">
        {session.model && <span>{session.model}</span>}
        {session.reasoningEffort && <span>reasoning={session.reasoningEffort}</span>}
        <span>{session.toolCalls} actions</span>
        <span>{session.messages} messages</span>
        {session.context && <span>{session.context.freePercent}% context free</span>}
        {session.costUsd ? <span>{formatCost(session.costUsd)}</span> : null}
        {session.live?.pid && <span>pid={session.live.pid}</span>}
        {session.cwd && <span className="max-w-full truncate">{session.cwd}</span>}
      </div>
    </div>
  );
}

function SessionDetail({
  session,
  loading,
  error,
  hasSelection,
}: {
  session?: SessionRecord;
  loading: boolean;
  error: unknown;
  hasSelection: boolean;
}) {
  if (!hasSelection) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
        Select a session.
      </div>
    );
  }
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
  return (
    <div className="min-h-0 flex-1 overflow-auto p-density-4 md:p-density-6">
      <SessionViewer session={session?.entries ?? []} defaultExpanded={false} />
    </div>
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
    <div className="grid grid-cols-3 gap-1.5">
      {values.map(([label, value]) => (
        <div key={label} className="min-w-0 rounded border border-border px-2 py-1">
          <div className="truncate text-[10px] uppercase text-muted-foreground">{label}</div>
          <div className="truncate text-xs font-medium">{value}</div>
        </div>
      ))}
    </div>
  );
}

function SessionBadges({ session }: { session: SessionRecord }) {
  const health = session.health ?? [];
  const badges = [
    session.live
      ? {
          key: "live",
          label: session.live.status || "live",
          className: "border-emerald-500/40 text-emerald-700",
        }
      : null,
    ...health.slice(0, 2).map((signal) => ({
      key: signal.kind,
      label: signal.kind.replace(/_/g, " "),
      className: healthClassName(signal.severity),
    })),
  ].filter(Boolean) as Array<{ key: string; label: string; className: string }>;
  if (badges.length === 0) return null;
  return (
    <div className="mt-1 flex min-w-0 flex-wrap gap-1">
      {badges.map((badge) => (
        <span
          key={badge.key}
          className={`rounded border px-1.5 py-0.5 text-[10px] uppercase ${badge.className}`}
        >
          {badge.label}
        </span>
      ))}
    </div>
  );
}
