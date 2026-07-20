import { isMap, parseDocument } from "yaml";

export type PromptSchemaKind = "input" | "output";

const FRONTMATTER =
  /^((?:(?:#[^\n]*|[ \t]*)\r?\n)*)---[ \t]*(?:\r\n|\r|\n)([\s\S]*?)(?:\r\n|\r|\n)---[ \t]*(?:\r\n|\r|\n)([\s\S]*)$/;
const EMPTY_FRONTMATTER =
  /^((?:(?:#[^\n]*|[ \t]*)\r?\n)*)---[ \t]*(?:\r\n|\r|\n)---[ \t]*(?:\r\n|\r|\n)([\s\S]*)$/;

type PromptSourceParts = {
  prefix: string;
  frontmatter: string;
  body: string;
};

export function parseSchemaEditorValue(
  value: string,
): Record<string, unknown> {
  if (!value.trim()) throw new Error("Schema must be a JSON object.");
  const parsed: unknown = JSON.parse(value);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Schema must be a JSON object.");
  }
  return parsed as Record<string, unknown>;
}

export function updatePromptSchemaSource(
  source: string,
  kind: PromptSchemaKind,
  schema: Record<string, unknown> | undefined,
): string {
  const { prefix, frontmatter, body } = splitPromptSource(source);
  const document = parsePromptFrontmatter(frontmatter);

  if (schema) {
    document.setIn([kind, "schema"], schema);
  } else {
    document.deleteIn([kind, "schema"]);
    const section = document.get(kind, true);
    if (isMap(section) && section.items.length === 0) document.delete(kind);
  }

  return `${prefix}---\n${document.toString()}---\n${body}`;
}

export function readPromptSchemas(source: string) {
  const document = parsePromptFrontmatter(splitPromptSource(source).frontmatter);
  const frontmatter = document.toJS() as Record<string, unknown>;
  return {
    input: readSchema(frontmatter, "input"),
    output: readSchema(frontmatter, "output"),
  };
}

function splitPromptSource(source: string): PromptSourceParts {
  const match = FRONTMATTER.exec(source);
  const emptyMatch = match ? null : EMPTY_FRONTMATTER.exec(source);
  return {
    prefix: match?.[1] ?? emptyMatch?.[1] ?? "",
    frontmatter: match?.[2]?.trim() ? match[2] : "{}\n",
    body: match?.[3] ?? emptyMatch?.[2] ?? source,
  };
}

function parsePromptFrontmatter(frontmatter: string) {
  const document = parseDocument(frontmatter);
  if (document.errors.length > 0) {
    throw new Error(`Parse prompt frontmatter: ${document.errors[0]?.message}`);
  }
  if (!isMap(document.contents)) {
    throw new Error("Prompt frontmatter must be a YAML object.");
  }
  document.contents.flow = false;
  return document;
}

function readSchema(
  frontmatter: Record<string, unknown>,
  kind: PromptSchemaKind,
) {
  const section = frontmatter[kind];
  if (!section || typeof section !== "object" || Array.isArray(section)) {
    return undefined;
  }
  const schema = (section as Record<string, unknown>).schema;
  if (schema === undefined) return undefined;
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) {
    throw new Error(`Prompt ${kind} schema must be a YAML object.`);
  }
  return schema as Record<string, unknown>;
}
