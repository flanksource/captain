import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SandboxesPage } from "./SandboxesPage";
import type { DeployConfig, DeployTarget } from "./gitAgentDeploymentData";

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function agent(target: DeployTarget, config: DeployConfig) {
  return {
    name: `worker-${target}`,
    status: "enrolled",
    url: "ssh://captain@127.0.0.1:7423/repo.git",
    hostFingerprint: "SHA256:agent",
    deployment: {
      target,
      namespace: config.namespace,
      workload: `captain-git-agent-worker-${target}`,
      image: config.image,
      config,
    },
  };
}

function stubFetch(target: DeployTarget, config: DeployConfig) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/captain/sandboxes") {
      return Promise.resolve(jsonResponse({ kinds: [] }));
    }
    if (url.includes("/sandbox/git-agent/agents")) {
      return Promise.resolve(jsonResponse([agent(target, config)]));
    }
    if (url.includes("/deploy/preflight")) {
      return Promise.resolve(jsonResponse({
        target,
        ready: true,
        supervisorRequired: target === "kubernetes",
        supervisorCandidates: ["https://captain.example.com"],
        mailboxListen: ":9020",
        transport: "https",
        runtime: target === "docker" ? "the local docker daemon" : "kubernetes v1.31.0",
        namespace: config.namespace,
        inCluster: false,
        domainRequired: target === "kubernetes",
        ingressClasses: ["nginx"],
        certManagerInstalled: true,
      }));
    }
    if (url.includes("/namespaces")) {
      return Promise.resolve(jsonResponse([config.namespace ?? "default"]));
    }
    if (url.includes("/cluster-issuers") || url.includes("/secrets")) {
      return Promise.resolve(jsonResponse([]));
    }
    if (url.includes("/sandbox/credentials")) {
      return Promise.resolve(jsonResponse({
        config: { refreshMargin: "", publish: [] },
        status: [],
        providers: [],
        defaultSecret: "captain-agent-credentials",
        defaultMargin: "1h",
      }));
    }
    if (url.includes("/sandbox/git-agent/tasks")) {
      return Promise.resolve(jsonResponse([]));
    }
    if (init?.method === "PUT" && url.includes("/deployments/")) {
      const request = JSON.parse(String(init.body)) as { name: string; image?: string; dryRun?: boolean };
      return Promise.resolve(jsonResponse({
        backend: "git-agent",
        agent: request.name,
        target,
        image: request.image,
        workload: `captain-git-agent-${request.name}`,
        volume: `captain-git-agent-${request.name}-state`,
        supervisor: config.supervisorAddress,
        supervisorFrom: "flag",
        advertise: config.advertise,
        advertiseFrom: "saved deployment",
        hostFingerprint: "SHA256:mailbox",
        security: "read-only-root",
        credentials: "configured",
        egressRestricted: false,
        enrolled: false,
        ready: false,
        replaced: true,
        dryRun: request.dryRun,
        mutations: ["replace the existing workload"],
      }));
    }
    throw new Error(`unexpected fetch ${init?.method ?? "GET"} ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SandboxesPage />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("SandboxesPage deployment editing", () => {
  it.each([
    {
      target: "docker" as const,
      config: {
        target: "docker" as const,
        image: "registry.example/captain:v1",
        supervisorAddress: "https://captain.example.com",
        advertise: "ssh://captain@127.0.0.1:7423/repo.git",
        credentialsDir: "/var/lib/captain/credentials",
      },
    },
    {
      target: "kubernetes" as const,
      config: {
        target: "kubernetes" as const,
        transport: "https",
        namespace: "agents",
        kubeContext: "agents-lab",
        image: "registry.example/captain:v1",
        supervisorAddress: "https://captain.example.com",
        advertise: "https://worker-kubernetes.agents.example.com/git/repo.git",
        domain: "agents.example.com",
        ingressClass: "nginx",
        ingressIssuer: "letsencrypt-prod",
        credentialsSecret: "captain-agent-credentials",
      },
    },
  ])("prefills and previews an in-place $target update", async ({ target, config }) => {
    const fetchMock = stubFetch(target, config);
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByText(`Edit worker-${target}`)).toBeTruthy();

    const name = await screen.findByDisplayValue(`worker-${target}`);
    expect(name).toHaveAttribute("id", "deploy-name");
    expect(name).toBeDisabled();
    expect(screen.getByRole("button", { name: target === "docker" ? "Docker" : "Kubernetes" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Show image and sizing" }));
    const image = screen.getByDisplayValue("registry.example/captain:v1");
    fireEvent.change(image, { target: { value: "registry.example/captain:v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview update" }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(
        ([url, init]) => String(url).includes("/deployments/worker-") && init?.method === "PUT",
      );
      expect(call).toBeTruthy();
      const request = JSON.parse(String(call?.[1]?.body)) as Record<string, unknown>;
      expect(request.target).toBe(target);
      expect(request.image).toBe("registry.example/captain:v2");
      expect(request.credentialsDir ?? request.credentialsSecret).toBe(
        target === "docker" ? "/var/lib/captain/credentials" : "captain-agent-credentials",
      );
      if (target === "kubernetes") {
        expect(
          fetchMock.mock.calls.some(([url]) =>
            String(url).includes("deploy/preflight?backend=git-agent&target=kubernetes&transport=https&kubeContext=agents-lab"),
          ),
        ).toBe(true);
      }
    });
  });
});
