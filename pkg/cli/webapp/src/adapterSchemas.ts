import type { JsonSchemaObject } from "@flanksource/clicky-ui/components";

export type AdapterSchemaSelection = {
  provider?: string;
  mode?: string;
};

export type AdapterSchemaDocument = {
  provider: string;
  mode: string;
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
  return (
    documents.find(
      (document) =>
        document.provider === selection.provider && document.mode === selection.mode,
    ) ?? documents[0]
  );
}

function assertAdapterSchemaDocument(value: unknown, index: number): asserts value is AdapterSchemaDocument {
  if (!value || typeof value !== "object") {
    throw new Error(`Adapter schema catalog entry ${index} must be an object.`);
  }
  const document = value as Record<string, unknown>;
  for (const field of ["provider", "mode", "title", "description"] as const) {
    if (typeof document[field] !== "string" || document[field].trim() === "") {
      throw new Error(`Adapter schema catalog entry ${index} has no ${field}.`);
    }
  }
  if (!document.schema || typeof document.schema !== "object" || Array.isArray(document.schema)) {
    throw new Error(`Adapter schema catalog entry ${index} has no schema object.`);
  }
}
