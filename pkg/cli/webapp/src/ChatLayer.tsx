import { useMemo } from "react";
import { ChatFab, ChatWindowLayer } from "@flanksource/clicky-ui/ai";
import { clickyOperationsToTools } from "@flanksource/clicky-ui/chat";
import { useOperations } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { isReadOnlyDbContext } from "./dbContext";
import { isChatToolOperation } from "./session";

export function ChatLayer() {
  const { operations } = useOperations(apiClient);
  // Every chat action creates or appends to a thread, and a read-only database
  // context rejects those writes. The composer is withheld entirely rather than
  // left to fail: the chat transport is built inside clicky-ui, so there is no
  // per-control disable to reach from here.
  const readOnly = isReadOnlyDbContext();
  const tools = useMemo(
    () => clickyOperationsToTools(operations.filter(isChatToolOperation)),
    [operations],
  );

  if (readOnly) return null;

  return (
    <>
      <ChatFab />
      <ChatWindowLayer
        sessionsApi="/api/chat/sessions"
        runtimesApi="/api/chat/runtimes"
        tools={tools}
        defaultToolMode="auto"
        chat={{
          api: "/api/chat",
          modelsApi: "/api/chat/models",
          // No defaultModel: the served menu marks captain's own default, so a
          // literal here would only go stale or name a disabled model.
          enableAttachments: true,
          suggestions: [
            "Summarize this run",
            "Show recent changed files",
            "Check the current repo status",
          ],
          placeholder: "Continue the agent session...",
        }}
      />
    </>
  );
}
