import type {
  SessionCollectionInput,
  SessionCollectionItem,
  UnifiedSessionInput,
} from "@flanksource/clicky-ui/ai";
import type {
  PromptBatchHandle,
  PromptBatchRunHandle,
} from "./hooks/usePromptRunStream";
import type { SessionGetItem, SessionGetResult } from "./sessionData";

export function sessionResultCollection(
  result: SessionGetResult,
): SessionCollectionInput | undefined {
  const rootID = result.rootSessionId;
  if (!rootID) return undefined;
  const sessions = result.sessions.map(sessionCollectionItem);
  if (!sessions.some((item) => item.id === rootID)) return undefined;
  return {
    kind: "session-collection",
    id: rootID,
    currentSessionId: rootID,
    defaultSelectedSessionIds: directChildIDs(sessions, rootID),
    sessions,
  };
}

export function batchSessionCollection(
  handle: PromptBatchHandle,
  result?: SessionGetResult,
  liveSessions: ReadonlyMap<string, UnifiedSessionInput> = new Map(),
): SessionCollectionInput {
  const persisted = new Map(
    result?.sessions.map((item) => [item.captainId, item]) ?? [],
  );
  const root = persisted.get(handle.batchId);
  const sessions: SessionCollectionItem[] = [
    root
      ? sessionCollectionItem(root)
      : {
          id: handle.batchId,
          label: "Multi-model run",
          status: handle.status,
          summary: { provider: "multi-model", status: handle.status },
          session: emptySession(handle.batchId, "captain", "Multi-model run"),
        },
    ...handle.runs.map((run) => {
      const saved = persisted.get(run.sessionId);
      const item: SessionCollectionItem = saved
        ? sessionCollectionItem(saved)
        : {
            id: run.sessionId,
            parentId: handle.batchId,
            label: run.selector || run.model || run.sessionId,
            status: run.status,
            summary: {
              provider: run.backend,
              backend: run.backend,
              model: run.model,
              effort: run.effort,
              status: run.status,
            },
            session: emptySession(
              run.sessionId,
              run.backend || "captain",
              run.selector || run.model,
            ),
          };
      const live = liveSessions.get(run.sessionId);
      return live ? { ...item, session: live } : item;
    }),
  ];
  return {
    kind: "session-collection",
    id: handle.batchId,
    currentSessionId: handle.batchId,
    defaultSelectedSessionIds: handle.runs.map((run) => run.sessionId),
    sessions,
  };
}

export function batchChatTargets(handle: PromptBatchHandle) {
  return handle.runs.filter(
    (run) =>
      run.chat &&
      Boolean(
        run.capabilities?.steer ||
        run.capabilities?.followUp ||
        run.capabilities?.resume,
      ),
  );
}

export function batchChatTargetState(
  result: SessionGetResult | undefined,
  target: PromptBatchRunHandle | undefined,
) {
  if (!target) return undefined;
  return result?.sessions.find((item) => item.captainId === target.sessionId)
    ?.chatState;
}

function sessionCollectionItem(item: SessionGetItem): SessionCollectionItem {
  return {
    id: item.captainId,
    ...(item.parentSessionId ? { parentId: item.parentSessionId } : {}),
    label:
      item.summary.title ||
      item.summary.model ||
      item.summary.id ||
      item.captainId,
    status: item.summary.lifecycleStatus || item.summary.live?.status,
    summary: {
      provider: item.summary.provider || item.summary.source,
      backend: item.summary.backend,
      model: item.summary.model,
      effort: item.summary.reasoningEffort,
      status: item.summary.lifecycleStatus || item.summary.live?.status,
      updatedAt: item.summary.endedAt || item.summary.startedAt,
      cost: item.summary.costUsd,
    },
    session:
      item.detail ??
      emptySession(
        item.captainId,
        item.summary.source,
        item.summary.title || item.summary.model,
      ),
  };
}

function directChildIDs(sessions: SessionCollectionItem[], rootID: string) {
  return sessions
    .filter((item) => item.parentId === rootID)
    .map((item) => item.id);
}

function emptySession(id: string, source: string, title?: string) {
  return {
    id,
    source,
    ...(title ? { title } : {}),
    messages: [],
  } satisfies UnifiedSessionInput;
}
