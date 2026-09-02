import { describe, expect, it } from "vitest";
import { promptCatalogEntry } from "./promptCatalogEntries";
import type { PromptSummary } from "./promptData";

const local: PromptSummary = {
  id: "bG9jYWw",
  name: "review",
  description: "Review a diff",
  sourceKind: "local",
  sourceId: "dir:.captain/prompts",
  source: ".captain/prompts",
  path: "/repo/.captain/prompts/review.prompt",
  relPath: "review.prompt",
  writable: true,
  model: "claude-sonnet-4-6",
  mode: "agent",
  variables: [{ name: "diff", required: true }, { name: "notes" }],
  version: "0c9f6601ed6035f3",
  updatedAt: "2026-08-27T09:00:00Z",
};

describe("promptCatalogEntry", () => {
  it("maps a writable local prompt to a single editable file layer", () => {
    expect(promptCatalogEntry(local)).toEqual({
      id: "bG9jYWw",
      title: "review",
      description: "Review a diff",
      configPath: "review.prompt",
      owner: ".captain/prompts",
      source: "file",
      path: "/repo/.captain/prompts/review.prompt",
      version: "0c9f6601ed6035f3",
      updatedAt: "2026-08-27T09:00:00Z",
      parseError: undefined,
      variables: ["diff", "notes"],
      effective: {
        model: "claude-sonnet-4-6",
        backend: "agent",
        modelSource: "prompt default",
      },
      layers: [
        {
          origin: ".captain/prompts",
          path: "/repo/.captain/prompts/review.prompt",
          scope: "dir:.captain/prompts",
          editable: true,
          source: "file",
          filePath: "/repo/.captain/prompts/review.prompt",
        },
      ],
    });
  });

  it("marks embedded examples as built-in, read-only, and runtime-modelled", () => {
    const entry = promptCatalogEntry({
      ...local,
      sourceKind: "embedded",
      source: "Embedded examples",
      writable: false,
      model: undefined,
      mode: undefined,
      parseError: "yaml: line 2",
    });
    expect(entry.source).toBe("builtin");
    expect(entry.owner).toBe("embedded");
    expect(entry.parseError).toBe("yaml: line 2");
    expect(entry.effective).toEqual({ model: undefined, backend: undefined, modelSource: "runtime" });
    expect(entry.layers[0]?.editable).toBe(false);
  });
});
