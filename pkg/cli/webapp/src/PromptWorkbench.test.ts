import { describe, expect, it } from "vitest";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
import {
  mergePromptModelCatalogs,
  promptOptions,
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

describe("mergePromptModelCatalogs", () => {
  const model = (
    id: string,
    backend: string,
    configured: boolean,
    state: "available" | "disabled" = "available",
  ): ChatModel => ({
    id,
    provider: backend.startsWith("claude") ? "claude-agent" : "anthropic",
    label: `${backend} ${id}`,
    runtime: { model: id, backend },
    reasoning: true,
    configured,
    availability: { state },
  });

  it("keeps prompt selections and adds only distinct unavailable status rows", () => {
    const promptModels = [
      model("claude-opus-5", "claude-agent", true),
      model("claude-opus-5", "claude-cli", true),
    ];
    const result = mergePromptModelCatalogs(promptModels, [
      model("claude-opus-5", "claude-agent", true),
      model("claude-sonnet-5", "claude-agent", false, "disabled"),
      model("claude-opus-5", "anthropic", false, "disabled"),
    ]);

    expect(result).toEqual([
      ...promptModels,
      model("claude-sonnet-5", "claude-agent", false, "disabled"),
      model("claude-opus-5", "anthropic", false, "disabled"),
    ]);
  });

  it("treats an exact prompt model as authoritative over stale availability", () => {
    const promptModel = model("claude-opus-5", "claude-agent", true);

    expect(
      mergePromptModelCatalogs(
        [promptModel],
        [model("claude-opus-5", "claude-agent", false, "disabled")],
      ),
    ).toEqual([promptModel]);
  });

  it("adds a configured-false model even when no availability detail was served", () => {
    const unavailable = {
      ...model("claude-sonnet-5", "claude-agent", false),
      availability: undefined,
    };

    expect(mergePromptModelCatalogs([], [unavailable])).toEqual([unavailable]);
  });
});
