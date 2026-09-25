import type { ReactNode } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionDetail } from "./SessionDetail";
import type { SessionGetResult } from "./sessionData";
import type { VerifyFrame, VerifyReport } from "./types/verifyReport";

const { chatState, useSessionChatMock } = vi.hoisted(() => {
  const chatState = () => ({
    messages: [], verify: null as VerifyFrame | null, chatState: { status: "idle", queued: 0 },
    activeRunID: undefined as string | undefined, send: vi.fn(async (_text: string) => {}),
  });
  return { chatState, useSessionChatMock: vi.fn(chatState) };
});

vi.mock("./hooks/useSessionChat", async (importOriginal) => ({
  ...await importOriginal<typeof import("./hooks/useSessionChat")>(),
  useSessionChat: useSessionChatMock,
}));

type TranscriptProps = {
  pendingTools?: readonly { tool: string }[];
  onPendingToolDecision?: (decision: { allow: boolean; answers?: Record<string, string | string[]> }) => unknown;
};

vi.mock("@flanksource/clicky-ui/ai", async (importOriginal) => ({
  ...await importOriginal<typeof import("@flanksource/clicky-ui/ai")>(),
  SessionInspector: ({ composer, transcriptProps }: { composer?: ReactNode; transcriptProps?: TranscriptProps }) => (
    <div>
      Stored transcript{composer}
      {transcriptProps?.pendingTools?.map((tool) => <span key={tool.tool}>Pending {tool.tool}</span>)}
      {transcriptProps?.onPendingToolDecision && (
        <button onClick={() => transcriptProps.onPendingToolDecision?.({
          allow: true, answers: { "Which work should I implement?": "The plan" },
        })}>Send answer</button>
      )}
    </div>
  ),
  SessionChatComposer: ({ permissionFamily }: { permissionFamily?: string }) => (
    <div>Composer for {permissionFamily}</div>
  ),
}));

afterEach(() => {
  cleanup();
  useSessionChatMock.mockImplementation(chatState);
});

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
      ...chatState(),
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

describe("SessionDetail over a launcher run folded with its transcript", () => {
  const runId = "010d861b-ea19-5d5a-8303-a01072803dd2";
  const folded: SessionGetResult = {
    total: 1,
    sessions: [{
      captainId: runId,
      providerSessionId: "6c8440dd-5fad-43c5-b8f8-8047940ca5e5",
      detailAvailable: true,
      summary: { key: runId, id: runId, source: "gavel", messages: 0, toolCalls: 11 },
      detail: { id: runId, source: "gavel", messages: [] },
      execution: { captainId: "613e86fd-9a21-4d1b-9cd0-c60e7485d0e5", source: "claude", cwd: "/work/tree" },
      chat: { resume: true, interrupt: false, steer: false, followUp: false, setPermissionMode: false },
    }],
  };

  it("renders one transcript whose composer speaks the executing provider's permission modes", () => {
    render(<SessionDetail result={folded} loading={false} error={undefined} onRefresh={vi.fn()} />);

    expect(screen.getAllByText("Stored transcript")).toHaveLength(1);
    expect(screen.queryByText(runId)).not.toBeInTheDocument();
    expect(screen.getByText("Composer for claude")).toBeInTheDocument();
  });

  describe("when the run ended by asking", () => {
    const asked = (overrides: Partial<SessionGetResult["sessions"][number]> = {}): SessionGetResult => ({
      ...folded,
      sessions: [{
        ...folded.sessions[0]!,
        detail: {
          id: runId, source: "gavel", messages: [],
          awaitingInput: {
            origin: "envelope", summary: "Blocked on scope.",
            questions: [{ text: "Which work should I implement?", options: ["The plan", "The todo"] }],
          },
        },
        ...overrides,
      }],
    });

    it("puts the questions forward and sends the chosen answer as the session's next turn", () => {
      const chat = chatState();
      useSessionChatMock.mockReturnValue(chat);

      render(<SessionDetail result={asked()} loading={false} error={undefined} onRefresh={vi.fn()} />);
      fireEvent.click(screen.getByRole("button", { name: "Send answer" }));

      expect(screen.getByText("Pending AskUserQuestion")).toBeInTheDocument();
      expect(chat.send).toHaveBeenCalledWith("Answers:\n1. Which work should I implement?\n→ The plan");
    });

    it.each([
      ["the session cannot be resumed", { chat: { ...folded.sessions[0]!.chat!, resume: false } }, undefined],
      ["a run is already answering", {}, "run-1"],
    ])("offers no answer form when %s", (_name, overrides, activeRunID) => {
      useSessionChatMock.mockReturnValue({ ...chatState(), activeRunID });

      render(<SessionDetail result={asked(overrides)} loading={false} error={undefined} onRefresh={vi.fn()} />);

      expect(screen.queryByText("Pending AskUserQuestion")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Send answer" })).not.toBeInTheDocument();
    });
  });
});
