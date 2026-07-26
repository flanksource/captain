import type { AISpecRuntimeValue } from "@flanksource/clicky-ui/ai";

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
