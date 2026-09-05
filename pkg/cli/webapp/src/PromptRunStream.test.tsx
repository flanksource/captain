import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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
    tests: [
      { name: "Compile packages", framework: "fixture", passed: true },
      { name: "Check policy", framework: "fixture", running: true },
    ],
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
    expect(screen.queryByRole("region", { name: "Verification" })).not.toBeInTheDocument();
  });

  it("shows live verification checks beside the transcript", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "streaming",
      run: { runId: "run-verify", status: "running" },
      verify: { report: verifyReport(), done: false },
    });

    render(<PromptRunStream runID="run-verify" />);

    expect(screen.getByText("Running verification…")).toBeInTheDocument();
    expect(screen.getByText("Compile packages")).toBeInTheDocument();
    expect(screen.getByText("Check policy")).toBeInTheDocument();
    expect(screen.getByText("Starting run…")).toBeInTheDocument();
  });

  it("keeps the verification checks visible once the run has passed", () => {
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

    expect(screen.getByText("Compile packages")).toBeInTheDocument();
    expect(screen.queryByText("Running verification…")).not.toBeInTheDocument();
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
    expect(screen.queryByRole("region", { name: "Verification" })).not.toBeInTheDocument();
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

    expect(screen.getByText("2 of 5 checks failed")).toBeInTheDocument();
  });

  it("opens failed fixture evidence including command output", () => {
    useSessionChatMock.mockReturnValue({
      messages: [],
      status: "error",
      run: { runId: "run-verify", status: "error" },
      verify: {
        report: verifyReport({
          state: "failed",
          tests: [{
            name: "Policy assertion",
            framework: "fixture",
            failed: true,
            command: "check-policy",
            stdout: "Observed retry limit: 2",
            context: { exit_code: 1, cel_expression: "retry_limit == 3" },
          }],
        }),
        done: true,
      },
    });

    render(<PromptRunStream runID="run-verify" />);
    fireEvent.click(screen.getByText("Policy assertion"));
    expect(screen.getByText("Observed retry limit: 2")).toBeInTheDocument();
  });
});
