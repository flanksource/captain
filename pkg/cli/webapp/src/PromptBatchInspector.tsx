import { memo, useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Select } from "@flanksource/clicky-ui/components";
import {
  SessionChatComposer,
  SessionInspector,
  type UnifiedSessionInput,
} from "@flanksource/clicky-ui/ai";
import {
  usePromptRunStream,
  type PromptBatchHandle,
  type PromptBatchRunHandle,
  type PromptRunStreamState,
} from "./hooks/usePromptRunStream";
import { useSessionChat, mergeSessionMessages } from "./hooks/useSessionChat";
import { fetchSession } from "./sessionData";
import { batchChatTargets, batchSessionCollection } from "./sessionCollection";

export function PromptBatchInspector({
  handle,
  onEdit,
}: {
  handle: PromptBatchHandle;
  onEdit: () => void;
}) {
  const query = useQuery({
    queryKey: ["prompt-batch", handle.batchId],
    queryFn: () => fetchSession(handle.batchId),
    refetchInterval: 2_000,
  });
  const [streams, setStreams] = useState<Record<string, PromptRunStreamState>>(
    {},
  );
  const updateStream = useCallback(
    (runID: string, state: PromptRunStreamState) =>
      setStreams((current) =>
        current[runID] === state ? current : { ...current, [runID]: state },
      ),
    [],
  );
  const liveSessions = useMemo(() => {
    const sessions = new Map<string, UnifiedSessionInput>();
    for (const run of handle.runs) {
      const stream = streams[run.runId];
      if (!stream) continue;
      const saved = query.data?.sessions.find(
        (item) => item.captainId === run.sessionId,
      )?.detail;
      sessions.set(run.sessionId, {
        ...saved,
        id: run.sessionId,
        source: run.backend || saved?.source || "captain",
        title: run.selector || run.model || saved?.title,
        backend: run.backend || saved?.backend,
        model: run.model || saved?.model,
        reasoningEffort: run.effort || saved?.reasoningEffort,
        messages: mergeSessionMessages(saved?.messages ?? [], stream.messages),
      });
    }
    return sessions;
  }, [handle.runs, query.data?.sessions, streams]);
  const collection = useMemo(
    () => batchSessionCollection(handle, query.data, liveSessions),
    [handle, liveSessions, query.data],
  );
  const targets = useMemo(() => batchChatTargets(handle), [handle]);
  const [targetID, setTargetID] = useState(targets[0]?.sessionId);
  const target =
    targets.find((candidate) => candidate.sessionId === targetID) ?? targets[0];
  const chat = useSessionChat({
    initialRunID: target?.runId,
    sessionID: target?.sessionId,
    initialCapabilities: target?.capabilities,
    onTerminal: async () => {
      await query.refetch();
    },
  });

  useEffect(() => {
    if (target && target.sessionId !== targetID) setTargetID(target.sessionId);
  }, [target, targetID]);

  return (
    <div className="flex h-full min-h-0 flex-col gap-density-3">
      {handle.runs.map((run) => (
        <BatchRunSubscription
          key={run.runId}
          run={run}
          onChange={updateStream}
        />
      ))}
      <div className="flex shrink-0 items-center justify-between gap-density-2">
        <div>
          <div className="text-sm font-semibold">Multi-model run</div>
          <div className="font-mono text-xs text-muted-foreground">
            {handle.batchId}
          </div>
        </div>
        <Button size="sm" variant="outline" onClick={onEdit}>
          Edit run
        </Button>
      </div>
      {query.error && (
        <div className="shrink-0 text-xs text-destructive">
          {errorMessage(query.error)}
        </div>
      )}
      <SessionInspector
        className="min-h-0 flex-1"
        session={collection}
        transcriptProps={{ defaultExpanded: false }}
        renderSessionActions={(item) => {
          const run = handle.runs.find(
            (candidate) => candidate.sessionId === item.id,
          );
          return run ? <StopSessionAction runID={run.runId} /> : undefined;
        }}
        {...(target
          ? {
              composer: (
                <SessionChatComposer
                  status={chat.chatState.status}
                  capabilities={chat.capabilities}
                  queued={chat.chatState.queued}
                  error={chat.actionError}
                  onSubmit={chat.send}
                  onInterrupt={chat.interrupt}
                  toolbar={
                    <label className="flex items-center gap-density-2 text-xs text-muted-foreground">
                      <span className="shrink-0">Chat target</span>
                      <Select
                        aria-label="Chat target"
                        value={target.sessionId}
                        options={targets.map((candidate) => ({
                          value: candidate.sessionId,
                          label:
                            candidate.selector ||
                            candidate.model ||
                            candidate.sessionId,
                        }))}
                        onChange={(event) => setTargetID(event.target.value)}
                      />
                    </label>
                  }
                />
              ),
            }
          : {})}
      />
    </div>
  );
}

const BatchRunSubscription = memo(function BatchRunSubscription({
  run,
  onChange,
}: {
  run: PromptBatchRunHandle;
  onChange: (runID: string, state: PromptRunStreamState) => void;
}) {
  const state = usePromptRunStream(run.runId);
  useEffect(() => onChange(run.runId, state), [onChange, run.runId, state]);
  return null;
});

function StopSessionAction({ runID }: { runID: string }) {
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string>();
  return (
    <div className="flex items-center gap-density-1">
      {error && <span className="text-xs text-destructive">{error}</span>}
      <Button
        size="sm"
        variant="ghost"
        disabled={stopping}
        onClick={async () => {
          setStopping(true);
          setError(undefined);
          try {
            const response = await fetch(
              `/api/captain/prompt/runs/${encodeURIComponent(runID)}/stop`,
              {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: "{}",
              },
            );
            if (!response.ok)
              throw new Error((await response.text()) || "stop failed");
          } catch (cause) {
            setError(errorMessage(cause));
          } finally {
            setStopping(false);
          }
        }}
      >
        {stopping ? "Stopping…" : "Stop"}
      </Button>
    </div>
  );
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
