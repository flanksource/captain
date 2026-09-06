import type {
  ResolvedRuntimeProfile,
  ResolvedRuntimeSpec,
  RuntimePreset,
  RuntimeProfile,
  RuntimeProfileResolveRequest,
  RuntimeProfilesClient,
} from "@flanksource/clicky-ui/ai";
import { fetchPermissionCatalog, type PromptSchemaDoc } from "./promptWorkbenchApi";
import { readError } from "./sandboxData";

export type RuntimeRecordKind = "preset" | "profile";

/** Where a preset or profile record lives, as reported by the catalog. */
export type RuntimeRecordSource = {
  kind: "db" | "file";
  id: string;
  label: string;
  root?: string;
  writable: boolean;
  implicit?: boolean;
  /** The record kinds this source holds; a create must target a source that accepts its kind. */
  records: RuntimeRecordKind[];
};

type StoredRecord = {
  key: string;
  source: RuntimeRecordSource;
  updatedAt: string;
};

type EntityRecordMetadata = Partial<StoredRecord> & {
  _id?: string;
};

export type StoredRuntimePreset = RuntimePreset & StoredRecord;
export type StoredRuntimeProfile = RuntimeProfile & StoredRecord;

export type RuntimePresetWrite = Pick<
  RuntimePreset,
  "name" | "description" | "scope" | "spec"
>;
export type RuntimeProfileWrite = Pick<
  RuntimeProfile,
  "name" | "description" | "spec" | "presets"
>;

export function presetWrite(preset: RuntimePreset): RuntimePresetWrite {
  return {
    name: preset.name,
    scope: preset.scope,
    spec: preset.spec,
    ...(preset.description !== undefined ? { description: preset.description } : {}),
  };
}

export function profileWrite(profile: RuntimeProfile): RuntimeProfileWrite {
  return {
    name: profile.name,
    spec: profile.spec,
    presets: profile.presets,
    ...(profile.description !== undefined ? { description: profile.description } : {}),
  };
}

export type RuntimeProfileResolution = {
  profile: StoredRuntimeProfile;
  presets: StoredRuntimePreset[];
  resolved: ResolvedRuntimeSpec;
};

type RuntimeProfileResolveInput = {
  profile: RuntimeProfile & EntityRecordMetadata;
  presets: Array<RuntimePreset & EntityRecordMetadata>;
};

/** The catalog's database source id, the default create target. */
export const RUNTIME_DB_TARGET = "db";
export const RUNTIME_PRESETS_URL = "/api/v1/runtime-preset";
export const RUNTIME_PROFILES_URL = "/api/v1/runtime-profile";
export const RUNTIME_PROFILE_RESOLVE_URL = "/api/chat/runtime-profiles/resolve";

const JSON_HEADERS = {
  Accept: "application/json",
  "Content-Type": "application/json",
};

export function fetchRuntimePresets() {
  return fetchRecords<StoredRuntimePreset>(RUNTIME_PRESETS_URL);
}

export function fetchRuntimeProfiles() {
  return fetchRecords<StoredRuntimeProfile>(RUNTIME_PROFILES_URL);
}

export function createRuntimePreset(
  input: RuntimePresetWrite & { target: string },
) {
  return writeRecord<StoredRuntimePreset>(RUNTIME_PRESETS_URL, "POST", input);
}

/** Updates go to the collection URL with the id in the body; clicky routes no `PUT …/{id}`. */
export function updateRuntimePreset(id: string, input: RuntimePresetWrite) {
  return writeRecord<StoredRuntimePreset>(RUNTIME_PRESETS_URL, "PUT", { id, ...input });
}

export function deleteRuntimePreset(id: string) {
  return deleteRecord(recordURL(RUNTIME_PRESETS_URL, id));
}

export function createRuntimeProfile(
  input: RuntimeProfileWrite & { target: string },
) {
  return writeRecord<StoredRuntimeProfile>(RUNTIME_PROFILES_URL, "POST", input);
}

export function updateRuntimeProfile(id: string, input: RuntimeProfileWrite) {
  return writeRecord<StoredRuntimeProfile>(RUNTIME_PROFILES_URL, "PUT", { id, ...input });
}

export function deleteRuntimeProfile(id: string) {
  return deleteRecord(recordURL(RUNTIME_PROFILES_URL, id));
}

/** Resolves a saved profile by id or unique name through the entity action. */
export async function fetchRuntimeProfileResolution(
  id: string,
): Promise<RuntimeProfileResolution> {
  const url = `${recordURL(RUNTIME_PROFILES_URL, id)}/resolve`;
  const response = await fetch(url, { headers: { Accept: "application/json" } });
  if (!response.ok) await readError(response, `GET ${url} failed`);
  const value: unknown = await response.json();
  if (!isResolution(value)) {
    throw new Error(`${url} must return profile, presets and resolved`);
  }
  return value;
}

/**
 * Resolves an unsaved draft against the chat tool catalog. Catalog records also
 * carry `key`, `source`, `updatedAt` and the entity list's `_id`; the endpoint
 * decodes its contract strictly, so only the contract fields are sent.
 */
export async function resolveRuntimeProfile(
  request: RuntimeProfileResolveInput,
  signal?: AbortSignal,
): Promise<ResolvedRuntimeProfile> {
  const body: RuntimeProfileResolveRequest = {
    profile: { id: request.profile.id, ...profileWrite(request.profile) },
    presets: request.presets.map((preset) => ({ id: preset.id, ...presetWrite(preset) })),
  };
  const response = await fetch(RUNTIME_PROFILE_RESOLVE_URL, {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
    ...(signal ? { signal } : {}),
  });
  if (!response.ok) {
    await readError(response, `POST ${RUNTIME_PROFILE_RESOLVE_URL} failed`);
  }
  const value: unknown = await response.json();
  if (!isResolvedProfile(value)) {
    throw new Error(
      `${RUNTIME_PROFILE_RESOLVE_URL} must return resolved, tools and permissions`,
    );
  }
  return value;
}

export const runtimeProfilesClient: RuntimeProfilesClient = {
  resolve: resolveRuntimeProfile,
  loadPermissionCatalog: fetchPermissionCatalog,
};

/** The catalog sources served in the prompt schema document; a server without them predates runtime profiles. */
export function runtimeSourcesOf(doc: PromptSchemaDoc): RuntimeRecordSource[] {
  const sources: unknown = (doc as PromptSchemaDoc & { runtimeSources?: unknown }).runtimeSources;
  if (!Array.isArray(sources) || !sources.every(isSource)) {
    throw new Error(
      "Prompt schema document has no runtimeSources list; this server does not serve runtime profiles.",
    );
  }
  return sources;
}

function recordURL(base: string, id: string) {
  return `${base}/${encodeURIComponent(id)}`;
}

async function fetchRecords<T extends StoredRecord>(url: string): Promise<T[]> {
  const response = await fetch(url, { headers: { Accept: "application/json" } });
  if (!response.ok) await readError(response, `GET ${url} failed`);
  const value: unknown = await response.json();
  if (!Array.isArray(value)) {
    throw new Error(`${url} must return a JSON array of runtime records`);
  }
  value.forEach((item, index) => assertRecord(item, `${url}[${index}]`));
  return value.map((item: StoredRecord) => withoutEntityID(item) as T);
}

/** Entity list rows repeat the id as clicky's `_id`; records must match the shape of write responses so draft comparisons hold. */
function withoutEntityID(record: StoredRecord): StoredRecord {
  return Object.fromEntries(
    Object.entries(record).filter(([key]) => key !== "_id"),
  ) as StoredRecord;
}

async function writeRecord<T extends StoredRecord>(
  url: string,
  method: "POST" | "PUT",
  body: object,
): Promise<T> {
  const response = await fetch(url, {
    method,
    headers: JSON_HEADERS,
    body: JSON.stringify(body),
  });
  if (!response.ok) await readError(response, `${method} ${url} failed`);
  const value: unknown = await response.json();
  assertRecord(value, `${method} ${url}`);
  return value as T;
}

async function deleteRecord(url: string): Promise<void> {
  const response = await fetch(url, {
    method: "DELETE",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) await readError(response, `DELETE ${url} failed`);
}

function assertRecord(value: unknown, where: string): asserts value is StoredRecord {
  const record = asObject(value);
  if (
    !record ||
    typeof record.id !== "string" ||
    typeof record.name !== "string" ||
    !asObject(record.spec) ||
    !isSource(record.source)
  ) {
    throw new Error(
      `${where} must be a runtime record with id, name, spec and source`,
    );
  }
}

function isSource(value: unknown): value is RuntimeRecordSource {
  const source = asObject(value);
  return Boolean(
    source &&
      (source.kind === "db" || source.kind === "file") &&
      typeof source.id === "string" &&
      typeof source.label === "string" &&
      typeof source.writable === "boolean" &&
      Array.isArray(source.records) &&
      source.records.every((kind) => kind === "preset" || kind === "profile"),
  );
}

/** The writable sources a new record of `kind` can be created in. */
export function createTargetsFor(sources: RuntimeRecordSource[], kind: RuntimeRecordKind) {
  return sources.filter((source) => source.writable && source.records.includes(kind));
}

function isResolution(value: unknown): value is RuntimeProfileResolution {
  const result = asObject(value);
  return Boolean(
    result &&
      asObject(result.profile) &&
      Array.isArray(result.presets) &&
      isResolvedSpec(result.resolved),
  );
}

function isResolvedProfile(value: unknown): value is ResolvedRuntimeProfile {
  const result = asObject(value);
  return Boolean(
    result &&
      isResolvedSpec(result.resolved) &&
      Array.isArray(result.tools) &&
      asObject(result.permissions),
  );
}

function isResolvedSpec(value: unknown): value is ResolvedRuntimeSpec {
  const resolved = asObject(value);
  return Boolean(
    resolved && asObject(resolved.spec) && Array.isArray(resolved.trace),
  );
}

function asObject(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}
