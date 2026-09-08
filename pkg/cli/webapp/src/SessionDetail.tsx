import { useCallback, useMemo } from "react";

import {
  SessionChatComposer,
  SessionContextMeter,
  SessionInspector,
  getSessionMetadata,
} from "@flanksource/clicky-ui/ai";

import {
  type SessionGetItem,
  type SessionGetResult,
  errorMessage,
} from "./sessionData";
import { sessionResultCollection } from "./sessionCollection";
import {
  findSessionForApproval,
  resolveSessionApproval,
  type ApprovalResolveAction,
} from "./sessionApprovals";
import { RunVerification } from "./RunVerification";
import {
  mergeSessionMessages,
  useSessionChat,
} from "./hooks/useSessionChat";

const SESSIONS_API = "/api/chat/sessions";

export function SessionDetail({
  result,
  loading,
  error,
  onRefresh,
}: {
  result?: SessionGetResult;
  loading: boolean;
  error: unknown;
  onRefresh: () => Promise<unknown>;
}) {
  const onResolveApproval = useResolveApproval(result?.sessions, onRefresh);

  if (loading) {
    return (
      <div className="flex min-h-0 flex-1 items-center justify-center p-6 text-sm text-muted-foreground">
        Loading session...
      </div>
    );
  }
  if (error) {
    return (
      <div className="min-h-0 flex-1 overflow-auto p-6 text-sm text-destructive">
        {errorMessage(error)}
      </div>
    );
  }
  if (!result?.sessions.length) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-sm text-muted-foreground">
        No matching sessions.
      </div>
    );
  }
  const collection = sessionResultCollection(result);
  if (collection) {
    return (
      <div className="flex h-full min-h-0 flex-col gap-density-3 overflow-auto p-density-4">
        <div className="min-h-80 flex-1">
          <SessionInspector
            session={collection}
            transcriptProps={{ defaultExpanded: false }}
            onResolveApproval={onResolveApproval}
          />
        </div>
        {result.sessions.map((item) => (
          <RunVerification key={item.captainId}
            storedReport={item.detail?.structuredOutput?.verify}
            title={`Verification · ${item.summary.title || item.captainId}`}
          />
        ))}
      </div>
    );
  }
  return (
    <div className="h-full min-h-0 overflow-auto">
      {result.sessions.map((item) => (
        <SessionGetItemDetail
          key={item.captainId}
          item={item}
          single={result.sessions.length === 1}
          onRefresh={onRefresh}
          onResolveApproval={onResolveApproval}
        />
      ))}
    </div>
  );
}

function SessionGetItemDetail({
  item,
  single,
  onRefresh,
  onResolveApproval,
}: {
  item: SessionGetItem;
  single: boolean;
  onRefresh: () => Promise<unknown>;
  onResolveApproval: (
    approvalId: string,
    action: ApprovalResolveAction,
    message?: string,
  ) => Promise<void>;
}) {
  const chat = useSessionChat({
    initialRunID: item.activeRunId,
    sessionID: item.captainId,
    initialCapabilities: item.chat,
    initialState: item.chatState,
    clearOnTerminal: true,
    onTerminal: onRefresh,
  });
  const detail = useMemo(
    () =>
      item.detail
        ? {
            ...item.detail,
            messages: mergeSessionMessages(
              item.detail.messages ?? [],
              chat.messages,
            ),
          }
        : undefined,
    [chat.messages, item.detail],
  );
  const composerToolbar = useMemo(() => {
    const metadata = detail ? getSessionMetadata(detail) : undefined;
    if (!metadata?.context) return undefined;
    return (
      <div className="flex flex-1 items-center gap-2">
        <div className="flex-1" />
        <SessionContextMeter metadata={metadata} mode="gauge" />
      </div>
    );
  }, [detail]);
  const composer =
    item.chat?.resume || item.activeRunId ? (
      <SessionChatComposer
        status={chat.chatState.status}
        capabilities={chat.capabilities}
        queued={chat.chatState.queued}
        error={chat.actionError}
        onSubmit={chat.send}
        onInterrupt={chat.interrupt}
        {...(composerToolbar ? { toolbar: composerToolbar } : {})}
      />
    ) : undefined;
  return (
    <section
      className={
        single ? "flex h-full min-h-0 flex-col" : "border-b border-border"
      }
    >
      {!single ? (
        <div className="shrink-0 border-b border-border px-density-4 py-density-3 text-xs">
          <div className="font-mono font-semibold text-foreground">
            {item.captainId}
          </div>
          <div className="mt-1 flex flex-wrap gap-x-density-3 gap-y-1 text-muted-foreground">
            <span>{item.summary.source}</span>
            {item.providerSessionId && (
              <span>provider={item.providerSessionId}</span>
            )}
            {item.host && <span>host={item.host}</span>}
            {item.summary.project && (
              <span>project={item.summary.project}</span>
            )}
            {item.summary.cwd && (
              <span className="max-w-full truncate">{item.summary.cwd}</span>
            )}
          </div>
        </div>
      ) : null}
      {detail ? (
        <div className={single ? "min-h-80 flex-1" : "h-[70vh] min-h-[32rem]"}>
          <SessionInspector
            session={detail}
            transcriptProps={{ defaultExpanded: false }}
            onResolveApproval={onResolveApproval}
            {...(composer ? { composer } : {})}
          />
        </div>
      ) : (
        <div className="p-density-4 text-sm text-muted-foreground">
          Transcript unavailable.
        </div>
      )}
      <RunVerification frame={chat.verify} storedReport={detail?.structuredOutput?.verify} />
    </section>
  );
}

/** Resolves a pending tool approval by id, regardless of which session in
 *  `sessions` owns it — the Approvals tab keys off `session.requests[]`
 *  directly, so this is the only place a sessionId needs recovering before
 *  hitting the existing `POST .../approvals/{approvalId}` endpoint. */
function useResolveApproval(
  sessions: SessionGetItem[] | undefined,
  onRefresh: () => Promise<unknown>,
) {
  return useCallback(
    async (approvalId: string, action: ApprovalResolveAction, message?: string) => {
      const sessionId = findSessionForApproval(sessions ?? [], approvalId);
      if (!sessionId) {
        throw new Error(`No session found for approval ${approvalId}.`);
      }
      await resolveSessionApproval({
        sessionsApi: SESSIONS_API,
        sessionId,
        approvalId,
        approved: action === "approve",
        ...(message ? { reason: message } : {}),
      });
      await onRefresh();
    },
    [sessions, onRefresh],
  );
}
