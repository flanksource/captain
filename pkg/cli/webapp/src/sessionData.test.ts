import { describe, expect, it } from "vitest";
import { mergeSessionListPages, type SessionListResult } from "./sessionData";

function page(
  ids: string[],
  nextCursor?: string,
  summary?: SessionListResult["summary"],
): SessionListResult {
  return {
    sessions: ids.map((id) => ({
      key: id,
      id,
      source: "codex",
      toolCalls: 0,
      messages: 0,
    })),
    total: 5,
    source: "codex",
    scope: "all",
    nextCursor,
    summary,
  };
}

describe("session list pages", () => {
  it("preserves first, middle, and final page order and final cursor state", () => {
    const result = mergeSessionListPages([
        page(["session-5", "session-4"], "cursor-4", {
          totalSessions: 2,
          liveSessions: 2,
          activeSessions: 1,
          stoppedSessions: 0,
          alertSessions: 1,
          inputTokens: 10,
          outputTokens: 5,
          totalTokens: 15,
          costUsd: 0.1,
          lowestContextFree: 70,
        }),
        page(["session-3", "session-2"], "cursor-2", {
          totalSessions: 2,
          liveSessions: 1,
          activeSessions: 1,
          stoppedSessions: 1,
          alertSessions: 0,
          inputTokens: 20,
          outputTokens: 10,
          totalTokens: 30,
          costUsd: 0.2,
          lowestContextFree: 40,
        }),
        page(["session-1"], undefined, {
          totalSessions: 1,
          liveSessions: 1,
          activeSessions: 0,
          stoppedSessions: 1,
          alertSessions: 1,
          inputTokens: 30,
          outputTokens: 15,
          totalTokens: 45,
          costUsd: 0.3,
          lowestContextFree: 60,
        }),
      ]);

    expect(result).toMatchObject({
      sessions: [
        { id: "session-5" },
        { id: "session-4" },
        { id: "session-3" },
        { id: "session-2" },
        { id: "session-1" },
      ],
      total: 5,
      nextCursor: undefined,
      summary: {
        totalSessions: 5,
        liveSessions: 4,
        activeSessions: 2,
        stoppedSessions: 2,
        alertSessions: 2,
        inputTokens: 60,
        outputTokens: 30,
        totalTokens: 90,
        lowestContextFree: 40,
      },
    });
    expect(result?.summary?.costUsd).toBeCloseTo(0.6);
  });
});
