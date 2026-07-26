import type {
  ExecutionResponse,
  OperationCommandPageProps,
  ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";

export type CommandValues = Parameters<
  NonNullable<OperationCommandPageProps["onResult"]>
>[2];

const DEFAULT_AGENT_MODEL = "claude-sonnet-5";

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

export function agentModelFor(values: CommandValues) {
  const backend = stringValue(values.backend).toLowerCase();
  const model = stringValue(values.model).toLowerCase();

  if (
    backend.includes("codex") ||
    model.includes("codex") ||
    model.includes("gpt-5-codex")
  ) {
    return "gpt-5.6-sol";
  }
  if (model.includes("opus")) return "claude-opus-5";
  if (model.includes("haiku")) return "claude-haiku-4-5";
  if (model.startsWith("claude-agent-")) return model.replace(/^claude-agent-/, "claude-");
  return DEFAULT_AGENT_MODEL;
}

export function titleFor(values: CommandValues, sessionId: string) {
  const prompt = stringValue(values.prompt).trim();
  if (!prompt) return `Captain agent ${sessionId}`;
  return prompt.length > 96 ? `${prompt.slice(0, 93)}...` : prompt;
}

export function extractSessionId(response: ExecutionResponse) {
  return (
    stringFromHeaders(response.responseHeaders) ||
    sessionIdFromValue(response.parsed) ||
    sessionIdFromText(response.stdout) ||
    sessionIdFromText(response.output)
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

function sessionIdFromValue(value: unknown, parentKey = ""): string {
  if (value == null) return "";
  if (typeof value === "string") {
    return looksLikeSessionKey(parentKey) ? value.trim() : "";
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = sessionIdFromValue(item, parentKey);
      if (found) return found;
    }
    return "";
  }
  if (typeof value !== "object") return "";

  const record = value as Record<string, unknown>;
  for (const key of [
    "sessionId",
    "sessionID",
    "session_id",
    "SessionID",
    "ProviderSessionID",
    "providerSessionId",
  ]) {
    const direct = stringValue(record[key]).trim();
    if (direct) return direct;
  }

  const fieldName = stringValue(record.name) || stringValue(record.label) || parentKey;
  if (looksLikeSessionKey(fieldName)) {
    const text = nodeText(record.value) || nodeText(record);
    if (text) return text;
  }

  for (const key of ["node", "fields", "rows", "value", "children", "items"]) {
    const found = sessionIdFromValue(record[key], fieldName);
    if (found) return found;
  }

  for (const [key, nested] of Object.entries(record)) {
    const found = sessionIdFromValue(nested, key);
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

function sessionIdFromText(text: string | undefined) {
  if (!text) return "";
  try {
    const parsed = JSON.parse(text) as unknown;
    const found = sessionIdFromValue(parsed);
    if (found) return found;
  } catch {
    // Fall through to the plain-text patterns.
  }
  const match =
    text.match(/"sessionId"\s*:\s*"([^"]+)"/i) ||
    text.match(/"session_id"\s*:\s*"([^"]+)"/i) ||
    text.match(/\bSession\s+([A-Za-z0-9_.:-]+)/i);
  return match?.[1]?.trim() ?? "";
}

function looksLikeSessionKey(key: string) {
  const normalized = normalizeCommandName(key);
  return (
    normalized === "session" ||
    normalized === "session/id" ||
    normalized === "sessionid" ||
    normalized === "provider/session/id" ||
    normalized === "providersessionid"
  );
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
