import { SessionViewer } from "@flanksource/clicky-ui/ai";
import { usePromptRunStream, type PromptRunStreamStatus, type PromptRunSummary } from "./hooks/usePromptRunStream";

/**
 * PromptRunStream renders a prompt run's session history live: it subscribes to
 * the run's SSE stream and feeds the growing SessionEntry[] into SessionViewer,
 * with a status pill and a completion summary footer.
 */
export function PromptRunStream({ runID }: { runID: string }) {
  const { entries, summary, status, error } = usePromptRunStream(runID);
  const empty = entries.length === 0;

  return (
    <div className="flex h-full min-h-0 flex-col gap-density-3">
      <div className="flex items-center gap-density-2 text-xs text-muted-foreground">
        <StatusPill status={status} />
      </div>
      {error && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-density-3 text-sm text-destructive">
          {error}
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-auto rounded-md border border-border">
        {empty ? (
          <div className="flex min-h-[240px] items-center justify-center p-density-6 text-sm text-muted-foreground">
            {status === "done" ? "No session activity." : "Starting run…"}
          </div>
        ) : (
          <div className="p-density-4">
            <SessionViewer session={entries} defaultExpanded={false} />
          </div>
        )}
      </div>
      {summary && <RunSummaryFooter summary={summary} />}
    </div>
  );
}

const STATUS_LABEL: Record<PromptRunStreamStatus, string> = {
  idle: "idle",
  connecting: "connecting…",
  streaming: "streaming…",
  done: "done",
  error: "error",
};

const STATUS_DOT: Record<PromptRunStreamStatus, string> = {
  idle: "bg-muted-foreground",
  connecting: "bg-blue-500 animate-pulse",
  streaming: "bg-blue-500 animate-pulse",
  done: "bg-green-500",
  error: "bg-destructive",
};

function StatusPill({ status }: { status: PromptRunStreamStatus }) {
  return (
    <span className="inline-flex items-center gap-density-1">
      <span className={`size-2 rounded-full ${STATUS_DOT[status]}`} />
      {STATUS_LABEL[status]}
    </span>
  );
}

function RunSummaryFooter({ summary }: { summary: PromptRunSummary }) {
  const parts: string[] = [];
  if (summary.model) parts.push(summary.model);
  if (summary.backend) parts.push(summary.backend);
  if (summary.duration) parts.push(summary.duration);
  if (summary.inputTokens != null) parts.push(`${summary.inputTokens} in`);
  if (summary.outputTokens != null) parts.push(`${summary.outputTokens} out`);
  if (summary.costUSD != null) parts.push(`$${summary.costUSD.toFixed(4)}`);
  if (summary.sessionId) parts.push(`session ${summary.sessionId}`);
  return (
    <div className="flex flex-wrap gap-density-2 border-t border-border pt-density-2 text-xs text-muted-foreground">
      {parts.map((part, i) => (
        <span key={i} className="truncate">
          {part}
        </span>
      ))}
    </div>
  );
}
