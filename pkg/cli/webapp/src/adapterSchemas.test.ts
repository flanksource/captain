import { afterEach, describe, expect, it, vi } from "vitest";
import {
  adapterSchemaLocation,
  fetchNativeResourceSchemas,
  NATIVE_RESOURCE_CATALOG_URL,
  parseAdapterSchemaPath,
  selectAdapterSchema,
  validateAdapterSchema,
  type AdapterSchemaDocument,
} from "./adapterSchemas";

const DOCUMENTS: AdapterSchemaDocument[] = [
  {
    id: "anthropic/api", kind: "execution",
    provider: "anthropic",
    mode: "api",
    title: "Anthropic API",
    description: "Anthropic Messages API options.",
    schema: { type: "object" },
  },
  {
    id: "openai/api", kind: "execution",
    provider: "openai",
    mode: "api",
    title: "OpenAI API",
    description: "OpenAI Responses API options.",
    schema: { type: "object" },
  },
  {
    id: "openai/cli", kind: "execution",
    provider: "openai",
    mode: "cli",
    title: "OpenAI CLI",
    description: "Codex CLI options.",
    schema: { type: "object" },
  },
];

const NATIVE: AdapterSchemaDocument = {
  id: "anthropic/sandbox", kind: "resource", provider: "anthropic",
  title: "Anthropic Sandbox", description: "Native anthropic sandbox configuration.",
  schema: { type: "object", properties: { enabled: { type: "boolean" } } },
};

afterEach(() => vi.unstubAllGlobals());

describe("published native resource catalog", () => {
  it("loads native resources from the schemas branch and marks them as published", async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => [NATIVE] });
    vi.stubGlobal("fetch", fetch);

    const documents = await fetchNativeResourceSchemas();

    expect(fetch).toHaveBeenCalledWith(NATIVE_RESOURCE_CATALOG_URL, expect.anything());
    expect(NATIVE_RESOURCE_CATALOG_URL).toBe("https://raw.githubusercontent.com/flanksource/captain/schemas/adapters/native-resources.json");
    expect(documents).toEqual([{ ...NATIVE, published: true }]);
  });

  it("fails naming the published location when the catalog cannot be fetched", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 404, text: async () => "404: Not Found" }));

    await expect(fetchNativeResourceSchemas()).rejects.toThrow(`${NATIVE_RESOURCE_CATALOG_URL} returned 404: 404: Not Found`);
  });

  it("sends a published document's schema for validation and looks embedded documents up by id", async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ valid: true, errors: [] }) });
    vi.stubGlobal("fetch", fetch);
    const signal = new AbortController().signal;

    await validateAdapterSchema({ ...NATIVE, published: true }, { enabled: true }, signal);
    await validateAdapterSchema(DOCUMENTS[0]!, { model: "example" }, signal);

    const bodies = fetch.mock.calls.map(([, init]) => JSON.parse((init as RequestInit).body as string));
    expect(bodies).toEqual([
      { id: "anthropic/sandbox", schema: NATIVE.schema, value: { enabled: true } },
      { id: "anthropic/api", value: { model: "example" } },
    ]);
  });
});

describe("adapter schema routes", () => {
  it("round trips a selected provider and mode through the URL", () => {
    const location = adapterSchemaLocation({ provider: "openai", mode: "cli" });

    expect(location).toBe("/adapter-schemas/openai/cli");
    expect(parseAdapterSchemaPath(location)).toEqual({ provider: "openai", mode: "cli" });
  });

  it("uses the canonical schema only at the catalog root and refuses unknown routes", () => {
    expect(selectAdapterSchema(DOCUMENTS, {})).toBe(DOCUMENTS[0]);
    expect(selectAdapterSchema(DOCUMENTS, { provider: "unknown", mode: "api" })).toBeUndefined();
  });

  it("selects the exact provider and mode from a valid route", () => {
    expect(selectAdapterSchema(DOCUMENTS, { provider: "openai", mode: "cli" })).toBe(DOCUMENTS[2]);
  });
});
