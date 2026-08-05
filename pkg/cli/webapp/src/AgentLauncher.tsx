import { useMemo, useState } from "react";
import { Button } from "@flanksource/clicky-ui/components";
import {
  OperationCommandPage,
  useOperations,
  type ExecutionResponse,
  type ResolvedOperation,
} from "@flanksource/clicky-ui/rpc";
import { useChatWindowManager } from "@flanksource/clicky-ui/ai";
import { apiClient } from "./api";
import { isReadOnlyDbContext } from "./dbContext";
import {
  agentModelFor,
  extractSessionId,
  isAgentOperation,
  titleFor,
  type CommandValues,
} from "./session";

type LaunchThreadResponse = {
  id: string;
  title: string;
  launchUrl: string;
  providerSessionId?: string;
};

type AgentLauncherProps = {
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
};

export function AgentLauncher({ onNavigate }: AgentLauncherProps) {
  const { operations, isLoading, error } = useOperations(apiClient);
  const operation = useMemo(
    () => operations.find(isAgentOperation),
    [operations],
  );
  const { openPanel } = useChatWindowManager();
  const [launchError, setLaunchError] = useState("");
  const [launching, setLaunching] = useState(false);

  async function handleResult(
    response: ExecutionResponse,
    _operation: ResolvedOperation,
    values: CommandValues,
  ) {
    setLaunchError("");
    if (!response.success) {
      setLaunchError(response.error || "Agent command failed.");
      return;
    }

    const sessionId = extractSessionId(response);
    if (!sessionId) {
      setLaunchError("Agent command completed without a session id.");
      return;
    }

    const model = agentModelFor(response);
    setLaunching(true);
    try {
      const thread = await createThreadFromAgent({
        providerSessionId: sessionId,
        title: titleFor(values, sessionId),
        model,
      });
      openPanel({
        threadId: thread.id,
        ...(model ? { initialModel: model } : {}),
      });
      onNavigate(thread.launchUrl || `/chat/${encodeURIComponent(thread.id)}`);
    } catch (err) {
      setLaunchError(err instanceof Error ? err.message : String(err));
    } finally {
      setLaunching(false);
    }
  }

  if (isLoading) {
    return <div className="p-6 text-sm text-muted-foreground">Loading operations...</div>;
  }

  if (error) {
    return (
      <div className="p-6 text-sm text-destructive">
        {error instanceof Error ? error.message : String(error)}
      </div>
    );
  }

  // Launching an agent creates a chat thread, which a read-only database
  // context rejects. The form is withheld rather than left to fail: its submit
  // control lives inside clicky-ui's OperationCommandPage.
  if (isReadOnlyDbContext()) {
    return (
      <div className="p-6">
        <div className="mb-4 text-sm text-muted-foreground">
          Launching agents is disabled while a read-only database context is selected.
          Switch back to the monitored database from the project picker to launch one.
        </div>
        <Button onClick={() => onNavigate("/sessions")}>Browse sessions</Button>
      </div>
    );
  }

  if (!operation) {
    return (
      <div className="p-6">
        <div className="mb-4 text-sm text-muted-foreground">
          `captain ai agent` was not found in the operation catalog.
        </div>
        <Button onClick={() => onNavigate("/operations")}>Open operations</Button>
      </div>
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4 md:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-3">
        <div className="min-w-0">
          <h1 className="truncate text-lg font-semibold">New Captain Agent</h1>
          <div className="truncate text-xs text-muted-foreground">
            {operation.operation["x-clicky"]?.command ?? "captain ai agent"}
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={() => onNavigate("/operations")}>
          Operations
        </Button>
      </div>

      {launchError && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {launchError}
        </div>
      )}
      {launching && (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
          Opening chat window...
        </div>
      )}

      <OperationCommandPage
        client={apiClient}
        operation={operation}
        operations={operations}
        autoRun={false}
        backHref="/operations"
        backLabel="Operations"
        onNavigate={onNavigate}
        onResult={handleResult}
      />
    </div>
  );
}

async function createThreadFromAgent(body: {
  title: string;
  providerSessionId: string;
  model: string;
}) {
  const response = await fetch("/api/captain/chat/threads/from-agent", {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Create chat thread failed with ${response.status}`);
  }
  return (await response.json()) as LaunchThreadResponse;
}
