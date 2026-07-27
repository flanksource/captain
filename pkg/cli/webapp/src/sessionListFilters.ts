import { withProjectScope } from "./shellHelpers";
import type { ProjectScope, SourceFilter } from "./sessionData";

export type SessionMode = "live" | "all";

export type SessionListFilters = {
  mode: SessionMode;
  source: SourceFilter;
  query: string;
  from: string;
  to: string;
};

export const DEFAULT_SESSION_LIST_FILTERS: SessionListFilters = {
  mode: "live",
  source: "all",
  query: "",
  from: "",
  to: "",
};

export function subscribeSessionListSearch(onChange: () => void): () => void {
  window.addEventListener("popstate", onChange);
  return () => window.removeEventListener("popstate", onChange);
}

export function getSessionListSearchSnapshot(): string {
  return typeof window === "undefined" ? "" : window.location.search;
}

export function parseSessionListFilters(search: string): SessionListFilters {
  const params = new URLSearchParams(search);
  const mode = params.get("mode");
  const source = params.get("source");
  return {
    mode: mode === "all" ? "all" : "live",
    source: source === "claude" || source === "codex" ? source : "all",
    query: params.get("q") ?? "",
    from: params.get("from") ?? "",
    to: params.get("to") ?? "",
  };
}

export function withSessionListFilters(
  path: string,
  filters: SessionListFilters,
): string {
  const [pathname, rawSearch = ""] = path.split("?");
  const params = new URLSearchParams(rawSearch);
  setOptionalParam(params, "mode", filters.mode === "live" ? "" : filters.mode);
  setOptionalParam(params, "source", filters.source === "all" ? "" : filters.source);
  setOptionalParam(params, "q", filters.query);
  setOptionalParam(params, "from", filters.from);
  setOptionalParam(params, "to", filters.to);
  const nextSearch = params.toString();
  return `${pathname}${nextSearch ? `?${nextSearch}` : ""}`;
}

export function sessionListPath(
  path: string,
  projectScope: ProjectScope,
  filters: SessionListFilters,
): string {
  return withSessionListFilters(withProjectScope(path, projectScope), filters);
}

export function sessionActivityBounds(
  from: string,
  to: string,
): { from?: string; before?: string } {
  const fromDate = /^\d{4}-\d{2}-\d{2}$/.test(from)
    ? parseLocalDate(from)
    : undefined;
  const toDate = /^\d{4}-\d{2}-\d{2}$/.test(to)
    ? parseLocalDate(to)
    : undefined;
  if (fromDate && toDate && fromDate > toDate) {
    throw new Error("Session date from must not be after to.");
  }
  if (toDate) {
    toDate.setDate(toDate.getDate() + 1);
  }
  return {
    ...(from ? { from: fromDate?.toISOString() ?? from } : {}),
    ...(to ? { before: toDate?.toISOString() ?? to } : {}),
  };
}

function parseLocalDate(value: string): Date {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) {
    throw new Error(`Invalid session date "${value}".`);
  }
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const date = new Date(year, month - 1, day);
  if (
    date.getFullYear() !== year ||
    date.getMonth() !== month - 1 ||
    date.getDate() !== day
  ) {
    throw new Error(`Invalid session date "${value}".`);
  }
  return date;
}

function setOptionalParam(
  params: URLSearchParams,
  key: string,
  value: string,
) {
  if (value) {
    params.set(key, value);
  } else {
    params.delete(key);
  }
}
