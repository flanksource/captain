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
  type SpecRuntimeFamily,
} from "@flanksource/clicky-ui/ai";
import {
  EffortSelector,
  ModelSelector,
  type ChatModel,
} from "@flanksource/clicky-ui/chat";
import {
  addRuntimeRow,
  validateRuntimeRows,
} from "./promptRuntimeRowsHelpers";
import { backendForRow } from "./promptWorkbenchHelpers";

export function PromptRuntimeRows({
  rows,
  models,
  families = SPEC_RUNTIME_FAMILIES,
  efforts: effortUniverse = [],
  onChange,
}: {
  rows: AISpecRuntimeValue[];
  models: ChatModel[];
  /**
   * The runtime catalog the server projected from its model registry, already
   * stripped of the backends the user disabled. Falling back to the offline
   * default would re-offer them, so the schema query is the only source.
   */
  families?: SpecRuntimeFamily[];
  /**
   * Fallback tiers for a model whose catalog entry carries no supportedEfforts.
   * The prompt schema serves this and has already dropped disabled tiers, so an
   * empty list means the server said nothing — not that every tier is off.
   */
  efforts?: string[];
  onChange: (rows: AISpecRuntimeValue[]) => void;
}) {
  const error = validateRuntimeRows(rows);
  const update = (index: number, value: AISpecRuntimeValue) =>
    onChange(rows.map((row, rowIndex) => (rowIndex === index ? value : row)));

  return (
    <div className="space-y-density-3">
      {rows.map((row, index) => {
        const backend = backendForRow(row, models);
        const selection = selectionForBackend(families, backend);
        const family = familyById(families, selection.family);
        const availableModels = modelsForFamily(models, family, backend);
        const selectedModel = models.find((model) => model.id === row.model);
        const efforts = effortOptionsForModel(selectedModel, effortUniverse);
        return (
          <section
            key={row.id ?? `${row.backend ?? ""}:${row.model ?? ""}:${row.effort ?? ""}`}
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
              families={families}
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
                        effortUniverse,
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
