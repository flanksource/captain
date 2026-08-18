import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SandboxesPage } from "./SandboxesPage";

const CATALOG = {
  default: "prod-pool",
  kinds: [
    {
      kind: "none",
      description: "Run the agent directly on the host, unconfined",
      capabilities: [],
      modes: ["api", "cli", "agent", "cmux"],
    },
    {
      kind: "git-agent",
      description: "Relocate the run onto an enrolled remote agent over git",
      capabilities: ["remote-exec", "isolate-workspace", "egress-proxy"],
      modes: ["cli", "agent", "cmux"],
      backends: [
        { name: "prod-pool", default: true, agents: [{ name: "worker-01" }] },
      ],
    },
  ],
  invalid: [
    { name: "typo", kind: "git-agnet", error: 'unknown kind "git-agnet"' },
  ],
};

const AGENTS = [
  {
    name: "worker-01",
    fingerprint: "SHA256:aaa",
    hostFingerprint: "SHA256:bbb",
    url: "ssh://worker-01:7422",
    addedAt: "2026-08-01T00:00:00Z",
    status: "enrolled",
    dispatchable: true,
  },
  // Enrolled by hand without a host key: dispatch pins the host key, so this
  // one looks healthy but cannot actually be dispatched to.
  {
    name: "worker-02",
    fingerprint: "SHA256:ccc",
    url: "ssh://worker-02:7422",
    status: "enrolled",
    dispatchable: false,
    dispatchIssue: "missing host key",
  },
  // Placed by captain: it knows the runtime, so it can offer to tear it down.
  {
    name: "worker-03",
    fingerprint: "SHA256:ddd",
    url: "https://worker-03.agents.example.com/git/repo.git",
    status: "enrolled",
    dispatchable: true,
    deployment: {
      target: "kubernetes" as const,
      namespace: "captain",
      workload: "captain-git-agent-worker-03",
      image: "ghcr.io/flanksource/captain:latest",
    },
  },
  // The workload exists but has not finished joining. Invisible before deploy
  // recorded it, which left an operator with a running sidecar and no way to
  // remove it from here.
  {
    name: "worker-04",
    status: "deployed — waiting to enroll",
    dispatchable: false,
    deployment: {
      target: "docker" as const,
      workload: "captain-git-agent-worker-04",
    },
  },
];

const PREFLIGHT = {
  target: "docker" as const,
  ready: true,
  supervisorRequired: false,
  mailboxListen: ":7422",
  hostFingerprint: "SHA256:mailbox",
  transport: "ssh",
  supervisor: "ssh://captain@host.docker.internal:7422",
  supervisorFrom: "docker-host-gateway",
  runtime: "the local docker daemon",
  inCluster: false,
  domainRequired: false,
  certManagerInstalled: false,
};

/**
 * Commits a value into a Combobox.
 *
 * Typing alone only filters the menu; the value is emitted on Enter or on
 * click-away, so a test that only fires `change` is asserting against a control
 * the operator has not finished using.
 */
function selectCombobox(name: string, value: string) {
  const input = screen.getByRole("combobox", { name });
  fireEvent.focus(input);
  fireEvent.change(input, { target: { value } });
  fireEvent.keyDown(input, { key: "Enter" });
}

/**
 * The blocker summary beside the submit buttons.
 *
 * Every blocker deliberately renders twice — once on its own field, once here —
 * so assertions have to say which one they mean.
 */
function errorSummary() {
  return screen.getByRole("alert", { name: "Form errors" });
}

/** The body of the deploy request, which is the contract with the server. */
function deployBody(
  fetchMock: ReturnType<typeof stubFetch>,
): Record<string, unknown> {
  const call = fetchMock.mock.calls.find(
    ([url, init]) =>
      String(url).includes("/deployments") &&
      (init as RequestInit | undefined)?.method === "POST",
  );
  if (!call) throw new Error("no deploy request was sent");
  return JSON.parse(String((call[1] as RequestInit).body)) as Record<
    string,
    unknown
  >;
}

const NAMESPACES = ["captain", "default", "kube-system"];
const CLUSTER_ISSUERS = ["letsencrypt-prod", "letsencrypt-staging"];
const SECRETS = ["agents-example-com-wildcard", "captain-agent-credentials"];

const CREDENTIALS = {
  config: {
    refreshMargin: "1h",
    publish: [{ namespace: "captain", secret: "" }],
  },
  status: [
    {
      provider: "claude",
      source: "keychain",
      key: "oauth",
      expiresAt: "2026-08-25T00:00:00Z",
      expiresIn: "6d",
      expired: false,
      targets: ["secret captain/captain-agent-credentials"],
    },
  ],
  providers: ["claude", "codex"],
  defaultSecret: "captain-agent-credentials",
  defaultMargin: "5m0s",
};

const TASKS = [
  {
    id: "11111111-1111-1111-1111-111111111111",
    taskId: "task-1",
    mailbox: "mailboxes/aaa.git",
    repository: "/repo/project",
    backend: "prod-pool",
    agent: "worker-01",
    base: "main",
    dispatchCommit: "deadbeef",
    attempts: 1,
    maxAttempts: 3,
    status: "running" as const,
    dispatchedAt: "2026-08-16T09:00:00Z",
    updatedAt: "2026-08-16T09:30:00Z",
  },
];

const AGENT_WHOAMI = {
  adapters: [
    {
      backend: "codex-agent",
      type: "cli",
      provider: "openai",
      mode: "agent",
      authenticated: true,
      authMethod: "codex login",
      binary: "/usr/local/bin/codex",
      modelCount: 1,
      models: ["gpt-5.6-sol"],
    },
  ],
  defaultProvider: "openai",
  providerDefaults: {},
  disabled: {},
  axes: {},
  runtimes: [],
};

function jsonResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

function stubFetch(overrides: Record<string, unknown> = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.startsWith("/api/captain/sandboxes")) {
      return Promise.resolve(jsonResponse(overrides.catalog ?? CATALOG));
    }
    if (url.includes("/sandbox/git-agent/tasks/")) {
      return Promise.resolve(
        jsonResponse({
          task: TASKS[0],
          attempts: [
            {
              attempt: 1,
              tier: "supervisor",
              status: "rejected",
              findings: [{ hook: "verify", message: "make lint failed" }],
              recordedAt: "2026-08-16T10:00:00Z",
            },
          ],
        }),
      );
    }
    if (url.includes("/sandbox/git-agent/tasks")) {
      return Promise.resolve(jsonResponse(overrides.tasks ?? TASKS));
    }
    if (url.includes("/sandbox/git-agent/deploy/preflight")) {
      return Promise.resolve(jsonResponse(overrides.preflight ?? PREFLIGHT));
    }
    if (url.includes("/sandbox/git-agent/namespaces")) {
      return Promise.resolve(jsonResponse(overrides.namespaces ?? NAMESPACES));
    }
    if (url.includes("/sandbox/git-agent/cluster-issuers")) {
      return Promise.resolve(
        jsonResponse(overrides.clusterIssuers ?? CLUSTER_ISSUERS),
      );
    }
    if (url.includes("/sandbox/git-agent/secrets")) {
      return Promise.resolve(jsonResponse(overrides.secrets ?? SECRETS));
    }
    if (url.includes("/sandbox/credentials")) {
      return Promise.resolve(
        jsonResponse(overrides.credentials ?? CREDENTIALS),
      );
    }
    if (url.includes("/sandbox/git-agent/agents/worker-03/whoami")) {
      return Promise.resolve(
        jsonResponse(overrides.agentWhoami ?? AGENT_WHOAMI),
      );
    }
    if (url.includes("/sandbox/git-agent/deployments")) {
      if (init?.method === "DELETE") {
        return Promise.resolve(
          jsonResponse({
            backend: "git-agent",
            agent: "worker-03",
            target: "kubernetes",
            removed: ["Deployment/captain-git-agent-worker-03"],
            revoked: true,
          }),
        );
      }
      const body = JSON.parse(String(init?.body ?? "{}")) as {
        dryRun?: boolean;
      };
      return Promise.resolve(
        jsonResponse(
          overrides.deploy ?? {
            backend: "git-agent",
            agent: "worker-09",
            target: "docker",
            image: "ghcr.io/flanksource/captain:latest",
            workload: "captain-git-agent-worker-09",
            volume: "captain-git-agent-worker-09-state",
            supervisor: "ssh://captain@host.docker.internal:7422",
            supervisorFrom: "docker-host-gateway",
            advertise: "ssh://captain@127.0.0.1:41234/repo.git",
            advertiseFrom: "docker-published-port",
            hostFingerprint: "SHA256:mailbox",
            security: "unprivileged, all capabilities dropped",
            credentials:
              "none declared — the agent cannot reach a model provider",
            egressRestricted: false,
            enrolled: !body.dryRun,
            ready: !body.dryRun,
            ...(body.dryRun
              ? {
                  dryRun: true,
                  mutations: [
                    'mint a durable captain token for agent "worker-09"',
                    "run: docker run --rm captain",
                  ],
                }
              : { objects: ["container/abc123def456"] }),
          },
        ),
      );
    }
    if (url.includes("/sandbox/git-agent/agents")) {
      if (init?.method === "DELETE") {
        return Promise.resolve(
          jsonResponse({
            backend: "git-agent",
            agent: "worker-01",
            revoked: true,
          }),
        );
      }
      if (init?.method === "POST") {
        return Promise.resolve(
          jsonResponse({
            backend: "git-agent",
            agent: "worker-09",
            tokenId: "abc123",
            hostFingerprint: "SHA256:host",
            dispatchKey: "SHA256:dispatch",
            joinCommand:
              "captain sandbox git-agent serve --token cptn_abc123.SECRET --supervisor ssh://s:7422 --host-fingerprint SHA256:host",
          }),
        );
      }
      return Promise.resolve(jsonResponse(overrides.agents ?? AGENTS));
    }
    throw new Error(`unexpected fetch: ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <SandboxesPage />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("SandboxesPage", () => {
  it("lists each adapter with its capabilities and modes", async () => {
    stubFetch();
    renderPage();
    await screen.findByText("git-agent");
    expect(screen.getByText("remote-exec")).toBeTruthy();
    expect(screen.getByText("egress-proxy")).toBeTruthy();
    // The configured backend appears under the kind it selects.
    expect(screen.getByText("prod-pool")).toBeTruthy();
  });

  it("reports a backend whose kind does not resolve instead of hiding it", async () => {
    stubFetch();
    renderPage();
    await waitFor(() =>
      expect(
        screen
          .getAllByRole("alert")
          .some((alert) => alert.textContent?.includes("git-agnet")),
      ).toBe(true),
    );
  });

  it("distinguishes enrolled, still-joining, and undispatchable agents", async () => {
    stubFetch();
    renderPage();
    await screen.findByText("worker-01");
    // worker-02 is enrolled over SSH but has no host key to pin.
    expect(
      screen.getByText("enrolled — not dispatchable (missing host key)"),
    ).toBeTruthy();
    // worker-03 is reached over HTTPS and uses a dispatch token, not a host key.
    const httpsRow = screen
      .getByText("https://worker-03.agents.example.com/git/repo.git")
      .closest("tr");
    expect(httpsRow?.textContent).toContain("enrolled");
    expect(httpsRow?.textContent).not.toContain("not dispatchable");
    // worker-04's workload exists but has not joined yet.
    expect(screen.getByText(/deployed — waiting to enroll/)).toBeTruthy();
  });

  it("loads an HTTPS agent's whoami details only when requested", async () => {
    const fetchMock = stubFetch();
    renderPage();
    await screen.findByText("worker-03");

    expect(screen.queryByText("codex login")).toBeNull();
    expect(
      fetchMock.mock.calls.some(([url]) =>
        String(url).includes("/worker-03/whoami"),
      ),
    ).toBe(false);

    fireEvent.click(
      screen.getByRole("button", { name: "Inspect worker-03 runtimes" }),
    );

    expect(await screen.findByText("codex login")).toBeInTheDocument();
    expect(screen.getByText("gpt-5.6-sol")).toBeInTheDocument();
    expect(screen.getByText("1 ready adapter")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/captain/sandbox/git-agent/agents/worker-03/whoami?backend=git-agent",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  // Captain can only tear down what it placed: an agent enrolled by hand has no
  // recorded runtime, so offering to undeploy it would be a guess.
  it("offers undeploy only for agents captain deployed, and revoke only for the rest", async () => {
    stubFetch();
    renderPage();
    await screen.findByText("worker-01");

    expect(screen.getAllByRole("button", { name: "Undeploy" })).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(2);
    expect(screen.getAllByText("self-managed")).toHaveLength(2);
    expect(screen.getByText(/kubernetes/)).toBeTruthy();
  });

  it("undeploys once confirmed, without naming a target the server should resolve", async () => {
    const fetchMock = stubFetch();
    vi.stubGlobal(
      "confirm",
      vi.fn(() => true),
    );
    renderPage();
    await screen.findByText("worker-03");

    fireEvent.click(screen.getAllByRole("button", { name: "Undeploy" })[0]!);

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) =>
            String(url).includes("/deployments/worker-03") &&
            (init as RequestInit)?.method === "DELETE",
        ),
      ).toBe(true),
    );
    // Tearing down the wrong runtime removes nothing and reports success, so
    // the target comes from what deploy recorded rather than from the browser.
    const call = fetchMock.mock.calls.find(([url]) =>
      String(url).includes("/deployments/worker-03"),
    );
    expect(String(call?.[0])).not.toContain("target=");
  });

  it("confirms before revoking, and does nothing when declined", async () => {
    const fetchMock = stubFetch();
    vi.stubGlobal(
      "confirm",
      vi.fn(() => false),
    );
    renderPage();
    await screen.findByText("worker-01");

    fireEvent.click(screen.getAllByRole("button", { name: "Revoke" })[0]!);
    await waitFor(() => expect(window.confirm).toHaveBeenCalled());
    expect(
      fetchMock.mock.calls.some(
        ([, init]) => (init as RequestInit)?.method === "DELETE",
      ),
    ).toBe(false);
  });

  it("revokes once confirmed", async () => {
    const fetchMock = stubFetch();
    vi.stubGlobal(
      "confirm",
      vi.fn(() => true),
    );
    renderPage();
    await screen.findByText("worker-01");

    fireEvent.click(screen.getAllByRole("button", { name: "Revoke" })[0]!);
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([, init]) => (init as RequestInit)?.method === "DELETE",
        ),
      ).toBe(true),
    );
  });

  it("shows the join command after enrolling, and never key material", async () => {
    stubFetch();
    renderPage();
    await screen.findByText("worker-01");

    fireEvent.click(
      screen.getByRole("button", { name: "Enroll existing host" }),
    );
    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Enroll" }));

    const command = await screen.findByText(
      /captain sandbox git-agent serve --token/,
    );
    expect(command.textContent).toContain("--host-fingerprint");
    // A7.1: the hand-off carries a token, never a private key.
    expect(document.body.textContent).not.toContain("PRIVATE KEY");
    // A token with no expiry says so, rather than rendering "Invalid Date".
    expect(document.body.textContent).toContain("until it is revoked");
    expect(document.body.textContent).not.toContain("Invalid Date");
  });
});

describe("SandboxesPage deploy", () => {
  const openDeploy = async () => {
    renderPage();
    await screen.findByText("worker-01");
    fireEvent.click(screen.getByRole("button", { name: "Deploy agent" }));
  };

  it("shows what the target resolved to before asking for anything", async () => {
    stubFetch();
    await openDeploy();

    // The addresses are proven, not typed, so they are stated up front.
    await screen.findByText("the local docker daemon");
    expect(
      screen.getByText("ssh://captain@host.docker.internal:7422"),
    ).toBeTruthy();
    expect(screen.getByText(/docker-host-gateway/)).toBeTruthy();
  });

  // The deploy command refuses rather than guessing; discovering that only on
  // submit would put the operator back where the command started.
  it("blocks the form and explains when the target cannot be deployed to", async () => {
    stubFetch({
      preflight: {
        target: "docker",
        ready: false,
        supervisorRequired: false,
        reason:
          'no mailbox has served from backend "git-agent" on this host; run `captain sandbox git-agent serve --role mailbox` first',
      },
    });
    await openDeploy();

    await screen.findByText(/Cannot deploy to docker from this host/);
    expect(screen.getByText(/serve --role mailbox/)).toBeTruthy();
    expect(screen.queryByPlaceholderText("worker-01")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  // Which process is the supervisor decides which one an operator restarts to
  // fix anything, and https means `captain serve` already is it — so the
  // transport is stated rather than left to be inferred from a port.
  it("names the transport the mailbox answered on", async () => {
    stubFetch({
      preflight: {
        ...PREFLIGHT,
        transport: "https",
        mailboxListen: "0.0.0.0:9020",
        supervisor: "https://host.docker.internal:9020",
      },
    });
    await openDeploy();

    await screen.findByText("over https");
    expect(
      screen.getByText("https://host.docker.internal:9020"),
    ).toBeInTheDocument();
  });

  // Namespace is select-or-create: the cluster's own namespaces are offered so
  // an operator does not have to remember one, and a name that is not there is
  // a creation rather than a typo to discover at apply time.
  const openKubernetesForm = async () => {
    await openDeploy();
    fireEvent.click(screen.getByRole("button", { name: /Kubernetes/ }));
    await screen.findByText(/kubeconfig context's/);
  };

  const KUBERNETES_READY = {
    target: "kubernetes",
    ready: true,
    supervisorRequired: true,
    supervisorCandidates: ["ssh://192.168.1.20:7422", "ssh://172.17.0.1:7422"],
    mailboxListen: ":7422",
    transport: "ssh",
    namespace: "default",
    runtime: "kubernetes v1.31.0",
    inCluster: false,
    // ssh: an Ingress cannot front it, so the advertise URL is the route half.
    domainRequired: true,
    ingressClasses: ["nginx", "traefik"],
    certManagerInstalled: true,
  };

  /** The same cluster reached from an https mailbox, where an Ingress can front it. */
  const KUBERNETES_HTTPS = {
    ...KUBERNETES_READY,
    transport: "https",
    mailboxListen: "0.0.0.0:9020",
    supervisorCandidates: ["https://192.168.1.20:9020"],
  };

  /**
   * Everything an out-of-cluster kubernetes deploy cannot detect, so a test
   * about one field is not also a test about the other two. Uses the advertise
   * URL rather than a domain because the fixture's mailbox answered over ssh,
   * which an Ingress cannot front.
   */
  const fillRequiredKubernetesFields = async () => {
    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    selectCombobox("Supervisor address", "ssh://captain.internal:7422");
    fireEvent.change(
      screen.getByPlaceholderText("https://worker-01.agents.example.com"),
      { target: { value: "ssh://captain@worker-09.agents.internal:7422" } },
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
      ).toBe(false),
    );
  };

  it("offers the cluster's namespaces", async () => {
    stubFetch({ preflight: KUBERNETES_READY });
    await openKubernetesForm();

    fireEvent.focus(screen.getByRole("combobox", { name: "Namespace" }));
    expect(
      await screen.findByRole("option", { name: "captain" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "kube-system" }),
    ).toBeInTheDocument();
  });

  it("treats a name the cluster does not have as a namespace to create", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_READY });
    await openKubernetesForm();

    const namespace = screen.getByRole("combobox", { name: "Namespace" });
    fireEvent.focus(namespace);
    fireEvent.change(namespace, { target: { value: "agents" } });
    fireEvent.keyDown(namespace, { key: "Enter" });

    // Stated before submit: it is the one cluster-scoped change, and undeploy
    // does not undo it.
    await screen.findByText(
      /agents does not exist in this cluster and will be created/,
    );

    await fillRequiredKubernetesFields();
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const deployCall = fetchMock.mock.calls.find(
        ([url, init]) =>
          String(url).includes("/deployments") &&
          (init as RequestInit | undefined)?.method === "POST",
      );
      expect(deployCall).toBeTruthy();
      const body = JSON.parse(
        String((deployCall![1] as RequestInit).body),
      ) as Record<string, unknown>;
      expect(body.namespace).toBe("agents");
      expect(body.createNamespace).toBe(true);
    });
  });

  it("does not ask to create a namespace the cluster already has", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_READY });
    await openKubernetesForm();

    fireEvent.focus(screen.getByRole("combobox", { name: "Namespace" }));
    fireEvent.mouseDown(await screen.findByRole("option", { name: "captain" }));

    expect(screen.queryByText(/will be created/)).toBeNull();

    await fillRequiredKubernetesFields();
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const deployCall = fetchMock.mock.calls.find(
        ([url, init]) =>
          String(url).includes("/deployments") &&
          (init as RequestInit | undefined)?.method === "POST",
      );
      expect(deployCall).toBeTruthy();
      const body = JSON.parse(
        String((deployCall![1] as RequestInit).body),
      ) as Record<string, unknown>;
      expect(body.namespace).toBe("captain");
      // Pruned rather than sent false, so an existing namespace never carries a
      // create intent to the server.
      expect(body).not.toHaveProperty("createNamespace");
    });
  });

  // For kubernetes no route back can be proven, so the address is required
  // rather than guessed — a guess produces a pod that CrashLoops on enroll.
  it("requires a supervisor address for kubernetes and says why", async () => {
    stubFetch({ preflight: KUBERNETES_READY });
    await openDeploy();
    fireEvent.click(screen.getByRole("button", { name: /Kubernetes/ }));

    await waitFor(() =>
      expect(errorSummary().textContent).toMatch(
        /Supervisor address: .*no route back to this host can be proven/,
      ),
    );
    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    expect(
      screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
    ).toBe(true);

    await fillRequiredKubernetesFields();
    // Everything supplied, so the summary is gone rather than left empty.
    expect(screen.queryByRole("alert", { name: "Form errors" })).toBeNull();
  });

  // The addresses worth trying are facts about this host the preflight already
  // enumerated, so the operator picks rather than recalling which interface a
  // cluster can reach.
  it("offers this host's addresses for the supervisor, and sends the pick", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_READY });
    await openKubernetesForm();

    fireEvent.focus(
      screen.getByRole("combobox", { name: "Supervisor address" }),
    );
    expect(
      await screen.findByRole("option", { name: "ssh://192.168.1.20:7422" }),
    ).toBeInTheDocument();
    fireEvent.mouseDown(
      screen.getByRole("option", { name: "ssh://172.17.0.1:7422" }),
    );

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.change(
      screen.getByPlaceholderText("https://worker-01.agents.example.com"),
      { target: { value: "ssh://captain@worker-09.agents.internal:7422" } },
    );
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
      ).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() =>
      expect(deployBody(fetchMock).supervisorAddress).toBe(
        "ssh://172.17.0.1:7422",
      ),
    );
  });

  // A docker sidecar is reached on its published loopback port, and the server
  // refuses route flags on that target rather than ignoring them.
  it("asks about routing only for kubernetes", async () => {
    stubFetch();
    await openDeploy();
    await screen.findByText("the local docker daemon");

    expect(screen.queryByPlaceholderText("agents.example.com")).toBeNull();
  });

  it("offers the cluster's ingress classes", async () => {
    stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.focus(screen.getByRole("combobox", { name: "Ingress class" }));
    expect(
      await screen.findByRole("option", { name: "traefik" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "nginx" })).toBeInTheDocument();
  });

  // The name is the one thing captain does NOT create, so it is shown before
  // submit rather than left to be reconstructed from the mutation list.
  it("states the host it would publish, and that the DNS record is not created", async () => {
    stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.change(screen.getByPlaceholderText("agents.example.com"), {
      target: { value: "agents.example.com" },
    });

    await screen.findByText(/Published at worker-09\.agents\.example\.com/);
    expect(screen.getByText(/does NOT create the DNS record/)).toBeTruthy();
  });

  // Over https the Ingress is the supported route, so the form demands it the
  // same way it demands the supervisor address — both are refusals the server
  // would otherwise give after the modal is gone.
  it("requires a domain and a certificate over an https mailbox", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    selectCombobox("Supervisor address", "https://192.168.1.20:9020");
    await waitFor(() =>
      expect(errorSummary().textContent).toMatch(/Domain: .*left to advertise/),
    );
    expect(
      screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
    ).toBe(true);

    fireEvent.change(screen.getByPlaceholderText("agents.example.com"), {
      target: { value: "agents.example.com" },
    });
    // A domain alone still blocks: without a certificate the controller answers
    // for that host with its own and the supervisor's push fails verification.
    await waitFor(() =>
      expect(errorSummary().textContent).toMatch(
        /ClusterIssuer: .*needs a certificate/,
      ),
    );
    expect(
      screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
    ).toBe(true);
    // And the field itself is marked, not just described in the summary.
    expect(
      screen.getByRole("combobox", { name: "ClusterIssuer" }),
    ).toHaveAttribute("aria-invalid", "true");

    selectCombobox("ClusterIssuer", "letsencrypt-prod");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
      ).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const body = deployBody(fetchMock);
      expect(body.domain).toBe("agents.example.com");
      expect(body.ingressIssuer).toBe("letsencrypt-prod");
      // Blank keeps the server's own default rather than the form restating it.
      expect(body).not.toHaveProperty("ingressClass");
    });
  });

  // An issuer annotation is inert without the controller that acts on it, so the
  // option is closed rather than left to fail after the token is minted.
  it("falls back to a TLS Secret when the cluster has no cert-manager", async () => {
    const fetchMock = stubFetch({
      preflight: { ...KUBERNETES_HTTPS, certManagerInstalled: false },
    });
    await openKubernetesForm();

    await screen.findByText(/cert-manager is not installed/);
    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    selectCombobox("Supervisor address", "https://192.168.1.20:9020");
    fireEvent.change(screen.getByPlaceholderText("agents.example.com"), {
      target: { value: "agents.example.com" },
    });
    selectCombobox("TLS Secret", "agents-wildcard");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
      ).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const body = deployBody(fetchMock);
      expect(body.ingressTlsSecret).toBe("agents-wildcard");
      expect(body).not.toHaveProperty("ingressIssuer");
    });
  });

  // Where captain knows a controller's translation there is nothing for the
  // operator to decide, so it is applied on selection rather than demanded.
  it("applies traefik's equivalents on selection instead of refusing", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    selectCombobox("Supervisor address", "https://192.168.1.20:9020");
    fireEvent.change(screen.getByPlaceholderText("agents.example.com"), {
      target: { value: "agents.example.com" },
    });
    selectCombobox("ClusterIssuer", "letsencrypt-prod");
    selectCombobox("Ingress class", "traefik");

    // No prompt to acknowledge anything, and no blocker left to clear.
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
      ).toBe(false),
    );
    expect(screen.queryByRole("alert", { name: "Form errors" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() =>
      expect(deployBody(fetchMock).ingressAnnotation).toEqual([
        "traefik.ingress.kubernetes.io/service.serversscheme=https",
      ]),
    );
  });

  // A controller captain has no translation for still has to be acknowledged:
  // its nginx defaults are inert there, and captain cannot invent equivalents.
  it("still makes an unknown controller acknowledge what a git push needs", async () => {
    stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    selectCombobox("Supervisor address", "https://192.168.1.20:9020");
    fireEvent.change(screen.getByPlaceholderText("agents.example.com"), {
      target: { value: "agents.example.com" },
    });
    selectCombobox("ClusterIssuer", "letsencrypt-prod");
    selectCombobox("Ingress class", "contour");

    // Named against the annotations field, which is what fixes it — not against
    // the class field, which is already what the operator wanted.
    await waitFor(() =>
      expect(errorSummary().textContent).toMatch(
        /Ingress annotations: contour is not ingress-nginx/,
      ),
    );
    expect(
      screen.getByRole("button", { name: "Deploy" }).hasAttribute("disabled"),
    ).toBe(true);
  });

  // A Secret that is not kubernetes.io/tls cannot serve the agent's host, and
  // the Ingress would be created pointing at it regardless — so the field
  // offers the cluster's real TLS Secrets, scoped to the target namespace.
  it("offers the namespace's TLS Secrets and the cluster's issuers", async () => {
    const fetchMock = stubFetch({ preflight: KUBERNETES_HTTPS });
    await openKubernetesForm();

    fireEvent.focus(screen.getByRole("combobox", { name: "ClusterIssuer" }));
    expect(
      await screen.findByRole("option", { name: "letsencrypt-staging" }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: /existing TLS Secret/ }));
    fireEvent.focus(
      await screen.findByRole("combobox", { name: "TLS Secret" }),
    );
    expect(
      await screen.findByRole("option", {
        name: "agents-example-com-wildcard",
      }),
    ).toBeInTheDocument();

    // Scoped, not cluster-wide: the same name is a different object elsewhere.
    const secretCall = fetchMock.mock.calls.find(([url]) =>
      String(url).includes("/git-agent/secrets"),
    );
    expect(String(secretCall?.[0])).toContain("type=kubernetes.io%2Ftls");
    expect(String(secretCall?.[0])).toContain("namespace=");
  });

  it("previews every intended mutation before creating anything", async () => {
    const fetchMock = stubFetch();
    await openDeploy();
    await screen.findByText("the local docker daemon");

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    await screen.findByText(/mint a durable captain token/);
    expect(screen.getByText(/run: docker run/)).toBeTruthy();
    // A preview creates nothing: the request that produced it was a dry run.
    const preview = fetchMock.mock.calls.find(
      ([url, init]) =>
        String(url).includes("/deployments") &&
        (init as RequestInit)?.method === "POST",
    );
    expect(
      JSON.parse(String((preview?.[1] as RequestInit)?.body)),
    ).toMatchObject({
      dryRun: true,
      name: "worker-09",
      target: "docker",
    });
  });

  // An agent with no model credentials enrols, goes ready, and fails its first
  // task — so it is stated rather than discovered.
  it("warns when the deployed agent has no way to reach a model provider", async () => {
    stubFetch();
    await openDeploy();
    await screen.findByText("the local docker daemon");

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await screen.findByText(/fail its first task/);
    expect(screen.getByText("container/abc123def456")).toBeTruthy();
  });

  it("sends only the fields the operator set, so the rest keep CLI defaults", async () => {
    const fetchMock = stubFetch();
    await openDeploy();
    await screen.findByText("the local docker daemon");

    fireEvent.change(screen.getByPlaceholderText("worker-01"), {
      target: { value: "worker-09" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Deploy" }));

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) =>
            String(url).includes("/deployments") &&
            (init as RequestInit)?.method === "POST",
        ),
      ).toBe(true),
    );
    const call = fetchMock.mock.calls.find(
      ([url, init]) =>
        String(url).includes("/deployments") &&
        (init as RequestInit)?.method === "POST",
    );
    const body = JSON.parse(String((call?.[1] as RequestInit)?.body));
    expect(Object.keys(body).sort()).toEqual(["name", "target"]);
  });
});

describe("SandboxesPage remote tasks", () => {
  it("lists recorded remote tasks", async () => {
    stubFetch();
    renderPage();
    await screen.findByText("task-1");
    expect(screen.getByText("running")).toBeTruthy();
    expect(screen.getByText("1 / 3")).toBeTruthy();
  });

  it("shows an empty state when nothing has been dispatched", async () => {
    stubFetch({ tasks: [] });
    renderPage();
    await screen.findByText(/No remote tasks recorded yet/);
  });

  it("opens a task and shows its per-tier verdict findings", async () => {
    stubFetch();
    renderPage();
    fireEvent.click(await screen.findByText("task-1"));
    await screen.findByText(/make lint failed/);
    expect(screen.getByText("attempt 1")).toBeTruthy();
    expect(screen.getByText("supervisor")).toBeTruthy();
  });
});
