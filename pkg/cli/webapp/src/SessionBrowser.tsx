import { useState } from "react";
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
  SOURCE_OPTIONS,
  errorMessage,
  fetchLiveSessions,
  fetchSession,
  formatCompactNumber,
  formatCost,
  formatTime,
  healthClassName,
  sessionCostTotal,
  sessionTitle,
  sessionToolCount,
  unifiedSessionTitle,
  type SessionDashboard,
  type SessionRecord,
  type ProjectScope,
  type SourceFilter,
  type UnifiedSession,
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
          session={detailQuery.data}
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
        session={detailQuery.data}
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

      <div className="min-h-0 flex-1 overflow-y-auto">
        {error ? (
          <div className="p-density-3 text-sm text-destructive">{errorMessage(error)}</div>
        ) : sessions.length === 0 && !loading ? (
          <div className="p-density-3 text-sm text-muted-foreground">No sessions found.</div>
        ) : (
          <div className="mx-auto max-w-4xl divide-y divide-border">
            {sessions.map((session) => {
              const detailAvailable = session.detailAvailable !== false;
              return (
                <button
                  key={session.key}
                  type="button"
                  onClick={() => detailAvailable && onSelect(session)}
                  disabled={!detailAvailable}
                  className={[
                    "block w-full px-density-3 py-density-2 text-left transition-colors",
                    detailAvailable ? "hover:bg-muted/60" : "cursor-default opacity-75",
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
  timing,
  loading,
}: {
  session?: UnifiedSession;
  timing?: TimingMetric[];
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
        <div className="truncate text-sm font-semibold">{unifiedSessionTitle(session)}</div>
        <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
          {session.source}
        </span>
        {session.live && (
          <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
            {session.live.status || "live"}
          </span>
        )}
        <TimingBadge metrics={timing} />
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-density-3 gap-y-1 text-xs text-muted-foreground">
        {session.model && <span>{session.model}</span>}
        <span>{sessionToolCount(session.messages)} actions</span>
        <span>{session.messages?.length ?? 0} messages</span>
        {session.turns?.length ? <span>{session.turns.length} turns</span> : null}
        {session.agents?.length ? <span>{session.agents.length} agents</span> : null}
        {session.files ? <span>{fileCountLabel(session.files)}</span> : null}
        {session.approvals ? <span>{approvalCountLabel(session.approvals)}</span> : null}
        {sessionCostTotal(session.cost) ? <span>{formatCost(sessionCostTotal(session.cost))}</span> : null}
        {session.provider && <span>{session.provider}</span>}
        {session.version && <span>{session.version}</span>}
        {session.git?.branch && <span>{session.git.branch}</span>}
        {session.live?.pid && <span>pid={session.live.pid}</span>}
        {session.historyFile && <span className="max-w-full truncate">{session.historyFile}</span>}
        {session.cwd && <span className="max-w-full truncate">{session.cwd}</span>}
      </div>
    </div>
  );
}

function SessionDetail({
  session,
  loading,
  error,
}: {
  session?: UnifiedSession;
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
  return (
    // AppShell's content region is a bounded block (h-full), not a flex parent, so
    // `h-full` (not `flex-1`) is what gives the viewer a definite height to scroll within.
    <div className="flex h-full min-h-0 flex-col">
      <SessionInspector session={session ?? []} transcriptProps={{ defaultExpanded: false }} />
    </div>
  );
}

function fileCountLabel(files: NonNullable<UnifiedSession["files"]>) {
  const read = files.read?.length ?? 0;
  const written = files.written?.length ?? 0;
  if (read && written) return `${read} read / ${written} written`;
  if (read) return `${read} read`;
  if (written) return `${written} written`;
  return "0 files";
}

function approvalCountLabel(approvals: NonNullable<UnifiedSession["approvals"]>) {
  const approved = approvals.approved ?? 0;
  const denied = approvals.denied ?? 0;
  if (approved && denied) return `${approved} approved / ${denied} denied`;
  if (approved) return `${approved} approved`;
  if (denied) return `${denied} denied`;
  return "0 approvals";
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
