import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
import {
  familiesFromRuntimeCatalog,
  type RuntimeCatalogFamily,
} from "@flanksource/clicky-ui/ai";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PromptRuntimeRows } from "./PromptRuntimeRows";
import {
  addRuntimeRow,
  validateRuntimeRows,
} from "./promptRuntimeRowsHelpers";

// A model whose capabilities the catalog does not describe, so the row falls
// back to the effort universe the prompt schema serves.
const UNDESCRIBED_MODEL: ChatModel = {
  id: "gpt-5.6-sol",
  label: "GPT-5.6 Sol",
  backends: ["codex-cli"],
  provider: "openai",
  reasoning: true,
};

// What the prompt schema serves once the user disables the cmux mode on
// /whoami: every provider×mode pair the registry knows, with the switched-off
// ones annotated rather than missing.
const SERVED_CATALOG: RuntimeCatalogFamily[] = [
  {
    family: "claude",
    provider: "anthropic",
    catalogPrefix: "anthropic",
    modes: [
      { mode: "api", backend: "anthropic" },
      { mode: "agent", backend: "claude-agent" },
      { mode: "cli", backend: "claude-cli" },
      {
        mode: "cmux",
        backend: "claude-cmux",
        disabled: true,
        disabledReason: "mode cmux",
      },
    ],
  },
  {
    family: "codex",
    provider: "openai",
    catalogPrefix: "openai",
    modes: [
      { mode: "cli", backend: "codex-cli" },
      {
        mode: "cmux",
        backend: "codex-cmux",
        disabled: true,
        disabledReason: "mode cmux",
      },
    ],
  },
];

const CLI_MODEL: ChatModel = {
  id: "claude-sonnet-5",
  label: "Claude Sonnet 5",
  backends: ["claude-cli"],
  provider: "anthropic",
  reasoning: true,
};

afterEach(cleanup);

describe("PromptRuntimeRows", () => {
  it("offers no cmux mode once the served catalog marks it disabled", () => {
    render(
      <PromptRuntimeRows
        rows={[{ backend: "claude-cli", model: CLI_MODEL.id }]}
        models={[CLI_MODEL]}
        families={familiesFromRuntimeCatalog(SERVED_CATALOG)}
        onChange={vi.fn()}
      />,
    );

    const modes = within(
      screen.getByRole("radiogroup", { name: "Runtime mode" }),
    ).getAllByRole("radio");
    expect(modes.map((mode) => mode.textContent)).toEqual([
      "API",
      "Agent",
      "CLI",
    ]);
  });

  // The registry has one Anthropic provider with four modes where the offline
  // default split it into "Claude" (agent/cli/cmux) and "Anthropic" (api).
  it("renders one family per provider rather than one per backend group", () => {
    render(
      <PromptRuntimeRows
        rows={[{ backend: "claude-cli", model: CLI_MODEL.id }]}
        models={[CLI_MODEL]}
        families={familiesFromRuntimeCatalog(SERVED_CATALOG)}
        onChange={vi.fn()}
      />,
    );

    const families = within(
      screen.getByRole("radiogroup", { name: "Provider family" }),
    ).getAllByRole("radio");
    expect(families.map((family) => family.textContent)).toEqual([
      "Claude",
      "Codex",
    ]);
  });

  it("offers only the effort tiers the server served", () => {
    render(
      <PromptRuntimeRows
        rows={[{ backend: "codex-cli", model: UNDESCRIBED_MODEL.id }]}
        models={[UNDESCRIBED_MODEL]}
        efforts={["low", "xhigh"]}
        onChange={vi.fn()}
      />,
    );

    fireEvent.focus(screen.getByRole("combobox", { name: "Reasoning effort" }));

    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "None",
      "Low",
      "Extra high",
    ]);
  });

  it("hides the effort control when the server served no tiers", () => {
    render(
      <PromptRuntimeRows
        rows={[{ backend: "codex-cli", model: UNDESCRIBED_MODEL.id }]}
        models={[UNDESCRIBED_MODEL]}
        onChange={vi.fn()}
      />,
    );

    expect(screen.queryByRole("combobox", { name: "Reasoning effort" })).toBeNull();
  });

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
