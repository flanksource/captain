import { apiClient } from "./api";
import { parseServerTiming, type TimingMetric } from "./serverTiming";
import {
  fetchLiveSessions,
  projectScopeQuery,
  type ProjectScope,
  type SessionListResult,
  type SourceFilter,
} from "./sessionData";
import type { SessionMode } from "./sessionListFilters";

export async function fetchSessionListPage(params: {
  mode: SessionMode;
  source: SourceFilter;
  project: ProjectScope;
  query?: string;
  from?: string;
  before?: string;
  limit?: number;
  cursor?: string;
}): Promise<SessionListResult & { timing?: TimingMetric[] }> {
  if (params.mode === "live") {
    return fetchLiveSessions(params);
  }
  const response = await apiClient.executeCommand(
    "/api/v1/sessions",
    "GET",
    {
      source: params.source,
      ...projectScopeQuery(params.project),
      q: params.query ?? "",
      limit: String(params.limit ?? 100),
      ...(params.from ? { from: params.from } : {}),
      ...(params.before ? { before: params.before } : {}),
      ...(params.cursor ? { cursor: params.cursor } : {}),
    },
    { Accept: "application/json" },
  );
  if (!response.success) {
    throw new Error(response.error || "Failed to load sessions.");
  }
  const timing = parseServerTiming(response.responseHeaders?.["server-timing"]);
  return {
    ...(response.parsed as SessionListResult),
    ...(timing.length ? { timing } : {}),
  };
}
