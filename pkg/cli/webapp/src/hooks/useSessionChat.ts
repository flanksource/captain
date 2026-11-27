import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  SessionUIMessage,
  SpecPermissionMode,
} from "@flanksource/clicky-ui/ai";
import {
  usePromptRunStream,
  type ChatCapabilities,
  type ChatMessageResponse,
  type ChatStateFrame,
} from "./usePromptRunStream";

const EMPTY_CAPABILITIES: ChatCapabilities = {
  interrupt: false,
  steer: false,
  followUp: false,
  resume: false,
  setPermissionMode: false,
};

export type UseSessionChatOptions = {
  initialRunID?: string;
  sessionID?: string;
  initialCapabilities?: ChatCapabilities;
  initialState?: ChatStateFrame;
  clearOnTerminal?: boolean;
  onTerminal?: () => Promise<unknown>;
  /** The session's persisted transcript, which a message delivered to an
   *  external live session reaches only once its provider writes it. */
  transcript?: SessionUIMessage[];
  initialPermissionMode?: SpecPermissionMode;
  permissionModes?: SpecPermissionMode[];
};

/** How often the transcript is refreshed while a channel-delivered message
 *  waits for its provider to write it and a reply. */
export const DELIVERY_REFRESH_MS = 2_000;

/** A message an external live session accepted, and how many transcript
 *  messages already carried its text when it was sent. */
type DeliveredMessage = { id: string; text: string; seen: number };

/** Reports whether the transcript holds a new copy of the delivered text,
 *  followed by a message the agent wrote after it. Providers file the message
 *  under their own id, and may wrap its text. */
export function deliveredMessageSettled(
  transcript: SessionUIMessage[] | undefined,
  delivered: DeliveredMessage,
): boolean {
  const matches = deliveredTextIndexes(transcript, delivered.text);
  const last = matches[matches.length - 1];
  return (
    last !== undefined &&
    matches.length > delivered.seen &&
    transcript!.slice(last + 1).some((message) => message.role !== "user")
  );
}

function deliveredTextIndexes(
  transcript: SessionUIMessage[] | undefined,
  text: string,
): number[] {
  const needle = text.trim();
  return (transcript ?? []).flatMap((message, index) =>
    message.parts.some((part) => part.type === "text" && part.text?.includes(needle))
      ? [index]
      : [],
  );
}

/** Picks the permission mode a not-yet-live session should start selected on:
 *  the caller's initial mode when the runtime still honours it, else
 *  "default" when offered, else the runtime's first supported mode. */
export function selectInitialPermissionMode(
  initial: SpecPermissionMode | undefined,
  modes: SpecPermissionMode[] | undefined,
): SpecPermissionMode | undefined {
  if (!modes || modes.length === 0) return undefined;
  if (initial && modes.includes(initial)) return initial;
  if (modes.includes("default")) return "default";
  return modes[0];
}

export function useSessionChat(options: UseSessionChatOptions) {
  const [activeRunID, setActiveRunID] = useState(options.initialRunID);
  const [optimistic, setOptimistic] = useState<SessionUIMessage[]>([]);
  const [actionError, setActionError] = useState<string>();
  const [localPermissionMode, setLocalPermissionMode] = useState<
    SpecPermissionMode | undefined
  >(() =>
    selectInitialPermissionMode(
      options.initialPermissionMode,
      options.permissionModes,
    ),
  );
  // A posture is sent only once it is known — recorded on the session, picked,
  // or carried over from a live run — because a session that is still live
  // refuses any posture it is not already running under.
  const [permissionModeKnown, setPermissionModeKnown] = useState(
    () =>
      Boolean(options.initialPermissionMode) &&
      selectInitialPermissionMode(options.initialPermissionMode, options.permissionModes) ===
        options.initialPermissionMode,
  );
  const [delivered, setDelivered] = useState<DeliveredMessage[]>([]);
  const terminalHandled = useRef<string | undefined>(undefined);
  const stream = usePromptRunStream(activeRunID);
  const onTerminal = useRef(options.onTerminal);
  onTerminal.current = options.onTerminal;
  const transcript = useRef(options.transcript);
  transcript.current = options.transcript;

  const refreshDelivered = useCallback(() => {
    void onTerminal.current?.().catch((error: unknown) =>
      setActionError(`Message accepted. Could not refresh the session. ${errorMessage(error)}`),
    );
  }, []);

  // A delivered message stays visible until the transcript holds it and a
  // reply, refreshed meanwhile: nothing else refetches an external session.
  useEffect(() => {
    if (delivered.length === 0) return;
    const settled = new Set(
      delivered
        .filter((item) => deliveredMessageSettled(options.transcript, item))
        .map((item) => item.id),
    );
    if (settled.size === 0) return;
    setOptimistic((current) => current.filter((item) => !item.id || !settled.has(item.id)));
    setDelivered((current) => current.filter((item) => !settled.has(item.id)));
  }, [delivered, options.transcript]);

  const awaitingDelivery = delivered.length > 0;
  useEffect(() => {
    if (!awaitingDelivery) return;
    const timer = setInterval(refreshDelivered, DELIVERY_REFRESH_MS);
    return () => clearInterval(timer);
  }, [awaitingDelivery, refreshDelivered]);

  useEffect(() => {
    if (options.initialRunID) setActiveRunID(options.initialRunID);
  }, [options.initialRunID]);

  useEffect(() => {
    if (!activeRunID || (stream.status !== "done" && stream.status !== "error"))
      return;
    if (terminalHandled.current === activeRunID) return;
    terminalHandled.current = activeRunID;
    if (!options.onTerminal) return;
    void options
      .onTerminal()
      .then(() => {
        if (options.clearOnTerminal) {
          setOptimistic([]);
          setActiveRunID(undefined);
        }
      })
      .catch((error: unknown) => setActionError(errorMessage(error)));
  }, [activeRunID, options.clearOnTerminal, options.onTerminal, stream.status]);

  const messages = useMemo(
    () => mergeSessionMessages(stream.messages, optimistic),
    [optimistic, stream.messages],
  );
  const resolvedChatState = resolveChatState(
    stream.chatState,
    options.initialState,
  );
  const capabilities =
    resolvedChatState?.capabilities ??
    stream.run?.capabilities ??
    options.initialCapabilities ??
    EMPTY_CAPABILITIES;
  const liveChatState = resolvedChatState ?? {
    runId: activeRunID ?? "",
    status: activeRunID ? "starting" : "idle",
    turn: 0,
    capabilities,
  };
  const chatState =
    stream.status === "done" || stream.status === "error"
      ? { ...liveChatState, status: "idle" as const, queued: [] }
      : liveChatState;

  const isLiveRun =
    Boolean(activeRunID) &&
    stream.status !== "done" &&
    stream.status !== "error";
  const livePermissionMode = isLiveRun
    ? resolvedChatState?.permissionMode
    : undefined;
  const permissionMode = isLiveRun ? livePermissionMode : localPermissionMode;
  const permissionModes = isLiveRun
    ? (resolvedChatState?.permissionModes ?? [])
    : (options.permissionModes ?? []);
  const canSetPermissionMode = isLiveRun
    ? capabilities.setPermissionMode
    : permissionModes.length > 0;

  // The next resumed run should continue in whatever posture the live run ended on.
  useEffect(() => {
    if (!livePermissionMode) return;
    setLocalPermissionMode(livePermissionMode);
    setPermissionModeKnown(true);
  }, [livePermissionMode]);
  const sessionPermissionMode = permissionModeKnown ? permissionMode : undefined;

  const send = useCallback(
    async (text: string) => {
      const messageID = crypto.randomUUID();
      const message = userMessage(messageID, text);
      setOptimistic((current) => mergeSessionMessages(current, [message]));
      setActionError(undefined);
      try {
        const sessionID = options.sessionID ?? stream.summary?.sessionId;
        const active =
          activeRunID && stream.status !== "done" && stream.status !== "error";
        let response: ChatMessageResponse;
        if (active) {
          try {
            response = await postChatMessage(
              `/api/captain/prompt/runs/${encodeURIComponent(activeRunID)}/message`,
              text,
              messageID,
            );
          } catch (error) {
            if (
              !(error instanceof ChatRequestError) ||
              error.status !== 409 ||
              !sessionID
            )
              throw error;
            response = await postChatMessage(
              `/api/captain/sessions/${encodeURIComponent(sessionID)}/message`,
              text,
              messageID,
              sessionPermissionMode,
            );
          }
        } else {
          if (!sessionID) throw new Error("session is not resumable yet");
          response = await postChatMessage(
            `/api/captain/sessions/${encodeURIComponent(sessionID)}/message`,
            text,
            messageID,
            sessionPermissionMode,
          );
        }
        if (response.channel) {
          setActiveRunID(undefined);
          const seen = deliveredTextIndexes(transcript.current, text).length;
          setDelivered((current) => [...current, { id: messageID, text, seen }]);
          refreshDelivered();
        } else if (response.runId !== activeRunID) {
          terminalHandled.current = undefined;
          setActiveRunID(response.runId);
        }
      } catch (error) {
        setOptimistic((current) =>
          current.filter((item) => item.id !== messageID),
        );
        setActionError(errorMessage(error));
      }
    },
    [
      activeRunID,
      options.sessionID,
      refreshDelivered,
      sessionPermissionMode,
      stream.status,
      stream.summary?.sessionId,
    ],
  );

  const setPermissionMode = useCallback(
    async (mode: SpecPermissionMode) => {
      if (!isLiveRun) {
        setLocalPermissionMode(mode);
        setPermissionModeKnown(true);
        return;
      }
      if (!activeRunID) return;
      setActionError(undefined);
      try {
        await postJSON(
          `/api/captain/prompt/runs/${encodeURIComponent(activeRunID)}/permission-mode`,
          { mode },
        );
      } catch (error) {
        setActionError(errorMessage(error));
      }
    },
    [activeRunID, isLiveRun],
  );

  const interrupt = useCallback(async () => {
    if (!activeRunID) return;
    setActionError(undefined);
    try {
      await postJSON(
        `/api/captain/prompt/runs/${encodeURIComponent(activeRunID)}/interrupt`,
        {},
      );
    } catch (error) {
      setActionError(errorMessage(error));
    }
  }, [activeRunID]);

  const stop = useCallback(async () => {
    if (!activeRunID) return;
    setActionError(undefined);
    try {
      await postJSON(
        `/api/captain/prompt/runs/${encodeURIComponent(activeRunID)}/stop`,
        {},
      );
    } catch (error) {
      setActionError(errorMessage(error));
    }
  }, [activeRunID]);

  return {
    ...stream,
    activeRunID,
    messages,
    capabilities,
    chatState,
    actionError,
    send,
    interrupt,
    stop,
    permissionMode,
    permissionModes,
    setPermissionMode,
    canSetPermissionMode,
  };
}

export function resolveChatState(
  streamState: ChatStateFrame | undefined,
  polledState: ChatStateFrame | undefined,
) {
  if (!streamState) return polledState;
  if (!polledState) return streamState;
  if (streamState.runId !== polledState.runId) return streamState;
  if (streamState.turn !== polledState.turn) {
    return streamState.turn > polledState.turn ? streamState : polledState;
  }
  if (
    polledState.status === "idle" &&
    (streamState.status === "starting" || streamState.status === "running")
  ) {
    return polledState;
  }
  return streamState;
}

export function mergeSessionMessages(
  base: SessionUIMessage[],
  additional: SessionUIMessage[],
): SessionUIMessage[] {
  const byID = new Map<string, SessionUIMessage>();
  const order: string[] = [];
  let sequence = 0;
  for (const message of [...base, ...additional]) {
    const id = message.id ?? `message-${sequence++}`;
    if (!byID.has(id)) order.push(id);
    byID.set(id, message);
  }
  return order.map((id) => byID.get(id)!);
}

function userMessage(id: string, text: string): SessionUIMessage {
  return { id, role: "user", parts: [{ type: "text", text }] };
}

async function postChatMessage(
  path: string,
  text: string,
  messageId: string,
  permissionMode?: SpecPermissionMode,
) {
  return postJSON<ChatMessageResponse>(path, {
    text,
    messageId,
    ...(permissionMode ? { permissionMode } : {}),
  });
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new ChatRequestError(
      response.status,
      (await response.text()).trim() || "request failed",
    );
  }
  return (await response.json()) as T;
}

class ChatRequestError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
