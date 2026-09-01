import { useMemo } from "react";
import {
  Button,
  Combobox,
  SegmentedControl,
} from "@flanksource/clicky-ui/components";
import { Icon, UiAdd, UiRefresh } from "@flanksource/clicky-ui/data";
import type { PromptSourceFilter, PromptSummary } from "./promptData";
import { promptOptions } from "./promptWorkbenchHelpers";
import { errorMessage } from "./promptWorkbenchApi";

const SOURCE_OPTIONS = [
  { id: "all", label: "All" },
  { id: "embedded", label: "Embedded" },
  { id: "local", label: "Local" },
] satisfies Array<{ id: PromptSourceFilter; label: string }>;

export function PromptSidebar({
  source,
  onSourceChange,
  onQueryChange,
  prompts,
  selected,
  loading,
  error,
  onSelect,
  onRefresh,
  onCreate,
}: {
  source: PromptSourceFilter;
  onSourceChange: (source: PromptSourceFilter) => void;
  onQueryChange: (query: string) => void;
  prompts: PromptSummary[];
  selected?: PromptSummary;
  loading: boolean;
  error: unknown;
  onSelect: (id: string) => void;
  onRefresh: () => void;
  onCreate: () => void;
}) {
  const options = useMemo(
    () => promptOptions(prompts, selected),
    [prompts, selected],
  );
  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="shrink-0 space-y-density-2 border-b border-border p-density-3">
        <div className="flex items-center justify-between gap-density-2">
          <div className="text-sm font-semibold">Prompts</div>
          <div className="flex items-center gap-density-1">
            <Button
              size="sm"
              variant="ghost"
              onClick={onRefresh}
              title="Refresh prompts"
            >
              <Icon icon={UiRefresh} className="size-4" />
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={onCreate}
              title="Create prompt"
            >
              <Icon icon={UiAdd} className="size-4" />
            </Button>
          </div>
        </div>
        <Combobox
          value={selected?.id ?? ""}
          onChange={onSelect}
          options={options}
          onSearch={onQueryChange}
          loading={loading}
          allowCustomValue={false}
          placeholder="Select a prompt"
          ariaLabel="Prompt"
          size="sm"
          className="w-full"
        />
        <SegmentedControl
          value={source}
          options={SOURCE_OPTIONS}
          onChange={onSourceChange}
          size="sm"
          aria-label="Prompt source"
          className="w-full"
        />
        <div className="text-xs text-muted-foreground">
          {loading ? "Loading..." : `${prompts.length} prompts`}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {error ? (
          <div className="p-density-3 text-sm text-destructive">
            {errorMessage(error)}
          </div>
        ) : !selected ? (
          <div className="p-density-3 text-sm text-muted-foreground">
            No prompt selected.
          </div>
        ) : (
          <div className="space-y-density-2 p-density-3">
            <div className="flex min-w-0 items-center justify-between gap-density-2">
              <span className="min-w-0 truncate text-sm font-medium">
                {selected.name}
              </span>
              <span className="shrink-0 rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
                {selected.sourceKind}
              </span>
            </div>
            <div className="truncate text-xs text-muted-foreground">
              {selected.model || selected.backend || "no model"}
              {selected.variables?.length
                ? ` - ${selected.variables.length} vars`
                : ""}
            </div>
            <div className="truncate text-xs text-muted-foreground">
              {selected.relPath}
            </div>
            {selected.description && (
              <div className="text-xs text-muted-foreground">
                {selected.description}
              </div>
            )}
            {selected.parseError && (
              <div className="text-xs text-destructive">
                {selected.parseError}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

export function PromptHeader({
  prompt,
  loading,
  ready,
  dirty,
}: {
  prompt?: PromptSummary;
  loading: boolean;
  ready: boolean;
  dirty: boolean;
}) {
  if (!ready) {
    return (
      <div>
        <div className="text-sm font-semibold">Prompt Workbench</div>
        <div className="text-xs text-muted-foreground">
          Loading prompt operations...
        </div>
      </div>
    );
  }
  if (loading && !prompt) {
    return (
      <div className="text-sm text-muted-foreground">Loading prompt...</div>
    );
  }
  if (!prompt) {
    return (
      <div>
        <div className="text-sm font-semibold">Prompt Workbench</div>
        <div className="text-xs text-muted-foreground">
          Select or create a prompt.
        </div>
      </div>
    );
  }
  return (
    <div className="min-w-0">
      <div className="flex min-w-0 flex-wrap items-center gap-density-2">
        <div className="truncate text-sm font-semibold">{prompt.name}</div>
        <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
          {prompt.sourceKind}
        </span>
        {prompt.writable && (
          <span className="rounded border border-border px-1.5 py-0.5 text-[11px] uppercase text-muted-foreground">
            editable
          </span>
        )}
        {dirty && (
          <span
            className="rounded border border-amber-500/50 px-1.5 py-0.5 text-[11px] uppercase text-amber-600 dark:text-amber-400"
            title="This prompt has unsaved changes"
          >
            ● unsaved
          </span>
        )}
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-density-3 gap-y-1 text-xs text-muted-foreground">
        {prompt.model && <span>{prompt.model}</span>}
        {prompt.backend && <span>{prompt.backend}</span>}
        <span className="max-w-full truncate">{prompt.path}</span>
      </div>
    </div>
  );
}
