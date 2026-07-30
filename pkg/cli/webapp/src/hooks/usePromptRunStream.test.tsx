import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { usePromptRunStream } from "./usePromptRunStream";

const useEventSourceMock = vi.hoisted(() => vi.fn());

vi.mock("./useEventSource", () => ({
  useEventSource: useEventSourceMock,
}));

describe("usePromptRunStream", () => {
  it("retains the terminal failure summary from the error event", () => {
    const { result } = renderHook(() => usePromptRunStream("run-4a82"));
    const onEvent = useEventSourceMock.mock.calls.at(-1)?.[1].onEvent;

    act(() => {
      onEvent(
        "error",
        JSON.stringify({
          runId: "run-4a82",
          sessionId: "session-91bd",
          model: "claude-sonnet-5",
          backend: "anthropic",
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
        backend: "anthropic",
        success: false,
        error: "provider rejected the request",
      },
      status: "error",
      error: "provider rejected the request",
      run: undefined,
      chatState: undefined,
    });
  });
});
