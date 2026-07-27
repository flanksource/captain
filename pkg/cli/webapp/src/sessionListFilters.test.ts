import { describe, expect, it } from "vitest";
import {
  DEFAULT_SESSION_LIST_FILTERS,
  getSessionListSearchSnapshot,
  parseSessionListFilters,
  sessionActivityBounds,
  subscribeSessionListSearch,
  withSessionListFilters,
} from "./sessionListFilters";

describe("session list filters", () => {
  it("round-trips non-default filters while preserving project scope", () => {
    const filters = parseSessionListFilters(
      "?project=%2Frepos%2Fgavel&mode=all&source=codex&q=file+changes&from=2026-07-26&to=2026-07-27",
    );

    expect(filters).toEqual({
      mode: "all",
      source: "codex",
      query: "file changes",
      from: "2026-07-26",
      to: "2026-07-27",
    });
    expect(withSessionListFilters("/sessions?project=%2Frepos%2Fgavel", filters)).toBe(
      "/sessions?project=%2Frepos%2Fgavel&mode=all&source=codex&q=file+changes&from=2026-07-26&to=2026-07-27",
    );
  });

  it("omits defaults from a shareable session URL", () => {
    expect(
      withSessionListFilters(
        "/sessions?project=%2Frepos%2Fgavel&mode=all&q=stale",
        DEFAULT_SESSION_LIST_FILTERS,
      ),
    ).toBe("/sessions?project=%2Frepos%2Fgavel");
  });

  it("notifies when navigation changes only the query string", () => {
    const initial = window.location.href;
    const notified: string[] = [];
    const unsubscribe = subscribeSessionListSearch(() => {
      notified.push(getSessionListSearchSnapshot());
    });

    window.history.replaceState(null, "", "/sessions?mode=all");
    window.dispatchEvent(new PopStateEvent("popstate"));

    expect(notified).toEqual(["?mode=all"]);
    unsubscribe();
    window.history.replaceState(null, "", initial);
  });

  it("converts an inclusive local date range to timestamp bounds", () => {
    const bounds = sessionActivityBounds("2026-07-26", "2026-07-27");
    const from = new Date(bounds.from!);
    const before = new Date(bounds.before!);

    expect([
      from.getFullYear(),
      from.getMonth() + 1,
      from.getDate(),
      from.getHours(),
      from.getMinutes(),
    ]).toEqual([2026, 7, 26, 0, 0]);
    expect([
      before.getFullYear(),
      before.getMonth() + 1,
      before.getDate(),
      before.getHours(),
      before.getMinutes(),
    ]).toEqual([2026, 7, 28, 0, 0]);
  });

  it("passes date math through for the Go API to resolve", () => {
    expect(sessionActivityBounds("now-7d", "now")).toEqual({
      from: "now-7d",
      before: "now",
    });
  });

  it("rejects malformed and reversed dates", () => {
    expect(() => sessionActivityBounds("2026-02-30", "")).toThrow(
      'Invalid session date "2026-02-30".',
    );
    expect(() => sessionActivityBounds("2026-07-28", "2026-07-27")).toThrow(
      "Session date from must not be after to.",
    );
  });
});
