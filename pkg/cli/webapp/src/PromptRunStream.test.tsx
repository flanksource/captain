import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PromptRunStream } from "./PromptRunStream";
import type { VerifyReport } from "./types/verifyReport";

const useSessionChatMock = vi.hoisted(() => vi.fn());

vi.mock("./hooks/useSessionChat", () => ({
  useSessionChat: useSessionChatMock,
}));

afterEach(cleanup);

function verifyReport(overrides: Partial<VerifyReport> = {}): VerifyReport {
  return {
    kind: "fixture",
    name: "acceptance",
    ran: true,
    passed: false,
    iteration: 1,
    summary: {
      total: 5,
      passed: 3,
      failed: 0,
      warned: 0,
      skipped: 0,
      pending: 2,
      running: 0,
      timedout: 0,
    },
    state: "running",
    ...overrides,
  };
}

describe("PromptRunStream", () => {
  beforeEach(() => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      summary: {
        runId: "run-4a82",
        sessionId: "session-91bd",
        model: "claude-sonnet-5",
        provider: "anthropic",
        mode: "api",
        success: false,
        error: "provider rejected the request",
      },
      status: "error",
      error: "provider rejected the request",
      run: {
        runId: "run-4a82",
        sessionId: "session-91bd",
        status: "error",
        chat: false,
        model: "claude-sonnet-5",
        provider: "anthropic",
        mode: "api",
      },
      verify: null,
    });
  });

  it("shows failed-run diagnostics including the session UID", () => {
    render(<PromptRunStream runID="run-4a82" />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "provider rejected the request",
    );
    expect(screen.getByText("Run UID")).toBeInTheDocument();
    expect(screen.getByText("run-4a82")).toBeInTheDocument();
    expect(screen.getByText("Session UID")).toBeInTheDocument();
    expect(screen.getByText("session-91bd")).toBeInTheDocument();
    expect(screen.getByText("claude-sonnet-5")).toBeInTheDocument();
    expect(screen.getByText("anthropic")).toBeInTheDocument();
    expect(screen.getByText("api")).toBeInTheDocument();
    expect(
      screen.getByText("Run failed before session activity."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Starting run…")).not.toBeInTheDocument();
  });

  it("shows nothing when there is no verify frame yet", () => {
    render(<PromptRunStream runID="run-4a82" />);
    expect(screen.queryByTestId("verify-status")).not.toBeInTheDocument();
  });

  it("shows a live progress line while a check is still running", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "streaming",
      run: { runId: "run-verify", status: "running" },
      verify: { report: verifyReport(), done: false },
    });

    render(<PromptRunStream runID="run-verify" />);

    expect(screen.getByTestId("verify-status")).toHaveTextContent(
      "verifying · 3/5 passed",
    );
  });

  it("shows a plain verdict once the check has passed", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "done",
      run: { runId: "run-verify", status: "done" },
      verify: {
        report: verifyReport({ passed: true, state: "passed" }),
        done: true,
      },
    });

    render(<PromptRunStream runID="run-verify" />);

    expect(screen.getByTestId("verify-status")).toHaveTextContent("verified");
  });

  it("renders a malformed verify frame's error without dropping the transcript", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "streaming",
      run: { runId: "run-verify", status: "running" },
      verify: null,
      error: 'invalid verify frame: verify report: "state" must be one of queued, running, passed, failed, errored, warned, skipped, cancelled, timed_out, got "bogus"',
    });

    render(<PromptRunStream runID="run-verify" />);

    expect(screen.getByRole("alert")).toHaveTextContent(/invalid verify frame/);
    expect(screen.queryByTestId("verify-status")).not.toBeInTheDocument();
  });

  it("shows the failure reason once the check has failed", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "done",
      run: { runId: "run-verify", status: "done" },
      verify: {
        report: verifyReport({
          state: "failed",
          reason: "2 of 5 checks failed",
        }),
        done: true,
      },
    });

    render(<PromptRunStream runID="run-verify" />);

    expect(screen.getByTestId("verify-status")).toHaveTextContent(
      "verification failed · 2 of 5 checks failed",
    );
  });
});
