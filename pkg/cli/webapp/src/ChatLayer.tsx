import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChatFab,
  ChatWindow,
  useChatWindowManager,
  type ChatWindowState,
  type SpecRuntimeSandboxCatalog,
  type ToolMeta,
} from "@flanksource/clicky-ui/ai";
import type { ChatModelRuntime } from "@flanksource/clicky-ui/chat";
import { isReadOnlyDbContext } from "./dbContext";
import { fetchSandboxCatalog } from "./sandboxData";
import {
  fetchChatTools,
  RemoteAgentDelegation,
  type RemoteAgentSelection,
} from "./RemoteAgentDelegation";

const LOCAL_EXECUTION: RemoteAgentSelection = {
  backend: "",
  agent: "",
  callerTools: [],
};

export function ChatLayer() {
  // Every chat action creates or appends to a thread, and a read-only database
  // context rejects those writes. The composer is withheld entirely rather than
  // left to fail: the chat transport is built inside clicky-ui, so there is no
  // per-control disable to reach from here.
  const readOnly = isReadOnlyDbContext();
  const { panels } = useChatWindowManager();
  const tools = useQuery({
    queryKey: ["chat-tool-catalog"],
    queryFn: fetchChatTools,
    staleTime: 30_000,
  });
  const sandboxes = useQuery({
    queryKey: ["sandbox-catalog"],
    queryFn: fetchSandboxCatalog,
    staleTime: 30_000,
  });

  if (readOnly) return null;

  return (
    <>
      <ChatFab />
      {panels.map((panel) => (
        <DelegatingChatWindow
          key={panel.id}
          panel={panel}
          tools={tools.data ?? []}
          toolsLoading={tools.isLoading}
          toolsError={queryError(tools.error)}
          sandboxCatalog={sandboxes.data}
          sandboxLoading={sandboxes.isLoading}
          sandboxError={queryError(sandboxes.error)}
        />
      ))}
    </>
  );
}

type DelegatingChatWindowProps = {
  panel: ChatWindowState;
  tools: ToolMeta[];
  toolsLoading: boolean;
  toolsError?: string;
  sandboxCatalog?: SpecRuntimeSandboxCatalog;
  sandboxLoading: boolean;
  sandboxError?: string;
};

function DelegatingChatWindow({
  panel,
  tools,
  toolsLoading,
  toolsError,
  sandboxCatalog,
  sandboxLoading,
  sandboxError,
}: DelegatingChatWindowProps) {
  const [selection, setSelection] = useState<RemoteAgentSelection>(LOCAL_EXECUTION);
  const [runtimeMode, setRuntimeMode] = useState<string>();
  const previousThread = useRef(panel.threadId);

  useEffect(() => {
    if (previousThread.current && previousThread.current !== panel.threadId) {
      setSelection(LOCAL_EXECUTION);
    }
    previousThread.current = panel.threadId;
  }, [panel.threadId]);

  const sandbox = selection.backend
    ? {
        backend: selection.backend,
        ...(selection.agent ? { agent: selection.agent } : {}),
        ...(selection.callerTools.length > 0
          ? { callerTools: selection.callerTools }
          : {}),
      }
    : undefined;

  return (
    <ChatWindow
      panel={panel}
      sessionsApi="/api/chat/sessions"
      runtimesApi="/api/chat/runtimes"
      tools={tools}
      toolsApi={null}
      defaultToolPolicy="auto"
      headerExtras={(
        <RemoteAgentDelegation
          value={selection}
          onChange={setSelection}
          catalog={sandboxCatalog}
          tools={tools}
          loadingCatalog={sandboxLoading}
          loadingTools={toolsLoading}
          catalogError={sandboxError}
          toolsError={toolsError}
          runtimeMode={runtimeMode}
        />
      )}
      chat={{
        api: "/api/chat",
        modelsApi: "/api/chat/models",
        body: sandbox ? { sandbox } : {},
        onRuntimeChange: (runtime) => setRuntimeMode(modeForRuntime(runtime)),
        // No defaultModel: the served menu marks captain's own default, so a
        // literal here would only go stale or name a disabled model.
        enableAttachments: true,
        suggestions: [
          "Summarize this run",
          "Show recent changed files",
          "Check the current repo status",
        ],
        placeholder: sandbox
          ? `Send a task to ${selection.agent || selection.backend}...`
          : "Continue the agent session...",
      }}
    />
  );
}

function queryError(error: Error | null): string | undefined {
  return error?.message;
}

function modeForRuntime(runtime: ChatModelRuntime): string | undefined {
  if (runtime.backend?.endsWith("-agent")) return "agent";
  if (runtime.backend?.endsWith("-cli")) return "cli";
  if (runtime.backend?.endsWith("-cmux")) return "cmux";
  return runtime.mode;
}
