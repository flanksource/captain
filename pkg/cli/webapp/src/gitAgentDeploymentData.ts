export type DeployTarget = "docker" | "kubernetes";

export type DeployConfig = {
  target: DeployTarget;
  transport?: string;
  namespace?: string;
  kubeContext?: string;
  domain?: string;
  ingressClass?: string;
  ingressIssuer?: string;
  ingressTlsSecret?: string;
  ingressAnnotation?: string[];
  image?: string;
  imagePullPolicy?: string;
  imagePullSecret?: string;
  supervisorAddress?: string;
  advertise?: string;
  listenPort?: number;
  hostPort?: number;
  cpuRequest?: string;
  cpuLimit?: string;
  memoryRequest?: string;
  memoryLimit?: string;
  storage?: string;
  storageClass?: string;
  tmpSize?: string;
  pidsLimit?: number;
  runAsUser?: number;
  runAsGroup?: number;
  home?: string;
  readOnlyRoot?: boolean;
  network?: string;
  capAdd?: string[];
  env?: string[];
  envFromSecret?: string[];
  credentialsSecret?: string;
  credentialsDir?: string;
  wait?: boolean;
  timeout?: string;
};

export type DeployRequest = DeployConfig & {
  name: string;
  createNamespace?: boolean;
  replace?: boolean;
  dryRun?: boolean;
};

export type GitAgentDeployment = {
  target: DeployTarget;
  namespace?: string;
  workload: string;
  image?: string;
  deployedAt?: string;
  config?: DeployConfig;
};

export type DeployPreflight = {
  target: DeployTarget;
  ready: boolean;
  reason?: string;
  mailboxListen?: string;
  hostFingerprint?: string;
  transport?: string;
  supervisor?: string;
  supervisorFrom?: string;
  supervisorRequired: boolean;
  supervisorCandidates?: string[];
  namespace?: string;
  kubeContext?: string;
  runtime?: string;
  inCluster: boolean;
  domainRequired: boolean;
  ingressClasses?: string[];
  certManagerInstalled: boolean;
};

export type DeployResult = {
  backend: string;
  agent: string;
  target: string;
  image: string;
  workload: string;
  namespace?: string;
  objects?: string[];
  volume: string;
  supervisor: string;
  supervisorFrom: string;
  advertise: string;
  advertiseFrom: string;
  route?: string;
  routeClass?: string;
  offHostAddresses?: string[];
  hostFingerprint: string;
  security: string;
  credentials: string;
  egressRestricted: boolean;
  enrolled: boolean;
  ready: boolean;
  replaced?: boolean;
  dryRun?: boolean;
  mutations?: string[];
};

export type UndeployResult = {
  backend: string;
  agent: string;
  target: string;
  removed: string[];
  revoked: boolean;
  retained?: string;
  dryRun?: boolean;
};

async function readError(response: Response, fallback: string): Promise<never> {
  const message = (await response.text()).trim();
  throw new Error(message || `${fallback} (${response.status})`);
}

async function getJSON<T>(url: string, fallback: string): Promise<T> {
  const response = await fetch(url, { headers: { Accept: "application/json" } });
  if (!response.ok) await readError(response, fallback);
  return (await response.json()) as T;
}

export function fetchDeployPreflight(params: {
  backend: string;
  target: DeployTarget;
  transport?: string;
  kubeContext?: string;
}): Promise<DeployPreflight> {
  const query = new URLSearchParams({ backend: params.backend, target: params.target });
  if (params.transport) query.set("transport", params.transport);
  if (params.kubeContext) query.set("kubeContext", params.kubeContext);
  return getJSON(
    `/api/captain/sandbox/git-agent/deploy/preflight?${query}`,
    "Preflight failed",
  );
}

export function fetchNamespaces(kubeContext?: string): Promise<string[]> {
  const query = kubeContext ? `?kubeContext=${encodeURIComponent(kubeContext)}` : "";
  return getJSON(`/api/captain/sandbox/git-agent/namespaces${query}`, "Listing namespaces failed");
}

export const TLS_SECRET_TYPE = "kubernetes.io/tls";

export function fetchSecrets(params: {
  namespace?: string;
  kubeContext?: string;
  type?: string;
} = {}): Promise<string[]> {
  const query = new URLSearchParams();
  if (params.namespace) query.set("namespace", params.namespace);
  if (params.kubeContext) query.set("kubeContext", params.kubeContext);
  if (params.type) query.set("type", params.type);
  const suffix = query.toString() ? `?${query}` : "";
  return getJSON(`/api/captain/sandbox/git-agent/secrets${suffix}`, "Listing secrets failed");
}

export function fetchClusterIssuers(kubeContext?: string): Promise<string[]> {
  const query = kubeContext ? `?kubeContext=${encodeURIComponent(kubeContext)}` : "";
  return getJSON(
    `/api/captain/sandbox/git-agent/cluster-issuers${query}`,
    "Listing cluster issuers failed",
  );
}

export async function deployGitAgent(
  backend: string,
  request: DeployRequest,
): Promise<DeployResult> {
  return writeDeployment(
    `/api/captain/sandbox/git-agent/deployments?backend=${encodeURIComponent(backend)}`,
    "POST",
    pruneEmpty(request),
    "Deploy failed",
  );
}

export async function updateGitAgent(
  backend: string,
  name: string,
  request: DeployRequest,
): Promise<DeployResult> {
  return writeDeployment(
    `/api/captain/sandbox/git-agent/deployments/${encodeURIComponent(name)}` +
      `?backend=${encodeURIComponent(backend)}`,
    "PUT",
    request,
    "Update failed",
  );
}

async function writeDeployment(
  url: string,
  method: "POST" | "PUT",
  request: Record<string, unknown> | DeployRequest,
  fallback: string,
): Promise<DeployResult> {
  const response = await fetch(url, {
    method,
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
  if (!response.ok) await readError(response, fallback);
  return (await response.json()) as DeployResult;
}

function pruneEmpty(request: DeployRequest): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(request).filter(([, value]) => {
      if (value === undefined || value === false) return false;
      if (typeof value === "string") return value.trim() !== "";
      if (Array.isArray(value)) return value.length > 0;
      return true;
    }),
  );
}

export async function undeployGitAgent(params: {
  backend: string;
  name: string;
  purge?: boolean;
  dryRun?: boolean;
}): Promise<UndeployResult> {
  const query = new URLSearchParams({ backend: params.backend });
  if (params.purge) query.set("purge", "true");
  if (params.dryRun) query.set("dryRun", "true");
  const response = await fetch(
    `/api/captain/sandbox/git-agent/deployments/${encodeURIComponent(params.name)}?${query}`,
    { method: "DELETE", headers: { Accept: "application/json" } },
  );
  if (!response.ok) await readError(response, "Undeploy failed");
  return (await response.json()) as UndeployResult;
}
