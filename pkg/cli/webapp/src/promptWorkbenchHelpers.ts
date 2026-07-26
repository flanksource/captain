import type { AISpecRuntimeValue } from "@flanksource/clicky-ui/ai";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
import type { ComboboxOption } from "@flanksource/clicky-ui/components";
import type { PromptSummary } from "./promptData";

const SOURCE_GROUP_ORDER = ["embedded", "local"];

export function promptOptions(
  prompts: PromptSummary[],
  selected?: PromptSummary,
): ComboboxOption[] {
  const all =
    selected && !prompts.some((prompt) => prompt.id === selected.id)
      ? [...prompts, selected]
      : prompts;
  return [...all]
    .sort(
      (a, b) =>
        sourceRank(a.sourceKind) - sourceRank(b.sourceKind) ||
        a.name.localeCompare(b.name),
    )
    .map((prompt) => ({
      value: prompt.id,
      label: prompt.name,
      group: prompt.sourceKind,
      title: prompt.description || prompt.relPath,
    }));
}

function sourceRank(sourceKind: string) {
  const rank = SOURCE_GROUP_ORDER.indexOf(sourceKind);
  return rank === -1 ? SOURCE_GROUP_ORDER.length : rank;
}

export function runtimeModelsPayload(
  runtimes: AISpecRuntimeValue[],
  models: ChatModel[],
) {
  return runtimes.map((runtime) => {
    const selected = normalizeRuntimeModel(
      runtime.model || "",
      models,
      runtime.backend,
    );
    return {
      model: selected.model,
      backend: selected.backend || runtime.backend,
      ...(runtime.id ? { id: runtime.id } : {}),
      ...(runtime.effort ? { effort: runtime.effort } : {}),
      ...(runtime.temperature !== undefined
        ? { temperature: runtime.temperature }
        : {}),
      ...(runtime.noCache ? { noCache: true } : {}),
      ...(runtime.fallbacks?.length ? { fallbacks: runtime.fallbacks } : {}),
    };
  });
}

export function runtimeRowsFromPrompt(
  prompt: PromptSummary,
): AISpecRuntimeValue[] {
  if (prompt.runtimes?.length) {
    return prompt.runtimes.map(
      ({ model, id, backend, temperature, effort, noCache, fallbacks }) => ({
        model,
        id,
        backend,
        temperature,
        effort,
        noCache,
        fallbacks,
      }),
    );
  }
  const backend =
    prompt.backend?.trim() || inferBackendFromModel(prompt.model || "");
  return [
    {
      ...(backend ? { backend } : {}),
      ...(prompt.model?.trim() ? { model: prompt.model.trim() } : {}),
    },
  ];
}

export function normalizeRuntimeModel(
  model: string,
  models: ChatModel[],
  backend?: string,
) {
  const id = model.trim();
  if (!id) return { model: "", backend: "" };
  const selected =
    models.find(
      (entry) => entry.id === id && modelSupportsBackend(entry, backend),
    ) ?? models.find((entry) => entry.id === id);
  if (!selected) return { model: id, backend: "" };
  const selectedBackend = backendForModel(selected, backend);
  if (
    ["anthropic", "openai", "googleai", "deepseek"].includes(selected.provider)
  ) {
    return {
      model: id.includes("/") ? id.slice(id.indexOf("/") + 1) : id,
      backend: selectedBackend,
    };
  }
  if (
    ["codex-cli", "codex-agent"].includes(selected.provider) &&
    id.startsWith("codex-")
  ) {
    return { model: id.slice("codex-".length), backend: selectedBackend };
  }
  return { model: id, backend: selectedBackend };
}

function backendForModel(model: ChatModel, preferredBackend?: string) {
  if (preferredBackend && modelSupportsBackend(model, preferredBackend))
    return preferredBackend;
  return model.backends?.find((candidate) => candidate.trim()) ??
    providerToBackend(model.provider);
}

function modelSupportsBackend(model: ChatModel, backend?: string) {
  if (!backend) return true;
  const backends = model.backends?.filter(Boolean);
  return !backends?.length || backends.includes(backend);
}

function inferBackendFromModel(model: string) {
  const value = model.trim().toLowerCase();
  if (!value) return "";
  if (value.startsWith("anthropic/")) return "anthropic";
  if (value.startsWith("claude-agent-")) return "claude-agent";
  if (value.startsWith("claude-code-")) return "claude-cli";
  if (value.startsWith("codex-agent-") || value.startsWith("codex"))
    return "codex-agent";
  if (value.startsWith("gemini-cli-")) return "gemini-cli";
  if (value.startsWith("claude-")) return "anthropic";
  if (
    value.startsWith("openai/") ||
    value.startsWith("gpt-") ||
    /^(o1|o3|o4)/.test(value)
  )
    return "openai";
  if (
    value.startsWith("googleai/") ||
    value.startsWith("gemini-") ||
    value.startsWith("models/gemini-")
  )
    return "gemini";
  if (value.startsWith("deepseek/") || value.startsWith("deepseek-"))
    return "deepseek";
  return "";
}

function providerToBackend(provider: string) {
  if (provider === "googleai") return "gemini";
  if (
    [
      "anthropic",
      "openai",
      "deepseek",
      "claude-agent",
      "claude-cli",
      "codex-cli",
      "codex-agent",
      "gemini-cli",
    ].includes(provider)
  )
    return provider;
  return "";
}
