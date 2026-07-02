import { useEffect, useRef } from "react";

export type SSEStatus = "idle" | "connecting" | "open" | "reconnecting" | "closed";

export interface UseEventSourceOptions {
  /** Disable the connection (e.g. before a run id is known, or once done). */
  enabled?: boolean;
  /** Named SSE events to bind, in addition to the default `message` event. */
  events?: string[];
  /** Called for every frame: the event name ("message" for default) and raw data. */
  onEvent: (event: string, data: string) => void;
  onStatus?: (status: SSEStatus) => void;
}

/**
 * useEventSource is a thin wrapper over the browser EventSource. It (re)connects
 * whenever url/enabled/events change and closes on unmount. Handlers are held in
 * refs so callers can pass inline closures without forcing reconnects.
 */
export function useEventSource(url: string | undefined, options: UseEventSourceOptions): void {
  const { enabled = true, events = [], onEvent, onStatus } = options;
  const onEventRef = useRef(onEvent);
  const onStatusRef = useRef(onStatus);
  onEventRef.current = onEvent;
  onStatusRef.current = onStatus;

  // Stable dependency: only re-bind when the set of named events actually changes.
  const eventsKey = events.join(",");

  useEffect(() => {
    if (!url || !enabled) return;
    const setStatus = (s: SSEStatus) => onStatusRef.current?.(s);
    setStatus("connecting");

    const es = new EventSource(url);
    let closedByUs = false;

    es.onopen = () => setStatus("open");
    es.onmessage = (e) => onEventRef.current("message", e.data);
    es.onerror = () => {
      if (!closedByUs) setStatus("reconnecting");
    };

    const named = eventsKey ? eventsKey.split(",") : [];
    const bound = named.map((name) => {
      const handler = (e: MessageEvent) => onEventRef.current(name, e.data);
      es.addEventListener(name, handler as EventListener);
      return { name, handler };
    });

    return () => {
      closedByUs = true;
      for (const { name, handler } of bound) es.removeEventListener(name, handler as EventListener);
      es.close();
      setStatus("closed");
    };
  }, [url, enabled, eventsKey]);
}
