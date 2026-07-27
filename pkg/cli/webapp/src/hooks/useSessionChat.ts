import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
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
};

export type UseSessionChatOptions = {
  initialRunID?: string;
  sessionID?: string;
  initialCapabilities?: ChatCapabilities;
  initialState?: ChatStateFrame;
  clearOnTerminal?: boolean;
  onTerminal?: () => Promise<unknown>;
};

export function useSessionChat(options: UseSessionChatOptions) {
  const [activeRunID, setActiveRunID] = useState(options.initialRunID);
  const [optimistic, setOptimistic] = useState<SessionUIMessage[]>([]);
  const [actionError, setActionError] = useState<string>();
  const terminalHandled = useRef<string | undefined>(undefined);
  const stream = usePromptRunStream(activeRunID);

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
            );
          }
        } else {
          if (!sessionID) throw new Error("session is not resumable yet");
          response = await postChatMessage(
            `/api/captain/sessions/${encodeURIComponent(sessionID)}/message`,
            text,
            messageID,
          );
        }
        if (response.runId !== activeRunID) {
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
    [activeRunID, options.sessionID, stream.status, stream.summary?.sessionId],
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

async function postChatMessage(path: string, text: string, messageId: string) {
  return postJSON<ChatMessageResponse>(path, { text, messageId });
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
