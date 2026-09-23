import { describe, expect, it } from "vitest";
import {
  adapterSchemaLocation,
  parseAdapterSchemaPath,
  selectAdapterSchema,
  type AdapterSchemaDocument,
} from "./adapterSchemas";

const DOCUMENTS: AdapterSchemaDocument[] = [
  {
    provider: "anthropic",
    mode: "api",
    title: "Anthropic API",
    description: "Anthropic Messages API options.",
    schema: { type: "object" },
  },
  {
    provider: "openai",
    mode: "api",
    title: "OpenAI API",
    description: "OpenAI Responses API options.",
    schema: { type: "object" },
  },
  {
    provider: "openai",
    mode: "cli",
    title: "OpenAI CLI",
    description: "Codex CLI options.",
    schema: { type: "object" },
  },
];

describe("adapter schema routes", () => {
  it("round trips a selected provider and mode through the URL", () => {
    const location = adapterSchemaLocation({ provider: "openai", mode: "cli" });

    expect(location).toBe("/adapter-schemas/openai/cli");
    expect(parseAdapterSchemaPath(location)).toEqual({ provider: "openai", mode: "cli" });
  });

  it("uses the first canonical schema when the route is incomplete or unknown", () => {
    expect(selectAdapterSchema(DOCUMENTS, {})).toBe(DOCUMENTS[0]);
    expect(selectAdapterSchema(DOCUMENTS, { provider: "unknown", mode: "api" })).toBe(DOCUMENTS[0]);
  });

  it("selects the exact provider and mode from a valid route", () => {
    expect(selectAdapterSchema(DOCUMENTS, { provider: "openai", mode: "cli" })).toBe(DOCUMENTS[2]);
  });
});
