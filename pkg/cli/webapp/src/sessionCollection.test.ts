import { describe, expect, it } from "vitest";
import type { PromptBatchHandle } from "./hooks/usePromptRunStream";
import { batchChatTargets, batchSessionCollection } from "./sessionCollection";

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
      { id: "session-1", parentId: "batch-1" },
      { id: "session-2", parentId: "batch-1" },
    ]);
  });

  it("offers chat only for direct children with chat capabilities", () => {
    expect(batchChatTargets(HANDLE).map((run) => run.sessionId)).toEqual([
      "session-1",
    ]);
  });
});
