import { describe, expect, it } from "vitest";
import { addRuntimeRow, validateRuntimeRows } from "./PromptRuntimeRows";

describe("PromptRuntimeRows", () => {
  it("adds a runtime with the primary backend and an intentionally blank model", () => {
    expect(
      addRuntimeRow([{ backend: "codex-cmux", model: "gpt-5.6-sol" }]),
    ).toEqual([
      { backend: "codex-cmux", model: "gpt-5.6-sol" },
      { backend: "codex-cmux" },
    ]);
  });

  it("rejects incomplete and duplicate comparison rows", () => {
    expect(
      validateRuntimeRows([
        { backend: "codex-cmux", model: "gpt-5.6-sol" },
        { backend: "codex-cmux" },
      ]),
    ).toEqual("Runtime 2 needs a model");
    expect(
      validateRuntimeRows([
        { backend: "codex-cmux", model: "gpt-5.6-sol", effort: "high" },
        { backend: "codex-cmux", model: "gpt-5.6-sol", effort: "high" },
      ]),
    ).toEqual("Runtime 2 duplicates codex-cmux:gpt-5.6-sol:high");
  });
});
