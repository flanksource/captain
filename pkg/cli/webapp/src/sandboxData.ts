import type { SpecRuntimeSandboxCatalog } from "@flanksource/clicky-ui/ai";
import type { GitAgentDeployment } from "./gitAgentDeploymentData";

export * from "./gitAgentDeploymentData";

/**
 * One enrolled or pending git-agent, mirroring cli.GitAgentListEntry.
 *
 * Dispatch readiness is resolved by the server because the required credential
 * depends on the endpoint transport: an SSH host key or an HTTPS token path.
 * The token path stays server-side and never crosses this API.
 */
export type GitAgent = {
  name: string;
  fingerprint?: string;
  hostFingerprint?: string;
  url?: string;
  addedAt?: string;
  /** "enrolled", or "deployed — waiting to enroll" for a workload still starting. */
  status: string;
  dispatchable: boolean;
  dispatchIssue?: string;
  /**
   * Set only when captain placed this agent's sidecar itself. An agent joined by
   * hand has none, and cannot be torn down from here — there is nothing that
   * knows which runtime it runs on.
   */
  deployment?: GitAgentDeployment;
};

/**
 * The join hand-off from enrollment, mirroring cli.GitAgentAddResult.
 *
 * `expires` is optional because a token minted without a lifetime never
 * expires; `tokenId` is the public handle a listing and a revocation use, and
 * the secret itself is deliberately absent — it exists only in the join command.
 */
export type GitAgentEnrollment = {
  backend: string;
  agent: string;
  tokenId?: string;
  pool?: boolean;
  expires?: string;
  hostFingerprint: string;
  dispatchKey: string;
  joinCommand: string;
  dryRun?: boolean;
};

export type GitAgentRevocation = {
  backend: string;
  agent: string;
  fingerprint?: string;
  revoked: boolean;
  dryRun?: boolean;
};

/** A workload captain placed that has not completed its join yet. */
export function isPending(agent: GitAgent) {
  return agent.status.startsWith("deployed");
}

/**
 * The server evaluates the transport-specific credential without exposing it.
 */
export function isDispatchable(agent: GitAgent) {
  return !isPending(agent) && agent.dispatchable;
}

async function readError(response: Response, fallback: string): Promise<never> {
  const message = (await response.text()).trim();
  throw new Error(message || `${fallback} (${response.status})`);
}

async function getJSON<T>(url: string, fallback: string): Promise<T> {
  const response = await fetch(url, {
    headers: { Accept: "application/json" },
  });
  if (!response.ok) await readError(response, fallback);
  return (await response.json()) as T;
}

export function fetchSandboxCatalog(): Promise<SpecRuntimeSandboxCatalog> {
  return getJSON("/api/captain/sandboxes", "Failed to load sandboxes");
}

export function fetchGitAgents(backend: string): Promise<GitAgent[]> {
  return getJSON(
    `/api/captain/sandbox/git-agent/agents?backend=${encodeURIComponent(backend)}`,
    "Failed to load agents",
  );
}

export type AgentWhoamiAdapter = {
  backend: string;
  type: "api" | "cli";
  provider: string;
  mode: string;
  authenticated: boolean;
  authMethod?: string;
  authDetail?: string;
  binary?: string;
  binaryMissing?: string;
  dependencyMissing?: string;
  provisioner?: string;
  runtimeError?: string;
  modelError?: string;
  modelCount: number;
  models?: string[];
  disabled?: boolean;
};

export type AgentWhoamiResult = {
  adapters: AgentWhoamiAdapter[];
  defaultProvider: string;
};

export async function fetchGitAgentWhoami(params: {
  backend: string;
  name: string;
}): Promise<AgentWhoamiResult> {
  const response = await fetch(
    `/api/captain/sandbox/git-agent/agents/${encodeURIComponent(params.name)}/whoami` +
      `?backend=${encodeURIComponent(params.backend)}`,
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      body: "{}",
    },
  );
  if (!response.ok) await readError(response, "Agent whoami failed");
  return (await response.json()) as AgentWhoamiResult;
}

export async function enrollGitAgent(params: {
  backend: string;
  name: string;
  endpoint?: string;
  dryRun?: boolean;
}): Promise<GitAgentEnrollment> {
  const response = await fetch(
    `/api/captain/sandbox/git-agent/agents?backend=${encodeURIComponent(params.backend)}`,
    {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      // Only the keys the server knows: it decodes strictly, so a stray field
      // is a 400 rather than a silently ignored value.
      body: JSON.stringify({
        name: params.name,
        ...(params.endpoint ? { endpoint: params.endpoint } : {}),
        ...(params.dryRun ? { dryRun: true } : {}),
      }),
    },
  );
  if (!response.ok) await readError(response, "Enrollment failed");
  return (await response.json()) as GitAgentEnrollment;
}

/** Lifecycle of one dispatched task, mirroring the Go enum. */
export type GitAgentTaskStatus =
  | "dispatched"
  | "running"
  | "accepted"
  | "rejected"
  | "errored"
  | "timed_out";

export type GitAgentVerdictStatus = "accepted" | "rejected" | "error";

export type GitAgentTask = {
  id: string;
  taskId: string;
  mailbox: string;
  repository?: string;
  backend?: string;
  agent?: string;
  promptRunId?: string;
  base: string;
  dispatchCommit: string;
  relay?: string;
  policy?: Record<string, unknown>;
  attempts: number;
  maxAttempts?: number;
  status: GitAgentTaskStatus;
  finalStatus?: GitAgentVerdictStatus;
  integratedBranch?: string;
  error?: string;
  dispatchedAt: string;
  concludedAt?: string;
  updatedAt: string;
};

export type GitAgentTaskAttempt = {
  attempt: number;
  /** "sidecar" or "supervisor" — the tier that reached this verdict. */
  tier: string;
  status: GitAgentVerdictStatus;
  findings?: Array<Record<string, unknown>>;
  resultCommit?: string;
  feedback?: string;
  recordedAt: string;
};

export type GitAgentTaskDetail = {
  task: GitAgentTask;
  attempts: GitAgentTaskAttempt[];
};

/** A task still in flight; the rest are history. */
export function isTaskOpen(task: GitAgentTask) {
  return task.status === "dispatched" || task.status === "running";
}

export function fetchGitAgentTasks(
  params: {
    agent?: string;
    status?: string;
  } = {},
): Promise<GitAgentTask[]> {
  const query = new URLSearchParams();
  if (params.agent) query.set("agent", params.agent);
  if (params.status) query.set("status", params.status);
  const suffix = query.toString() ? `?${query}` : "";
  return getJSON(
    `/api/captain/sandbox/git-agent/tasks${suffix}`,
    "Failed to load tasks",
  );
}

export function fetchGitAgentTask(
  taskId: string,
  mailbox: string,
): Promise<GitAgentTaskDetail> {
  return getJSON(
    `/api/captain/sandbox/git-agent/tasks/${encodeURIComponent(taskId)}` +
      `?mailbox=${encodeURIComponent(mailbox)}`,
    "Failed to load task",
  );
}

/** One provider login and how long it stays valid. Mirrors cli.CredentialStatus. */
export type CredentialStatus = {
  provider: string;
  source: string;
  key: string;
  expiresAt: string;
  expiresIn: string;
  expired: boolean;
  targets?: string[];
};

/** One destination in the `credentials.publish` list. */
export type CredentialDestination = {
  providers?: string[];
  directory?: string;
  namespace?: string;
  secret?: string;
  kubeContext?: string;
};

export type CredentialsConfig = {
  /** A Go duration such as "1h". Empty means the publisher's own default. */
  refreshMargin: string;
  publish: CredentialDestination[];
};

export type CredentialsView = {
  config: CredentialsConfig;
  status: CredentialStatus[];
  providers: string[];
  defaultSecret: string;
  defaultMargin: string;
};

/** What one publish pass did. Mirrors credsync.Result. */
export type CredentialsSyncResult = {
  published?: string[];
  targets?: string[];
  nextPublish?: string;
};

export function fetchCredentials(): Promise<CredentialsView> {
  return getJSON(
    "/api/captain/sandbox/credentials",
    "Failed to load agent credentials",
  );
}

export async function saveCredentialsConfig(
  config: CredentialsConfig,
): Promise<CredentialsConfig> {
  const response = await fetch("/api/captain/sandbox/credentials/config", {
    method: "PUT",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(config),
  });
  if (!response.ok)
    await readError(response, "Saving credential destinations failed");
  return (await response.json()) as CredentialsConfig;
}

/**
 * Publishes once, now. An empty body uses the saved destinations, which is what
 * the panel's "Sync now" does; the server decodes strictly, so `{}` is sent
 * rather than nothing.
 */
export async function syncCredentials(
  override: CredentialDestination = {},
): Promise<CredentialsSyncResult> {
  const response = await fetch("/api/captain/sandbox/credentials/sync", {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(override),
  });
  if (!response.ok) await readError(response, "Credential sync failed");
  return (await response.json()) as CredentialsSyncResult;
}

export async function revokeGitAgent(params: {
  backend: string;
  name: string;
}): Promise<GitAgentRevocation> {
  const response = await fetch(
    `/api/captain/sandbox/git-agent/agents/${encodeURIComponent(params.name)}` +
      `?backend=${encodeURIComponent(params.backend)}`,
    { method: "DELETE", headers: { Accept: "application/json" } },
  );
  if (!response.ok) await readError(response, "Revoke failed");
  return (await response.json()) as GitAgentRevocation;
}
