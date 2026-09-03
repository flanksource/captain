import {
  useCallback,
  useEffect,
  useReducer,
  useRef,
  type MutableRefObject,
} from "react";
import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
import { useEventSource } from "./useEventSource";
import { parseVerifyFrame, type VerifyFrame } from "../types/verifyReport";

/** Immediate response of the async prompt "run" action. */
export interface PromptRunHandle {
  runId: string;
  status: string;
  model?: string;
  provider?: string;
  mode?: string;
  chat?: boolean;
  capabilities?: ChatCapabilities;
}

export interface PromptBatchRunHandle {
  runId: string;
  sessionId: string;
  selector?: string;
  status: string;
  model?: string;
  provider?: string;
  mode?: string;
  effort?: string;
  chat?: boolean;
  capabilities?: ChatCapabilities;
}

export interface PromptBatchHandle {
  batchId: string;
  status: string;
  chat?: boolean;
  total: number;
  runs: PromptBatchRunHandle[];
}

export type PromptExecutionHandle = PromptRunHandle | PromptBatchHandle;

export function isPromptBatchHandle(
  handle: PromptExecutionHandle,
): handle is PromptBatchHandle {
  return "batchId" in handle && Array.isArray(handle.runs);
}

export interface ChatCapabilities {
  interrupt: boolean;
  steer: boolean;
  followUp: boolean;
  resume: boolean;
}

export interface ChatQueuedMessage {
  messageId: string;
  text: string;
}

export interface ChatMessageResponse {
  runId: string;
  messageId: string;
  status: "steered" | "queued" | "started";
  capabilities: ChatCapabilities;
}

export interface ChatStateFrame {
  runId: string;
  sessionId?: string;
  status: "starting" | "running" | "interrupting" | "idle" | "stopping";
  turn: number;
  capabilities: ChatCapabilities;
  queued?: ChatQueuedMessage[];
  discardedMessageIds?: string[];
  summary?: PromptRunSummary;
}

export interface PromptRunFrame extends PromptRunHandle {
  sessionId?: string;
}

/** Terminal payload delivered on the SSE `done` or `error` event. */
export interface PromptRunSummary {
  runId?: string;
  sessionId?: string;
  model?: string;
  provider?: string;
  mode?: string;
  inputTokens?: number;
  outputTokens?: number;
  costUSD?: number;
  duration?: string;
  success?: boolean;
  error?: string;
}

export type PromptRunStreamStatus =
  | "idle"
  | "connecting"
  | "streaming"
  | "done"
  | "error";

export interface PromptRunStreamState {
  messages: SessionUIMessage[];
  summary?: PromptRunSummary;
  status: PromptRunStreamStatus;
  error?: string;
  run?: PromptRunFrame;
  chatState?: ChatStateFrame;
  verify: VerifyFrame | null;
}

const PROMPT_RUN_BASE = "/api/captain/prompt/runs";

type PromptRunStreamReducerState = PromptRunStreamState & {
  done: boolean;
};

type PromptRunStreamAction =
  | { type: "reset"; runID?: string }
  | { type: "message"; messages: SessionUIMessage[] }
  | { type: "run"; run: PromptRunFrame }
  | {
      type: "chat-state";
      chatState: ChatStateFrame;
      messages: SessionUIMessage[];
    }
  | { type: "done"; summary?: PromptRunSummary }
  | { type: "error"; summary: PromptRunSummary }
  | { type: "verify"; verify: VerifyFrame }
  | { type: "verify-error"; message: string }
  | { type: "stream-handler-error"; message: string };

type MessageIndex = {
  byId: Map<string, SessionUIMessage>;
  order: string[];
  autoSeq: number;
  discarded: Set<string>;
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
        run: undefined,
        chatState: undefined,
        verify: null,
      };
    case "run":
      return { ...state, run: action.run };
    case "verify":
      return { ...state, verify: action.verify };
    case "verify-error":
      return { ...state, verify: null, error: action.message };
    case "stream-handler-error":
      return { ...state, error: action.message };
    case "chat-state":
      return {
        ...state,
        chatState: action.chatState,
        messages: action.messages,
      };
    case "message":
      return {
        ...state,
        messages: action.messages,
        status:
          state.status === "done" || state.status === "error"
            ? state.status
            : "streaming",
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
        summary: action.summary,
        status: "error",
        error: action.summary.error || "run failed",
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
    run: undefined,
    chatState: undefined,
    verify: null,
  };
}

/**
 * usePromptRunStream subscribes to a run's unified session.Message SSE stream and
 * accumulates a growing SessionUIMessage[] suitable for <SessionViewer>. Frames
 * are deduped by message id so the buffered replay a run sends on (re)connect is
 * idempotent, and the terminal `done`/`error` events stop the connection.
 */
export function usePromptRunStream(
  runID: string | undefined,
  basePath = PROMPT_RUN_BASE,
  onChange?: (runID: string, state: PromptRunStreamState) => void,
): PromptRunStreamState {
  const [state, dispatch] = useReducer(
    streamReducer,
    undefined,
    initialStreamState,
  );
  const stateRef = useRef(state);
  const messageIndex = useRef<MessageIndex | null>(null);

  useEffect(() => {
    messageIndex.current = null;
    const next = streamReducer(stateRef.current, { type: "reset", runID });
    stateRef.current = next;
    dispatch({ type: "reset", runID });
  }, [runID]);

  const update = useCallback(
    (action: PromptRunStreamAction) => {
      const next = streamReducer(stateRef.current, action);
      stateRef.current = next;
      dispatch(action);
      if (runID) onChange?.(runID, next);
    },
    [onChange, runID],
  );

  const onEvent = useCallback((event: string, data: string) => {
    if (event === "run") {
      const run = parse<PromptRunFrame>(data);
      if (run) update({ type: "run", run });
      return;
    }
    if (event === "state") {
      const chatState = parse<ChatStateFrame>(data);
      if (!chatState) return;
      const index = getMessageIndex(messageIndex);
      index.discarded = new Set(chatState.discardedMessageIds ?? []);
      update({
        type: "chat-state",
        chatState,
        messages: indexedMessages(index),
      });
      return;
    }
    if (event === "done") {
      const sum = parse<PromptRunSummary>(data);
      update({ type: "done", summary: sum });
      return;
    }
    if (event === "error") {
      const summary = parse<PromptRunSummary>(data);
      update({
        type: "error",
        summary: summary ?? { error: "run failed", success: false },
      });
      return;
    }
    if (event === "verify") {
      try {
        const raw: unknown = JSON.parse(data);
        update({ type: "verify", verify: parseVerifyFrame(raw) });
      } catch (error) {
        update({
          type: "verify-error",
          message: `invalid verify frame: ${describeError(error)}`,
        });
      }
      return;
    }
    if (event !== "entry") return;
    const message = parse<SessionUIMessage>(data);
    if (!message) return;
    const index = getMessageIndex(messageIndex);
    const key = message.id ?? `auto-${index.autoSeq++}`;
    if (!index.byId.has(key)) index.order.push(key);
    index.byId.set(key, message);
    update({ type: "message", messages: indexedMessages(index) });
  }, [update]);

  const url = runID
    ? `${basePath}/${encodeURIComponent(runID)}/stream`
    : undefined;
  const onError = useCallback(
    (event: string, message: string) => {
      update({
        type: "stream-handler-error",
        message: `${event} handler error: ${message}`,
      });
    },
    [update],
  );

  useEventSource(url, {
    enabled: Boolean(url) && !state.done,
    events: ["run", "entry", "state", "done", "error", "verify"],
    onEvent,
    onError,
  });

  return {
    messages: state.messages,
    summary: state.summary,
    status: state.status,
    error: state.error,
    run: state.run,
    chatState: state.chatState,
    verify: state.verify,
  };
}

function getMessageIndex(
  ref: MutableRefObject<MessageIndex | null>,
): MessageIndex {
  if (!ref.current) {
    ref.current = {
      byId: new Map<string, SessionUIMessage>(),
      order: [],
      autoSeq: 0,
      discarded: new Set<string>(),
    };
  }
  return ref.current;
}

function indexedMessages(index: MessageIndex): SessionUIMessage[] {
  const messages: SessionUIMessage[] = [];
  for (const key of index.order) {
    if (!index.discarded.has(key)) messages.push(index.byId.get(key)!);
  }
  return messages;
}

function parse<T>(data: string): T | undefined {
  try {
    return JSON.parse(data) as T;
  } catch {
    return undefined;
  }
}

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
