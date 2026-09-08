import { describe, expect, it, vi } from "vitest";
import {
  findSessionForApproval,
  resolveSessionApproval,
} from "./sessionApprovals";
import type { SessionGetItem } from "./sessionData";

function sessionItem(
  captainId: string,
  requests: { id: string }[] = [],
): SessionGetItem {
  return {
    captainId,
    detailAvailable: true,
    summary: {
      key: captainId,
      id: captainId,
      source: "captain",
      toolCalls: 0,
      messages: 0,
    },
    detail: {
      id: captainId,
      source: "captain",
      messages: [],
      requests: requests.map((request) => ({
        ...request,
        kind: "tool_approval",
        state: "pending" as const,
        tool: "Write",
      })),
    },
  };
}

describe("findSessionForApproval", () => {
  it("finds the session owning the given approval id", () => {
    const sessions = [
      sessionItem("session-a", [{ id: "approval-1" }]),
      sessionItem("session-b", [{ id: "approval-2" }]),
    ];
    expect(findSessionForApproval(sessions, "approval-2")).toBe("session-b");
  });

  it("returns undefined when no session owns the approval", () => {
    const sessions = [sessionItem("session-a", [{ id: "approval-1" }])];
    expect(findSessionForApproval(sessions, "approval-missing")).toBeUndefined();
  });

  it("returns undefined for an empty session list", () => {
    expect(findSessionForApproval([], "approval-1")).toBeUndefined();
  });
});

describe("resolveSessionApproval", () => {
  it("posts approve/deny to the session's approval endpoint", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));

    await resolveSessionApproval(
      {
        sessionsApi: "/api/chat/sessions",
        sessionId: "session-a",
        approvalId: "approval-1",
        approved: true,
        reason: "looks safe",
      },
      fetcher,
    );

    expect(fetcher).toHaveBeenCalledWith(
      "/api/chat/sessions/session-a/approvals/approval-1",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ approved: true, reason: "looks safe" }),
      }),
    );
  });

  it("omits the reason field when none is given", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));

    await resolveSessionApproval(
      {
        sessionsApi: "/api/chat/sessions",
        sessionId: "session-a",
        approvalId: "approval-1",
        approved: false,
      },
      fetcher,
    );

    expect(fetcher).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ body: JSON.stringify({ approved: false }) }),
    );
  });

  it("throws the server's refusal text when the prompt run is not waiting", async () => {
    const fetcher = vi.fn().mockResolvedValue(
      new Response("prompt run is not waiting", { status: 409 }),
    );

    await expect(
      resolveSessionApproval(
        {
          sessionsApi: "/api/chat/sessions",
          sessionId: "session-a",
          approvalId: "approval-1",
          approved: true,
        },
        fetcher,
      ),
    ).rejects.toThrow("prompt run is not waiting");
  });
});
