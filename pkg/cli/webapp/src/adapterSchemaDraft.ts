import { parseDocument, stringify } from "yaml";

export type SchemaDraft = { value: unknown; json: string; yaml: string; error?: string };

export function createSchemaDraft(value: unknown): SchemaDraft {
  return { value, json: JSON.stringify(value, null, 2), yaml: stringify(value) };
}

export function editSchemaDraft(previous: SchemaDraft, language: "json" | "yaml", text: string): SchemaDraft {
  try {
    let value: unknown;
    if (language === "json") value = JSON.parse(text);
    else {
      const document = parseDocument(text, { uniqueKeys: true });
      if (document.errors.length) throw new Error(document.errors.map((error) => error.message).join("\n"));
      value = document.toJS({ maxAliasCount: 100 });
    }
    const next = createSchemaDraft(value);
    return { ...next, [language]: text };
  } catch (error) {
    return { ...previous, [language]: text, error: error instanceof Error ? error.message : String(error) };
  }
}
