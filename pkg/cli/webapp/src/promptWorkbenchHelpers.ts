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
  // A prompt that declares only a model leaves the backend blank rather than
  // guessing from the id's prefix — that ladder could not name a cmux backend at
  // all. `backendForRow` fills it in from the served catalog at render time, and
  // `normalizeRuntimeModel` resolves the one that is actually submitted.
  const backend = prompt.backend?.trim() ?? "";
  const model = prompt.model?.trim() ?? "";
  return [
    {
      ...(backend ? { backend } : {}),
      ...(model ? { model } : {}),
    },
  ];
}

// The backend a runtime row runs on: its own when it declares one, else the
// first backend the served catalog lists for its model. Empty when neither
// answers — a picker then falls back to its first family rather than to a guess.
export function backendForRow(
  row: { backend?: string | undefined; model?: string | undefined },
  models: ChatModel[],
): string {
  const declared = row.backend?.trim();
  if (declared) return declared;
  const model = models.find((entry) => entry.id === row.model);
  return model?.backends?.find((backend) => backend.trim()) ?? "";
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
  return { model: bareModelId(id), backend: backendForModel(selected, backend) };
}

// API catalog ids are namespaced ("anthropic/claude-sonnet-5") for storage
// stability; a spec carries the bare id the provider itself answers to. CLI and
// agent ids are already exact, so they pass through untouched.
function bareModelId(id: string) {
  const slash = id.indexOf("/");
  return slash < 0 ? id : id.slice(slash + 1);
}

// The row's own backend when the model runs there, else the first backend the
// served catalog lists for it. Never guessed from the id: an empty answer lets
// the caller keep whatever the row already had.
function backendForModel(model: ChatModel, preferredBackend?: string) {
  if (preferredBackend && modelSupportsBackend(model, preferredBackend))
    return preferredBackend;
  return model.backends?.find((candidate) => candidate.trim()) ?? "";
}

function modelSupportsBackend(model: ChatModel, backend?: string) {
  if (!backend) return true;
  const backends = model.backends?.filter(Boolean);
  return !backends?.length || backends.includes(backend);
}
