import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { usePromptRunStream } from "./usePromptRunStream";
import type { VerifyReport } from "../types/verifyReport";

const useEventSourceMock = vi.hoisted(() => vi.fn());

vi.mock("./useEventSource", () => ({
  useEventSource: useEventSourceMock,
}));

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

function lastOnEvent() {
  const calls = useEventSourceMock.mock.calls;
  return calls[calls.length - 1]?.[1].onEvent;
}

describe("usePromptRunStream", () => {
  it("retains the terminal failure summary from the error event", () => {
    const { result } = renderHook(() => usePromptRunStream("run-4a82"));
    const onEvent = lastOnEvent();

    act(() => {
      onEvent(
        "error",
        JSON.stringify({
          runId: "run-4a82",
          sessionId: "session-91bd",
          model: "claude-sonnet-5",
          provider: "anthropic",
          mode: "api",
          success: false,
          error: "provider rejected the request",
        }),
      );
    });

    expect(result.current).toEqual({
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
      run: undefined,
      chatState: undefined,
      verify: null,
    });
  });

  it("keeps the latest verify snapshot, ending on the done verdict", () => {
    const { result } = renderHook(() => usePromptRunStream("run-verify-1"));
    const onEvent = lastOnEvent();

    const first = verifyReport({ summary: { ...verifyReport().summary, passed: 1, pending: 4 } });
    const second = verifyReport({ summary: { ...verifyReport().summary, passed: 3, pending: 2 } });
    const verdict = verifyReport({
      passed: true,
      state: "passed",
      summary: { ...verifyReport().summary, passed: 5, pending: 0 },
    });

    act(() => {
      onEvent("verify", JSON.stringify({ report: first, done: false }));
    });
    expect(result.current.verify).toEqual({ report: first, done: false });

    act(() => {
      onEvent("verify", JSON.stringify({ report: second, done: false }));
    });
    expect(result.current.verify).toEqual({ report: second, done: false });

    act(() => {
      onEvent("verify", JSON.stringify({ report: verdict, done: true }));
    });
    expect(result.current.verify).toEqual({ report: verdict, done: true });
  });

  it("resets verify to null when a frame reports no report", () => {
    const { result } = renderHook(() => usePromptRunStream("run-verify-2"));
    const onEvent = lastOnEvent();

    act(() => {
      onEvent(
        "verify",
        JSON.stringify({ report: verifyReport(), done: false }),
      );
    });
    expect(result.current.verify?.report).not.toBeNull();

    act(() => {
      onEvent("verify", JSON.stringify({ report: null, done: false }));
    });
    expect(result.current.verify).toEqual({ report: null, done: false });
  });

  it("clears verify and surfaces an error on a malformed frame, without swallowing it", () => {
    const { result } = renderHook(() => usePromptRunStream("run-verify-3"));
    const onEvent = lastOnEvent();

    act(() => {
      onEvent(
        "verify",
        JSON.stringify({ report: verifyReport(), done: false }),
      );
    });
    expect(result.current.verify?.report).not.toBeNull();

    expect(() =>
      act(() => {
        onEvent(
          "verify",
          JSON.stringify({
            report: { ...verifyReport(), state: "bogus" },
            done: false,
          }),
        );
      }),
    ).not.toThrow();

    expect(result.current.verify).toBeNull();
    expect(result.current.error).toMatch(/verify/);
    expect(result.current.error).toMatch(/state/);
  });

  it("preserves the data-verify part on a verdict transcript message", () => {
    const { result } = renderHook(() => usePromptRunStream("run-verify-4"));
    const onEvent = lastOnEvent();
    const report = verifyReport({ passed: true, state: "passed" });

    act(() => {
      onEvent(
        "entry",
        JSON.stringify({
          id: "msg-1",
          role: "verified",
          parts: [
            { type: "text", text: "verified: acceptance" },
            { type: "data-verify", data: report },
          ],
        }),
      );
    });

    expect(result.current.messages).toHaveLength(1);
    expect(result.current.messages[0]?.parts[1]).toEqual({
      type: "data-verify",
      data: report,
    });
  });
});
