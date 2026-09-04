import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WhoamiPage } from "./WhoamiPage";

const NOTHING_DISABLED = {
  modes: null,
  providers: null,
  runtimes: null,
  models: null,
  efforts: null,
};

const WHOAMI_RESULT = {
  defaultProvider: "openai",
  axes: {
    modes: ["api", "cli", "agent", "cmux"],
    providers: ["anthropic", "openai", "google", "deepseek"],
    efforts: ["low", "medium", "high"],
  },
  disabled: NOTHING_DISABLED,
  providerDefaults: {
    openai: {
      mode: "cli",
      model: "gpt-5.5-codex",
      effort: "high",
      configured: true,
    },
  },
  models: [
    {
      id: "gpt-5.5-codex",
      provider: "openai",
      label: "GPT-5.5 Codex",
      runtime: { model: "gpt-5.5-codex", mode: "agent" },
      reasoning: true,
      configured: true,
      availability: { state: "available" },
    },
  ],
  runtimes: [
    {
      family: "codex",
      provider: "openai",
      catalogPrefix: "openai",
      modes: [
        { mode: "api", kind: "api", keyless: false },
        { mode: "cli", kind: "cli", keyless: true },
      ],
    },
    {
      family: "deepseek",
      provider: "deepseek",
      catalogPrefix: "deepseek",
      modes: [{ mode: "api", kind: "api", keyless: false }],
    },
  ],
  adapters: [
    {
      type: "api",
      provider: "openai",
      mode: "api",
      authenticated: true,
      authMethod: "Captain vault",
      modelCount: 2,
      modelDetails: [
        {
          id: "gpt-5.6-sol",
          label: "GPT-5.6 Sol",
          provider: "openai",
          mode: "api",
          reasoning: true,
          supportedEfforts: ["low", "medium", "high"],
        },
        {
          id: "gpt-5.6-terra",
          label: "GPT-5.6 Terra",
          provider: "openai",
          mode: "api",
          reasoning: true,
        },
      ],
    },
    {
      type: "cli",
      provider: "openai",
      mode: "cli",
      authenticated: true,
      authMethod: "codex login",
      authDetail: "/home/example/.codex/auth.json",
      binary: "/usr/local/bin/codex",
      modelCount: 1,
      modelDetails: [
        {
          id: "gpt-5.5-codex",
          label: "GPT-5.5 Codex",
          provider: "openai",
          mode: "cli",
          reasoning: true,
        },
      ],
    },
    {
      type: "api",
      provider: "deepseek",
      mode: "api",
      authenticated: false,
      modelCount: 0,
      modelError: "set DEEPSEEK_API_KEY to list models",
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("WhoamiPage", () => {
  it("renders the provider, mode, and model topology from the consolidated contract", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(WHOAMI_RESULT));
    vi.stubGlobal("fetch", fetchMock);

    renderWhoamiPage();

    expect(await screen.findByRole("heading", { name: "Capability topology" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Runtime capability tree" })).toBeInTheDocument();
    expect(screen.getByRole("treeitem", { name: /OpenAI 2 runtimes · 3 models/ })).toBeInTheDocument();
    const apiRuntime = runtimeTreeItem("OpenAI", "API");
    const cliRuntime = runtimeTreeItem("OpenAI", "CLI");
    expect(apiRuntime).toHaveTextContent("Token valid");
    expect(cliRuntime).toHaveTextContent("CLI");
    expect(cliRuntime).toHaveTextContent("Ready");
    expect(cliRuntime).not.toHaveTextContent("Token");
    expect(screen.getAllByText("GPT-5.5 Codex").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: /Run model GPT-5.5 Codex via OpenAI \/ CLI/ })).toBeInTheDocument();
    expect(screen.queryAllByText("reasoning")).toHaveLength(0);
    expect(screen.queryByText("codex-cli")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Expand DeepSeek provider" }));
    expect(runtimeTreeItem("DeepSeek", "API")).toHaveTextContent("No token");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/whoami?models=true&limit=0&disabled=true",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
  });

  it("does not claim an available API token is valid when model validation failed", async () => {
    const unverified = {
      ...WHOAMI_RESULT,
      adapters: WHOAMI_RESULT.adapters.map((adapter) => adapter.provider === "openai" && adapter.mode === "api"
        ? { ...adapter, modelError: "validate openai credential: HTTP 401" }
        : adapter),
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(unverified)));
    renderWhoamiPage();

    await screen.findByRole("heading", { name: "Runtime capability tree" });
    expect(runtimeTreeItem("OpenAI", "API")).toHaveTextContent("Token unverified");
    expect(runtimeTreeItem("OpenAI", "API")).not.toHaveTextContent("Token valid");
  });

  it("tests a candidate API token without saving it", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse({
        provider: "openai",
        valid: true,
        saved: false,
        source: "candidate",
        maskedToken: "cand…cret",
        modelCount: 3,
      }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    await selectOpenAIAPI();
    const token = await screen.findByLabelText("OpenAI API token");
    fireEvent.change(token, { target: { value: "candidate-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Test token" }));

    expect(await screen.findByRole("status")).toHaveTextContent("Candidate token is valid for 3 models");
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/providers/openai/token/test",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ token: "candidate-secret" }),
      }),
    );
    expect(token).toHaveValue("candidate-secret");
  });

  it("validates and saves an API token before refreshing the shared catalog", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse({
        provider: "openai",
        valid: true,
        saved: true,
        source: "captain-vault",
        maskedToken: "repl…cret",
        modelCount: 2,
      }))
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    await selectOpenAIAPI();
    const token = await screen.findByLabelText("OpenAI API token");
    fireEvent.change(token, { target: { value: "replacement-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save token" }));

    expect(await screen.findByRole("status")).toHaveTextContent("Token saved and validated against 2 models");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/providers/openai/token",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ token: "replacement-secret" }),
      }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      "/api/v1/whoami?models=true&limit=0&disabled=true",
      expect.objectContaining({ method: "POST", body: "{}" }),
    );
    expect(token).toHaveValue("");
  });

  it("keeps a rejected API token and surfaces the validation error", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(new Response("validate openai credential: HTTP 401", { status: 422 }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    await selectOpenAIAPI();
    const token = await screen.findByLabelText("OpenAI API token");
    fireEvent.change(token, { target: { value: "rejected-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save token" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("validate openai credential: HTTP 401");
    expect(token).toHaveValue("rejected-secret");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("persists a runtime exclusion as a provider and mode pair", async () => {
    const saved = {
      ...NOTHING_DISABLED,
      runtimes: [{ provider: "openai", mode: "cli" }],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse(saved))
      .mockResolvedValueOnce(jsonResponse({ ...WHOAMI_RESULT, disabled: saved }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("checkbox", { name: "Enable OpenAI CLI runtime" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/disabled",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          modes: [],
          providers: [],
          runtimes: [{ provider: "openai", mode: "cli" }],
          models: [],
          efforts: [],
        }),
      }),
    );
  });

  it("persists model exclusions by provider rather than a composite backend", async () => {
    const saved = { ...NOTHING_DISABLED, models: ["openai/gpt-5.5-codex"] };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(jsonResponse(saved))
      .mockResolvedValueOnce(jsonResponse({ ...WHOAMI_RESULT, disabled: saved }));
    vi.stubGlobal("fetch", fetchMock);
    renderWhoamiPage();

    fireEvent.click(await screen.findByRole("checkbox", { name: "Enable gpt-5.5-codex model" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/captain/ai/disabled",
      expect.objectContaining({
        body: JSON.stringify({
          modes: [],
          providers: [],
          runtimes: [],
          models: ["openai/gpt-5.5-codex"],
          efforts: [],
        }),
      }),
    );
  });

  it("shows a rejected model exclusion as a banner above the capability tree", async () => {
    const rejection = "configuration changes require a loopback request host";
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(jsonResponse(WHOAMI_RESULT))
      .mockResolvedValueOnce(new Response(rejection, { status: 403 })));
    renderWhoamiPage();

    const checkbox = await screen.findByRole("checkbox", { name: "Enable gpt-5.5-codex model" });
    fireEvent.click(checkbox);

    const banner = await screen.findByRole("alert");
    const tree = screen.getByRole("tree", { name: "Capability topology" });
    expect(banner).toHaveTextContent(rejection);
    expect(banner.compareDocumentPosition(tree) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(checkbox).toBeChecked();
  });

  it("shows inherited provider policy without erasing child selections", async () => {
    const disabled = { ...NOTHING_DISABLED, providers: ["openai"] };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      ...WHOAMI_RESULT,
      disabled,
      adapters: WHOAMI_RESULT.adapters.map((adapter) => adapter.provider === "openai"
        ? {
            ...adapter,
            disabled: true,
            disabledReason: "provider openai",
            modelDetails: adapter.modelDetails?.map((model) => ({ ...model, disabled: true })),
          }
        : adapter),
    })));
    renderWhoamiPage();

    expect(await screen.findByRole("checkbox", { name: "Enable OpenAI provider" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Enable OpenAI CLI runtime" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Enable gpt-5.5-codex model" })).toBeChecked();
    expect(screen.getAllByText("Disabled").length).toBeGreaterThan(0);
    expect(screen.getByText("Excluded by provider policy")).toBeInTheDocument();
  });

  it("surfaces a failed whoami request", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("probe failed", { status: 500 })));

    renderWhoamiPage();

    expect(await screen.findByText("probe failed")).toBeInTheDocument();
  });
});

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function renderWhoamiPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <WhoamiPage />
    </QueryClientProvider>,
  );
}

async function selectOpenAIAPI() {
  await screen.findByRole("checkbox", { name: "Enable OpenAI API runtime" });
  fireEvent.click(within(runtimeTreeItem("OpenAI", "API")).getByRole("button", { name: /^API Ready/ }));
}

function runtimeTreeItem(provider: string, mode: string): HTMLElement {
  const checkbox = screen.getByRole("checkbox", { name: `Enable ${provider} ${mode} runtime` });
  const treeitem = checkbox.closest<HTMLElement>('[role="treeitem"]');
  if (!treeitem) throw new Error(`${provider} ${mode} runtime tree item was not rendered`);
  return treeitem;
}
