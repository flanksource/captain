import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SessionUIMessage } from "@flanksource/clicky-ui/ai";
import {
  DELIVERY_REFRESH_MS,
  selectInitialPermissionMode,
  useSessionChat,
} from "./useSessionChat";
import type { ChatStateFrame, PromptRunStreamState } from "./usePromptRunStream";

const usePromptRunStreamMock = vi.hoisted(() => vi.fn());

vi.mock("./usePromptRunStream", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./usePromptRunStream")>()),
  usePromptRunStream: usePromptRunStreamMock,
}));

afterEach(() => {
  vi.unstubAllGlobals();
  usePromptRunStreamMock.mockReset();
});

function streamState(
  overrides: Partial<PromptRunStreamState> = {},
): PromptRunStreamState {
  return {
    messages: [],
    status: "idle",
    verify: null,
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function requestBody(fetchMock: ReturnType<typeof vi.fn>, callIndex: number) {
  const init = fetchMock.mock.calls[callIndex]?.[1] as RequestInit;
  return JSON.parse(init.body as string) as Record<string, unknown>;
}

function requestUrl(fetchMock: ReturnType<typeof vi.fn>, callIndex: number) {
  return fetchMock.mock.calls[callIndex]?.[0] as string;
}

const liveCapabilities = {
  interrupt: true,
  steer: true,
  followUp: true,
  resume: true,
  setPermissionMode: true,
};

function liveChatState(
  overrides: Partial<ChatStateFrame> = {},
): ChatStateFrame {
  return {
    runId: "run-1",
    status: "running",
    turn: 1,
    capabilities: liveCapabilities,
    permissionMode: "plan",
    permissionModes: ["default", "plan", "auto"],
    ...overrides,
  };
}

describe("selectInitialPermissionMode", () => {
  it("honors the initial mode when the runtime still offers it", () => {
    expect(
      selectInitialPermissionMode("auto", ["default", "auto", "plan"]),
    ).toBe("auto");
  });

  it("falls back to default when the initial mode is no longer offered", () => {
    expect(
      selectInitialPermissionMode("dontAsk", ["default", "plan"]),
    ).toBe("default");
  });

  it("falls back to the first offered mode when default is not offered", () => {
    expect(selectInitialPermissionMode(undefined, ["plan", "auto"])).toBe(
      "plan",
    );
  });

  it("returns undefined when the runtime offers no modes", () => {
    expect(selectInitialPermissionMode("auto", [])).toBeUndefined();
    expect(selectInitialPermissionMode("auto", undefined)).toBeUndefined();
  });
});

describe("useSessionChat permission mode selection", () => {
  it("initializes the local selection per selectInitialPermissionMode for a not-live session", () => {
    usePromptRunStreamMock.mockReturnValue(streamState());

    const { result } = renderHook(() =>
      useSessionChat({
        sessionID: "session-1",
        initialPermissionMode: "auto",
        permissionModes: ["default", "auto", "plan"],
      }),
    );

    expect(result.current.permissionMode).toBe("auto");
    expect(result.current.permissionModes).toEqual([
      "default",
      "auto",
      "plan",
    ]);
    expect(result.current.canSetPermissionMode).toBe(true);
  });

  it("keeps the live run's last posture selected once the run ends", () => {
    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "streaming", chatState: liveChatState() }),
    );
    const { result, rerender } = renderHook(() =>
      useSessionChat({
        initialRunID: "run-1",
        sessionID: "session-1",
        initialPermissionMode: "default",
        permissionModes: ["default", "plan", "auto"],
      }),
    );
    expect(result.current.permissionMode).toBe("plan");

    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "done", chatState: liveChatState() }),
    );
    rerender();

    expect(result.current.permissionMode).toBe("plan");
  });

  it("reports no settable permission mode when the session offers none", () => {
    usePromptRunStreamMock.mockReturnValue(streamState());

    const { result } = renderHook(() =>
      useSessionChat({ sessionID: "session-1" }),
    );

    expect(result.current.permissionMode).toBeUndefined();
    expect(result.current.canSetPermissionMode).toBe(false);
  });
});

describe("useSessionChat send()", () => {
  it.each([["claude-socket", "sent"], ["codex-queue", "queued"]])("refreshes the transcript after %s delivery without opening a prompt stream", async (channel, status) => {
    usePromptRunStreamMock.mockReturnValue(streamState());
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      messageId: "m1",
      status,
      channel,
      capabilities: { ...liveCapabilities, interrupt: false },
    })));
    const onTerminal = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() =>
      useSessionChat({ sessionID: "session-1", onTerminal }),
    );

    await act(async () => {
      await result.current.send("hello");
    });

    expect(onTerminal).toHaveBeenCalledOnce();
    expect(result.current).toMatchObject({
      activeRunID: undefined,
      messages: [{ role: "user", parts: [{ type: "text", text: "hello" }] }],
      actionError: undefined,
    });
  });

  it("keeps refreshing and showing a delivered message until the transcript holds it and a reply", async () => {
    vi.useFakeTimers();
    try {
      usePromptRunStreamMock.mockReturnValue(streamState());
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
        messageId: "m1", status: "sent", channel: "claude-socket", capabilities: liveCapabilities,
      })));
      const onTerminal = vi.fn().mockResolvedValue(undefined);
      const earlier: SessionUIMessage = { id: "t0", role: "user", parts: [{ type: "text", text: "hello" }] };
      const { result, rerender } = renderHook(
        ({ transcript }: { transcript: SessionUIMessage[] }) =>
          useSessionChat({ sessionID: "session-1", onTerminal, transcript }),
        { initialProps: { transcript: [earlier] } },
      );

      await act(async () => {
        await result.current.send("hello");
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DELIVERY_REFRESH_MS * 2);
      });
      expect(onTerminal).toHaveBeenCalledTimes(3);

      const written: SessionUIMessage = { id: "t1", role: "user", parts: [{ type: "text", text: "hello" }] };
      rerender({ transcript: [earlier, written] });
      expect(result.current.messages).toHaveLength(1);

      const reply: SessionUIMessage = { id: "t2", role: "assistant", parts: [{ type: "text", text: "hi" }] };
      rerender({ transcript: [earlier, written, reply] });
      expect(result.current.messages).toEqual([]);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DELIVERY_REFRESH_MS * 2);
      });
      expect(onTerminal).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("includes permissionMode when posting directly to the session-message endpoint", async () => {
    usePromptRunStreamMock.mockReturnValue(streamState());
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        runId: "run-9",
        messageId: "m1",
        status: "started",
        capabilities: liveCapabilities,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() =>
      useSessionChat({
        sessionID: "session-1",
        initialPermissionMode: "auto",
        permissionModes: ["default", "auto"],
      }),
    );

    await act(async () => {
      await result.current.send("hello");
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestUrl(fetchMock, 0)).toBe(
      "/api/captain/sessions/session-1/message",
    );
    expect(requestBody(fetchMock, 0)).toMatchObject({
      text: "hello",
      permissionMode: "auto",
    });
  });

  it.each([
    ["no posture is recorded", undefined, ["default", "auto"], undefined],
    ["the recorded posture is not offered", "dontAsk", ["default", "plan"], undefined],
    ["the user picks one", undefined, ["default", "plan"], "plan"],
  ] as const)("sends a permission mode to the session-message endpoint only when known: %s", async (_name, initial, modes, picked) => {
    usePromptRunStreamMock.mockReturnValue(streamState());
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ messageId: "m1", status: "sent", channel: "claude-socket", capabilities: liveCapabilities }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() =>
      useSessionChat({ sessionID: "session-1", initialPermissionMode: initial, permissionModes: [...modes] }),
    );
    if (picked) {
      await act(async () => {
        await result.current.setPermissionMode(picked);
      });
    }

    await act(async () => {
      await result.current.send("hello");
    });

    const body = requestBody(fetchMock, 0);
    if (picked) expect(body).toMatchObject({ permissionMode: picked });
    else expect(body).not.toHaveProperty("permissionMode");
  });

  it("omits permissionMode when posting to the run-message endpoint of a live run", async () => {
    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "streaming", chatState: liveChatState() }),
    );
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        runId: "run-1",
        messageId: "m1",
        status: "steered",
        capabilities: liveCapabilities,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() =>
      useSessionChat({ initialRunID: "run-1", sessionID: "session-1" }),
    );

    await act(async () => {
      await result.current.send("hello");
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestUrl(fetchMock, 0)).toBe(
      "/api/captain/prompt/runs/run-1/message",
    );
    const body = requestBody(fetchMock, 0);
    expect(body).toEqual({ text: "hello", messageId: expect.any(String) });
    expect(body).not.toHaveProperty("permissionMode");
  });

  it("includes permissionMode on the 409 fallback to the session-message endpoint", async () => {
    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "streaming", chatState: liveChatState() }),
    );
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("conflict", { status: 409 }))
      .mockResolvedValueOnce(
        jsonResponse({
          runId: "run-2",
          messageId: "m1",
          status: "started",
          capabilities: liveCapabilities,
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() =>
      useSessionChat({ initialRunID: "run-1", sessionID: "session-1" }),
    );

    await act(async () => {
      await result.current.send("hello");
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestUrl(fetchMock, 0)).toBe(
      "/api/captain/prompt/runs/run-1/message",
    );
    expect(requestUrl(fetchMock, 1)).toBe(
      "/api/captain/sessions/session-1/message",
    );
    expect(requestBody(fetchMock, 1)).toMatchObject({
      text: "hello",
      permissionMode: "plan",
    });
  });
});

describe("useSessionChat live permission mode switching", () => {
  it("posts to the run's permission-mode endpoint for a live run", async () => {
    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "streaming", chatState: liveChatState() }),
    );
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ runId: "run-1", permissionMode: "auto" }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() =>
      useSessionChat({ initialRunID: "run-1", sessionID: "session-1" }),
    );

    expect(result.current.permissionMode).toBe("plan");
    expect(result.current.canSetPermissionMode).toBe(true);

    await act(async () => {
      await result.current.setPermissionMode("auto");
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestUrl(fetchMock, 0)).toBe(
      "/api/captain/prompt/runs/run-1/permission-mode",
    );
    expect(requestBody(fetchMock, 0)).toEqual({ mode: "auto" });
  });

  it("only updates local selection, without a network call, for a not-live session", async () => {
    usePromptRunStreamMock.mockReturnValue(streamState());
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() =>
      useSessionChat({
        sessionID: "session-1",
        initialPermissionMode: "default",
        permissionModes: ["default", "plan", "auto"],
      }),
    );

    await act(async () => {
      await result.current.setPermissionMode("plan");
    });

    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.permissionMode).toBe("plan");
  });

  it("surfaces a failed live switch as actionError without throwing", async () => {
    usePromptRunStreamMock.mockReturnValue(
      streamState({ status: "streaming", chatState: liveChatState() }),
    );
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("runtime cannot switch modes", { status: 409 }),
      ),
    );

    const { result } = renderHook(() =>
      useSessionChat({ initialRunID: "run-1", sessionID: "session-1" }),
    );

    await act(async () => {
      await result.current.setPermissionMode("bypassPermissions");
    });

    expect(result.current.actionError).toMatch(/runtime cannot switch modes/);
  });
});
