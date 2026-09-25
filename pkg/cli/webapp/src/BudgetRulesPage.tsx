import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, InputField, Modal, Select } from "@flanksource/clicky-ui/components";
import { Icon, UiAdd, UiEdit, UiRefresh, UiTrash } from "@flanksource/clicky-ui/data";
import {
  WINDOW_PRESETS,
  createBudgetRule,
  deleteBudgetRule,
  draftFromRule,
  draftToWrite,
  emptyDraft,
  fetchBudgetRules,
  formatUSD,
  updateBudgetRule,
  windowLabel,
  type BudgetRule,
  type BudgetRuleDraft,
} from "./budgetRulesApi";
import { StateMessage } from "./StateMessage";

const BUDGET_RULES_KEY = ["budget-rules"] as const;
const CUSTOM_WINDOW = "__custom__";

type Editing = { mode: "create" } | { mode: "edit"; rule: BudgetRule };

export function BudgetRulesPage() {
  const queryClient = useQueryClient();
  const rules = useQuery({ queryKey: BUDGET_RULES_KEY, queryFn: fetchBudgetRules });
  const [editing, setEditing] = useState<Editing | undefined>();
  const [deleting, setDeleting] = useState<BudgetRule | undefined>();
  const refresh = () => void queryClient.invalidateQueries({ queryKey: BUDGET_RULES_KEY });

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex w-full max-w-[110rem] flex-col gap-density-6 p-density-4 md:p-density-6">
        <header className="flex flex-wrap items-start justify-between gap-density-3">
          <div className="max-w-4xl space-y-density-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-primary">
              Captain · /budgets
            </p>
            <h1 className="text-2xl font-semibold tracking-tight">Budget rules</h1>
            <p className="text-sm text-muted-foreground">
              A budget rule matches chat requests by their dimensions and model, optionally splits
              them into groups, and sets a USD amount for a time window. Captain records which
              rules each model call counts toward. Rules only observe spend for now; they do not
              block requests.
            </p>
          </div>
          <div className="flex gap-density-2">
            <Button size="sm" variant="outline" disabled={rules.isFetching} onClick={() => void rules.refetch()}>
              <Icon icon={UiRefresh} className={rules.isFetching ? "size-4 animate-spin" : "size-4"} />
              Refresh
            </Button>
            <Button size="sm" onClick={() => setEditing({ mode: "create" })}>
              <Icon icon={UiAdd} className="size-4" />
              New rule
            </Button>
          </div>
        </header>

        {rules.isLoading ? (
          <StateMessage>Loading budget rules...</StateMessage>
        ) : rules.error ? (
          <StateMessage tone="error">{errorText(rules.error)}</StateMessage>
        ) : (rules.data?.length ?? 0) === 0 ? (
          <StateMessage>
            No budget rules yet. Create one to start attributing spend to it.
          </StateMessage>
        ) : (
          <BudgetRulesTable
            rules={rules.data ?? []}
            onEdit={(rule) => setEditing({ mode: "edit", rule })}
            onDelete={setDeleting}
          />
        )}
      </div>

      {editing && (
        <BudgetRuleEditor
          editing={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            refresh();
          }}
        />
      )}
      {deleting && (
        <BudgetRuleDeleteDialog
          rule={deleting}
          onClose={() => setDeleting(undefined)}
          onDeleted={() => {
            setDeleting(undefined);
            refresh();
          }}
        />
      )}
    </div>
  );
}

function BudgetRulesTable({
  rules,
  onEdit,
  onDelete,
}: {
  rules: BudgetRule[];
  onEdit: (rule: BudgetRule) => void;
  onDelete: (rule: BudgetRule) => void;
}) {
  return (
    <div className="overflow-x-auto rounded-md border border-border">
      <table className="w-full text-left text-sm">
        <thead className="bg-muted/30 text-xs text-muted-foreground">
          <tr>
            <Th>Name</Th>
            <Th>Applies to</Th>
            <Th>Group by</Th>
            <Th>Amount</Th>
            <Th>Window</Th>
            <Th>Updated</Th>
            <Th>
              <span className="sr-only">Actions</span>
            </Th>
          </tr>
        </thead>
        <tbody>
          {rules.map((rule) => (
            <tr key={rule.id} className="border-t border-border align-top">
              <Td>
                <span className="font-medium">{rule.name}</span>
              </Td>
              <Td>
                <MatchSummary rule={rule} />
              </Td>
              <Td>
                <Chips values={rule.groupBy ?? []} empty="—" />
              </Td>
              <Td>
                <span className="tabular-nums">{formatUSD(rule.amount)}</span>
              </Td>
              <Td>
                <span title={rule.window}>{windowLabel(rule.window)}</span>
              </Td>
              <Td>
                <span className="text-xs text-muted-foreground">
                  {new Date(rule.updatedAt).toLocaleString()}
                </span>
              </Td>
              <Td>
                <div className="flex justify-end gap-density-1">
                  <Button size="sm" variant="ghost" aria-label={`Edit ${rule.name}`} onClick={() => onEdit(rule)}>
                    <Icon icon={UiEdit} className="size-4" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive"
                    aria-label={`Delete ${rule.name}`}
                    onClick={() => onDelete(rule)}
                  >
                    <Icon icon={UiTrash} className="size-4" />
                  </Button>
                </div>
              </Td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MatchSummary({ rule }: { rule: BudgetRule }) {
  const dimensions = Object.entries(rule.match.dimensions ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, pattern]) => `${key}=${pattern}`);
  const models = rule.match.models ?? [];
  if (dimensions.length === 0 && models.length === 0) {
    return <span className="text-muted-foreground">All requests</span>;
  }
  return (
    <div className="space-y-1">
      {dimensions.length > 0 && <Chips values={dimensions} empty="" />}
      {models.length > 0 && (
        <div className="flex flex-wrap items-center gap-1">
          <span className="text-xs text-muted-foreground">models</span>
          <Chips values={models} empty="" />
        </div>
      )}
    </div>
  );
}

function BudgetRuleEditor({
  editing,
  onClose,
  onSaved,
}: {
  editing: Editing;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [draft, setDraft] = useState<BudgetRuleDraft>(() =>
    editing.mode === "edit" ? draftFromRule(editing.rule) : emptyDraft(),
  );
  const [problems, setProblems] = useState<string[]>([]);
  const [customWindow, setCustomWindow] = useState(
    () => !WINDOW_PRESETS.some((preset) => preset.value === draft.window),
  );
  const save = useMutation({
    mutationFn: async () => {
      const result = draftToWrite(draft);
      if (!result.ok) {
        setProblems(result.errors);
        return false;
      }
      setProblems([]);
      if (editing.mode === "edit") await updateBudgetRule(editing.rule.id, result.value);
      else await createBudgetRule(result.value);
      return true;
    },
    onSuccess: (saved) => {
      if (saved) onSaved();
    },
  });
  const set = (patch: Partial<BudgetRuleDraft>) => setDraft((current) => ({ ...current, ...patch }));
  const setDimension = (index: number, patch: Partial<BudgetRuleDraft["dimensions"][number]>) =>
    set({ dimensions: draft.dimensions.map((row, at) => (at === index ? { ...row, ...patch } : row)) });

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={editing.mode === "edit" ? `Edit ${editing.rule.name}` : "New budget rule"}
      footer={
        <div className="flex justify-end gap-density-2">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={save.isPending}>
            Cancel
          </Button>
          <Button size="sm" loading={save.isPending} onClick={() => save.mutate()}>
            {editing.mode === "edit" ? "Save changes" : "Create rule"}
          </Button>
        </div>
      }
    >
      <form
        className="grid gap-density-4"
        onSubmit={(event) => {
          event.preventDefault();
          save.mutate();
        }}
      >
        <FormField label="Name">
          <InputField value={draft.name} onChange={(name) => set({ name })} placeholder="Platform team monthly" autoFocus />
        </FormField>

        <div className="grid gap-density-4 sm:grid-cols-2">
          <FormField label="Amount (USD)">
            <InputField
              type="number"
              min="0"
              step="0.01"
              value={draft.amount}
              onChange={(amount) => set({ amount })}
              placeholder="100"
            />
          </FormField>
          <FormField label="Window" hint="Spend counts from this point until now.">
            <Select
              aria-label="Window"
              value={customWindow ? CUSTOM_WINDOW : draft.window}
              onChange={(event) => {
                const value = event.target.value;
                if (value === CUSTOM_WINDOW) {
                  setCustomWindow(true);
                  return;
                }
                setCustomWindow(false);
                set({ window: value });
              }}
              options={[
                ...WINDOW_PRESETS.map((preset) => ({ value: preset.value, label: `${preset.label} (${preset.value})` })),
                { value: CUSTOM_WINDOW, label: "Custom datemath…" },
              ]}
            />
            {customWindow && (
              <InputField
                aria-label="Custom window"
                className="mt-density-2"
                value={draft.window}
                onChange={(window) => set({ window })}
                placeholder="now-90d"
              />
            )}
          </FormField>
        </div>

        <FormField
          label="Dimensions"
          hint="Match requests whose dimension values fit these patterns. Leave empty to match every request."
        >
          <div className="space-y-density-2">
            {draft.dimensions.map((row, index) => (
              <div key={index} className="flex items-center gap-density-2">
                <InputField
                  aria-label={`Dimension ${index + 1} key`}
                  className="flex-1"
                  value={row.key}
                  onChange={(key) => setDimension(index, { key })}
                  placeholder="team"
                />
                <span className="text-muted-foreground">=</span>
                <InputField
                  aria-label={`Dimension ${index + 1} pattern`}
                  className="flex-1"
                  value={row.pattern}
                  onChange={(pattern) => setDimension(index, { pattern })}
                  placeholder="platform-*"
                />
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove dimension ${index + 1}`}
                  onClick={() => set({ dimensions: draft.dimensions.filter((_, at) => at !== index) })}
                >
                  <Icon icon={UiTrash} className="size-4" />
                </Button>
              </div>
            ))}
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => set({ dimensions: [...draft.dimensions, { key: "", pattern: "" }] })}
            >
              <Icon icon={UiAdd} className="size-4" />
              Add dimension
            </Button>
          </div>
        </FormField>

        <FormField
          label="Models"
          hint="Comma-separated model selectors, such as anthropic/claude-* or !gpt-4o. Leave empty for all models."
        >
          <InputField value={draft.models} onChange={(models) => set({ models })} placeholder="anthropic/claude-*" />
        </FormField>

        <FormField
          label="Group by"
          hint="Comma-separated dimension keys. Each distinct combination gets its own budget; leave empty for one shared budget."
        >
          <InputField value={draft.groupBy} onChange={(groupBy) => set({ groupBy })} placeholder="team, user" />
        </FormField>

        {(problems.length > 0 || save.error) && (
          <div role="alert" className="space-y-1 text-xs text-destructive">
            {problems.map((problem) => (
              <p key={problem}>{problem}</p>
            ))}
            {save.error && <p>{errorText(save.error)}</p>}
          </div>
        )}
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}

function BudgetRuleDeleteDialog({
  rule,
  onClose,
  onDeleted,
}: {
  rule: BudgetRule;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const remove = useMutation({ mutationFn: () => deleteBudgetRule(rule.id), onSuccess: onDeleted });
  return (
    <Modal
      open
      onClose={onClose}
      title={`Delete ${rule.name}?`}
      footer={
        <div className="flex justify-end gap-density-2">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={remove.isPending}>
            Cancel
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="border-destructive/40 text-destructive"
            loading={remove.isPending}
            onClick={() => remove.mutate()}
          >
            <Icon icon={UiTrash} className="size-4" />
            Delete rule
          </Button>
        </div>
      }
    >
      <div className="space-y-density-2 text-sm">
        <p>
          New requests stop counting toward this rule. Spend it has already recorded is kept, and
          its name becomes available for a new rule.
        </p>
        {remove.error && (
          <p role="alert" className="text-xs text-destructive">
            {errorText(remove.error)}
          </p>
        )}
      </div>
    </Modal>
  );
}

function FormField({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      {children}
      {hint && <p className="text-xs text-muted-foreground/80">{hint}</p>}
    </div>
  );
}

function Chips({ values, empty }: { values: string[]; empty: string }) {
  if (values.length === 0) return <span className="text-muted-foreground">{empty}</span>;
  return (
    <ul className="flex flex-wrap gap-1">
      {values.map((value) => (
        <li key={value} className="rounded-full border border-border bg-muted px-2 py-0.5 font-mono text-[11px]">
          {value}
        </li>
      ))}
    </ul>
  );
}

function Th({ children }: { children: ReactNode }) {
  return <th className="px-density-3 py-density-2 font-medium">{children}</th>;
}

function Td({ children }: { children: ReactNode }) {
  return <td className="px-density-3 py-density-2">{children}</td>;
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
