import { readError } from "./sandboxData";

export const BUDGET_RULES_URL = "/api/v1/budget-rule";

/** Selects requests by trusted host dimensions and by resolved model. */
export type BudgetRuleMatch = {
  dimensions?: Record<string, string>;
  models?: string[];
};

/** A live budget rule as the budget-rule entity returns it. */
export type BudgetRule = {
  id: string;
  name: string;
  match: BudgetRuleMatch;
  groupBy: string[] | null;
  amount: number;
  window: string;
  createdAt: string;
  updatedAt: string;
};

export type BudgetRuleWrite = {
  name: string;
  match: BudgetRuleMatch;
  groupBy: string[];
  amount: number;
  window: string;
};

const JSON_HEADERS = {
  Accept: "application/json",
  "Content-Type": "application/json",
};

export async function fetchBudgetRules(): Promise<BudgetRule[]> {
  const response = await fetch(BUDGET_RULES_URL, { headers: { Accept: "application/json" } });
  if (!response.ok) await readError(response, `GET ${BUDGET_RULES_URL} failed`);
  const value: unknown = await response.json();
  if (!Array.isArray(value)) {
    throw new Error(`${BUDGET_RULES_URL} must return a JSON array of budget rules`);
  }
  value.forEach((item, index) => assertRule(item, `${BUDGET_RULES_URL}[${index}]`));
  return value as BudgetRule[];
}

export function createBudgetRule(input: BudgetRuleWrite) {
  return writeRule("POST", input);
}

/** Updates go to the collection URL with the id in the body; clicky routes no `PUT …/{id}`. */
export function updateBudgetRule(id: string, input: BudgetRuleWrite) {
  return writeRule("PUT", { id, ...input });
}

export async function deleteBudgetRule(id: string): Promise<void> {
  const url = `${BUDGET_RULES_URL}/${encodeURIComponent(id)}`;
  const response = await fetch(url, { method: "DELETE", headers: { Accept: "application/json" } });
  if (!response.ok) await readError(response, `DELETE ${url} failed`);
}

async function writeRule(method: "POST" | "PUT", body: object): Promise<BudgetRule> {
  const response = await fetch(BUDGET_RULES_URL, {
    method,
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  });
  if (!response.ok) await readError(response, `${method} ${BUDGET_RULES_URL} failed`);
  const value: unknown = await response.json();
  assertRule(value, `${method} ${BUDGET_RULES_URL}`);
  return value;
}

function assertRule(value: unknown, where: string): asserts value is BudgetRule {
  const rule = value && typeof value === "object" ? (value as Record<string, unknown>) : undefined;
  if (
    !rule ||
    typeof rule.id !== "string" ||
    typeof rule.name !== "string" ||
    typeof rule.amount !== "number" ||
    typeof rule.window !== "string"
  ) {
    throw new Error(`${where} must be a budget rule with id, name, amount and window`);
  }
}

/** Common windows offered in the editor; any other `now…` datemath is still accepted. */
export const WINDOW_PRESETS: Array<{ value: string; label: string }> = [
  { value: "now/d", label: "Today" },
  { value: "now/w", label: "This week" },
  { value: "now/M", label: "This month" },
  { value: "now-24h", label: "Last 24 hours" },
  { value: "now-7d", label: "Last 7 days" },
  { value: "now-30d", label: "Last 30 days" },
];

export function windowLabel(window: string): string {
  return WINDOW_PRESETS.find((preset) => preset.value === window)?.label ?? window;
}

const USD = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });

export function formatUSD(amount: number): string {
  return USD.format(amount);
}

/** One editable `dimension = pattern` row. */
export type DimensionRow = { key: string; pattern: string };

/** The editor's state: list fields stay as the text the user typed until save. */
export type BudgetRuleDraft = {
  name: string;
  amount: string;
  window: string;
  dimensions: DimensionRow[];
  models: string;
  groupBy: string;
};

export function emptyDraft(): BudgetRuleDraft {
  return { name: "", amount: "", window: "now/M", dimensions: [], models: "", groupBy: "" };
}

export function draftFromRule(rule: BudgetRule): BudgetRuleDraft {
  return {
    name: rule.name,
    amount: String(rule.amount),
    window: rule.window,
    dimensions: Object.entries(rule.match.dimensions ?? {})
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, pattern]) => ({ key, pattern })),
    models: (rule.match.models ?? []).join(", "),
    groupBy: (rule.groupBy ?? []).join(", "),
  };
}

/**
 * Converts a draft to a write body, or returns the problems a user can fix
 * before saving. The server validates again; this only catches what is
 * obviously incomplete so the form can say so next to the field.
 */
export function draftToWrite(
  draft: BudgetRuleDraft,
): { ok: true; value: BudgetRuleWrite } | { ok: false; errors: string[] } {
  const errors: string[] = [];
  const name = draft.name.trim();
  if (!name) errors.push("Name is required.");
  const amount = Number(draft.amount);
  if (!draft.amount.trim() || !Number.isFinite(amount) || amount <= 0) {
    errors.push("Amount must be a positive number of US dollars.");
  }
  const window = draft.window.trim();
  if (!window.startsWith("now")) errors.push('Window must be relative to now, such as "now/M".');

  const dimensions: Record<string, string> = {};
  for (const row of draft.dimensions) {
    const key = row.key.trim();
    const pattern = row.pattern.trim();
    if (!key && !pattern) continue;
    if (!key || !pattern) {
      errors.push("Every dimension needs both a key and a pattern.");
      continue;
    }
    if (key in dimensions) {
      errors.push(`Dimension "${key}" is listed twice.`);
      continue;
    }
    dimensions[key] = pattern;
  }
  const groupBy = splitList(draft.groupBy);
  if (new Set(groupBy).size !== groupBy.length) errors.push("Group by lists a key twice.");

  if (errors.length > 0) return { ok: false, errors };
  const models = splitList(draft.models);
  return {
    ok: true,
    value: {
      name,
      amount,
      window,
      groupBy,
      match: {
        ...(Object.keys(dimensions).length > 0 ? { dimensions } : {}),
        ...(models.length > 0 ? { models } : {}),
      },
    },
  };
}

function splitList(raw: string): string[] {
  return raw
    .split(/[,\n]/)
    .map((value) => value.trim())
    .filter(Boolean);
}
