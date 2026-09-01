import type { ComboboxOption } from "@flanksource/clicky-ui/components";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
import type { PromptSummary } from "./promptData";

const SOURCE_GROUP_ORDER = ["embedded", "local"];

/**
 * Resolve the adapter id ("claude-agent") for an authored runtime selection.
 *
 * A spec authors `{model, backend}` where `backend` is the runtime mode, and the
 * catalog is deliberate about not publishing adapter ids on runtime modes. The
 * model catalog does carry the resolved adapter per model×mode row, so joining
 * on it is how a client turns an authored selection into an adapter — the two
 * values are not interchangeable and comparing them directly always misses.
 */
export function resolveAdapter(
  models: ChatModel[],
  model?: string,
  mode?: string,
): string | undefined {
  if (!model) return undefined;
  const matches = models.filter(
    (candidate) =>
      candidate.runtime?.model === model ||
      candidate.runtime?.id === model ||
      candidate.id === model,
  );
  const exact = mode
    ? matches.find((candidate) => candidate.runtime?.mode === mode)
    : undefined;
  return (exact ?? matches[0])?.runtime?.backend;
}

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

export function mergePromptModelCatalogs(
  promptModels: ChatModel[],
  availabilityModels: ChatModel[],
): ChatModel[] {
  const merged = [...promptModels];
  const identities = new Set(promptModels.map(promptModelIdentity));
  for (const model of availabilityModels) {
    if (
      model.configured !== false &&
      (!model.availability || model.availability.state === "available")
    ) {
      continue;
    }
    const identity = promptModelIdentity(model);
    if (identities.has(identity)) continue;
    identities.add(identity);
    merged.push(model);
  }
  return merged;
}

function promptModelIdentity(model: ChatModel): string {
  const backend =
    model.runtime?.backend ?? model.backends?.join(",") ?? model.provider;
  return `${backend}\u0000${model.runtime?.model ?? model.id}`;
}

function sourceRank(sourceKind: string) {
  const rank = SOURCE_GROUP_ORDER.indexOf(sourceKind);
  return rank === -1 ? SOURCE_GROUP_ORDER.length : rank;
}
