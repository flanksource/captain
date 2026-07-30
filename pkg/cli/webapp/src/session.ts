import type {
  ExecutionResponse,
  OperationCommandPageProps,
  ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";

export type CommandValues = Parameters<
  NonNullable<OperationCommandPageProps["onResult"]>
>[2];

// A field to pull out of an execution response. `keys` are exact record keys;
// `labels` are the normalized forms that name the field in a rendered node tree;
// `patterns` are the last-resort plain-text forms.
type ResponseField = {
  keys: string[];
  labels: string[];
  patterns: RegExp[];
};

const SESSION_FIELD: ResponseField = {
  keys: [
    "sessionId",
    "sessionID",
    "session_id",
    "SessionID",
    "ProviderSessionID",
    "providerSessionId",
  ],
  labels: [
    "session",
    "session/id",
    "sessionid",
    "provider/session/id",
    "providersessionid",
  ],
  patterns: [
    /"sessionId"\s*:\s*"([^"]+)"/i,
    /"session_id"\s*:\s*"([^"]+)"/i,
    /\bSession\s+([A-Za-z0-9_.:-]+)/i,
  ],
};

const MODEL_FIELD: ResponseField = {
  keys: ["model", "Model"],
  labels: ["model"],
  patterns: [/"model"\s*:\s*"([^"]+)"/i],
};

export function isAgentOperation(op: ResolvedOperation) {
  const operationId = normalizeCommandName(op.operation.operationId);
  const command = normalizeCommandName(op.operation["x-clicky"]?.command);
  const path = normalizeCommandName(op.path.replace(/^\/api\/v1\/?/, ""));

  return (
    operationId === "ai/agent" ||
    command === "ai/agent" ||
    path === "ai/agent"
  );
}

export function isChatToolOperation(op: ResolvedOperation) {
  const command =
    normalizeCommandName(op.operation["x-clicky"]?.command) ||
    normalizeCommandName(op.operation.operationId) ||
    normalizeCommandName(op.path.replace(/^\/api\/v1\/?/, ""));
  const root = command.split("/")[0];
  return ![
    "ai",
    "serve",
    "mcp",
    "hook",
    "container",
    "sandbox",
    "port",
    "configure",
  ].includes(root);
}

// The model the run actually resolved to: `AIAgentResult.model`, which the
// server fills from the provider's own answer. Never guessed from the submitted
// form values — an id ladder here would name models the catalog no longer
// carries, or ones the user disabled. Empty means the response named none, and
// the chat thread then opens on its own default rather than on a stale literal.
export function agentModelFor(response: ExecutionResponse) {
  return (
    fieldFromValue(response.parsed, MODEL_FIELD) ||
    fieldFromText(response.stdout, MODEL_FIELD) ||
    fieldFromText(response.output, MODEL_FIELD)
  );
}

export function titleFor(values: CommandValues, sessionId: string) {
  const prompt = stringValue(values.prompt).trim();
  if (!prompt) return `Captain agent ${sessionId}`;
  return prompt.length > 96 ? `${prompt.slice(0, 93)}...` : prompt;
}

export function extractSessionId(response: ExecutionResponse) {
  return (
    stringFromHeaders(response.responseHeaders) ||
    fieldFromValue(response.parsed, SESSION_FIELD) ||
    fieldFromText(response.stdout, SESSION_FIELD) ||
    fieldFromText(response.output, SESSION_FIELD)
  );
}

function stringFromHeaders(headers: Record<string, string> | undefined) {
  if (!headers) return "";
  return (
    headers["x-session-id"] ||
    headers["x-provider-session-id"] ||
    headers["x-captain-session-id"] ||
    ""
  );
}

function fieldFromValue(
  value: unknown,
  field: ResponseField,
  parentKey = "",
): string {
  if (value == null) return "";
  if (typeof value === "string") {
    return matchesField(field, parentKey) ? value.trim() : "";
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = fieldFromValue(item, field, parentKey);
      if (found) return found;
    }
    return "";
  }
  if (typeof value !== "object") return "";

  const record = value as Record<string, unknown>;
  for (const key of field.keys) {
    const direct = stringValue(record[key]).trim();
    if (direct) return direct;
  }

  const fieldName = stringValue(record.name) || stringValue(record.label) || parentKey;
  if (matchesField(field, fieldName)) {
    const text = nodeText(record.value) || nodeText(record);
    if (text) return text;
  }

  for (const key of ["node", "fields", "rows", "value", "children", "items"]) {
    const found = fieldFromValue(record[key], field, fieldName);
    if (found) return found;
  }

  for (const [key, nested] of Object.entries(record)) {
    const found = fieldFromValue(nested, field, key);
    if (found) return found;
  }
  return "";
}

function nodeText(value: unknown): string {
  if (value == null) return "";
  if (typeof value === "string") return value.trim();
  if (typeof value !== "object") return "";
  const record = value as Record<string, unknown>;
  return (
    stringValue(record.plain) ||
    stringValue(record.text) ||
    stringValue(record.value)
  ).trim();
}

function fieldFromText(text: string | undefined, field: ResponseField) {
  if (!text) return "";
  try {
    const found = fieldFromValue(JSON.parse(text) as unknown, field);
    if (found) return found;
  } catch {
    // Fall through to the plain-text patterns.
  }
  for (const pattern of field.patterns) {
    const match = text.match(pattern);
    if (match?.[1]) return match[1].trim();
  }
  return "";
}

function matchesField(field: ResponseField, key: string) {
  return field.labels.includes(normalizeCommandName(key));
}

function normalizeCommandName(value: unknown) {
  return stringValue(value)
    .trim()
    .toLowerCase()
    .replace(/^\/+|\/+$/g, "")
    .replace(/[_\s.-]+/g, "/")
    .replace(/\/+/g, "/");
}

function stringValue(value: unknown) {
  return typeof value === "string" ? value : "";
}
