import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchPermissionCatalog,
  operationRequestPath,
} from "./promptWorkbenchApi";

// Shapes as served by captain's OpenAPI document: the entity update is a
// collection-level PUT taking the id as the positional `args`; render/run are
// collection actions declaring an optional `id` query parameter; get/delete
// carry the id in the path.
const UPDATE = {
  path: "/api/v1/prompt",
  operation: {
    parameters: [{ name: "args", in: "query" }],
    "x-clicky": { idParam: "id" },
  },
};
const RENDER = {
  path: "/api/v1/prompt/render",
  operation: {
    parameters: [
      { name: "id", in: "query" },
      { name: "model", in: "query" },
    ],
    "x-clicky": { idParam: "id" },
  },
};
const GET = {
  path: "/api/v1/prompt/{id}",
  operation: { parameters: [{ name: "id", in: "path" }] },
};

const ID = "bG9jYWwAODgz+AHBoYXNlMC5wcm9tcHQ";

describe("operationRequestPath", () => {
  it("sends the update id as the positional args query parameter", () => {
    expect(operationRequestPath(UPDATE, { id: ID })).toBe(
      `/api/v1/prompt?args=${encodeURIComponent(ID)}`,
    );
  });

  it("sends a collection action id under the operation's id parameter", () => {
    expect(operationRequestPath(RENDER, { id: ID })).toBe(
      `/api/v1/prompt/render?id=${encodeURIComponent(ID)}`,
    );
  });

  it("omits the id for the scratch prompt so the action renders ephemerally", () => {
    expect(operationRequestPath(RENDER, { id: "" })).toBe("/api/v1/prompt/render");
  });

  it("fills a path placeholder instead of adding a query parameter", () => {
    expect(operationRequestPath(GET, { id: ID })).toBe(
      `/api/v1/prompt/${encodeURIComponent(ID)}`,
    );
  });

  it("drops an empty placeholder segment", () => {
    expect(operationRequestPath(GET, { id: "" })).toBe("/api/v1/prompt");
  });
});

describe("fetchPermissionCatalog", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("identifies the runtime by provider and mode", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ tools: [] }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await fetchPermissionCatalog({ provider: "openai", mode: "cli" });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/captain/ai/permissions/catalog?provider=openai&mode=cli",
      { headers: { Accept: "application/json" } },
    );
  });
});
