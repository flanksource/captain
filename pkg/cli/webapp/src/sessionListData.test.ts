import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchSessionListPage } from "./sessionListData";

const executeCommand = vi.hoisted(() => vi.fn());

vi.mock("./api", () => ({
  apiClient: { executeCommand },
}));

describe("session list requests", () => {
  beforeEach(() => {
    executeCommand.mockReset();
    executeCommand.mockResolvedValue({
      success: true,
      parsed: { sessions: [], total: 0, source: "all", scope: "project" },
      responseHeaders: {},
    });
  });

  it("uses the live endpoint with activity bounds", async () => {
    await fetchSessionListPage({
      mode: "live",
      source: "all",
      project: "/repos/gavel",
      query: "",
      from: "2026-07-26T21:00:00.000Z",
      before: "2026-07-28T21:00:00.000Z",
    });

    expect(executeCommand).toHaveBeenCalledWith(
      "/api/captain/sessions/live",
      "GET",
      expect.objectContaining({
        project: "/repos/gavel",
        from: "2026-07-26T21:00:00.000Z",
        before: "2026-07-28T21:00:00.000Z",
      }),
      { Accept: "application/json" },
    );
  });

  it("uses the historical endpoint without empty activity bounds", async () => {
    await fetchSessionListPage({
      mode: "all",
      source: "codex",
      project: "/repos/gavel",
      query: "file changes",
    });

    expect(executeCommand).toHaveBeenCalledWith(
      "/api/v1/sessions",
      "GET",
      {
        source: "codex",
        project: "/repos/gavel",
        q: "file changes",
        limit: "100",
      },
      { Accept: "application/json" },
    );
  });
});
