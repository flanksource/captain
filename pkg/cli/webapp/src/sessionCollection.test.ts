import { describe, expect, it } from "vitest";
import type { PromptBatchHandle } from "./hooks/usePromptRunStream";
import {
  batchChatTargetState,
  batchChatTargets,
  batchSessionCollection,
} from "./sessionCollection";

const HANDLE: PromptBatchHandle = {
  batchId: "batch-1",
  status: "running",
  total: 2,
  runs: [
    {
      runId: "run-1",
      sessionId: "session-1",
      selector: "codex-agent:gpt-5",
      status: "running",
      provider: "openai",
      mode: "agent",
      chat: true,
      capabilities: {
        interrupt: true,
        steer: true,
        followUp: true,
        resume: true,
      },
    },
    {
      runId: "run-2",
      sessionId: "session-2",
      selector: "gemini:gemini-2.5-flash",
      status: "running",
      provider: "google",
      mode: "cli",
      chat: false,
    },
  ],
};

describe("batch session collection", () => {
  it("selects every direct model session by default", () => {
    const collection = batchSessionCollection(HANDLE);

    expect(collection.defaultSelectedSessionIds).toEqual([
      "session-1",
      "session-2",
    ]);
    expect(collection.sessions).toMatchObject([
      { id: "batch-1" },
      {
        id: "session-1",
        parentId: "batch-1",
        summary: { provider: "openai", modelMode: "agent" },
      },
      {
        id: "session-2",
        parentId: "batch-1",
        summary: { provider: "google", modelMode: "cli" },
      },
    ]);
  });

  it("offers chat only for direct children with chat capabilities", () => {
    expect(batchChatTargets(HANDLE).map((run) => run.sessionId)).toEqual([
      "session-1",
    ]);
  });

  it("hydrates a chat target from its polled child session state", () => {
    const state = {
      runId: "run-1",
      status: "idle" as const,
      turn: 1,
      capabilities: {
        interrupt: true,
        steer: false,
        followUp: true,
        resume: true,
      },
    };

    expect(
      batchChatTargetState(
        {
          rootSessionId: "batch-1",
          total: 2,
          sessions: [
            {
              captainId: "session-1",
              detailAvailable: false,
              summary: {
                key: "session-1",
                id: "provider-session-1",
                source: "codex",
                toolCalls: 0,
                messages: 0,
              },
              chatState: state,
            },
          ],
        },
        HANDLE.runs[0]!,
      ),
    ).toEqual(state);
  });
});
