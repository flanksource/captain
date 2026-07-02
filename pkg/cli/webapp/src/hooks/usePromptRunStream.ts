import { useCallback, useEffect, useRef, useState } from "react";
import type { SessionEntry } from "@flanksource/clicky-ui/ai";
import { useEventSource } from "./useEventSource";

/** Immediate response of the async prompt "run" action. */
export interface PromptRunHandle {
  runId: string;
  status: string;
  model?: string;
  backend?: string;
}

/** Terminal payload delivered on the SSE `done` event. */
export interface PromptRunSummary {
  runId?: string;
  sessionId?: string;
  model?: string;
  backend?: string;
  inputTokens?: number;
  outputTokens?: number;
  costUSD?: number;
  duration?: string;
  success?: boolean;
  error?: string;
}

export type PromptRunStreamStatus = "idle" | "connecting" | "streaming" | "done" | "error";

export interface PromptRunStreamState {
  entries: SessionEntry[];
  summary?: PromptRunSummary;
  status: PromptRunStreamStatus;
  error?: string;
}

const PROMPT_RUN_BASE = "/api/captain/prompt/runs";

/**
 * usePromptRunStream subscribes to a run's SessionEntry SSE stream and
 * accumulates a growing SessionEntry[] suitable for <SessionViewer>. Frames are
 * deduped by uuid so the buffered replay a run sends on (re)connect is
 * idempotent, and the terminal `done`/`error` events stop the connection.
 */
export function usePromptRunStream(runID: string | undefined, basePath = PROMPT_RUN_BASE): PromptRunStreamState {
  const [entries, setEntries] = useState<SessionEntry[]>([]);
  const [summary, setSummary] = useState<PromptRunSummary | undefined>();
  const [status, setStatus] = useState<PromptRunStreamStatus>("idle");
  const [error, setError] = useState<string | undefined>();
  const [done, setDone] = useState(false);

  const byUUID = useRef(new Map<string, SessionEntry>());
  const order = useRef<string[]>([]);
  const autoSeq = useRef(0);

  useEffect(() => {
    byUUID.current = new Map();
    order.current = [];
    autoSeq.current = 0;
    setEntries([]);
    setSummary(undefined);
    setError(undefined);
    setDone(false);
    setStatus(runID ? "connecting" : "idle");
  }, [runID]);

  const onEvent = useCallback((event: string, data: string) => {
    if (event === "done") {
      const sum = parse<PromptRunSummary>(data);
      if (sum) setSummary(sum);
      if (sum?.error) {
        setError(sum.error);
        setStatus("error");
      } else {
        setStatus("done");
      }
      setDone(true);
      return;
    }
    if (event === "error") {
      setError(parse<{ error?: string }>(data)?.error ?? "run failed");
      setStatus("error");
      setDone(true);
      return;
    }
    const entry = parse<SessionEntry>(data);
    if (!entry) return;
    const key = entry.uuid ?? `auto-${autoSeq.current++}`;
    if (!byUUID.current.has(key)) order.current.push(key);
    byUUID.current.set(key, entry);
    setEntries(order.current.map((k) => byUUID.current.get(k)!));
    setStatus((s) => (s === "done" || s === "error" ? s : "streaming"));
  }, []);

  const url = runID ? `${basePath}/${encodeURIComponent(runID)}/stream` : undefined;
  useEventSource(url, { enabled: Boolean(url) && !done, events: ["entry", "done", "error"], onEvent });

  return { entries, summary, status, error };
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}
