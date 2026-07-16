import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
import { describe, expect, it } from "vitest";
import { mergeSessionMessages, resolveChatState } from "./useSessionChat";
import type { ChatStateFrame } from "./usePromptRunStream";

describe("mergeSessionMessages", () => {
  it("reconciles an optimistic user message by exact id", () => {
    const optimistic: SessionUIMessage = {
      id: "message-1",
      role: "user",
      parts: [{ type: "text", text: "optimistic" }],
    };
    const accepted: SessionUIMessage = {
      id: "message-1",
      role: "user",
      parts: [{ type: "text", text: "accepted" }],
    };

    const merged = mergeSessionMessages([optimistic], [accepted]);

    expect(merged).toEqual([accepted]);
  });
});

describe("resolveChatState", () => {
  const capabilities = {
    interrupt: true,
    steer: false,
    followUp: true,
    resume: true,
  };

  it("uses a polled idle state when the SSE state is behind on the same turn", () => {
    const stream: ChatStateFrame = {
      runId: "run-1",
      status: "running",
      turn: 1,
      capabilities,
    };
    const polled: ChatStateFrame = {
      ...stream,
      status: "idle",
    };

    expect(resolveChatState(stream, polled)).toBe(polled);
  });

  it("keeps a newer SSE turn over the previous polled idle state", () => {
    const polled: ChatStateFrame = {
      runId: "run-1",
      status: "idle",
      turn: 1,
      capabilities,
    };
    const stream: ChatStateFrame = {
      ...polled,
      status: "starting",
      turn: 2,
    };

    expect(resolveChatState(stream, polled)).toBe(stream);
  });

  it("keeps the SSE state after a follow-up starts a replacement run", () => {
    const polled: ChatStateFrame = {
      runId: "run-1",
      status: "idle",
      turn: 1,
      capabilities,
    };
    const stream: ChatStateFrame = {
      ...polled,
      runId: "run-2",
      status: "starting",
    };

    expect(resolveChatState(stream, polled)).toBe(stream);
  });
});
