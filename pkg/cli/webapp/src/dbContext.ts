import { apiClient } from "./api";

/**
 * The browser selects its database context with a cookie rather than a header:
 * EventSource streams and the embedded chat transport cannot set request
 * headers, and a cookie rides every transport with no per-call wiring.
 */
export const DB_CONTEXT_COOKIE = "captain_db_context";
export const DEFAULT_DB_CONTEXT = "default";

export type DbContextOption = {
  name: string;
  label: string;
  source: string;
  dsn: string;
  default: boolean;
  readOnly: boolean;
  status?: string;
};

export type DbContextsResult = {
  active: string;
  default: string;
  contexts: DbContextOption[];
};

export function getDbContext(): string {
  if (typeof document === "undefined") return DEFAULT_DB_CONTEXT;
  const entry = document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(`${DB_CONTEXT_COOKIE}=`));
  const value = entry ? decodeURIComponent(entry.slice(DB_CONTEXT_COOKIE.length + 1)) : "";
  return value || DEFAULT_DB_CONTEXT;
}

/**
 * Switching context changes the identity of every cached datum, and only a
 * reload re-establishes the open SSE streams and the chat transport, whose URLs
 * do not change. Switching is a rare, deliberate action, so a reload is the
 * correct formulation rather than selective query invalidation.
 */
export function setDbContext(name: string): void {
  if (typeof document === "undefined") return;
  const next = name || DEFAULT_DB_CONTEXT;
  document.cookie =
    next === DEFAULT_DB_CONTEXT
      ? `${DB_CONTEXT_COOKIE}=; Path=/; SameSite=Lax; Max-Age=0`
      : `${DB_CONTEXT_COOKIE}=${encodeURIComponent(next)}; Path=/; SameSite=Lax; Max-Age=31536000`;
  window.location.reload();
}

/**
 * Only the default context is written to, so any other selection is read-only
 * by construction — no round trip needed to know it. Controls that would POST
 * are disabled rather than left to fail with the server's 409.
 */
export function isReadOnlyDbContext(): boolean {
  return getDbContext() !== DEFAULT_DB_CONTEXT;
}

export async function fetchDbContexts(): Promise<DbContextsResult> {
  const response = await apiClient.executeCommand(
    "/api/captain/contexts",
    "GET",
    {},
    { Accept: "application/json" },
  );
  if (!response.success) {
    // A cookie naming a context that has since been removed from the config
    // would otherwise wedge every request; drop it and start over.
    if (isUnknownContextError(response)) {
      setDbContext(DEFAULT_DB_CONTEXT);
    }
    throw new Error(response.error || "Failed to load database contexts.");
  }
  return response.parsed as DbContextsResult;
}

function isUnknownContextError(response: { parsed?: unknown; error?: string }): boolean {
  const code = (response.parsed as { code?: string } | undefined)?.code;
  return code === "unknown_context" || Boolean(response.error?.includes("unknown_context"));
}
