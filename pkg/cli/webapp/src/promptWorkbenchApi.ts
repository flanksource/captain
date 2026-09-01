import type {
  AISpecRuntimePermissionCatalog,
  RuntimeCatalogFamily,
  SpecRuntimeSandboxCatalog,
} from "@flanksource/clicky-ui/ai";
import type { ChatModel } from "@flanksource/clicky-ui/chat";
import type {
  JsonSchemaObject,
  KeyPreview,
  SecretKind,
  SecretResource,
} from "@flanksource/clicky-ui/components";
import type { ResolvedOperation } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { isScratchPrompt } from "./promptDetailState";
import {
  unwrapResponse,
  type PromptDetail,
  type PromptSourceInfo,
} from "./promptData";

// One backend entry from `captain prompt --schema`: its kind/auth/model status
// plus, for cmux backends, the JSON schema for its extra CLI args.
export type PromptSchemaBackend = {
  backend: string;
  kind?: string;
  authenticated?: boolean;
  ready?: boolean;
  models?: string[];
  args?: JsonSchemaObject;
};

// The `captain prompt --schema` document. Only the fields the workbench consumes
// are typed; the served document also carries `spec`/`prompt`/`promptAction`
// schemas and a flat `models` list.
export type PromptSchemaDoc = {
  schemaVersion: number;
  backends?: PromptSchemaBackend[];
  models?: ChatModel[];
  /**
   * The provider×mode catalog the runtime picker renders. The server projects it
   * from its model registry and has already dropped the disabled entries, so the
   * client never re-derives which backends exist.
   */
  runtimes?: RuntimeCatalogFamily[];
  /** The enabled effort tiers, for models the catalog does not describe. */
  efforts?: string[];
  /**
   * The sandbox adapter catalog: what confines a run, what each adapter can do,
   * which runtime modes it serves, and the configured backends (with their
   * enrolled git-agent rosters) that select it.
   */
  sandboxes?: SpecRuntimeSandboxCatalog;
  spec?: JsonSchemaObject;
  /** Every discovered prompt source, so the save destination picker can offer empty writable dirs too. */
  sources: PromptSourceInfo[];
};

export async function fetchPromptSchema(): Promise<PromptSchemaDoc> {
  const response = await fetch("/api/captain/ai/prompt/schema", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Prompt schema failed with ${response.status}`);
  }
  const doc = (await response.json()) as PromptSchemaDoc;
  if (!Array.isArray(doc.sources)) {
    throw new Error("Prompt schema document has no sources list.");
  }
  return doc;
}

export async function fetchCatalog<T>(
  endpoint: string,
  label: string,
): Promise<T[]> {
  const response = await fetch(endpoint, {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `${label} failed with ${response.status}`);
  }
  const data: unknown = await response.json();
  if (!Array.isArray(data)) throw new Error(`${label} must be an array`);
  return data as T[];
}

export async function fetchPermissionCatalog(backend: string) {
  const response = await fetch(
    `/api/captain/ai/permissions/catalog?backend=${encodeURIComponent(backend)}`,
    { headers: { Accept: "application/json" } },
  );
  if (!response.ok) {
    const message = await response.text();
    throw new Error(
      message || `Permission catalog failed with ${response.status}`,
    );
  }
  return (await response.json()) as AISpecRuntimePermissionCatalog;
}

export const CAPTAIN_SECRET_SELECTOR = {
  loadResources: fetchSecretResources,
  loadKeyPreview: fetchSecretKeyPreview,
};

async function fetchSecretResources(
  kind: SecretKind,
): Promise<SecretResource[]> {
  const response = await fetch(
    `/api/captain/secrets/resources?kind=${encodeURIComponent(kind)}`,
    {
      headers: { Accept: "application/json" },
    },
  );
  if (!response.ok) return [];
  return (await response.json()) as SecretResource[];
}

async function fetchSecretKeyPreview(
  kind: SecretKind,
  name: string,
): Promise<KeyPreview[]> {
  const response = await fetch(
    `/api/captain/secrets/preview?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`,
    { headers: { Accept: "application/json" } },
  );
  if (!response.ok) return [];
  return (await response.json()) as KeyPreview[];
}

export async function fetchPromptDetail(op: ResolvedOperation, id: string) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    { id },
    { Accept: "application/json" },
  );
  return unwrapResponse<PromptDetail>(response);
}

export async function submitPromptOperation<T>(
  op: ResolvedOperation,
  params: Record<string, string>,
  body: Record<string, unknown>,
) {
  const response = await apiClient.submitForm!(
    operationRequestPath(op, params),
    op.method,
    body,
    { Accept: "application/json" },
  );
  return unwrapResponse<T>(response);
}

export type OperationPathSource = {
  path: string;
  operation: {
    parameters?: Array<{ name?: string; in?: string }>;
    "x-clicky"?: { idParam?: string };
  };
};

/**
 * The URL for an entity operation. Path placeholders are filled from params; an
 * id the path has no placeholder for goes in the query — as the positional
 * `args` when the operation declares one (entity update), else under the
 * operation's declared id parameter (collection actions such as render and run).
 * Without this the id was silently dropped: Save failed with "id is required"
 * and Render/Run executed the empty scratch prompt instead of the selected one.
 */
export function operationRequestPath(
  op: OperationPathSource,
  params: Record<string, string>,
) {
  let path = op.path;
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (path.includes(`{${key}}`)) {
      path = value
        ? path.replace(`{${key}}`, encodeURIComponent(value))
        : path.replace(new RegExp(`/\\{${key}\\}`, "g"), "").replace(`{${key}}`, "");
      continue;
    }
    if (value) query.set(queryParamFor(op, key), value);
  }
  path = path.replace(/\/{2,}/g, "/");
  const search = query.toString();
  return search ? `${path}?${search}` : path;
}

function queryParamFor(op: OperationPathSource, key: string) {
  const declared = op.operation.parameters ?? [];
  if (declared.some((param) => param.in === "query" && param.name === "args")) {
    return "args";
  }
  const idParam = op.operation["x-clicky"]?.idParam;
  return key === "id" && idParam ? idParam : key;
}

export async function executePromptOperation(
  op: ResolvedOperation,
  params: Record<string, string>,
  body: Record<string, string>,
) {
  const response = await apiClient.executeCommand(
    op.path,
    op.method,
    paramsWithPathValues(op.path, params, body),
    { Accept: "application/json" },
  );
  return unwrapResponse<unknown>(response);
}

function paramsWithPathValues(
  path: string,
  pathParams: Record<string, string>,
  params: Record<string, string>,
) {
  const next = { ...params };
  for (const key of path.matchAll(/\{([^{}]+)\}/g)) {
    const name = key[1];
    if (name && pathParams[name]) next[name] = pathParams[name];
  }
  return next;
}

export function promptActionParams(detail: PromptDetail) {
  return { id: isScratchPrompt(detail) ? "" : detail.id };
}

export function normalizeObjectSchema(
  schema: Record<string, unknown> | undefined,
): JsonSchemaObject | undefined {
  if (!schema || typeof schema !== "object") return undefined;
  const properties = schema.properties;
  if (
    !properties ||
    typeof properties !== "object" ||
    Array.isArray(properties)
  )
    return undefined;
  return {
    ...schema,
    type: "object",
    properties: properties as JsonSchemaObject["properties"],
  } as JsonSchemaObject;
}

export function errorMessage(error: unknown) {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  return "Unexpected error.";
}
