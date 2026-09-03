import { useEffect, useRef, type MutableRefObject } from "react";

export type SSEStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";

export interface UseEventSourceOptions {
  /** Disable the connection (e.g. before a run id is known, or once done). */
  enabled?: boolean;
  /** Named SSE events to bind, in addition to the default `message` event. */
  events?: string[];
  /** Called for every frame: the event name ("message" for default) and raw data. */
  onEvent: (event: string, data: string) => void;
  onStatus?: (status: SSEStatus) => void;
  /**
   * Called when `onEvent` throws while handling a frame (e.g. wire-shape
   * drift). The stream stays open and later events keep being delivered —
   * a bad frame must never go silent nor tear down the connection.
   */
  onError?: (event: string, message: string) => void;
}

/**
 * useEventSource is a thin wrapper over the browser EventSource. It (re)connects
 * whenever url/enabled/events change and closes on unmount. Handlers are held in
 * refs so callers can pass inline closures without forcing reconnects.
 */
export function useEventSource(url: string | undefined, options: UseEventSourceOptions): void {
  const { enabled = true, events = [], onEvent, onStatus, onError } = options;
  const onEventRef = useRef(onEvent);
  const onStatusRef = useRef(onStatus);
  const onErrorRef = useRef(onError);
  useEffect(() => {
    onEventRef.current = onEvent;
    onStatusRef.current = onStatus;
    onErrorRef.current = onError;
  }, [onEvent, onStatus, onError]);

  // Stable dependency: only re-bind when the set of named events actually changes.
  const eventsKey = events.join(",");

  useEffect(() => {
    if (!url || !enabled) return;
    return subscribeToEventSource(
      url,
      eventsKey,
      onEventRef,
      onStatusRef,
      onErrorRef,
    );
  }, [url, enabled, eventsKey]);
}

function subscribeToEventSource(
  url: string,
  eventsKey: string,
  onEventRef: MutableRefObject<UseEventSourceOptions["onEvent"]>,
  onStatusRef: MutableRefObject<UseEventSourceOptions["onStatus"]>,
  onErrorRef: MutableRefObject<UseEventSourceOptions["onError"]>,
) {
  const setStatus = (status: SSEStatus) => onStatusRef.current?.(status);
  const dispatch = (name: string, data: string) => {
    try {
      onEventRef.current(name, data);
    } catch (error) {
      onErrorRef.current?.(
        name,
        error instanceof Error ? error.message : String(error),
      );
    }
  };
  setStatus("connecting");
  const eventSource = new EventSource(url);
  let closed = false;

  eventSource.onopen = () => setStatus("open");
  eventSource.onmessage = (event) => dispatch("message", event.data);
  eventSource.onerror = () => {
    if (!closed) setStatus("reconnecting");
  };

  const listeners = (eventsKey ? eventsKey.split(",") : []).map((name) => {
    const handler = (event: MessageEvent) => dispatch(name, event.data);
    eventSource.addEventListener(name, handler as EventListener);
    return { name, handler };
  });

  return () => {
    closed = true;
    for (const { name, handler } of listeners) {
      eventSource.removeEventListener(name, handler as EventListener);
    }
    eventSource.close();
    setStatus("closed");
  };
}
