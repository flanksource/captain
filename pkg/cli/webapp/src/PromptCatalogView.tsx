import { useMemo } from "react";
import { PromptCatalogTable } from "@flanksource/clicky-ui/ai";
import { promptCatalogEntry } from "./promptCatalogEntries";
import type { PromptSummary } from "./promptData";
import { errorMessage } from "./promptWorkbenchApi";

// PromptCatalogView is the workbench's landing view: the shared prompts table
// over every discovered prompt, so a prompt is found by source, model, or
// variable rather than scrolled to in the sidebar.
export function PromptCatalogView({
  prompts,
  loading,
  error,
  onSelect,
}: {
  prompts: PromptSummary[];
  loading: boolean;
  error: unknown;
  onSelect: (id: string) => void;
}) {
  const entries = useMemo(() => prompts.map(promptCatalogEntry), [prompts]);
  return (
    <div className="flex h-full min-h-0 flex-col p-density-3">
      <PromptCatalogTable
        className="min-h-0 flex-1"
        entries={entries}
        loading={loading}
        error={error ? errorMessage(error) : null}
        showOwner
        onSelect={(entry) => onSelect(entry.id)}
        emptyMessage="No prompts found. Create one, or add a prompt directory."
      />
    </div>
  );
}
