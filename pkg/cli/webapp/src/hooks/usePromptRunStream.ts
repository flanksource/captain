import { useCallback, useEffect, useReducer, useRef, type MutableRefObject } from "react";
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

type PromptRunStreamReducerState = PromptRunStreamState & {
  done: boolean;
};

type PromptRunStreamAction =
  | { type: "reset"; runID?: string }
  | { type: "message"; messages: SessionUIMessage[] }
  | { type: "done"; summary?: PromptRunSummary }
  | { type: "error"; error: string };

type MessageIndex = {
  byId: Map<string, SessionUIMessage>;
  order: string[];
  autoSeq: number;
};

function streamReducer(
  state: PromptRunStreamReducerState,
  action: PromptRunStreamAction,
): PromptRunStreamReducerState {
  switch (action.type) {
    case "reset":
      return {
        messages: [],
        summary: undefined,
        status: action.runID ? "connecting" : "idle",
        error: undefined,
        done: !action.runID,
      };
    case "message":
      return {
        ...state,
        messages: action.messages,
        status: state.status === "done" || state.status === "error" ? state.status : "streaming",
      };
    case "done":
      return {
        ...state,
        summary: action.summary ?? state.summary,
        status: action.summary?.error ? "error" : "done",
        error: action.summary?.error ?? state.error,
        done: true,
      };
    case "error":
      return {
        ...state,
        status: "error",
        error: action.error,
        done: true,
      };
  }
}

function initialStreamState(): PromptRunStreamReducerState {
  return {
    messages: [],
    summary: undefined,
    status: "idle",
    error: undefined,
    done: true,
  };
}

/**
 * usePromptRunStream subscribes to a run's unified session.Message SSE stream and
 * accumulates a growing SessionUIMessage[] suitable for <SessionViewer>. Frames
 * are deduped by message id so the buffered replay a run sends on (re)connect is
 * idempotent, and the terminal `done`/`error` events stop the connection.
 */
export function usePromptRunStream(runID: string | undefined, basePath = PROMPT_RUN_BASE): PromptRunStreamState {
  const [state, dispatch] = useReducer(streamReducer, undefined, initialStreamState);
  const messageIndex = useRef<MessageIndex | null>(null);

  useEffect(() => {
    messageIndex.current = null;
    dispatch({ type: "reset", runID });
  }, [runID]);

  const onEvent = useCallback((event: string, data: string) => {
    if (event === "done") {
      const sum = parse<PromptRunSummary>(data);
      dispatch({ type: "done", summary: sum });
      return;
    }
    if (event === "error") {
      dispatch({ type: "error", error: parse<{ error?: string }>(data)?.error ?? "run failed" });
      return;
    }
    const message = parse<SessionUIMessage>(data);
    if (!message) return;
    const index = getMessageIndex(messageIndex);
    const key = message.id ?? `auto-${index.autoSeq++}`;
    if (!index.byId.has(key)) index.order.push(key);
    index.byId.set(key, message);
    dispatch({ type: "message", messages: index.order.map((k) => index.byId.get(k)!) });
  }, []);

  const url = runID ? `${basePath}/${encodeURIComponent(runID)}/stream` : undefined;
  useEventSource(url, { enabled: Boolean(url) && !state.done, events: ["entry", "done", "error"], onEvent });

  return { messages: state.messages, summary: state.summary, status: state.status, error: state.error };
}

function getMessageIndex(ref: MutableRefObject<MessageIndex | null>): MessageIndex {
  if (!ref.current) {
    ref.current = {
      byId: new Map<string, SessionUIMessage>(),
      order: [],
      autoSeq: 0,
    };
  }
  return ref.current;
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}
