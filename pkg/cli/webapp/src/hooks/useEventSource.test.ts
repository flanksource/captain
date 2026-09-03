import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useEventSource } from "./useEventSource";

/**
 * jsdom does not implement EventSource, so tests install this minimal double
 * as the global constructor and drive it directly via `emit`.
 */
class MockEventSource {
  static instances: MockEventSource[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  private readonly listeners = new Map<
    string,
    Set<(event: MessageEvent) => void>
  >();

  constructor(readonly url: string) {
    MockEventSource.instances.push(this);
  }

  addEventListener(name: string, handler: EventListener) {
    const set = this.listeners.get(name) ?? new Set();
    set.add(handler as (event: MessageEvent) => void);
    this.listeners.set(name, set);
  }

  removeEventListener(name: string, handler: EventListener) {
    this.listeners.get(name)?.delete(handler as (event: MessageEvent) => void);
  }

  close() {
    this.closed = true;
  }

  emit(name: string, data: string) {
    const event = { data } as MessageEvent;
    for (const handler of this.listeners.get(name) ?? []) {
      handler(event);
    }
  }
}

describe("useEventSource", () => {
  let originalEventSource: typeof EventSource | undefined;

  beforeEach(() => {
    originalEventSource = globalThis.EventSource;
    MockEventSource.instances = [];
    globalThis.EventSource =
      MockEventSource as unknown as typeof EventSource;
  });

  afterEach(() => {
    globalThis.EventSource = originalEventSource as typeof EventSource;
  });

  it("routes a handler error to onError and keeps delivering later events", () => {
    const onEvent = vi.fn((event: string) => {
      if (event === "verify") throw new Error("drifted verify frame");
    });
    const onError = vi.fn();

    renderHook(() =>
      useEventSource("http://test/stream", {
        events: ["verify", "entry"],
        onEvent,
        onError,
      }),
    );
    const source = MockEventSource.instances[0]!;

    act(() => {
      source.emit("verify", "bad-payload");
    });
    expect(onError).toHaveBeenCalledWith("verify", "drifted verify frame");
    expect(source.closed).toBe(false);

    act(() => {
      source.emit("entry", "good-payload");
    });
    expect(onEvent).toHaveBeenLastCalledWith("entry", "good-payload");
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("does not call onError when a handler completes normally", () => {
    const onEvent = vi.fn();
    const onError = vi.fn();

    renderHook(() =>
      useEventSource("http://test/stream", {
        events: ["entry"],
        onEvent,
        onError,
      }),
    );
    const source = MockEventSource.instances[0]!;

    act(() => {
      source.emit("entry", "fine");
    });
    expect(onEvent).toHaveBeenCalledWith("entry", "fine");
    expect(onError).not.toHaveBeenCalled();
  });

  it("wraps the default message dispatch the same way", () => {
    const onEvent = vi.fn(() => {
      throw new Error("boom");
    });
    const onError = vi.fn();

    renderHook(() =>
      useEventSource("http://test/stream", { onEvent, onError }),
    );
    const source = MockEventSource.instances[0]!;

    act(() => {
      source.onmessage?.({ data: "payload" } as MessageEvent);
    });
    expect(onError).toHaveBeenCalledWith("message", "boom");
  });
});
