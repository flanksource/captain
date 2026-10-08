import { afterEach, describe, expect, it, vi } from "vitest";
import { sizeSessionTokens } from "./sessionTokens";

afterEach(() => vi.unstubAllGlobals());

describe("canonical session token sizing", () => {
  it("posts only canonical row IDs and revision and forwards cancellation", async () => {
    const result = { sessionId: "session-example", revision: 3, rows: [] };
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(result), { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    const controller = new AbortController();
    expect(await sizeSessionTokens({ sessionId: "session-example", revision: 3, rowIds: ["user-0"], method: "provider" }, controller.signal)).toEqual(result);
    expect(fetch).toHaveBeenCalledWith("/api/captain/sessions/session-example/tokens", expect.objectContaining({ method: "POST", body: JSON.stringify({ method: "provider", rowIds: ["user-0"], revision: 3 }), signal: controller.signal }));
  });

  it("surfaces backend errors", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: "Session revision changed" }), { status: 409 })));
    await expect(sizeSessionTokens({ sessionId: "session-example", rowIds: ["user-0"], method: "estimate" }, new AbortController().signal)).rejects.toThrow("Session revision changed");
  });
});
