import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionDetail } from "./SessionDetail";
import type { SessionGetResult } from "./sessionData";
import type { VerifyFrame, VerifyReport } from "./types/verifyReport";

const useSessionChatMock = vi.hoisted(() => vi.fn(() => ({ messages: [], verify: null as VerifyFrame | null })));

vi.mock("./hooks/useSessionChat", async (importOriginal) => ({
  ...await importOriginal<typeof import("./hooks/useSessionChat")>(),
  useSessionChat: useSessionChatMock,
}));

vi.mock("@flanksource/clicky-ui/ai", async (importOriginal) => ({
  ...await importOriginal<typeof import("@flanksource/clicky-ui/ai")>(),
  SessionInspector: () => <div>Stored transcript</div>,
}));

afterEach(cleanup);

const report: VerifyReport = {
  kind: "fixture", ran: true, passed: false, state: "failed", iteration: 1,
  summary: { total: 1, passed: 0, failed: 1, warned: 0, skipped: 0, pending: 0, running: 0, timedout: 0 },
  tests: [{ name: "Persisted acceptance check", framework: "fixture", failed: true }],
  reason: "Expected three retries",
};

function storedSession(verify: unknown): SessionGetResult {
  return {
    total: 1,
    sessions: [{
      captainId: "stored-session",
      detailAvailable: true,
      summary: { key: "stored-session", id: "stored-session", source: "captain", messages: 1, toolCalls: 0 },
      detail: { id: "stored-session", source: "captain", messages: [], structuredOutput: { verify } },
    }],
  };
}

describe("SessionDetail verification", () => {
  it.each([false, true])("renders a persisted report with collection=%s", (collection) => {
    render(<SessionDetail
      result={{ ...storedSession(report), ...(collection ? { rootSessionId: "stored-session" } : {}) }}
      loading={false} error={undefined} onRefresh={vi.fn()}
    />);
    expect(screen.getByText("Persisted acceptance check")).toBeInTheDocument();
    expect(screen.getByText("Expected three retries")).toBeInTheDocument();
    expect(screen.getByText("Stored transcript")).toBeInTheDocument();
    expect(screen.queryByText("Running verification…")).not.toBeInTheDocument();
  });

  it("surfaces malformed persisted verification without losing the transcript", () => {
    render(<SessionDetail result={storedSession({ ...report, state: "bogus" })}
      loading={false} error={undefined} onRefresh={vi.fn()}
    />);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid stored verification report");
    expect(screen.getByText("Stored transcript")).toBeInTheDocument();
  });

  it("shows the live retry report instead of the stored prior verdict", () => {
    useSessionChatMock.mockReturnValueOnce({
      messages: [],
      verify: {
        done: false,
        report: {
          ...report,
          state: "running",
          reason: undefined,
          tests: [{ name: "Retrying acceptance check", framework: "fixture", running: true }],
          summary: { ...report.summary, failed: 0, running: 1 },
        },
      },
    });
    render(<SessionDetail result={storedSession(report)}
      loading={false} error={undefined} onRefresh={vi.fn()}
    />);
    expect(screen.getByText("Retrying acceptance check")).toBeInTheDocument();
    expect(screen.getByText("Running verification…")).toBeInTheDocument();
    expect(screen.queryByText("Persisted acceptance check")).not.toBeInTheDocument();
  });
});
