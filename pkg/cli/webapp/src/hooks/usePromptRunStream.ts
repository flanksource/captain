import { useCallback, useEffect, useRef, useState } from "react";
import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
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
  messages: SessionUIMessage[];
  summary?: PromptRunSummary;
  status: PromptRunStreamStatus;
  error?: string;
}

const PROMPT_RUN_BASE = "/api/captain/prompt/runs";

/**
 * usePromptRunStream subscribes to a run's unified session.Message SSE stream and
 * accumulates a growing SessionUIMessage[] suitable for <SessionViewer>. Frames
 * are deduped by message id so the buffered replay a run sends on (re)connect is
 * idempotent, and the terminal `done`/`error` events stop the connection.
 */
export function usePromptRunStream(runID: string | undefined, basePath = PROMPT_RUN_BASE): PromptRunStreamState {
  const [messages, setMessages] = useState<SessionUIMessage[]>([]);
  const [summary, setSummary] = useState<PromptRunSummary | undefined>();
  const [status, setStatus] = useState<PromptRunStreamStatus>("idle");
  const [error, setError] = useState<string | undefined>();
  const [done, setDone] = useState(false);

  const byId = useRef(new Map<string, SessionUIMessage>());
  const order = useRef<string[]>([]);
  const autoSeq = useRef(0);

  useEffect(() => {
    byId.current = new Map();
    order.current = [];
    autoSeq.current = 0;
    setMessages([]);
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
    const message = parse<SessionUIMessage>(data);
    if (!message) return;
    const key = message.id ?? `auto-${autoSeq.current++}`;
    if (!byId.current.has(key)) order.current.push(key);
    byId.current.set(key, message);
    setMessages(order.current.map((k) => byId.current.get(k)!));
    setStatus((s) => (s === "done" || s === "error" ? s : "streaming"));
  }, []);

  const url = runID ? `${basePath}/${encodeURIComponent(runID)}/stream` : undefined;
  useEventSource(url, { enabled: Boolean(url) && !done, events: ["entry", "done", "error"], onEvent });

  return { messages, summary, status, error };
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}
