import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  SearchInput,
  SegmentedControl,
  Switch,
} from "@flanksource/clicky-ui/components";
import { SessionViewer, type SessionEntry } from "@flanksource/clicky-ui/ai";
import { apiClient } from "./api";

type SourceFilter = "all" | "claude" | "codex";

type SessionListResult = {
  sessions: SessionRecord[];
  total: number;
  source: SourceFilter;
  scope: "current" | "all";
};

type SessionRecord = {
  key: string;
  id: string;
  source: "claude" | "codex";
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
  entries?: SessionEntry[];
};

type SessionBrowserProps = {
  selectedId?: string;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
  nav: ReactNode;
  actions: ReactNode;
};

const SOURCE_OPTIONS = [
  { id: "all", label: "All" },
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
] satisfies Array<{ id: SourceFilter; label: string }>;

export function SessionBrowser({
  selectedId,
  onNavigate,
  nav,
  actions,
}: SessionBrowserProps) {
  const [source, setSource] = useState<SourceFilter>("all");
  const [allProjects, setAllProjects] = useState(false);
  const [query, setQuery] = useState("");

  const listQuery = useQuery({
    queryKey: ["sessions", source, allProjects, query],
    queryFn: () => fetchSessions({ source, allProjects, query }),
  });
  const sessions = listQuery.data?.sessions ?? [];

  useEffect(() => {
    if (selectedId || sessions.length === 0) return;
    onNavigate(`/sessions/${encodeURIComponent(sessions[0].key)}`, { replace: true });
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
      nav={nav}
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
              return (
                <button
                  key={session.key}
                  type="button"
                  onClick={() => onSelect(session)}
                  className={[
                    "block w-full px-density-3 py-density-2 text-left transition-colors",
                    active ? "bg-accent text-accent-foreground" : "hover:bg-muted/60",
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
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-density-3 gap-y-1 text-xs text-muted-foreground">
        {session.model && <span>{session.model}</span>}
        {session.reasoningEffort && <span>reasoning={session.reasoningEffort}</span>}
        <span>{session.toolCalls} actions</span>
        <span>{session.messages} messages</span>
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
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        Select a session.
      </div>
    );
  }
  if (loading) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        Loading session...
      </div>
    );
  }
  if (error) {
    return (
      <div className="h-full overflow-auto p-6 text-sm text-destructive">
        {errorMessage(error)}
      </div>
    );
  }
  return (
    <div className="h-full overflow-auto p-density-4 md:p-density-6">
      <SessionViewer session={session?.entries ?? []} defaultExpanded={false} />
    </div>
  );
}

async function fetchSessions(params: {
  source: SourceFilter;
  allProjects: boolean;
  query: string;
}) {
  const response = await apiClient.executeCommand(
    "/api/v1/sessions",
    "GET",
    {
      source: params.source,
      all: params.allProjects ? "true" : "false",
      q: params.query,
      limit: "100",
    },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load sessions.");
  }
  return response.parsed as SessionListResult;
}

async function fetchSession(id: string) {
  const response = await apiClient.executeCommand(
    "/api/v1/sessions/{id}",
    "GET",
    { id },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load session.");
  }
  return response.parsed as SessionRecord;
}

function sessionTitle(session: SessionRecord) {
  if (session.gitBranch) return `${session.gitBranch} - ${shortID(session.id)}`;
  if (session.model) return `${session.model} - ${shortID(session.id)}`;
  return shortID(session.id) || session.key;
}

function shortID(id: string) {
  return id.length > 12 ? id.slice(0, 12) : id;
}

function formatTime(value: string | undefined) {
  if (!value) return "No timestamp";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
