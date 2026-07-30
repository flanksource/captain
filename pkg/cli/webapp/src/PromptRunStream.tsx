import { SessionChatComposer, SessionViewer } from "@flanksource/clicky-ui/ai";
import { Button } from "@flanksource/clicky-ui/components";
import {
  type PromptRunStreamStatus,
  type PromptRunSummary,
} from "./hooks/usePromptRunStream";
import { useSessionChat } from "./hooks/useSessionChat";

/**
 * PromptRunStream renders a prompt run's session history live: it subscribes to
 * the run's SSE stream and feeds the growing session.Message[] into SessionViewer,
 * with a status pill and a completion summary footer.
 */
export function PromptRunStream({ runID }: { runID: string }) {
  const chat = useSessionChat({ initialRunID: runID });
  const { messages, summary, status, error, run, chatState } = chat;
  const empty = messages.length === 0;

  return (
    <div className="flex h-full min-h-0 flex-col gap-density-3">
      <div className="flex items-center justify-between gap-density-2 text-xs text-muted-foreground">
        <StatusPill status={status} />
        {status !== "done" && status !== "error" ? (
          <Button size="sm" variant="outline" onClick={chat.stop}>
            Stop run
          </Button>
        ) : null}
      </div>
      {error && (
        <RunFailureDetails
          error={error}
          runID={summary?.runId ?? run?.runId ?? runID}
          sessionID={summary?.sessionId ?? run?.sessionId}
          model={summary?.model ?? run?.model}
          backend={summary?.backend ?? run?.backend}
        />
      )}
      <div className="min-h-0 flex-1 overflow-auto rounded-md border border-border">
        {empty ? (
          <div className="flex min-h-[240px] items-center justify-center p-density-6 text-sm text-muted-foreground">
            {status === "done"
              ? "No session activity."
              : status === "error"
                ? "Run failed before session activity."
                : "Starting run…"}
          </div>
        ) : (
          <div className="p-density-4">
            <SessionViewer session={messages} defaultExpanded={false} />
          </div>
        )}
      </div>
      {run?.chat && chatState ? (
        <SessionChatComposer
          status={chatState.status}
          capabilities={chat.capabilities}
          queued={chatState.queued}
          error={chat.actionError}
          onSubmit={chat.send}
          onInterrupt={chat.interrupt}
        />
      ) : null}
      {summary && status !== "error" && (
        <RunSummaryFooter summary={summary} />
      )}
    </div>
  );
}

function RunFailureDetails({
  error,
  runID,
  sessionID,
  model,
  backend,
}: {
  error: string;
  runID: string;
  sessionID?: string;
  model?: string;
  backend?: string;
}) {
  const details = [
    { label: "Run UID", value: runID },
    { label: "Session UID", value: sessionID || "Not assigned" },
    { label: "Model", value: model || "Unknown" },
    { label: "Backend", value: backend || "Unknown" },
  ];
  return (
    <div
      role="alert"
      className="rounded-md border border-destructive/40 bg-destructive/10 p-density-3"
    >
      <div className="text-sm font-medium text-destructive">{error}</div>
      <dl className="mt-density-3 grid gap-density-2 text-xs sm:grid-cols-2">
        {details.map((detail) => (
          <div key={detail.label} className="min-w-0">
            <dt className="text-muted-foreground">{detail.label}</dt>
            <dd className="break-all font-mono text-foreground">
              {detail.value}
            </dd>
          </div>
        ))}
      </dl>
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
  const parts: Array<{ id: string; label: string }> = [];
  if (summary.model) parts.push({ id: "model", label: summary.model });
  if (summary.backend) parts.push({ id: "backend", label: summary.backend });
  if (summary.duration) parts.push({ id: "duration", label: summary.duration });
  if (summary.inputTokens != null)
    parts.push({ id: "input-tokens", label: `${summary.inputTokens} in` });
  if (summary.outputTokens != null)
    parts.push({ id: "output-tokens", label: `${summary.outputTokens} out` });
  if (summary.costUSD != null)
    parts.push({ id: "cost", label: `$${summary.costUSD.toFixed(4)}` });
  if (summary.sessionId)
    parts.push({ id: "session", label: `session ${summary.sessionId}` });
  return (
    <div className="flex flex-wrap gap-density-2 border-t border-border pt-density-2 text-xs text-muted-foreground">
      {parts.map((part) => (
        <span key={part.id} className="truncate">
          {part.label}
        </span>
      ))}
    </div>
  );
}
