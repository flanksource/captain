import type { SessionGetItem } from "./sessionData";

/** Mirrors clicky-ui's `ApprovalResolveAction` (data/ai/SessionInspector.approvals).
 *  Kept local rather than imported: this webapp still depends on a published
 *  clicky-ui version that predates the onResolveApproval prop. */
export type ApprovalResolveAction = "approve" | "deny";

type ApprovalFetch = (
  input: RequestInfo | URL,
  init?: RequestInit,
) => Promise<Response>;

/** Finds which session owns a pending `requests[]` row, so a single
 *  `onResolveApproval(approvalId, action)` callback (scoped to no particular
 *  session — SessionInspector renders one or many sessions in a hierarchy)
 *  can still resolve against the right per-session endpoint. */
export function findSessionForApproval(
  sessions: SessionGetItem[],
  approvalId: string,
): string | undefined {
  return sessions.find((item) =>
    item.detail?.requests?.some((request) => request.id === approvalId),
  )?.captainId;
}

/** POSTs an approve/deny decision to the existing tool-approval endpoint:
 *  `POST {sessionsApi}/{sessionId}/approvals/{approvalId}`. The server
 *  refuses when the approval's prompt run is no longer `waiting`; that
 *  refusal surfaces as a rejected promise carrying the server's response
 *  text, so the caller can show it next to the approval instead of leaving
 *  the Approve/Deny buttons silently inert. */
export async function resolveSessionApproval(
  params: {
    sessionsApi: string;
    sessionId: string;
    approvalId: string;
    approved: boolean;
    reason?: string;
  },
  fetcher: ApprovalFetch = fetch,
): Promise<void> {
  const endpoint = `${params.sessionsApi}/${encodeURIComponent(params.sessionId)}/approvals/${encodeURIComponent(params.approvalId)}`;
  const response = await fetcher(endpoint, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": "application/json",
    },
    body: JSON.stringify(
      params.reason
        ? { approved: params.approved, reason: params.reason }
        : { approved: params.approved },
    ),
  });
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(
      `Tool approval failed with status ${response.status}${detail ? `: ${detail}` : "."}`,
    );
  }
}
