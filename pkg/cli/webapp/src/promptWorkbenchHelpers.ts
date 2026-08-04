import type { ComboboxOption } from "@flanksource/clicky-ui/components";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
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
