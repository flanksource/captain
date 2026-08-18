import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Panel } from "@flanksource/clicky-ui/components";
import { Badge } from "@flanksource/clicky-ui/data";
import type { SpecRuntimeSandboxKind } from "@flanksource/clicky-ui/ai";

import { GitAgentDeployModal } from "./GitAgentDeployModal";
import { GitAgentEnrollModal } from "./GitAgentEnrollModal";
import { GitAgentTasks } from "./GitAgentTasks";
import { GitAgentWhoami } from "./GitAgentWhoami";
import { SandboxCredentials } from "./SandboxCredentials";
import {
  fetchGitAgents,
  fetchSandboxCatalog,
  isDispatchable,
  isPending,
  revokeGitAgent,
  undeployGitAgent,
  type GitAgent,
} from "./sandboxData";

/**
 * The name of the git-agent backend this page administers. The routes accept
 * any configured backend; the page shows the one named after the adapter kind,
 * which is what `captain sandbox git-agent` defaults to.
 */
const GIT_AGENT_BACKEND = "git-agent";

export function SandboxesPage() {
  const queryClient = useQueryClient();
  const [enrolling, setEnrolling] = useState(false);
  const [deploying, setDeploying] = useState(false);
  const [editing, setEditing] = useState<GitAgent | undefined>();

  const catalog = useQuery({
    queryKey: ["sandbox-catalog"],
    queryFn: fetchSandboxCatalog,
  });
  const agents = useQuery({
    queryKey: ["git-agents", GIT_AGENT_BACKEND],
    queryFn: () => fetchGitAgents(GIT_AGENT_BACKEND),
  });

  const refreshAgents = () =>
    void queryClient.invalidateQueries({
      queryKey: ["git-agents", GIT_AGENT_BACKEND],
    });

  return (
    <div className="h-full space-y-density-4 overflow-auto p-density-4">
      <Panel title="Sandbox backends">
        {catalog.error ? (
          <ErrorText error={catalog.error} />
        ) : (
          <SandboxKindTable
            kinds={catalog.data?.kinds ?? []}
            defaultSelector={catalog.data?.default}
          />
        )}
        {(catalog.data?.invalid?.length ?? 0) > 0 && (
          <div className="mt-density-3 space-y-1">
            {catalog.data?.invalid?.map((backend) => (
              <p
                key={backend.name}
                role="alert"
                className="text-xs text-destructive"
              >
                Backend <strong>{backend.name}</strong> is unusable:{" "}
                {backend.error}
              </p>
            ))}
          </div>
        )}
      </Panel>

      <Panel
        title="Remote git-agents"
        actions={
          <div className="flex gap-density-2">
            {/* Enroll prints a command to run elsewhere; deploy creates the
                machine here. Deploy leads because it is the whole job. */}
            <Button size="sm" onClick={() => setDeploying(true)}>
              Deploy agent
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setEnrolling(true)}
            >
              Enroll existing host
            </Button>
          </div>
        }
      >
        {agents.error ? (
          <ErrorText error={agents.error} />
        ) : (
          <GitAgentTable
            agents={agents.data ?? []}
            loading={agents.isLoading}
            onEdit={setEditing}
            onChanged={refreshAgents}
          />
        )}
      </Panel>

      {/* Below the roster it serves: the Secret this publishes is what the
          deploy form's "Agent login Secret" names. */}
      <SandboxCredentials />

      <Panel title="Remote tasks">
        <GitAgentTasks />
      </Panel>

      <GitAgentDeployModal
        open={deploying}
        backend={GIT_AGENT_BACKEND}
        onClose={() => setDeploying(false)}
        onDeployed={refreshAgents}
      />
      {editing?.deployment && (
        <GitAgentDeployModal
          key={editing.name}
          open
          backend={GIT_AGENT_BACKEND}
          edit={{ name: editing.name, deployment: editing.deployment }}
          onClose={() => setEditing(undefined)}
          onDeployed={refreshAgents}
        />
      )}
      <GitAgentEnrollModal
        open={enrolling}
        backend={GIT_AGENT_BACKEND}
        onClose={() => setEnrolling(false)}
        onEnrolled={refreshAgents}
      />
    </div>
  );
}

function SandboxKindTable({
  kinds,
  defaultSelector,
}: {
  kinds: SpecRuntimeSandboxKind[];
  defaultSelector?: string | undefined;
}) {
  if (kinds.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">No sandbox adapters.</p>
    );
  }
  return (
    <table className="w-full text-left text-xs">
      <thead className="text-muted-foreground">
        <tr>
          <Th>Kind</Th>
          <Th>Capabilities</Th>
          <Th>Runtime modes</Th>
          <Th>Configured backends</Th>
        </tr>
      </thead>
      <tbody>
        {kinds.map((kind) => (
          <tr key={kind.kind} className="border-t border-border align-top">
            <Td>
              <div className="flex items-center gap-density-2">
                <span className="font-medium">{kind.kind}</span>
                {defaultSelector === kind.kind && <Badge>default</Badge>}
              </div>
              <p className="text-muted-foreground">{kind.description}</p>
            </Td>
            <Td>
              <Chips values={kind.capabilities ?? []} empty="none" />
            </Td>
            <Td>
              <Chips values={kind.modes ?? []} empty="none" />
            </Td>
            <Td>
              {(kind.backends?.length ?? 0) === 0 ? (
                <span className="text-muted-foreground">—</span>
              ) : (
                <ul className="space-y-1">
                  {kind.backends?.map((backend) => (
                    <li
                      key={backend.name}
                      className="flex items-center gap-density-2"
                    >
                      <span className="font-mono">{backend.name}</span>
                      {defaultSelector === backend.name && (
                        <Badge>default</Badge>
                      )}
                      {(backend.agents?.length ?? 0) > 0 && (
                        <span className="text-muted-foreground">
                          {backend.agents?.length} agent
                          {backend.agents?.length === 1 ? "" : "s"}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function GitAgentTable({
  agents,
  loading,
  onEdit,
  onChanged,
}: {
  agents: GitAgent[];
  loading: boolean;
  onEdit: (agent: GitAgent) => void;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState<string | undefined>();
  const [error, setError] = useState<string | undefined>();
  const [inspecting, setInspecting] = useState<string | undefined>();

  if (loading) {
    return <p className="text-xs text-muted-foreground">Loading agents…</p>;
  }
  if (agents.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No agents yet. Deploying one creates a sidecar on docker or kubernetes;
        enrolling one prints a join command to run on a host you already have.
      </p>
    );
  }

  const run = async (name: string, action: () => Promise<unknown>) => {
    setBusy(name);
    setError(undefined);
    try {
      await action();
      onChanged();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(undefined);
    }
  };

  const revoke = (agent: GitAgent) => {
    // Revocation rewrites ~/.captain.yaml and takes effect for connections
    // established after it, so confirm before dropping a working agent.
    if (
      !window.confirm(
        `Revoke ${agent.name}? Its key is refused from now on and its token is revoked, ` +
          `but the machine keeps running — use Undeploy to remove it.`,
      )
    ) {
      return;
    }
    void run(agent.name, () =>
      revokeGitAgent({ backend: GIT_AGENT_BACKEND, name: agent.name }),
    );
  };

  const undeploy = (agent: GitAgent) => {
    const deployment = agent.deployment;
    if (!deployment) return;
    // Undeploy removes the workload and revokes the agent together. The state
    // volume is kept unless asked: it holds the agent's private key, so purging
    // it makes the agent unrecoverable rather than merely stopped.
    if (
      !window.confirm(
        `Undeploy ${agent.name}? This removes ${deployment.workload} from ` +
          `${deployment.target}${deployment.namespace ? ` (${deployment.namespace})` : ""}, ` +
          `revokes its key and token, and keeps the state volume.`,
      )
    ) {
      return;
    }
    void run(agent.name, () =>
      undeployGitAgent({ backend: GIT_AGENT_BACKEND, name: agent.name }),
    );
  };

  return (
    <div className="space-y-density-2">
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <table className="w-full text-left text-xs">
        <thead className="text-muted-foreground">
          <tr>
            <Th>Agent</Th>
            <Th>Status</Th>
            <Th>Runs on</Th>
            <Th>Endpoint</Th>
            <Th>Added</Th>
            <Th> </Th>
          </tr>
        </thead>
        <tbody>
          {agents.map((agent) => (
            <AgentRows
              key={`${agent.name}-${agent.status}`}
              agent={agent}
              busy={busy === agent.name}
              inspecting={inspecting === agent.name}
              onInspect={() =>
                setInspecting((current) =>
                  current === agent.name ? undefined : agent.name,
                )
              }
              onEdit={() => onEdit(agent)}
              onUndeploy={() => undeploy(agent)}
              onRevoke={() => revoke(agent)}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AgentRows({
  agent,
  busy,
  inspecting,
  onInspect,
  onEdit,
  onUndeploy,
  onRevoke,
}: {
  agent: GitAgent;
  busy: boolean;
  inspecting: boolean;
  onInspect: () => void;
  onEdit: () => void;
  onUndeploy: () => void;
  onRevoke: () => void;
}) {
  const inspectable =
    isDispatchable(agent) && agent.url?.toLowerCase().startsWith("https://");
  return (
    <>
      <tr className="border-t border-border">
        <Td>
          <span className="font-medium">{agent.name}</span>
        </Td>
        <Td>
          <AgentStatus agent={agent} />
        </Td>
        <Td>
          <AgentRuntime agent={agent} />
        </Td>
        <Td>
          <span className="font-mono">{agent.url ?? "—"}</span>
        </Td>
        <Td>
          {agent.addedAt ? new Date(agent.addedAt).toLocaleString() : "—"}
        </Td>
        <Td>
          <div className="flex justify-end gap-density-1">
            {inspectable && (
              <Button
                size="sm"
                variant="ghost"
                aria-label={`${inspecting ? "Hide" : "Inspect"} ${agent.name} runtimes`}
                onClick={onInspect}
              >
                {inspecting ? "Hide whoami" : "Whoami"}
              </Button>
            )}
            {agent.deployment?.config && (
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={onEdit}
              >
                Edit
              </Button>
            )}
            {agent.deployment && (
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={onUndeploy}
              >
                {busy ? "Working…" : "Undeploy"}
              </Button>
            )}
            {!isPending(agent) && !agent.deployment && (
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={onRevoke}
              >
                {busy ? "Revoking…" : "Revoke"}
              </Button>
            )}
          </div>
        </Td>
      </tr>
      {inspecting && (
        <tr className="border-t border-border bg-muted/20">
          <td colSpan={6} className="px-density-3 py-density-3">
            <GitAgentWhoami backend={GIT_AGENT_BACKEND} agent={agent.name} />
          </td>
        </tr>
      )}
    </>
  );
}

/**
 * Where the sidecar runs, when captain placed it. An agent enrolled by hand
 * shows nothing — captain does not know, which is also why it cannot offer to
 * tear that one down.
 */
function AgentRuntime({ agent }: { agent: GitAgent }) {
  const deployment = agent.deployment;
  if (!deployment) {
    return <span className="text-muted-foreground">self-managed</span>;
  }
  return (
    <span>
      {deployment.target}
      {deployment.namespace && (
        <span className="text-muted-foreground"> / {deployment.namespace}</span>
      )}
    </span>
  );
}

function AgentStatus({ agent }: { agent: GitAgent }) {
  if (isPending(agent)) {
    return (
      <span className="text-muted-foreground">
        deployed — waiting to enroll
      </span>
    );
  }
  if (!isDispatchable(agent)) {
    return (
      <span className="text-amber-600 dark:text-amber-400">
        enrolled — not dispatchable (
        {agent.dispatchIssue ?? "missing dispatch credential"})
      </span>
    );
  }
  return <span>enrolled</span>;
}

function Chips({ values, empty }: { values: string[]; empty: string }) {
  if (values.length === 0) {
    return <span className="text-muted-foreground">{empty}</span>;
  }
  return (
    <ul className="flex flex-wrap gap-1">
      {values.map((value) => (
        <li
          key={value}
          className="rounded-full border border-border bg-muted px-2 py-0.5 font-mono text-[10px]"
        >
          {value}
        </li>
      ))}
    </ul>
  );
}

function Th({ children }: { children: ReactNode }) {
  return <th className="px-density-2 py-1 font-medium">{children}</th>;
}

function Td({ children }: { children: ReactNode }) {
  return <td className="px-density-2 py-1.5">{children}</td>;
}

function ErrorText({ error }: { error: unknown }) {
  return (
    <p role="alert" className="text-xs text-destructive">
      {error instanceof Error ? error.message : String(error)}
    </p>
  );
}
