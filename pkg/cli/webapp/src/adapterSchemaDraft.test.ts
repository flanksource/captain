import { describe, expect, it } from "vitest";
import { createSchemaDraft, editSchemaDraft } from "./adapterSchemaDraft";

describe("schema preview drafts", () => {
  it("preserves malformed text and the last parsed value", () => {
    const initial = createSchemaDraft({ mode: "plan" });
    const broken = editSchemaDraft(initial, "json", "{");
    expect(broken.json).toBe("{");
    expect(broken.value).toEqual({ mode: "plan" });
    expect(broken.error).toBeTruthy();
    const fixed = editSchemaDraft(broken, "json", '{"mode":"default"}');
    expect(fixed.error).toBeUndefined();
    expect(fixed.yaml).toContain("mode: default");
  });

  it("round trips arrays, booleans, zero and null between YAML and JSON", () => {
    const draft = editSchemaDraft(createSchemaDraft({}), "yaml", "values: [false, 0, null]\n");
    expect(JSON.parse(draft.json)).toEqual({ values: [false, 0, null] });
    expect(draft.error).toBeUndefined();
  });

  it("rejects duplicate YAML keys while retaining the recoverable source", () => {
    const text = "mode: plan\nmode: default\n";
    const draft = editSchemaDraft(createSchemaDraft({}), "yaml", text);
    expect(draft.error).toBeTruthy();
    expect(draft.yaml).toBe(text);
    expect(draft.value).toEqual({});
  });
});
