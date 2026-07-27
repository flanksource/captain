import { isMap, parseDocument } from "yaml";

export type PromptSchemaKind = "input" | "output";

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
  let cursor = 0;
  while (cursor < source.length) {
    const line = readPromptLine(source, cursor);
    if (!line.terminated || !isPromptPrefixLine(line.value)) break;
    cursor = line.next;
  }

  const prefixEnd = cursor;
  const opening = readPromptLine(source, cursor);
  if (!opening.terminated || !isFrontmatterDelimiter(opening.value)) {
    return { prefix: "", frontmatter: "{}\n", body: source };
  }

  const frontmatterStart = opening.next;
  cursor = frontmatterStart;
  while (cursor < source.length) {
    const lineStart = cursor;
    const line = readPromptLine(source, cursor);
    if (!line.terminated) break;
    if (isFrontmatterDelimiter(line.value)) {
      const frontmatter = source
        .slice(frontmatterStart, lineStart)
        .split("\r\n")
        .join("\n")
        .split("\r")
        .join("\n");
      return {
        prefix: source.slice(0, prefixEnd),
        frontmatter: frontmatter.trim() ? frontmatter : "{}\n",
        body: source.slice(line.next),
      };
    }
    cursor = line.next;
  }

  return { prefix: "", frontmatter: "{}\n", body: source };
}

function readPromptLine(source: string, start: number) {
  let end = start;
  while (end < source.length && source[end] !== "\r" && source[end] !== "\n") {
    end++;
  }
  let next = end;
  if (source[next] === "\r") next++;
  if (source[next] === "\n") next++;
  return {
    value: source.slice(start, end),
    next,
    terminated: next > end,
  };
}

function isPromptPrefixLine(line: string) {
  if (line.startsWith("#")) return true;
  for (const character of line) {
    if (character !== " " && character !== "\t") return false;
  }
  return true;
}

function isFrontmatterDelimiter(line: string) {
  if (!line.startsWith("---")) return false;
  for (const character of line.slice(3)) {
    if (character !== " " && character !== "\t") return false;
  }
  return true;
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
