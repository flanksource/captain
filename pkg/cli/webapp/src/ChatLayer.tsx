import { useMemo } from "react";
import { ChatFab, ChatWindowLayer } from "@flanksource/clicky-ui/ai";
import { clickyOperationsToTools } from "@flanksource/clicky-ui/chat";
import { useOperations } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { isChatToolOperation } from "./session";

export function ChatLayer() {
  const { operations } = useOperations(apiClient);
  const tools = useMemo(
    () => clickyOperationsToTools(operations.filter(isChatToolOperation)),
    [operations],
  );

  return (
    <>
      <ChatFab />
      <ChatWindowLayer
        threadsApi="/api/chat/threads"
        tools={tools}
        chat={{
          api: "/api/chat",
          modelsApi: "/api/chat/models",
          defaultModel: "claude-agent-sonnet",
          enableAttachments: true,
          toolApproval: "manual",
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

