import type { JsonSchemaObject } from "@flanksource/clicky-ui/components";

export type AdapterSchemaSelection = {
  provider?: string;
  mode?: string;
};

export type AdapterSchemaDocument = {
  id: string;
  kind: "execution" | "configuration" | "resource";
  provider: string;
  mode?: string;
  title: string;
  description: string;
  schema: JsonSchemaObject;
};

export async function fetchAdapterSchemas(): Promise<AdapterSchemaDocument[]> {
  const response = await fetch("/api/captain/ai/adapter-schemas", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Adapter schemas failed with ${response.status}`);
  }
  const value: unknown = await response.json();
  if (!Array.isArray(value)) {
    throw new Error("Adapter schema catalog must be a JSON array.");
  }
  value.forEach(assertAdapterSchemaDocument);
  return value;
}

export function parseAdapterSchemaPath(pathname: string): AdapterSchemaSelection {
  const raw = pathname.slice("/adapter-schemas".length).replace(/^\/+/, "");
  const [provider, mode] = raw.split("/");
  return {
    ...(provider ? { provider: decodeURIComponent(provider) } : {}),
    ...(mode ? { mode: decodeURIComponent(mode) } : {}),
  };
}

export function adapterSchemaLocation(selection: Required<AdapterSchemaSelection>): string {
  return `/adapter-schemas/${encodeURIComponent(selection.provider)}/${encodeURIComponent(selection.mode)}`;
}

export function selectAdapterSchema(
  documents: AdapterSchemaDocument[],
  selection: AdapterSchemaSelection,
): AdapterSchemaDocument | undefined {
  if (!selection.provider && !selection.mode) return documents[0];
  return documents.find((document) => document.id === `${selection.provider}/${selection.mode}`);
}

function assertAdapterSchemaDocument(value: unknown, index: number): asserts value is AdapterSchemaDocument {
  if (!value || typeof value !== "object") {
    throw new Error(`Adapter schema catalog entry ${index} must be an object.`);
  }
  const document = value as Record<string, unknown>;
  for (const field of ["id", "kind", "provider", "title", "description"] as const) {
    if (typeof document[field] !== "string" || document[field].trim() === "") {
      throw new Error(`Adapter schema catalog entry ${index} has no ${field}.`);
    }
  }
  if (!["execution", "configuration", "resource"].includes(String(document.kind))) {
    throw new Error(`Adapter schema catalog entry ${index} has an invalid kind.`);
  }
  if (!document.schema || typeof document.schema !== "object" || Array.isArray(document.schema)) {
    throw new Error(`Adapter schema catalog entry ${index} has no schema object.`);
  }
}

export type SchemaValidation = {
  valid: boolean;
  errors: Array<{ instancePath: string; schemaPath: string; message: string }>;
};

export async function validateAdapterSchema(id: string, value: unknown, signal: AbortSignal): Promise<SchemaValidation> {
  const response = await fetch("/api/captain/ai/adapter-schemas/validate", {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, value }), signal,
  });
  if (!response.ok) throw new Error(await response.text());
  const result: unknown = await response.json();
  if (!result || typeof result !== "object" || !("valid" in result) || typeof result.valid !== "boolean" ||
      !("errors" in result) || !Array.isArray(result.errors) || !result.errors.every((issue: unknown) =>
        issue && typeof issue === "object" && "message" in issue && typeof issue.message === "string" &&
        "instancePath" in issue && typeof issue.instancePath === "string" && "schemaPath" in issue && typeof issue.schemaPath === "string")) {
    throw new Error("Schema validation returned an invalid response.");
  }
  return result as SchemaValidation;
}
