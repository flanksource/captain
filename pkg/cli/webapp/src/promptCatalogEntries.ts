import type { PromptCatalogEntry } from "@flanksource/clicky-ui/ai";
import type { PromptSummary } from "./promptData";

// promptCatalogEntry maps a captain prompt summary onto the shared catalog row.
// Captain prompts are files in discovered sources, so each one has exactly one
// layer — its source — and no override chain: the document that runs is the
// file itself (or the embedded example).
export function promptCatalogEntry(summary: PromptSummary): PromptCatalogEntry {
  const embedded = summary.sourceKind === "embedded";
  return {
    id: summary.id,
    title: summary.name,
    description: summary.description,
    configPath: summary.relPath,
    owner: embedded ? "embedded" : summary.source,
    source: embedded ? "builtin" : "file",
    path: summary.path,
    version: summary.version,
    updatedAt: summary.updatedAt,
    parseError: summary.parseError,
    variables: summary.variables?.map((variable) => variable.name),
    effective: {
      model: summary.model,
      backend: summary.mode,
      modelSource: summary.model ? "prompt default" : "runtime",
    },
    layers: [
      {
        origin: summary.source,
        path: summary.path,
        scope: summary.sourceId,
        editable: summary.writable,
        source: "file",
        filePath: summary.path,
      },
    ],
  };
}
