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

/** Mirrors clicky-ui's `ApprovalDecisionFields` (data/ai/approval-request), the
 *  typed part of a decision beyond approve/deny + message. Kept local for the
 *  same reason as `ApprovalResolveAction`; import it once the pinned clicky-ui
 *  release carries it. `grants` is captain's `api.NativeSandboxPolicy` JSON. */
export interface ApprovalDecisionExtras {
  answers?: Record<string, string | string[]>;
  interrupt?: boolean;
  scope?: "turn" | "session";
  grants?: object;
  content?: Record<string, unknown>;
}

/** The POST body for a decision. Answers travel as `updatedInput.answers` and
 *  form content as `updatedInput` itself, the conventions captain's
 *  `api.ApprovalDecision` documents. A combination captain would refuse is
 *  refused here, so it fails on the row instead of being silently trimmed. */
export function approvalDecisionBody(params: {
  approved: boolean;
  reason?: string;
  decision?: ApprovalDecisionExtras;
}): Record<string, unknown> {
  const { approved, reason, decision = {} } = params;
  const { answers, interrupt, scope, grants, content } = decision;
  if (!approved && (answers || content || scope || grants)) {
    throw new Error("A denied approval cannot carry answers, content, a scope or grants.");
  }
  if (approved && interrupt) {
    throw new Error("Interrupt needs a denial: cancel an approval by denying it.");
  }
  if (answers && content) {
    throw new Error("A decision carries either answers or content, not both.");
  }
  const updatedInput = answers ? { answers } : content;
  return {
    approved,
    ...(reason ? { reason } : {}),
    ...(updatedInput ? { updatedInput } : {}),
    ...(interrupt ? { interrupt } : {}),
    ...(scope ? { scope } : {}),
    ...(grants ? { grants } : {}),
  };
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
    decision?: ApprovalDecisionExtras;
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
    body: JSON.stringify(approvalDecisionBody(params)),
  });
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(
      `Tool approval failed with status ${response.status}${detail ? `: ${detail}` : "."}`,
    );
  }
}
