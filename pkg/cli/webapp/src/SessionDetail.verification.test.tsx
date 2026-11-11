import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionDetail } from "./SessionDetail";
import type { SessionGetResult } from "./sessionData";
import type { VerifyFrame, VerifyReport } from "./types/verifyReport";

const { useSessionChatMock } = vi.hoisted(() => ({
  useSessionChatMock: vi.fn(() => ({
    messages: [],
    verify: null as VerifyFrame | null,
    chatState: { status: "idle", queued: 0 },
    send: vi.fn(),
  })),
}));

vi.mock("./hooks/useSessionChat", async (importOriginal) => ({
  ...await importOriginal<typeof import("./hooks/useSessionChat")>(),
  useSessionChat: useSessionChatMock,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const report: VerifyReport = {
  kind: "fixture", ran: true, passed: false, state: "failed", iteration: 1,
  summary: { total: 1, passed: 0, failed: 1, warned: 0, skipped: 0, pending: 0, running: 0, timedout: 0 },
  tests: [{ name: "Persisted acceptance check", framework: "fixture", failed: true }],
  reason: "Expected three retries",
};

function storedSession(collection = false): SessionGetResult {
  return {
    ...(collection ? { rootSessionId: "stored-session" } : {}),
    total: 1,
    sessions: [{
      captainId: "stored-session",
      detailAvailable: true,
      summary: { key: "stored-session", id: "stored-session", source: "captain", messages: 0, toolCalls: 0 },
      detail: {
        id: "stored-session", source: "captain", messages: [],
        verifications: [{ iteration: 1, report }],
      },
    }],
  };
}

describe("SessionDetail verification tab", () => {
  it.each([false, true])("shows stored results only in the tab with collection=%s", (collection) => {
    render(<SessionDetail result={storedSession(collection)}
      loading={false} error={undefined} onRefresh={vi.fn()}
    />);

    expect(screen.queryByText(report.reason!)).not.toBeInTheDocument();
    expect(screen.queryByText("Persisted acceptance check")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Verification 1" }));
    expect(screen.getAllByText(report.reason!)).toHaveLength(1);
    expect(screen.getByText("Persisted acceptance check")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Transcript" }));
    expect(screen.queryByText(report.reason!)).not.toBeInTheDocument();
  });

  it.each([1, 2])("merges live iteration %s into the tab and preserves other iterations", (iteration) => {
    const liveReport: VerifyReport = {
      ...report, iteration, state: "running", reason: undefined,
      tests: [{ name: "Retrying acceptance check", framework: "fixture", running: true }],
      summary: { ...report.summary, failed: 0, running: 1 },
    };
    useSessionChatMock.mockReturnValueOnce({
      messages: [], verify: { done: false, report: liveReport },
      chatState: { status: "idle", queued: 0 }, send: vi.fn(),
    });
    render(<SessionDetail result={storedSession()}
      loading={false} error={undefined} onRefresh={vi.fn()}
    />);

    expect(screen.queryByText("Retrying acceptance check")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: `Verification ${iteration}` }));
    expect(screen.getAllByText("Retrying acceptance check")).toHaveLength(1);
    expect(screen.getByText("Running verification…")).toBeInTheDocument();
    expect(screen.queryByText("Persisted acceptance check")).not.toBeInTheDocument();
    if (iteration === 2) {
      fireEvent.click(screen.getByRole("button", { name: "stored-s · 1. fixture · failed" }));
      expect(screen.getByText("Persisted acceptance check")).toBeInTheDocument();
    }
  });
});
