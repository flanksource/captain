import type { SessionTokenSizer, SessionTokenSizingResult } from "@flanksource/clicky-ui/ai";

export const sizeSessionTokens: SessionTokenSizer = async (request, signal) => {
  const response = await fetch(`/api/captain/sessions/${encodeURIComponent(request.sessionId)}/tokens`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ method: request.method, rowIds: request.rowIds, ...(request.revision !== undefined ? { revision: request.revision } : {}) }),
    signal,
  });
  const result: SessionTokenSizingResult & { error?: string } = await response.json();
  if (!response.ok) throw new Error(result.error ?? `Token sizing failed (${response.status})`);
  return result;
};
