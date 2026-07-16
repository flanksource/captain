import { Button } from "@flanksource/clicky-ui/components";
import { Icon, UiAdd, UiTrash } from "@flanksource/clicky-ui/data";
import {
  RuntimeModePicker,
  SPEC_RUNTIME_FAMILIES,
  effortOptionsForModel,
  familyById,
  modelsForFamily,
  reconcileModelCapabilities,
  selectionForBackend,
  type AISpecRuntimeValue,
} from "@flanksource/clicky-ui/ai";
import {
  EffortSelector,
  ModelSelector,
  type ChatModel,
} from "@flanksource/clicky-ui/chat";

const REASONING_EFFORTS = ["low", "medium", "high", "xhigh"];

export function PromptRuntimeRows({
  rows,
  models,
  onChange,
}: {
  rows: AISpecRuntimeValue[];
  models: ChatModel[];
  onChange: (rows: AISpecRuntimeValue[]) => void;
}) {
  const error = validateRuntimeRows(rows);
  const update = (index: number, value: AISpecRuntimeValue) =>
    onChange(rows.map((row, rowIndex) => (rowIndex === index ? value : row)));

  return (
    <div className="space-y-density-3">
      {rows.map((row, index) => {
        const selection = selectionForBackend(
          SPEC_RUNTIME_FAMILIES,
          row.backend,
        );
        const family = familyById(SPEC_RUNTIME_FAMILIES, selection.family);
        const availableModels = modelsForFamily(models, family, row.backend);
        const selectedModel = models.find((model) => model.id === row.model);
        const efforts = effortOptionsForModel(selectedModel, REASONING_EFFORTS);
        return (
          <section
            key={index}
            className="space-y-density-2 rounded-md border border-border p-density-3"
            aria-label={`Runtime ${index + 1}`}
          >
            <div className="flex items-center justify-between gap-density-2">
              <span className="text-xs font-semibold uppercase text-muted-foreground">
                Runtime {index + 1}
              </span>
              {index > 0 && (
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove runtime ${index + 1}`}
                  onClick={() =>
                    onChange(rows.filter((_, rowIndex) => rowIndex !== index))
                  }
                >
                  <Icon icon={UiTrash} className="size-4" />
                </Button>
              )}
            </div>
            <RuntimeModePicker
              value={row}
              onChange={(value) => update(index, value)}
              models={models}
            />
            <div className="grid gap-density-2 sm:grid-cols-2">
              <label className="space-y-1 text-xs text-muted-foreground">
                <span>Model</span>
                <ModelSelector
                  models={availableModels}
                  value={row.model}
                  onChange={(model) =>
                    update(
                      index,
                      reconcileModelCapabilities(
                        { ...row, model },
                        models.find((item) => item.id === model),
                        REASONING_EFFORTS,
                      ),
                    )
                  }
                  size="md"
                  className="w-full"
                />
              </label>
              {efforts.length > 0 && (
                <label className="space-y-1 text-xs text-muted-foreground">
                  <span>Effort</span>
                  <EffortSelector
                    efforts={efforts}
                    value={row.effort}
                    onChange={(effort) => update(index, { ...row, effort })}
                    size="md"
                    className="w-full"
                  />
                </label>
              )}
            </div>
          </section>
        );
      })}
      <div className="flex items-center justify-between gap-density-2">
        <Button
          size="sm"
          variant="outline"
          aria-label="Add runtime"
          onClick={() => onChange(addRuntimeRow(rows))}
        >
          <Icon icon={UiAdd} className="size-4" />
          Add runtime
        </Button>
        {rows.length > 1 && error && (
          <span className="text-xs text-destructive">{error}</span>
        )}
      </div>
    </div>
  );
}

export function addRuntimeRow(rows: AISpecRuntimeValue[]) {
  return [
    ...rows,
    { ...(rows[0]?.backend ? { backend: rows[0].backend } : {}) },
  ];
}

export function validateRuntimeRows(rows: AISpecRuntimeValue[]) {
  if (rows.length < 2) return undefined;
  const seen = new Set<string>();
  for (let index = 0; index < rows.length; index++) {
    const row = rows[index]!;
    if (!row.backend?.trim()) return `Runtime ${index + 1} needs a backend`;
    if (!row.model?.trim()) return `Runtime ${index + 1} needs a model`;
    const selector = [row.backend, row.model, row.effort]
      .filter(Boolean)
      .join(":");
    if (seen.has(selector))
      return `Runtime ${index + 1} duplicates ${selector}`;
    seen.add(selector);
  }
}
