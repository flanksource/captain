import { useId, useMemo } from "react";
import {
  Button,
  Combobox,
  DropdownMenu,
  type ComboboxOption,
} from "@flanksource/clicky-ui/components";
import {
  Icon,
  UiRobotAi,
} from "@flanksource/clicky-ui/data";
import type {
  SpecRuntimeSandboxCatalog,
  ToolMeta,
} from "@flanksource/clicky-ui/ai";
import { normalizeToolPolicy } from "@flanksource/clicky-ui/chat";

// This control owns only the serialized delegation request. The supervisor
// resolves policy and issues the task capability after the user submits a turn;
// no credential or durable authority is present in browser state.

export type RemoteAgentSelection = {
  backend: string;
  agent: string;
  callerTools: string[];
};

type RemoteAgentDelegationProps = {
  value: RemoteAgentSelection;
  onChange: (value: RemoteAgentSelection) => void;
  catalog?: SpecRuntimeSandboxCatalog;
  tools: ToolMeta[];
  loadingCatalog: boolean;
  loadingTools: boolean;
  catalogError?: string;
  toolsError?: string;
  runtimeMode?: string;
};

type GitAgentBackend = {
  name: string;
  agents: string[];
};

/** Selects the remote Git agent and the exact supervisor tools requested for delegation. */
export function RemoteAgentDelegation({
  value,
  onChange,
  catalog,
  tools,
  loadingCatalog,
  loadingTools,
  catalogError,
  toolsError,
  runtimeMode,
}: RemoteAgentDelegationProps) {
  const runtimeErrorId = useId();
  const backends = useMemo(() => gitAgentBackends(catalog), [catalog]);
  const selected = backends.find((backend) => backend.name === value.backend);
  const backendOptions = useMemo<ComboboxOption[]>(
    () => [
      {
        value: "",
        label: "Run on this supervisor",
        description: "Do not dispatch this turn to a remote Git agent.",
      },
      ...backends.map((backend) => ({
        value: backend.name,
        label: backend.name,
        description: backend.agents.length > 0
          ? `${backend.agents.length} dispatchable agent${backend.agents.length === 1 ? "" : "s"}`
          : "No dispatchable agents",
        disabled: backend.agents.length === 0,
      })),
    ],
    [backends],
  );
  const agentOptions = useMemo<ComboboxOption[]>(
    () => (selected?.agents ?? []).map((agent) => ({ value: agent, label: agent })),
    [selected],
  );
  const toolOptions = useMemo<ComboboxOption[]>(
    () => tools.map((tool) => ({
      value: tool.name,
      label: tool.parent ? `${tool.parent}: ${tool.label}` : tool.label,
      description: tool.description,
      title: tool.name,
    })),
    [tools],
  );
  const remote = value.backend !== "";
  const incompatibleRuntime = runtimeMode !== undefined && runtimeMode !== "agent";
  const triggerTitle = remote
    ? `Remote Git agent: ${value.agent || value.backend}; ${value.callerTools.length} delegated tool${value.callerTools.length === 1 ? "" : "s"}`
    : "Run on this supervisor";

  return (
    <DropdownMenu
      align="right"
      menuLabel="Remote Git agent delegation"
      menuClassName="w-96 max-w-[calc(100vw-2rem)] p-3"
      trigger={(
        <Button
          type="button"
          variant={remote ? "outline" : "ghost"}
          size="icon"
          title={triggerTitle}
          aria-label={triggerTitle}
          className={remote ? "relative border-primary/50 text-primary" : ""}
        >
          <Icon icon={UiRobotAi} className="size-4" />
          {remote && value.callerTools.length > 0 && (
            <span className="absolute -right-1 -top-1 min-w-4 rounded-full bg-primary px-1 text-center text-[9px] leading-4 text-primary-foreground">
              {value.callerTools.length}
            </span>
          )}
        </Button>
      )}
    >
      {() => (
        <div className="grid gap-3" onPointerDown={(event) => event.stopPropagation()}>
          <div>
            <div className="text-sm font-semibold">Remote Git agent</div>
            <p className="mt-1 text-xs text-muted-foreground">
              Dispatch this turn remotely and delegate only the selected supervisor tools.
            </p>
          </div>

          <label className="grid gap-1 text-xs font-medium">
            Execution
            <Combobox
              value={value.backend}
              onChange={(backend) => onChange(backend
                ? { ...value, backend, agent: "" }
                : { backend: "", agent: "", callerTools: [] })}
              options={backendOptions}
              placeholder="Run on this supervisor"
              ariaLabel="Execution location"
              allowCustomValue={false}
              loading={loadingCatalog}
            />
          </label>

          {catalogError && (
            <p role="alert" className="text-xs text-destructive">{catalogError}</p>
          )}

          {remote && (
            <>
              <label className="grid gap-1 text-xs font-medium">
                Agent
                <Combobox
                  value={value.agent}
                  onChange={(agent) => onChange({ ...value, agent })}
                  options={agentOptions}
                  placeholder="Any dispatchable agent"
                  ariaLabel="Remote Git agent"
                  allowCustomValue={false}
                />
              </label>

              <label className="grid gap-1 text-xs font-medium">
                Delegated supervisor tools
                <Combobox
                  multiple
                  value={value.callerTools}
                  onChange={(callerTools) => onChange({ ...value, callerTools })}
                  options={toolOptions}
                  placeholder={loadingTools ? "Loading tools..." : "Search and select tools"}
                  ariaLabel="Delegated supervisor tools"
                  allowCustomValue={false}
                  loading={loadingTools}
                  disabled={incompatibleRuntime && value.callerTools.length === 0}
                  invalid={incompatibleRuntime && value.callerTools.length > 0}
                  describedBy={incompatibleRuntime ? runtimeErrorId : undefined}
                />
              </label>

              {incompatibleRuntime && (
                <p id={runtimeErrorId} role="alert" className="text-xs text-destructive">
                  Delegated tools require an Agent runtime. The selected {runtimeMode} runtime cannot connect to Captain’s caller-tool endpoint.
                </p>
              )}

              {toolsError && (
                <p role="alert" className="text-xs text-destructive">{toolsError}</p>
              )}

              <div className="rounded-md border border-border bg-muted/40 px-2.5 py-2 text-xs text-muted-foreground">
                {value.callerTools.length === 0
                  ? "No supervisor tools will be exposed to the remote model."
                  : `${value.callerTools.length} tool${value.callerTools.length === 1 ? "" : "s"} requested. The supervisor’s effective allow/deny/ask policy can only narrow this list.`}
                <div className="mt-1">
                  Captain issues a fresh task-scoped capability at dispatch and never stores it in Git.
                </div>
                <div className="mt-1">
                  Caller tools require an Agent runtime; CLI and cmux runs cannot use them.
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </DropdownMenu>
  );
}

/** Loads the canonical tools backed by the supervisor's live handlers. */
export async function fetchChatTools(): Promise<ToolMeta[]> {
  const response = await fetch("/api/chat/tools", {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `Tool catalog failed with ${response.status}`);
  }
  const body = (await response.json()) as { tools?: ChatToolCatalogEntry[] };
  return (body.tools ?? []).map((tool) => ({
    ...tool,
    label: tool.title || tool.operationName || tool.name,
    defaultPermission: normalizeToolPolicy(tool.defaultPermission),
  }));
}

type ChatToolCatalogEntry = Omit<ToolMeta, "label" | "defaultPermission"> & {
  title?: string;
  defaultPermission?: string;
};

function gitAgentBackends(catalog?: SpecRuntimeSandboxCatalog): GitAgentBackend[] {
  const kind = catalog?.kinds?.find((item) => item.kind === "git-agent");
  if (!kind) return [];
  const backends = kind.backends ?? [];
  if (backends.length === 0) {
    return [{ name: kind.kind, agents: [] }];
  }
  return backends.map((backend) => ({
    name: backend.name,
    agents: (backend.agents ?? [])
      .filter((agent) => agent.dispatchable && (!agent.status || agent.status === "enrolled"))
      .map((agent) => agent.name),
  }));
}
