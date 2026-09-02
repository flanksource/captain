import { useMemo } from "react";
import {
  ChatFab,
  ChatWindowLayer,
  familiesFromRuntimeCatalog,
} from "@flanksource/clicky-ui/ai";
import { clickyOperationsToTools } from "@flanksource/clicky-ui/chat";
import { useOperations } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { isReadOnlyDbContext } from "./dbContext";
import { isChatToolOperation } from "./session";
import { useWhoamiCatalog } from "./whoamiCatalog";

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
  const whoamiCatalogQuery = useWhoamiCatalog();
  const runtimeFamilies = useMemo(
    () => whoamiCatalogQuery.data
      ? familiesFromRuntimeCatalog(whoamiCatalogQuery.data.runtimes)
      : undefined,
    [whoamiCatalogQuery.data],
  );

  if (readOnly) return null;

  return (
    <>
      <ChatFab />
      <ChatWindowLayer
        sessionsApi="/api/chat/sessions"
        tools={tools}
        defaultToolPolicy="auto"
        chat={{
          api: "/api/chat",
          models: whoamiCatalogQuery.data?.models ?? [],
          modelsApi: null,
          runtimeFamilies,
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
