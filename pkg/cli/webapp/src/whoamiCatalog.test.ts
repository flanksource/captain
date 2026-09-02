import { afterEach, describe, expect, it, vi } from "vitest";
import {
  WHOAMI_CATALOG_URL,
  fetchWhoamiCatalog,
} from "./whoamiCatalog";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fetchWhoamiCatalog", () => {
  it("loads models and runtimes from the consolidated whoami request", async () => {
    const result = {
      adapters: [],
      defaultProvider: "openai",
      providerDefaults: {},
      disabled: {},
      axes: { modes: [], providers: [], efforts: [] },
      models: [{
        id: "gpt-5.6-sol",
        provider: "openai",
        label: "GPT-5.6 Sol",
        runtime: { model: "gpt-5.6-sol", mode: "cli" },
        reasoning: true,
      }],
      runtimes: [{
        family: "codex",
        provider: "openai",
        catalogPrefix: "openai",
        modes: [{ mode: "cli", schema: { properties: {} } }],
      }],
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(result), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(fetchWhoamiCatalog()).resolves.toEqual(result);
    expect(fetchMock).toHaveBeenCalledWith(
      WHOAMI_CATALOG_URL,
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  it("rejects a response that omits picker catalogs", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ adapters: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));

    await expect(fetchWhoamiCatalog()).rejects.toThrow("models and runtimes");
  });
});
