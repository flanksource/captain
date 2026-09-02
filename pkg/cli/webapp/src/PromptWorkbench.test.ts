import { describe, expect, it } from "vitest";
import {
  promptOptions,
  resolveProvider,
} from "./promptWorkbenchHelpers";

function prompt(
  name: string,
  sourceKind: string,
  extra: { description?: string; relPath?: string } = {},
) {
  return {
    id: `${sourceKind}/${name}`,
    name,
    sourceKind,
    sourceId: sourceKind,
    source: sourceKind,
    path: `/prompts/${name}.prompt`,
    relPath: extra.relPath ?? `${name}.prompt`,
    writable: sourceKind === "local",
    description: extra.description,
  };
}

describe("promptOptions", () => {
  it("emits embedded before local so each group header renders once", () => {
    const options = promptOptions([
      prompt("zeta", "local"),
      prompt("alpha", "embedded"),
      prompt("beta", "local"),
      prompt("omega", "embedded"),
    ]);

    expect(options.map((option) => [option.group, option.label])).toEqual([
      ["embedded", "alpha"],
      ["embedded", "omega"],
      ["local", "beta"],
      ["local", "zeta"],
    ]);
  });

  it("sorts unknown source kinds after the known groups", () => {
    const options = promptOptions([
      prompt("scratch", "ephemeral"),
      prompt("alpha", "local"),
      prompt("beta", "embedded"),
    ]);

    expect(options.map((option) => option.group)).toEqual([
      "embedded",
      "local",
      "ephemeral",
    ]);
  });

  it("keeps a selected prompt that the server filter excluded, with its name", () => {
    const selected = prompt("triage", "local");
    const options = promptOptions([prompt("alpha", "embedded")], selected);

    expect(options).toContainEqual(
      expect.objectContaining({
        value: selected.id,
        label: "triage",
        group: "local",
      }),
    );
  });

  it("does not duplicate a selected prompt already present in the list", () => {
    const selected = prompt("alpha", "embedded");
    const options = promptOptions(
      [selected, prompt("beta", "local")],
      selected,
    );

    expect(
      options.filter((option) => option.value === selected.id),
    ).toHaveLength(1);
  });

  it("titles each option with its description, falling back to the path", () => {
    const options = promptOptions([
      prompt("alpha", "embedded", { description: "Reviews Go diffs" }),
      prompt("beta", "local", { relPath: "nested/beta.prompt" }),
    ]);

    expect(options.map((option) => option.title)).toEqual([
      "Reviews Go diffs",
      "nested/beta.prompt",
    ]);
  });
});

describe("resolveProvider", () => {
  it("returns the provider that owns the selected model", () => {
    expect(
      resolveProvider(
        [
          {
            id: "gpt-5",
            provider: "openai",
            label: "GPT-5",
            runtime: { model: "gpt-5" },
            reasoning: true,
          },
        ],
        "gpt-5",
      ),
    ).toBe("openai");
  });
});
