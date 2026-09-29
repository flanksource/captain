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

  const postedBody = (fetcher: ReturnType<typeof vi.fn>): unknown =>
    JSON.parse((fetcher.mock.lastCall?.[1] as RequestInit).body as string);

  const resolveWith = async (
    approved: boolean,
    decision: Parameters<typeof resolveSessionApproval>[0]["decision"],
    reason?: string,
  ): Promise<unknown> => {
    const fetcher = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    await resolveSessionApproval(
      {
        sessionsApi: "/api/chat/sessions",
        sessionId: "session-a",
        approvalId: "approval-1",
        approved,
        ...(reason ? { reason } : {}),
        ...(decision ? { decision } : {}),
      },
      fetcher,
    );
    return postedBody(fetcher);
  };

  it("sends a plain approval exactly as before when the decision carries nothing typed", async () => {
    expect(await resolveWith(true, {})).toEqual({ approved: true });
  });

  it("sends Cancel as a denial with interrupt", async () => {
    expect(await resolveWith(false, { interrupt: true }, "stop")).toEqual({
      approved: false,
      reason: "stop",
      interrupt: true,
    });
  });

  it("sends scope and the granted subset of a permissions request", async () => {
    const grants = { filesystem: { writableRoots: ["/repo/.git"] } };
    expect(await resolveWith(true, { scope: "session", grants })).toEqual({
      approved: true,
      scope: "session",
      grants,
    });
  });

  it("sends form content as the updated input", async () => {
    expect(await resolveWith(true, { content: { repo: "flanksource/captain" } })).toEqual({
      approved: true,
      updatedInput: { repo: "flanksource/captain" },
    });
  });

  it("sends question answers under updatedInput.answers", async () => {
    expect(await resolveWith(true, { answers: { data_model: "Replace Forex" } })).toEqual({
      approved: true,
      updatedInput: { answers: { data_model: "Replace Forex" } },
    });
  });

  it("refuses a denial that carries input, scope or grants instead of dropping them", async () => {
    await expect(resolveWith(false, { content: { a: "b" } })).rejects.toThrow(/denied approval cannot carry/);
    await expect(resolveWith(false, { scope: "turn" })).rejects.toThrow(/denied approval cannot carry/);
    await expect(resolveWith(false, { grants: {} })).rejects.toThrow(/denied approval cannot carry/);
  });

  it("refuses interrupt on an approval", async () => {
    await expect(resolveWith(true, { interrupt: true })).rejects.toThrow(/Interrupt needs a denial/);
  });

  it("refuses content together with answers", async () => {
    await expect(resolveWith(true, { content: { a: "b" }, answers: { q: "x" } })).rejects.toThrow(
      /either answers or content/,
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
